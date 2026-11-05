package main

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// /ws/hosts?h=a.com,b.org : page counts of the named sites, pushed every 50 ms (only when something changed,
// and at least once a second). It is what the `lm` command on the box shows.

var (
	hostsConns atomic.Int32
	hostRe     = regexp.MustCompile(`^[a-z0-9.-]{3,80}$`)
)

func wsHosts(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || key == "" {
		http.Error(w, "WebSocket only", 400)
		return
	}
	var hosts []string
	for _, h := range strings.Split(strings.ToLower(r.URL.Query().Get("h")), ",") {
		if hostRe.MatchString(h) && len(hosts) < 500 {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 || hostsConns.Load() >= 5 {
		http.Error(w, "no hosts, or too many connections", 400)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", 500)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	hostsConns.Add(1)
	defer hostsConns.Add(-1)
	defer conn.Close()
	conn.SetDeadline(time.Time{})
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	if brw.Flush() != nil {
		return
	}
	done := make(chan struct{})
	go func() { // the client only ever sends a close
		defer close(done)
		io.Copy(io.Discard, brw)
	}()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var last string
	var lastSent time.Time
	for {
		select {
		case <-done:
			return
		case <-tick.C:
		}
		counts := make(map[string]int, len(hosts))
		for _, h := range hosts {
			counts[h] = ix.HostPagesAny(h)
		}
		b, _ := json.Marshal(map[string]any{"p": ix.NDocs(), "h": counts})
		if s := string(b); s != last || time.Since(lastSent) > time.Second {
			last, lastSent = s, time.Now()
			b, _ = json.Marshal(map[string]any{"t": time.Now().UnixMilli(), "p": ix.NDocs(), "h": counts})
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Write(wsFrame(b)); err != nil {
				return
			}
		}
	}
}

// /ws/forums: the Lemmy tab of the Live page. Every second it pushes the forum numbers and the crawl's progress (when they changed, and
// at least every 5 seconds).
var forumsConns atomic.Int32

func wsForums(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || key == "" {
		http.Error(w, "WebSocket only", 400)
		return
	}
	if forumsConns.Load() >= 20 {
		http.Error(w, "too many connections", 503)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", 500)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	forumsConns.Add(1)
	defer forumsConns.Add(-1)
	defer conn.Close()
	conn.SetDeadline(time.Time{})
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	if brw.Flush() != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		io.Copy(io.Discard, brw)
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var last string
	var lastSent time.Time
	for {
		b, _ := json.Marshal(forumsLiveData())
		if s := string(b); s != last || time.Since(lastSent) > 5*time.Second {
			last, lastSent = s, time.Now()
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Write(wsFrame(b)); err != nil {
				return
			}
		}
		select {
		case <-done:
			return
		case <-tick.C:
		}
	}
}
