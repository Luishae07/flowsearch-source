package main

// Forums run on Cloudflare's decision model (clef-flash) and keep what it decided: the verdicts are saved in a file, so a search
// that was judged once (or warmed up by forums_warm.py) answers at once, also after a restart.

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"
)

var forumCacheFile = fsData + "/forums-cache.jsonl"

// clefClient asks Cloudflare's clef-flash directly (the same questions as for Jev).
var clefClient = &http.Client{Timeout: 12 * time.Second, Transport: clefTransport{}}

type clefTransport struct{}

func (clefTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
		for {
			n, err := req.Body.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if err != nil {
				break
			}
		}
		req.Body.Close()
		body = buf
	}
	return cfDecision(req, body)
}

var (
	fcMu     sync.Mutex
	fcLoaded bool
	fcData   = map[string]json.RawMessage{}
	fcFile   *os.File
)

func fcLoad() {
	if fcLoaded {
		return
	}
	fcLoaded = true
	if f, err := os.Open(forumCacheFile); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var j struct {
				K string          `json:"k"`
				V json.RawMessage `json:"v"`
			}
			if json.Unmarshal(sc.Bytes(), &j) == nil && j.K != "" {
				fcData[j.K] = j.V
			}
		}
		f.Close()
	}
	fcFile, _ = os.OpenFile(forumCacheFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
}

// fcGet fills v from the saved verdict for k.
func fcGet(k string, v any) bool {
	fcMu.Lock()
	defer fcMu.Unlock()
	fcLoad()
	raw, ok := fcData[k]
	return ok && json.Unmarshal(raw, v) == nil
}

func fcPut(k string, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	fcMu.Lock()
	defer fcMu.Unlock()
	fcLoad()
	fcData[k] = raw
	if fcFile != nil {
		line, _ := json.Marshal(map[string]any{"k": k, "v": json.RawMessage(raw), "at": time.Now().Unix()})
		fcFile.Write(append(line, '\n'))
	}
}

func fcCount() int {
	fcMu.Lock()
	defer fcMu.Unlock()
	fcLoad()
	return len(fcData)
}
