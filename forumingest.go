package main

// Forum posts from GitHub Actions. The forum crawl (lemmycrawl, running in Chrome on a GitHub runner) cannot write lemmy-posts.jsonl itself, so it sends
// the posts here, to the same public address as the page ingest:
//   POST /api/forum/known   ["post address", ...]   -> {"new": [the addresses this server does not have yet]}   (so it only opens the new posts)
//   POST /api/forum/posts   [ {post with comments}, ... ] (gzip allowed) -> {"added": n, "known": m}
// Both need the same Bearer token as /api/ingest. A post is saved to lemmy-posts.jsonl (the file the Forums tab reads) and its text goes to the search
// index through the same queue as the pages.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"hash/fnv"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

var forumPostsFile = fsData + "/lemmy-posts.jsonl"

type forumComment struct {
	A string `json:"a"`
	T string `json:"t"`
	S int    `json:"s"`
}

type forumPost struct {
	U         string         `json:"u"`
	Ap        string         `json:"ap"`
	T         string         `json:"t"`
	Body      string         `json:"body"`
	Community string         `json:"community"`
	Name      string         `json:"name"`
	Instance  string         `json:"instance"`
	Score     int            `json:"score"`
	Comments  int            `json:"comments"`
	Published string         `json:"published"`
	Top       []forumComment `json:"top,omitempty"`
	Platform  string         `json:"platform,omitempty"`
}

// forumPageQ: the text of saved forum posts waiting for the search index (up to 1000 batches). One worker hands them to the page queue (ingestQ) and
// waits there when it is full, so the pages are indexed one batch after the other without anybody being turned away.
var forumPageQ = make(chan []Page, 1000)

func init() {
	go func() {
		for pages := range forumPageQ {
			ingestQ <- pages // waits while the page queue is full
		}
	}()
}

var (
	fkMu     sync.Mutex
	fkSet    = map[uint64]struct{}{}
	fkLoaded bool
)

func fkHash(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

func fkKey(p forumPost) string {
	if p.Ap != "" {
		return p.Ap
	}
	return p.U
}

// fkLoad reads the addresses of the posts already saved (once, the first time they are needed). Called with fkMu held.
func fkLoad() {
	if fkLoaded {
		return
	}
	f, err := os.Open(forumPostsFile)
	if err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var p struct {
				U  string `json:"u"`
				Ap string `json:"ap"`
			}
			if json.Unmarshal(sc.Bytes(), &p) == nil {
				k := p.Ap
				if k == "" {
					k = p.U
				}
				if k != "" {
					fkSet[fkHash(k)] = struct{}{}
				}
			}
		}
		f.Close()
	}
	fkLoaded = true
}

// forumBody reads a request body (at most 64 MB packed, 256 MB unpacked; gzip allowed).
func forumBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		return io.ReadAll(io.LimitReader(zr, 256<<20))
	}
	return body, nil
}

func forumKnown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !authed(r) {
		http.Error(w, `{"error":"POST with the token"}`, 401)
		return
	}
	body, err := forumBody(r)
	var urls []string
	if err != nil || json.Unmarshal(body, &urls) != nil || len(urls) > 2000 {
		http.Error(w, `{"error":"bad request"}`, 400)
		return
	}
	fkMu.Lock()
	fkLoad()
	unknown := []string{}
	for _, u := range urls {
		if _, ok := fkSet[fkHash(u)]; !ok {
			unknown = append(unknown, u)
		}
	}
	fkMu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{"new": unknown})
}

func forumPosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !authed(r) {
		http.Error(w, `{"error":"POST with the token"}`, 401)
		return
	}
	body, err := forumBody(r)
	var posts []forumPost
	if err != nil || json.Unmarshal(body, &posts) != nil || len(posts) > 5000 {
		http.Error(w, `{"error":"bad request"}`, 400)
		return
	}
	fkMu.Lock()
	defer fkMu.Unlock()
	fkLoad()
	var lines bytes.Buffer
	var pages []Page
	var keys []uint64
	known := 0
	for _, p := range posts {
		k := fkKey(p)
		if k == "" || strings.TrimSpace(p.T) == "" || strings.TrimSpace(p.U) == "" {
			continue
		}
		if _, ok := fkSet[fkHash(k)]; ok {
			known++
			continue
		}
		keys = append(keys, fkHash(k))
		fkSet[fkHash(k)] = struct{}{}
		line, _ := json.Marshal(p)
		lines.Write(line)
		lines.WriteByte('\n')
		var cs []string
		for _, c := range p.Top {
			cs = append(cs, c.T)
		}
		source := "lemmy"
		if p.Platform != "" {
			source = p.Platform
		}
		text := p.T + ". " + p.Community + " on " + p.Instance + ". " + p.Body + " " + strings.Join(cs, " ")
		pages = append(pages, Page{URL: p.U, Title: p.T, Description: p.Community + " - " + p.Name, Text: clip(text, 2000), Source: source})
	}
	if len(pages) > 0 {
		// 1. the posts are saved at once (the Forums tab reads this file); 2. their text is put in a queue for the search index, which takes it at its own
		// speed. The sender gets its answer as soon as the posts are on disk, so a busy search index never turns it away.
		f, err := os.OpenFile(forumPostsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			for _, k := range keys {
				delete(fkSet, k)
			}
			http.Error(w, `{"error":"could not save"}`, 500)
			return
		}
		_, err = f.Write(lines.Bytes()) // one write for the whole batch
		f.Close()
		if err != nil {
			for _, k := range keys {
				delete(fkSet, k)
			}
			http.Error(w, `{"error":"could not save"}`, 500)
			return
		}
		select {
		case forumPageQ <- pages:
		default:
			log.Printf("forum posts: the queue for the search index is full (%d batches): the text of %d posts was not indexed", cap(forumPageQ), len(pages))
		}
	}
	json.NewEncoder(w).Encode(map[string]int{"added": len(pages), "known": known, "waiting": len(forumPageQ)})
}
