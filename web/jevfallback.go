package main

// Jev through TypeSafe's own API (api.typesafe.ai) is the first choice for every decision; OpenRouter is not used. When TypeSafe
// refuses (no credit, rate limit, an error) the same question goes to Cloudflare's own decision model, clef-flash, which answers in
// the same shape. After a failure the failing provider is skipped for a while, so a search does not wait for an answer that will
// not come.

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

const cfDecisionModel = "@cf/cloudflare/clef-flash"

type jevTransport struct{}

var (
	jevDownMu    sync.Mutex
	jevDownUntil time.Time
)

func jevIsDown() bool {
	jevDownMu.Lock()
	defer jevDownMu.Unlock()
	return time.Now().Before(jevDownUntil)
}

func jevMarkDown(d time.Duration, why string) {
	jevDownMu.Lock()
	defer jevDownMu.Unlock()
	if time.Now().After(jevDownUntil) {
		log.Printf("jev: TypeSafe is not answering (%s); using Cloudflare clef-flash for %s", why, d)
	}
	jevDownUntil = time.Now().Add(d)
}

func (jevTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body.Close()
	}
	if !jevIsDown() {
		r2 := req.Clone(req.Context())
		r2.Body = io.NopCloser(bytes.NewReader(body))
			if bytes.Contains(body, []byte(`"image_url"`)) {
				r2.Header.Set("TypeSafe-Danger-Zone-Images", "true") // TypeSafe wants this header for pictures
			}
		resp, err := http.DefaultTransport.RoundTrip(r2)
		if err == nil && resp.StatusCode == 200 {
			return resp, nil
		}
		switch {
		case err != nil:
			jevMarkDown(2*time.Minute, err.Error())
		case resp.StatusCode == 402:
			jevMarkDown(30*time.Minute, "no credit")
		default:
			jevMarkDown(2*time.Minute, resp.Status)
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
	return cfDecision(req, body)
}

// cfDecision sends the question to clef-flash and gives back an answer shaped like Jev's: {"answers": {...}}.
func cfDecision(req *http.Request, body []byte) (*http.Response, error) {
	var q map[string]json.RawMessage
	if err := json.Unmarshal(body, &q); err != nil {
		return nil, err
	}
	delete(q, "model")
	// clef-flash takes at most 64 questions in one request, and a request is faster the smaller it is: a long list goes in parts, at the same time
	var qs map[string]json.RawMessage
	if err := json.Unmarshal(q["questions"], &qs); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(qs))
	for k := range qs {
		keys = append(keys, k)
	}
	// the time of a request grows with its size, so a big list is split into parts of about 24 questions that run at the same time
	const part = 64
	answers := map[string]json.RawMessage{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	for i := 0; i < len(keys); i += part {
		sub := map[string]json.RawMessage{}
		for _, k := range keys[i:min(i+part, len(keys))] {
			sub[k] = qs[k]
		}
		subBody := map[string]json.RawMessage{"state": q["state"]}
		subBody["questions"], _ = json.Marshal(sub)
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := cfRunCtx(req, cfDecisionModel, subBody)
			var res struct {
				Answers map[string]json.RawMessage `json:"answers"`
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil || json.Unmarshal(raw, &res) != nil || len(res.Answers) == 0 {
				if firstErr == nil {
					firstErr = err
					if err == nil {
						firstErr = io.ErrUnexpectedEOF
					}
				}
				return
			}
			for k, v := range res.Answers {
				answers[k] = v
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	res := struct{ Answers map[string]json.RawMessage }{answers}
	ans, _ := json.Marshal(res.Answers)
	out, _ := json.Marshal(map[string]json.RawMessage{"answers": ans})
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewReader(out)), ContentLength: int64(len(out)), Request: req}, nil
}
