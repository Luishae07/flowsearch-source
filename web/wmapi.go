package main

// REST API for the webmaster tools, with API keys made in the webmaster tools ("API" tab). No rate limits.
//
//	Authorization: Bearer fsk_...      (or the header  X-API-Key: fsk_...)
//
//	GET    /api/webmaster/me
//	GET    /api/webmaster/sites
//	POST   /api/webmaster/sites                     {"site": "example.com", "lang": "any"|"de"}
//	GET    /api/webmaster/sites/{site}
//	GET    /api/webmaster/sites/{site}/pages?limit=50
//	POST   /api/webmaster/sites/{site}/recrawl      {"lang": "any"|"de"}
//	GET    /api/webmaster/sites/{site}/cost?visits=1000
//
// Keys are stored only as SHA-256 hashes: the key itself is shown once, when it is made.

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var wmapiKeysPath = fsData + "/apikeys.jsonl"

const wmapiMaxKeys = 10

type wmapiKey struct {
	ID      string `json:"id"`
	User    string `json:"user"`
	Hash    string `json:"hash"`
	Prefix  string `json:"prefix"`
	Label   string `json:"label"`
	Created int64  `json:"created"`
	Last    int64  `json:"last"`
	Revoked bool   `json:"revoked,omitempty"`
}

var (
	wmapiMu     sync.Mutex
	wmapiKeys   []*wmapiKey
	wmapiLoaded bool
)

func wmapiLoad() { // call with wmapiMu held
	if wmapiLoaded {
		return
	}
	wmapiLoaded = true
	f, err := os.Open(wmapiKeysPath)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var k wmapiKey
		if json.Unmarshal(sc.Bytes(), &k) == nil && k.ID != "" {
			wmapiKeys = append(wmapiKeys, &k)
		}
	}
}

func wmapiSave() { // call with wmapiMu held
	var b strings.Builder
	for _, k := range wmapiKeys {
		line, _ := json.Marshal(k)
		b.Write(line)
		b.WriteByte('\n')
	}
	tmp := wmapiKeysPath + ".tmp"
	if os.WriteFile(tmp, []byte(b.String()), 0o600) == nil {
		os.Rename(tmp, wmapiKeysPath)
	}
}

func wmapiHash(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

func wmapiRandHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// wmapiCreate makes a key for the user and returns it (the only time it is ever visible).
func wmapiCreate(user, label string) (string, error) {
	wmapiMu.Lock()
	defer wmapiMu.Unlock()
	wmapiLoad()
	active := 0
	for _, k := range wmapiKeys {
		if k.User == user && !k.Revoked {
			active++
		}
	}
	if active >= wmapiMaxKeys {
		return "", fmt.Errorf("an account can have up to %d API keys; revoke one first", wmapiMaxKeys)
	}
	label = strings.TrimSpace(label)
	if len(label) > 40 {
		label = label[:40]
	}
	full := "fsk_" + wmapiRandHex(20)
	wmapiKeys = append(wmapiKeys, &wmapiKey{ID: wmapiRandHex(5), User: user, Hash: wmapiHash(full), Prefix: full[:10],
		Label: label, Created: time.Now().Unix()})
	wmapiSave()
	return full, nil
}

func wmapiRevoke(user, id string) bool {
	wmapiMu.Lock()
	defer wmapiMu.Unlock()
	wmapiLoad()
	for _, k := range wmapiKeys {
		if k.ID == id && k.User == user && !k.Revoked {
			k.Revoked = true
			wmapiSave()
			return true
		}
	}
	return false
}

func wmapiKeysOf(user string) []wmapiKey {
	wmapiMu.Lock()
	defer wmapiMu.Unlock()
	wmapiLoad()
	var out []wmapiKey
	for _, k := range wmapiKeys {
		if k.User == user && !k.Revoked {
			out = append(out, *k)
		}
	}
	return out
}

// wmapiAllow used to be a sliding-window rate limit per key. There are no rate limits any more, so it
// always says yes; the call sites are kept so a limit can be switched back on here in one place.
func wmapiAllow(keyID, bucket string, max int, per time.Duration) bool {
	return true
}

func wmJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func wmErr(w http.ResponseWriter, status int, msg string) {
	wmJSON(w, status, map[string]string{"error": msg})
}

// wmapiAuth finds the key in the request and checks it.
func wmapiAuth(r *http.Request) *wmapiKey {
	tok := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if a := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(a), "bearer ") {
		tok = strings.TrimSpace(a[7:])
	}
	if !strings.HasPrefix(tok, "fsk_") || len(tok) > 80 {
		return nil
	}
	h := wmapiHash(tok)
	wmapiMu.Lock()
	defer wmapiMu.Unlock()
	wmapiLoad()
	for _, k := range wmapiKeys {
		if !k.Revoked && k.Hash == h {
			k.Last = time.Now().Unix() // kept in memory; written to disk with the next change
			return k
		}
	}
	return nil
}

func wmapiHandler(fn func(w http.ResponseWriter, r *http.Request, k *wmapiKey)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		k := wmapiAuth(r)
		if k == nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="flowsearch webmaster"`)
			wmErr(w, 401, "missing or invalid API key. Make one in the webmaster tools, on the API tab.")
			return
		}
		if !wmapiAllow(k.ID, "all", 0, time.Minute) {
			w.Header().Set("Retry-After", "60")
			wmErr(w, 429, "too many requests: at most 120 a minute per key")
			return
		}
		fn(w, r, k)
	}
}

// the site in the URL must be one of this account's sites
func wmapiSite(w http.ResponseWriter, r *http.Request, k *wmapiKey) (string, bool) {
	site, ok := normalizeSite(r.PathValue("site"))
	if !ok || !hasSite(k.User, site) {
		wmErr(w, 404, "no such site in this account")
		return "", false
	}
	return site, true
}

func wmapiReadBody(r *http.Request) map[string]string {
	out := map[string]string{}
	if strings.Contains(r.Header.Get("Content-Type"), "json") {
		var m map[string]any
		if json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4096)).Decode(&m) == nil {
			for k, v := range m {
				out[k] = fmt.Sprint(v)
			}
		}
		return out
	}
	r.ParseForm()
	for k := range r.Form {
		out[k] = r.Form.Get(k)
	}
	return out
}

func wmapiSiteInfo(user, site string) map[string]any {
	_, pages := ix.HostsWithSuffix(site)
	verified := false
	for _, o := range userSites(user) {
		if o.S == site {
			verified = o.V
		}
	}
	return map[string]any{"site": site, "pages_indexed": pages, "ownership_verified": verified, "removed": isBlocked(site)}
}

func wmapiMe(w http.ResponseWriter, r *http.Request, k *wmapiKey) {
	wmJSON(w, 200, map[string]any{
		"user": k.User, "key": map[string]any{"id": k.ID, "prefix": k.Prefix, "label": k.Label, "created": k.Created, "last_used": k.Last},
		"sites": len(userSites(k.User)), "site_limit": maxSites,
	})
}

func wmapiSites(w http.ResponseWriter, r *http.Request, k *wmapiKey) {
	out := []map[string]any{}
	for _, o := range userSites(k.User) {
		out = append(out, wmapiSiteInfo(k.User, o.S))
	}
	wmJSON(w, 200, map[string]any{"sites": out})
}

func wmapiAddSite(w http.ResponseWriter, r *http.Request, k *wmapiKey) {
	if !wmapiAllow(k.ID, "submit", 30, time.Hour) {
		wmErr(w, 429, "at most 30 site submissions an hour per key")
		return
	}
	body := wmapiReadBody(r)
	in := firstNonEmpty(body["site"], body["url"])
	site, ok := normalizeSite(in)
	if !ok {
		wmErr(w, 400, "give a website address in \"site\", like example.com")
		return
	}
	if !hasSite(k.User, site) && len(userSites(k.User)) >= maxSites {
		wmErr(w, 403, fmt.Sprintf("an account can add up to %d sites", maxSites))
		return
	}
	if isBlocked(site) {
		wmErr(w, 403, site+" was removed from Flowsearch by its owner")
		return
	}
	resp, err := safeClient.Get("https://" + site + "/")
	if err != nil {
		resp, err = safeClient.Get("http://" + site + "/")
	}
	if err != nil {
		wmErr(w, 422, "Flowsearch could not reach "+site+". Is the address right, and is the site public?")
		return
	}
	resp.Body.Close()
	lang := "any"
	de := body["lang"] == "de"
	if de {
		lang = "de"
	}
	if !hasSite(k.User, site) {
		recordSite(k.User, site, false)
	}
	sp := userSubmittedPath(k.User, de)
	if fileHas(sp, site) {
		wmJSON(w, 200, map[string]any{"site": site, "status": "already_listed", "note": "it is crawled again about once a week; use /recrawl to start one now"})
		return
	}
	appendLine(sp, site)
	triggerSiteCrawl(site, lang, site)
	wmJSON(w, 202, map[string]any{"site": site, "status": "queued", "language": lang})
}

func wmapiSiteGet(w http.ResponseWriter, r *http.Request, k *wmapiKey) {
	if site, ok := wmapiSite(w, r, k); ok {
		wmJSON(w, 200, wmapiSiteInfo(k.User, site))
	}
}

func wmapiPages(w http.ResponseWriter, r *http.Request, k *wmapiKey) {
	site, ok := wmapiSite(w, r, k)
	if !ok {
		return
	}
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n >= 1 && n <= 100 {
		limit = n
	}
	a := run("site:"+site, 1, limit, false)
	_, pages := ix.HostsWithSuffix(site)
	wmJSON(w, 200, map[string]any{"site": site, "pages_indexed": pages, "returned": len(a.Results), "pages": a.Results})
}

func wmapiRecrawl(w http.ResponseWriter, r *http.Request, k *wmapiKey) {
	site, ok := wmapiSite(w, r, k)
	if !ok {
		return
	}
	if !wmapiAllow(k.ID, "recrawl", 20, time.Hour) {
		wmErr(w, 429, "at most 20 recrawls an hour per key")
		return
	}
	lang := "any"
	if wmapiReadBody(r)["lang"] == "de" {
		lang = "de"
	}
	triggerSiteCrawl(site, lang, site)
	wmJSON(w, 202, map[string]any{"site": site, "status": "recrawl_started", "language": lang})
}

func wmapiCost(w http.ResponseWriter, r *http.Request, k *wmapiKey) {
	site, ok := wmapiSite(w, r, k)
	if !ok {
		return
	}
	visits := 1000
	if n, err := strconv.Atoi(r.URL.Query().Get("visits")); err == nil && n >= 1 && n <= 100_000_000 {
		visits = n
	}
	wmCostMu.Lock()
	c, cached := wmCostCache[site]
	wmCostMu.Unlock()
	if !cached || time.Since(c.At) >= 6*time.Hour {
		if !wmapiAllow(k.ID, "cost", 30, time.Hour) {
			wmErr(w, 429, "at most 30 fresh cost measurements an hour per key")
			return
		}
		var fresh bool
		c = wmCostMeasure(site, &fresh)
	}
	if c.Err != "" {
		wmErr(w, 502, "could not load the site to measure it: "+c.Err)
		return
	}
	perVisit := float64(c.Total) / 1e9 * wmCostEgressPerGB
	wmJSON(w, 200, map[string]any{
		"site": site, "bytes_per_visit": c.Total, "html_bytes": c.HTMLBytes, "linked_files": c.Files,
		"visits_per_hour": visits, "egress_usd_per_gb": wmCostEgressPerGB,
		"usd_per_1000_visits": perVisit * 1000, "usd_per_hour": perVisit * float64(visits), "usd_per_10_hours": perVisit * float64(visits) * 10,
		"measured_at": c.At.UTC().Format(time.RFC3339), "note": "bandwidth only; counts the home page and up to 25 files it links to",
	})
}

func registerWebmasterAPI() {
	http.HandleFunc("OPTIONS /api/webmaster/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.WriteHeader(204)
	})
	http.HandleFunc("GET /api/webmaster/me", wmapiHandler(wmapiMe))
	http.HandleFunc("GET /api/webmaster/sites", wmapiHandler(wmapiSites))
	http.HandleFunc("POST /api/webmaster/sites", wmapiHandler(wmapiAddSite))
	http.HandleFunc("GET /api/webmaster/sites/{site}", wmapiHandler(wmapiSiteGet))
	http.HandleFunc("GET /api/webmaster/sites/{site}/pages", wmapiHandler(wmapiPages))
	http.HandleFunc("POST /api/webmaster/sites/{site}/recrawl", wmapiHandler(wmapiRecrawl))
	http.HandleFunc("GET /api/webmaster/sites/{site}/cost", wmapiHandler(wmapiCost))
	http.HandleFunc("POST /webmaster/apikeys/create", wmapiCreateKey)
	http.HandleFunc("POST /webmaster/apikeys/revoke", wmapiRevokeKey)
}

// ---------------------------------------------------------------- the API tab

func wmapiBase(r *http.Request) string {
	scheme := "https"
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	} else if r.TLS == nil && (strings.HasPrefix(r.Host, "127.") || strings.HasPrefix(r.Host, "192.168.") || strings.HasPrefix(r.Host, "localhost")) {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

func wmapiTab(r *http.Request, user, csrf string) string {
	var rows strings.Builder
	for _, k := range wmapiKeysOf(user) {
		last := "never"
		if k.Last > 0 {
			last = time.Unix(k.Last, 0).Format("2 Jan 15:04")
		}
		rows.WriteString(fmt.Sprintf(`<tr><td><code>%s…</code><small>%s</small></td><td>%s</td><td>%s</td><td><form method=post action=/webmaster/apikeys/revoke><input type=hidden name=csrf value="%s"><input type=hidden name=id value="%s"><button class=alt>Revoke</button></form></td></tr>`,
			html.EscapeString(k.Prefix), html.EscapeString(k.Label), time.Unix(k.Created, 0).Format("2 Jan 2006"), last, csrf, html.EscapeString(k.ID)))
	}
	if rows.Len() == 0 {
		rows.WriteString(`<tr><td colspan=4>No API keys yet.</td></tr>`)
	}
	base := wmapiBase(r)
	return `<h1>API</h1><p class=lead>Manage your sites from scripts and other tools. Make a key, then send it with every request.</p>
<div class=panel><form class=addrow method=post action=/webmaster/apikeys/create><input type=hidden name=csrf value="` + csrf + `"><input type=text name=label maxlength=40 placeholder="What is this key for? (optional)" aria-label="Key label"><button>Make an API key</button></form></div>
<div class=panel><table class=gt><tr><th>Key</th><th>Made</th><th>Last used</th><th></th></tr>` + rows.String() + `</table><p style="margin:12px 0 0;color:var(--dim)">A key is shown only once, when you make it. Treat it like a password: anyone with it can manage your sites.</p></div>
<div class=panel><h2>Use it</h2><pre>curl -H "Authorization: Bearer fsk_YOUR_KEY" ` + base + `/api/webmaster/sites

# add a site (it is checked, then crawled)
curl -X POST -H "Authorization: Bearer fsk_YOUR_KEY" -H "Content-Type: application/json" \
  -d '{"site":"example.com"}' ` + base + `/api/webmaster/sites</pre>
<table class=gt>
<tr><td><code>GET /api/webmaster/me</code></td><td>who the key belongs to, and the limits</td></tr>
<tr><td><code>GET /api/webmaster/sites</code></td><td>your sites, with pages indexed</td></tr>
<tr><td><code>POST /api/webmaster/sites</code></td><td>add a site: <code>{"site":"example.com","lang":"any"}</code> (<code>"de"</code> for German pages only)</td></tr>
<tr><td><code>GET /api/webmaster/sites/{site}</code></td><td>one site: pages indexed, ownership proved, removed</td></tr>
<tr><td><code>GET /api/webmaster/sites/{site}/pages?limit=50</code></td><td>its best indexed pages (up to 100)</td></tr>
<tr><td><code>POST /api/webmaster/sites/{site}/recrawl</code></td><td>crawl it again now</td></tr>
<tr><td><code>GET /api/webmaster/sites/{site}/cost?visits=1000</code></td><td>what a visitor costs, per hour and per 10 hours</td></tr>
</table>
<p style="margin:12px 0 0;color:var(--dim)">There are no rate limits. An account can have up to 10 keys and 120 sites. Answers are JSON; errors look like <code>{"error":"…"}</code>.</p></div>`
}

func wmapiCreateKey(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == "" || !csrfOK(r, user) {
		http.Redirect(w, r, "/webmaster", http.StatusSeeOther)
		return
	}
	key, err := wmapiCreate(user, r.FormValue("label"))
	if err != nil {
		http.Redirect(w, r, "/webmaster?view=api&msg="+strings.ReplaceAll(err.Error(), " ", "+"), http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	base := wmapiBase(r)
	fmt.Fprint(w, wmPageHTML("API key", `<div class=gbody style="display:block;max-width:760px;margin:40px auto;padding:0 18px"><h1>Your API key</h1>
<p class=lead>Copy it now. Flowsearch keeps only a scrambled copy, so it cannot show it to you again.</p>
<div class=panel><pre style="font-size:16px;user-select:all">`+html.EscapeString(key)+`</pre></div>
<div class=panel><pre>curl -H "Authorization: Bearer `+html.EscapeString(key)+`" `+base+`/api/webmaster/me</pre></div>
<p><a class=btn href="/webmaster?view=api">Done</a></p></div>`))
}

func wmapiRevokeKey(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == "" || !csrfOK(r, user) {
		http.Redirect(w, r, "/webmaster", http.StatusSeeOther)
		return
	}
	msg := "Could+not+find+that+key."
	if wmapiRevoke(user, r.FormValue("id")) {
		msg = "Key+revoked."
	}
	http.Redirect(w, r, "/webmaster?view=api&msg="+msg, http.StatusSeeOther)
}
