package main

// /internal/llms: opens a site's llms.txt in a real browser (Browser Use) and returns what the page shows. Only the
// Flowsearch MCP server calls it, with the secret from /srv/secrets/flowmcp_internal; a request that came through a
// tunnel (it carries a forwarded address) is refused.

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var llmsVisitHost = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
var llmsVisitSlots = make(chan struct{}, 3) // at most 3 browsers at a time

type llmsVisit struct {
	Status int    `json:"status"`
	CT     string `json:"ct"`
	Text   string `json:"text"`
}

func buLlms(site, name string) (v llmsVisit, err error) {
	err = buRun(site+"/"+name, true, func(c *cdpConn) error {
		r, e := c.call("Runtime.evaluate", map[string]any{
			"expression":    `JSON.stringify({status: (performance.getEntriesByType('navigation')[0]||{}).responseStatus||0, ct: document.contentType||'', text: document.body ? document.body.innerText : ''})`,
			"returnByValue": true,
		})
		if e != nil {
			return e
		}
		var d struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		json.Unmarshal(r, &d)
		return json.Unmarshal([]byte(d.Result.Value), &v)
	})
	if len(v.Text) > 200<<10 {
		v.Text = v.Text[:200<<10]
	}
	return
}

func internalLlms(w http.ResponseWriter, r *http.Request) {
	secret, _ := os.ReadFile(fsSecrets + "/flowmcp_internal")
	got := r.Header.Get("X-Internal-Key")
	if len(secret) == 0 || subtle.ConstantTimeCompare([]byte(got), []byte(strings.TrimSpace(string(secret)))) != 1 ||
		r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("CF-Connecting-IP") != "" {
		http.Error(w, "forbidden", 403)
		return
	}
	site := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("site"))), ".")
	if !llmsVisitHost.MatchString(site) {
		http.Error(w, "bad site", 400)
		return
	}
	name := "llms.txt"
	if r.URL.Query().Get("full") == "1" {
		name = "llms-full.txt"
	}
	select {
	case llmsVisitSlots <- struct{}{}:
		defer func() { <-llmsVisitSlots }()
	case <-time.After(20 * time.Second):
		http.Error(w, "busy", 503)
		return
	}
	v, err := buLlms(site, name)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
