package main

// /internal/amazon-urls: lists the Amazon product pages (/dp/ASIN) that the index holds, a slice at a time, for the bulk price crawl.
// Only the crawl script on this machine calls it, with the secret from /srv/secrets/flowmcp_internal.
//   ?site=amazon.com&offset=0&limit=5000   one line per page of the index: the product number, or "-" when the page is not a product

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var amazonDP = regexp.MustCompile(`/dp/([A-Z0-9]{10})`)

func internalAmazonURLs(w http.ResponseWriter, r *http.Request) {
	secret, _ := os.ReadFile(fsSecrets + "/flowmcp_internal")
	if len(secret) == 0 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Key")), []byte(strings.TrimSpace(string(secret)))) != 1 ||
		r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("CF-Connecting-IP") != "" {
		http.Error(w, "forbidden", 403)
		return
	}
	site := r.URL.Query().Get("site")
	if site != "amazon.com" && site != "amazon.de" {
		http.Error(w, "bad site", 400)
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	limit = min(max(limit, 1), 20000)
	ids, total := ix.HostDocPage(site, offset, limit)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Total", strconv.Itoa(total))
	for _, id := range ids {
		d, ok := ix.Doc(id)
		if !ok {
			fmt.Fprintln(w, "-")
			continue
		}
		rec := load(d)
		if m := amazonDP.FindStringSubmatch(rec.URL); m != nil && strings.Contains(d.Host, site) {
			fmt.Fprintln(w, m[1])
		} else {
			fmt.Fprintln(w, "-")
		}
	}
}
