package main

// /internal/forums-warm: one request warms the Forums tab for up to 10 searches at once (forums_warm.py sends its searches in
// batches of 10). Each search is run as a visitor's search would be, so clef-flash's verdicts end up in the cache file.

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
)

func internalForumsWarm(w http.ResponseWriter, r *http.Request) {
	secret, _ := os.ReadFile(fsSecrets + "/flowmcp_internal")
	if len(secret) == 0 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Key")), []byte(strings.TrimSpace(string(secret)))) != 1 ||
		r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("CF-Connecting-IP") != "" {
		http.Error(w, "forbidden", 403)
		return
	}
	var in struct{ Queries []string }
	if r.Method != "POST" || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil || len(in.Queries) == 0 || len(in.Queries) > 10 {
		http.Error(w, `send {"queries":[up to 10 searches]}`, 400)
		return
	}
	var wg sync.WaitGroup
	for _, q := range in.Queries {
		q = strings.TrimSpace(q)
		if q == "" || len(q) > 150 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub := httptest.NewRequest("GET", "/search?tab=forums&q="+url.QueryEscape(q), nil)
			forumsSearch(httptest.NewRecorder(), sub, q)
		}()
	}
	wg.Wait()
	fmt.Fprintf(w, "warmed %d searches; %d verdicts saved\n", len(in.Queries), fcCount())
}
