package main

// "Request one page": the owner types an address of their own site and a GitHub Actions run (crawl-url.yml) fetches
// just that page and sends it to the index again. No limit on how often.

import (
	"net/http"
	"net/url"
	"strings"
)

func wmRecrawlURL(w http.ResponseWriter, r *http.Request) {
	back := func(site, m string) {
		http.Redirect(w, r, "/webmaster?view=pages&site="+url.QueryEscape(site)+"&msg="+url.QueryEscape(m), http.StatusSeeOther)
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/webmaster", http.StatusSeeOther)
		return
	}
	user := currentUser(r)
	if user == "" || !csrfOK(r, user) {
		back(r.FormValue("site"), "Please log in first.")
		return
	}
	site, ok := normalizeSite(r.FormValue("site"))
	if !ok || !hasSite(user, site) {
		back(r.FormValue("site"), "Add the site first.")
		return
	}
	raw := strings.TrimSpace(r.FormValue("url"))
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || !hasSite(user, strings.ToLower(u.Hostname())) {
		back(site, "That address is not on one of your sites.")
		return
	}
	u.Fragment = ""
	go ghCmd("workflow", "run", "crawl-url.yml", "--repo", flowRepo, "-f", "url="+u.String()).Run()
	back(site, "Requested "+u.String()+": a GitHub Actions run is fetching it. It shows up in a minute or two.")
}
