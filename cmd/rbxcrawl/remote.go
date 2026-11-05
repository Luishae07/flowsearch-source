package main

// For a GitHub Actions runner:
//   -firefox          the browser is a Firefox started here (see firefox.go)
//   -remote <address> the games are sent to the Flowsearch ingest (<address>/api/roblox/games) instead of being written to files here, and the ingest
//                     is asked which games it does not have yet (<address>/api/roblox/known)
//   -minutes <n>      the crawl stops by itself after n minutes
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
	"strings"
	"time"
)

var (
	remoteURL   string
	rbxDeadline time.Time
	rGames      []map[string]string
)

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

// remoteFlush sends the finished games.
func remoteFlush() {
	if remoteURL == "" || len(rGames) == 0 {
		return
	}
	raw, _ := json.Marshal(map[string]any{"games": rGames})
	var zb bytes.Buffer
	zw := gzip.NewWriter(&zb)
	zw.Write(raw)
	zw.Close()
	log.Printf("sending %d games to the ingest", len(rGames))
	if _, err := remotePost("/api/roblox/games", zb.Bytes(), true); err != nil {
		log.Printf("these were not accepted: %v", err)
	}
	rGames = nil
}

// remoteUnknown: of these game addresses, the ones the ingest does not have yet (all of them when the ingest cannot be asked).
func remoteUnknown(urls []string) []string {
	if remoteURL == "" || len(urls) == 0 {
		return urls
	}
	var out []string
	for i := 0; i < len(urls); i += 20000 {
		part := urls[i:min(i+20000, len(urls))]
		body, _ := json.Marshal(map[string]any{"urls": part})
		data, err := remotePost("/api/roblox/known", body, false)
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
