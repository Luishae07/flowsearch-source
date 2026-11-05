package main

// Two modes for running on a GitHub Actions runner:
//   -chrome          the browser is a real Chrome started here (headless, DevTools on a local port) instead of a Browser Use browser. The crawler talks to it
//                    with the same DevTools protocol (newCDP) as before.
//   -remote <address> the posts are not written to lemmy-posts.jsonl but sent to the Flowsearch ingest (<address>/api/forum/posts, 300 at a time, gzip),
//                    and before a post is opened the ingest is asked which addresses it does not have yet (<address>/api/forum/known).
// The ingest token comes from the environment variable MYSEARCH_TOKEN (a repository secret) or /srv/secrets/mysearch_token.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

// plainGet fetches a JSON address with a normal HTTP request (no browser at all). It waits a little before each request, and a "slow down" (429) or a
// server error is tried again a few times.
func plainGet(u string, v any) error {
	time.Sleep(250 * time.Millisecond)
	var last error
	for try := 0; try < 4; try++ {
		req, _ := http.NewRequest("GET", u, nil)
		req.Header.Set("User-Agent", "MySearchBot/1.0")
		req.Header.Set("Accept", "application/json")
		resp, err := (&http.Client{Timeout: 25 * time.Second}).Do(req)
		if err != nil {
			if strings.Contains(err.Error(), "no such host") || try >= 1 {
				return err // the forum does not exist, or did not answer twice: no more tries
			}
			last = err
			time.Sleep(2 * time.Second)
			continue
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == 200 && rerr == nil:
			if err := json.Unmarshal(data, v); err != nil {
				return fmt.Errorf("no JSON at that address (%q)", clip(string(data), 60))
			}
			return nil
		case resp.StatusCode == 429:
			wait := 10 * time.Second
			if s := resp.Header.Get("Retry-After"); s != "" {
				var n int
				if _, e := fmt.Sscanf(s, "%d", &n); e == nil && n > 0 && n <= 60 {
					wait = time.Duration(n) * time.Second
				}
			}
			last = fmt.Errorf("HTTP 429")
			time.Sleep(wait)
		case resp.StatusCode >= 500:
			last = fmt.Errorf("HTTP %d", resp.StatusCode)
			time.Sleep(3 * time.Second)
		case resp.StatusCode == 403:
			return errBlocked // the forum does not let this client in: with -firefox a real browser tries
		default:
			return fmt.Errorf("HTTP %d", resp.StatusCode) // 404 ...: trying again does not help
		}
	}
	return last
}

// Time limits instead of post limits: -minutes is how long the whole crawl may run (it stops by itself and sends what it has, so a GitHub job that is
// cut off at 6 hours loses nothing), -hostminutes is how long one forum may keep a worker (so a forum with millions of posts cannot hold the others up).
var (
	deadline   time.Time
	hostBudget time.Duration
)

func timeUp() bool { return !deadline.IsZero() && time.Now().After(deadline) }

func hostOver(start time.Time) bool { return hostBudget > 0 && time.Since(start) > hostBudget }

var (
	plainMode  bool
	chromeMode bool
	remoteURL  string
	rbatch     []post
	chromeN    int32
)

func chromeBin() string {
	for _, n := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// chromeStart starts a headless Chrome and returns how to stop it and the address of its DevTools (http://127.0.0.1:port).
func chromeStart() (func(), string, error) {
	bin := chromeBin()
	if bin == "" {
		return nil, "", fmt.Errorf("no Chrome found on this machine")
	}
	port := 9300 + int(atomic.AddInt32(&chromeN, 1))
	dir, err := os.MkdirTemp("", "chrome-")
	if err != nil {
		return nil, "", err
	}
	var errb bytes.Buffer
	cmd := exec.Command(bin, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--no-first-run", "--no-default-browser-check",
		"--disable-extensions", "--remote-debugging-address=127.0.0.1", fmt.Sprintf("--remote-debugging-port=%d", port), "--user-data-dir="+dir,
		"--window-size=1280,720", "about:blank")
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return nil, "", err
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	stop := func() {
		cmd.Process.Kill()
		<-exited
		os.RemoveAll(dir)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	// a Chrome that has just been started needs a moment before its DevTools port answers: ask every 0.1 seconds, for 15 seconds at most
	for i := 0; i < 150; i++ {
		select {
		case <-exited:
			os.RemoveAll(dir)
			return nil, "", fmt.Errorf("Chrome stopped at once: %s", clip(errb.String(), 300))
		default:
		}
		if resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(base + "/json/version"); err == nil {
			resp.Body.Close()
			return stop, base, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	return nil, "", fmt.Errorf("Chrome did not open its DevTools port within 15 seconds")
}

func remoteToken() string {
	if t := strings.TrimSpace(os.Getenv("MYSEARCH_TOKEN")); t != "" {
		return t
	}
	b, _ := os.ReadFile(fsSecrets + "/mysearch_token")
	return strings.TrimSpace(string(b))
}

// remotePost sends one JSON body to the ingest and returns the answer; a busy ingest or a lost connection is tried again.
func remotePost(path string, body []byte, gz bool) ([]byte, error) {
	var last error
	for try := 0; try < 12; try++ {
		req, _ := http.NewRequest("POST", remoteURL+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+remoteToken())
		req.Header.Set("Content-Type", "application/json")
		if gz {
			req.Header.Set("Content-Encoding", "gzip")
		}
		resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
		if err == nil {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return data, nil
			}
			last = fmt.Errorf("the ingest answered HTTP %d %s", resp.StatusCode, strings.TrimSpace(clip(string(data), 100)))
			if resp.StatusCode != 503 && resp.StatusCode != 429 && resp.StatusCode < 500 {
				return nil, last
			}
		} else {
			last = err
		}
		log.Printf("ingest %s: %v; again in 30 seconds (attempt %d)", path, last, try+1)
		time.Sleep(30 * time.Second)
	}
	return nil, last
}

// remoteFlush sends the finished posts (called with the lock held).
func remoteFlush() {
	if remoteURL == "" || len(rbatch) == 0 {
		return
	}
	raw, _ := json.Marshal(rbatch)
	var zb bytes.Buffer
	zw := gzip.NewWriter(&zb)
	zw.Write(raw)
	zw.Close()
	log.Printf("sending %d posts to the ingest (%d KB packed)", len(rbatch), zb.Len()/1024)
	if _, err := remotePost("/api/forum/posts", zb.Bytes(), true); err != nil {
		log.Printf("these %d posts were not accepted: %v", len(rbatch), err)
	}
	rbatch = nil
}

// remoteUnknown: of these post addresses, the ones the ingest does not have yet. nil when not in -remote mode (then every post counts as new).
func remoteUnknown(urls []string) map[string]bool {
	if remoteURL == "" {
		return nil
	}
	out := map[string]bool{}
	if len(urls) == 0 {
		return out
	}
	body, _ := json.Marshal(urls)
	data, err := remotePost("/api/forum/known", body, false)
	if err != nil {
		log.Printf("known: %v: treating all as new", err)
		for _, u := range urls {
			out[u] = true
		}
		return out
	}
	var r struct {
		New []string `json:"new"`
	}
	json.Unmarshal(data, &r)
	for _, u := range r.New {
		out[u] = true
	}
	return out
}
