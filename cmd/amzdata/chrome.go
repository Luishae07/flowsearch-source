package main

// For a GitHub Actions runner:
//   -chrome           the browser is a real Chrome started here (headless, DevTools on a local port) instead of a Browser Use browser
//   -remote <address> the products are sent to the Flowsearch ingest (<address>/api/amazon/products) instead of being written to files here, and the
//                     ingest is asked which product numbers it does not have yet (<address>/api/amazon/known)
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
	"time"
)

var amzDeadline time.Time // -minutes: when the crawl stops by itself

var (
	chromeMode bool
	remoteURL  string
	remoteSite string
	rProducts  []map[string]string
	rSkipped   []string
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
	port := 9301
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
	for i := 0; i < 150; i++ { // ask every 0.1 seconds, for 15 seconds at most
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

// remoteFlush sends the finished products and the skipped product numbers.
func remoteFlush() {
	if remoteURL == "" || (len(rProducts) == 0 && len(rSkipped) == 0) {
		return
	}
	raw, _ := json.Marshal(map[string]any{"site": remoteSite, "products": rProducts, "skipped": rSkipped})
	var zb bytes.Buffer
	zw := gzip.NewWriter(&zb)
	zw.Write(raw)
	zw.Close()
	log.Printf("sending %d products and %d skipped numbers to the ingest", len(rProducts), len(rSkipped))
	if _, err := remotePost("/api/amazon/products", zb.Bytes(), true); err != nil {
		log.Printf("these were not accepted: %v", err)
	}
	rProducts, rSkipped = nil, nil
}

// remoteUnknown: of these product numbers, the ones the ingest does not have yet (all of them when the ingest cannot be asked).
func remoteUnknown(asins []string) []string {
	if remoteURL == "" || len(asins) == 0 {
		return asins
	}
	var out []string
	for i := 0; i < len(asins); i += 5000 {
		part := asins[i:min(i+5000, len(asins))]
		body, _ := json.Marshal(map[string]any{"site": remoteSite, "asins": part})
		data, err := remotePost("/api/amazon/known", body, false)
		if err != nil {
			log.Printf("known: %v: taking all as new", err)
			out = append(out, part...)
			continue
		}
		var r struct {
			New []string `json:"new"`
		}
		json.Unmarshal(data, &r)
		out = append(out, r.New...)
	}
	return out
}
