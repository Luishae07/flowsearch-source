package main

import (
	"fmt"
	"net/http"
)

// settingsPage: the choices a visitor can make. Each one is a cookie in their own browser; there is no account.
func settingsPage(w http.ResponseWriter, r *http.Request) {
	store := amazonStore(w, r)
	dvd := dvdSetting(w, r)
	saved := ""
	if r.URL.Query().Get("store") != "" || r.URL.Query().Get("dvd") != "" {
		saved = `<div class=gc style="margin:0 0 14px;padding:12px 18px;border-color:var(--acc)">Saved in this browser.</div>`
	}
	seg := func(label, href string, on bool) string {
		cls := ""
		if on {
			cls = ` class=on aria-current=true`
		}
		return `<a` + cls + ` href="` + href + `">` + label + `</a>`
	}
	body := `<main class="wrap" style="max-width:760px">` + pageHead("Settings", "Make Flowsearch work the way you like. Your choices stay in this browser.") + saved +
		`<div class=gstack>` +
		`<section class=gc><h2>Shopping</h2><p style="color:var(--dim)">Which Amazon store the Shopping tab shows first.</p><p><span class=seg2>` +
		seg("amazon.com (US)", "/settings?store=com", store == "com") + seg("amazon.de (Germany)", "/settings?store=de", store == "de") + seg("Both", "/settings?store=both", store == "both") +
		`</span></p></section>` +
		`<section class=gc><h2>Home page banner</h2><p style="color:var(--dim)">A small “over N pages indexed” banner that bounces around the home page like an old DVD logo. It is off unless you turn it on.</p><p><span class=seg2>` +
		seg("Off", "/settings?dvd=off", !dvd) + seg("On", "/settings?dvd=on", dvd) +
		`</span></p></section>` +
		`<section class=gc><h2>Privacy</h2><p style="color:var(--dim)">These settings are cookies in the browser. Flowsearch does not keep them on its side, and there is no account to sign in to. <a href=/privacy>Read the privacy page</a>.</p></section>` +
		`</div></main>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, page("Settings - Flowsearch", "", body))
}
