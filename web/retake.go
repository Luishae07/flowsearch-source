package main

// /internal/screenshot?site=example.com: takes the screenshots of a site again (its front page and every page of it that has a preview), now that the
// screenshot code waits out captcha pages. Only this machine calls it, with the secret from /srv/secrets/flowmcp_internal.

import (
	"bufio"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

var retakeHost = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

func internalRetake(w http.ResponseWriter, r *http.Request) {
	secret, _ := os.ReadFile(fsSecrets + "/flowmcp_internal")
	if len(secret) == 0 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Key")), []byte(strings.TrimSpace(string(secret)))) != 1 ||
		r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("CF-Connecting-IP") != "" {
		http.Error(w, "forbidden", 403)
		return
	}
	site := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("site"))), "www.")
	if !retakeHost.MatchString(site) {
		http.Error(w, "bad site", 400)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := takeScreenshot(site); err != nil {
		fmt.Fprintf(w, "front page of %s: failed: %v\n", site, err)
	} else {
		fmt.Fprintf(w, "front page of %s: taken again\n", site)
	}
	// the pages of this site that have a preview
	seen := map[string]bool{}
	var urls []string
	if f, err := os.Open(pageShotDir + "/index.jsonl"); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var v struct {
				U string `json:"u"`
			}
			if json.Unmarshal(sc.Bytes(), &v) != nil || seen[v.U] {
				continue
			}
			if u, err := url.Parse(v.U); err == nil && (strings.TrimPrefix(u.Host, "www.") == site || strings.HasSuffix(u.Host, "."+site)) {
				seen[v.U] = true
				urls = append(urls, v.U)
			}
		}
		f.Close()
	}
	ok := 0
	for i, u := range urls {
		if i >= 50 {
			break
		}
		if err := takePageShot(u); err != nil {
			fmt.Fprintf(w, "  %s: failed: %v\n", u, err)
			continue
		}
		ok++
		fmt.Fprintf(w, "  %s: taken again\n", u)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	fmt.Fprintf(w, "%d of %d page previews taken again\n", ok, len(urls))
}
