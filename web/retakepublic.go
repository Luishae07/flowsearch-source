package main

// /api/retake?site=example.com: anybody can ask for a new screenshot of the front page of any website. POST starts it (it takes 5 to 30 seconds, so the answer
// comes at once and the page asks again with GET until it is done). Each visitor can ask for 50 new screenshots an hour, nothing else limits it: a name that is not a
// website name, and blocked or adult websites, are refused. A website that is already being photographed is not started twice.

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

type retakeJob struct {
	State string // "running", "done" or "failed"
	Msg   string
	At    time.Time
}

const retakePerHour = 50

var (
	rtMu   sync.Mutex
	rtJobs = map[string]*retakeJob{}
	rtHits = map[string][]time.Time{}
)

func retakeReply(w http.ResponseWriter, code int, state, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"state": state, "message": msg, "at": time.Now().Unix()})
}

func retakePublic(w http.ResponseWriter, r *http.Request) {
	site := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("site"))), "www.")
	if !retakeHost.MatchString(site) {
		retakeReply(w, 400, "failed", "That is not a website name.")
		return
	}
	if r.Method == http.MethodGet { // how far is it?
		rtMu.Lock()
		j := rtJobs[site]
		rtMu.Unlock()
		if j == nil {
			retakeReply(w, 200, "none", "")
			return
		}
		retakeReply(w, 200, j.State, j.Msg)
		return
	}
	if r.Method != http.MethodPost {
		retakeReply(w, 405, "failed", "POST starts a screenshot, GET asks how far it is.")
		return
	}
	if isBlocked(site) || adultHost(site) {
		retakeReply(w, 403, "failed", "No screenshot is taken of that website.")
		return
	}
	rtMu.Lock()
	if j := rtJobs[site]; j != nil && j.State == "running" { // it is being taken already: wait for that one
		rtMu.Unlock()
		retakeReply(w, 202, "running", "A screenshot of this website is being taken.")
		return
	}
	ip := clientIP(r) // 50 new screenshots an hour for each visitor
	now := time.Now()
	h := rtHits[ip][:0]
	for _, t := range rtHits[ip] {
		if now.Sub(t) < time.Hour {
			h = append(h, t)
		}
	}
	if len(h) >= retakePerHour {
		rtHits[ip] = h
		rtMu.Unlock()
		retakeReply(w, 429, "failed", "That was 50 new screenshots in an hour. Try again a little later.")
		return
	}
	rtHits[ip] = append(h, now)
	if len(rtHits) > 5000 {
		rtHits = map[string][]time.Time{}
	}
	rtJobs[site] = &retakeJob{State: "running", At: now}
	if len(rtJobs) > 500 {
		for k, j := range rtJobs {
			if j.State != "running" && time.Since(j.At) > time.Hour {
				delete(rtJobs, k)
			}
		}
	}
	rtMu.Unlock()
	go func() {
		err := takeScreenshot(site)
		rtMu.Lock()
		j := rtJobs[site]
		if err != nil {
			j.State, j.Msg = "failed", "The screenshot could not be taken: the website did not load in time, or it asked for a captcha."
		} else {
			j.State, j.Msg = "done", "A new screenshot was taken."
		}
		j.At = time.Now()
		rtMu.Unlock()
		if err == nil {
			shotListMu.Lock()
			shotListAt = time.Time{} // the galleries pick the new picture up at once
			shotListMu.Unlock()
		}
	}()
	retakeReply(w, 202, "running", "Taking a new screenshot …")
}
