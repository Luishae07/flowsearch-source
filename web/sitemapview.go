package main

// The Sitemaps tab shows what a GitHub Actions run found: the sitemaps listed in the site's robots.txt and what is
// inside each one. The run (.github/workflows/sitemap-scan.yml) saves its answer to sitemap-scans/<site>.json in the
// Flowsearch repository; this file reads that and draws it.

import (
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type scanChild struct {
	Addr  string `json:"addr"`
	Total *int   `json:"total"`
	Error string `json:"error"`
}

type scanSM struct {
	Addr     string      `json:"addr"`
	Error    string      `json:"error"`
	Index    bool        `json:"index"`
	Total    int         `json:"total"`
	Locs     []string    `json:"locs"`
	Children []scanChild `json:"children"`
}

type scanFile struct {
	RobotsBody string `json:"robots_body"`
	RobotsCode int    `json:"robots_code"`
	Site     string   `json:"site"`
	Scanned  string   `json:"scanned"`
	Robots   string   `json:"robots"`
	FoundBy  string   `json:"found_by"`
	Listed   int      `json:"listed"`
	Sitemaps []scanSM `json:"sitemaps"`
}

var (
	scanMu    sync.Mutex
	scanCache = map[string]struct {
		at time.Time
		f  *scanFile
	}{}
	scanLast = map[string]time.Time{}
)

func loadScan(site string) *scanFile {
	scanMu.Lock()
	if c, ok := scanCache[site]; ok && time.Since(c.at) < 15*time.Second {
		scanMu.Unlock()
		return c.f
	}
	scanMu.Unlock()
	out, err := ghCmd("api", "repos/"+flowRepo+"/contents/sitemap-scans/"+site+".json", "-H", "Accept: application/vnd.github.raw").Output()
	var f *scanFile
	if err == nil {
		f = &scanFile{}
		if json.Unmarshal(out, f) != nil {
			f = nil
		}
	}
	scanMu.Lock()
	scanCache[site] = struct {
		at time.Time
		f  *scanFile
	}{time.Now(), f}
	scanMu.Unlock()
	return f
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min ago"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + " h ago"
	}
	return strconv.Itoa(int(d.Hours()/24)) + " days ago"
}

func link(addr string) string {
	if u, err := url.Parse(addr); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return `<a href="` + html.EscapeString(addr) + `" target=_blank rel="noopener noreferrer">` + html.EscapeString(addr) + `</a>`
	}
	return html.EscapeString(addr)
}

// scanButton starts a new scan; view is the tab to come back to.
func scanButton(site, csrf, view string) string {
	return `<form method=post action=/webmaster/sitemap-scan style="margin:0"><input type=hidden name=csrf value="` + csrf + `"><input type=hidden name=site value="` + html.EscapeString(site) + `"><input type=hidden name=view value="` + view + `"><button>Scan now</button></form>`
}

// robotsSitemapsPanel draws the last scan of the site, with a button that starts a new one.
func robotsSitemapsPanel(site, csrf string) string {
	btn := scanButton(site, csrf, "sitemaps")
	head := func(sub string) string {
		return `<div class=panel><div class=smhead><div><h2 style="margin-bottom:6px">Sitemaps from ` + html.EscapeString(site) + `/robots.txt</h2><p style="margin:0">` + sub + `</p></div>` + btn + `</div>`
	}
	f := loadScan(site)
	if f == nil {
		return head(`No scan yet. Press <b>Scan now</b>: GitHub Actions reads the file and the sitemaps in it, usually within a minute.`) + `</div>`
	}
	when := f.Scanned
	if t, err := time.Parse(time.RFC3339, f.Scanned); err == nil {
		when = t.UTC().Format("2006-01-02 15:04 UTC") + " (" + ago(time.Since(t)) + ")"
	}
	sub := `Scanned ` + html.EscapeString(when) + ` by GitHub Actions. robots.txt: ` + html.EscapeString(f.Robots) + `.`
	var b strings.Builder
	b.WriteString(head(sub))
	switch {
	case f.FoundBy == "guess":
		b.WriteString(`<p class=note>robots.txt lists no sitemap, so the scan tried the usual file names and found these.</p>`)
	case len(f.Sitemaps) == 0:
		b.WriteString(`<p style="margin:14px 0 0">No sitemap is listed in robots.txt and none of the usual file names exist. Add a line like <code>Sitemap: https://` + html.EscapeString(site) + `/sitemap.xml</code> to your robots.txt.</p>`)
	}
	for _, in := range f.Sitemaps {
		var kind, count string
		switch {
		case in.Error != "":
			kind = `<span class="pill bad">Problem</span>`
			count = html.EscapeString(in.Error)
		case in.Index:
			kind = `<span class=pill style="background:#35e0cf26;color:#35e0cf">Index</span>`
			count = strconv.Itoa(in.Total) + ` sitemaps`
		default:
			kind = `<span class="pill good">Pages</span>`
			count = strconv.Itoa(in.Total) + ` URLs`
		}
		b.WriteString(`<details class=smitem><summary>` + kind + `<span class=smaddr>` + html.EscapeString(in.Addr) + `</span><span class=smcount>` + count + `</span></summary>`)
		if in.Error == "" {
			b.WriteString(`<table class=gt>`)
			if in.Index && len(in.Children) > 0 {
				for _, k := range in.Children {
					n := k.Error
					if k.Error == "" && k.Total != nil {
						n = strconv.Itoa(*k.Total) + " URLs"
					}
					b.WriteString(`<tr><td>` + link(k.Addr) + `</td><td style="white-space:nowrap;color:var(--dim)">` + html.EscapeString(n) + `</td></tr>`)
				}
				if in.Total > len(in.Children) {
					b.WriteString(`<tr><td colspan=2>…and ` + strconv.Itoa(in.Total-len(in.Children)) + ` more sitemaps.</td></tr>`)
				}
			} else {
				for _, l := range in.Locs {
					b.WriteString(`<tr><td>` + link(l) + `</td></tr>`)
				}
				if in.Total > len(in.Locs) {
					b.WriteString(`<tr><td>…and ` + strconv.Itoa(in.Total-len(in.Locs)) + ` more.</td></tr>`)
				}
			}
			b.WriteString(`</table>`)
		}
		b.WriteString(`</details>`)
	}
	if f.Listed > len(f.Sitemaps) {
		b.WriteString(`<p class=note>…and ` + strconv.Itoa(f.Listed-len(f.Sitemaps)) + ` more sitemaps in the file.</p>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// wmSitemapScan starts the GitHub Actions run that reads the site's sitemaps.
func wmSitemapScan(w http.ResponseWriter, r *http.Request) {
	tab := "sitemaps"
	if r.FormValue("view") == "robots" {
		tab = "robots"
	}
	back := func(m string) {
		http.Redirect(w, r, "/webmaster?view="+tab+"&site="+url.QueryEscape(r.FormValue("site"))+"&msg="+url.QueryEscape(m), http.StatusSeeOther)
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/webmaster", http.StatusSeeOther)
		return
	}
	user := currentUser(r)
	if user == "" || !csrfOK(r, user) {
		back("Please log in first.")
		return
	}
	site, ok := normalizeSite(r.FormValue("site"))
	if !ok || !hasSite(user, site) {
		back("Add the site first.")
		return
	}
	scanMu.Lock()
	wait := time.Minute - time.Since(scanLast[site])
	if wait > 0 {
		scanMu.Unlock()
		back("A scan of " + site + " was started a moment ago. Try again in a minute.")
		return
	}
	scanLast[site] = time.Now()
	scanMu.Unlock()
	go ghCmd("workflow", "run", "sitemap-scan.yml", "--repo", flowRepo, "-f", "site="+site).Run()
	back("Scanning " + site + ": a GitHub Actions run is starting. Reload this tab in about a minute.")
}
