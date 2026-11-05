package main

// Webmaster tools for people who run a website: check how Flowsearch sees it, submit it to be crawled (by its own
// crawler, the service sgcrawl-sites), and, after proving ownership, remove it from the results or restore it.

import (
	"bytes"
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"mysearch/sgindex"
)

var (
	submittedPath       = fsData + "/submitted.txt"    // combined file, regenerated from per-user files below
	submittedDePath     = fsData + "/submitted-de.txt" // combined file, regenerated from per-user files below
	submittedUsersDir   = fsData + "/submitted-users/"
	submittedUsersDeDir = fsData + "/submitted-users-de/"
	blockedPath         = fsData + "/blocked.txt"
)

// userSubmittedPath is where one account's own submitted sites live, so one user's list is never mixed
// into another's (the bug: everyone's sites used to share a single submitted.txt).
func userSubmittedPath(user string, de bool) string {
	dir := submittedUsersDir
	if de {
		dir = submittedUsersDeDir
	}
	os.MkdirAll(dir, 0o755)
	return dir + user + ".txt"
}

// regenerateCombined rebuilds submitted.txt/submitted-de.txt from every account's own file, deduped,
// so the crawler still gets one list to work from while storage stays separated per account.
func regenerateCombined() {
	build := func(dir, out string) {
		seen := map[string]bool{}
		var lines []string
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			f, err := os.Open(dir + e.Name())
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				l := strings.TrimSpace(sc.Text())
				if l != "" && !seen[l] {
					seen[l] = true
					lines = append(lines, l)
				}
			}
			f.Close()
		}
		os.WriteFile(out, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	}
	build(submittedUsersDir, submittedPath)
	build(submittedUsersDeDir, submittedDePath)
	loadSubmittedHosts()
}

var (
	wmMu          sync.Mutex
	blockedSet    = map[string]bool{}
	submittedSet  = map[string]bool{} // every host a webmaster submitted (crawled with -lang any, see isEnglish bypass)
	wmHits        = map[string][]time.Time{}
	siteRe        = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*\.[a-z]{2,}$`)
)

// loadSubmittedHosts rebuilds the in-memory set of webmaster-submitted hosts from the combined submitted.txt
// files, so search() can skip the isEnglish() filter for them (they were crawled with -lang any for a reason:
// sites like shortypower.org are genuinely English but too data-dense in tables/numbers to pass the heuristic).
func loadSubmittedHosts() {
	m := map[string]bool{}
	for _, p := range []string{submittedPath, submittedDePath} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			l := strings.TrimSpace(sc.Text())
			if l == "" {
				continue
			}
			if i := strings.IndexByte(l, '\t'); i >= 0 {
				l = l[:i]
			}
			m[strings.ToLower(strings.TrimPrefix(l, "www."))] = true
		}
		f.Close()
	}
	wmMu.Lock()
	submittedSet = m
	wmMu.Unlock()
}

// isSubmittedHost reports whether a host (or its parent domain) was submitted through webmaster tools.
func isSubmittedHost(host string) bool {
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	wmMu.Lock()
	defer wmMu.Unlock()
	for h := host; h != ""; {
		if submittedSet[h] {
			return true
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			break
		}
		h = h[i+1:]
	}
	return false
}

func loadBlocked() {
	m := map[string]bool{}
	if f, err := os.Open(blockedPath); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if d := strings.TrimSpace(sc.Text()); d != "" {
				m[d] = true
			}
		}
		f.Close()
	}
	wmMu.Lock()
	blockedSet = m
	wmMu.Unlock()
}

// isBlocked reports whether a host belongs to a site whose owner asked for it to be left out of the results.
func isBlocked(host string) bool {
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	wmMu.Lock()
	defer wmMu.Unlock()
	for h := host; h != ""; {
		if blockedSet[h] {
			return true
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			break
		}
		h = h[i+1:]
	}
	return false
}

// normalizeSite turns what a person typed (example.com, https://www.example.com/page) into example.com.
func normalizeSite(in string) (string, bool) {
	in = strings.TrimSpace(strings.ToLower(in))
	if in == "" || len(in) > 200 {
		return "", false
	}
	if !strings.Contains(in, "://") {
		in = "https://" + in
	}
	u, err := url.Parse(in)
	if err != nil {
		return "", false
	}
	h := strings.TrimPrefix(u.Hostname(), "www.")
	if len(h) > 100 || !siteRe.MatchString(h) || net.ParseIP(h) != nil {
		return "", false
	}
	return h, true
}

func wmLimited(r *http.Request, cap int) bool {
	ip := clientIP(r)
	wmMu.Lock()
	defer wmMu.Unlock()
	now := time.Now()
	h := wmHits[ip][:0]
	for _, t := range wmHits[ip] {
		if now.Sub(t) < time.Hour {
			h = append(h, t)
		}
	}
	h = append(h, now)
	wmHits[ip] = h
	return len(h) > cap
}

func fileHas(path, line string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == line {
			return true
		}
	}
	return false
}

func appendLine(path, line string) {
	wmMu.Lock()
	defer wmMu.Unlock()
	if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.WriteString(line + "\n")
		f.Close()
	}
}

// ---- webmaster submissions now run on GitHub Actions (crawl-submitted.yml), not a local crawler ----

const (
	flowRepo = "Luishae07/flowsearch"
)

func ghCmd(args ...string) *exec.Cmd {
	c := exec.Command("gh", args...)
	tok, _ := os.ReadFile(fsSecrets + "/github_token.txt")
	c.Env = append(os.Environ(), "HOME=/root", "GH_TOKEN="+strings.TrimSpace(string(tok)))
	return c
}

// pushRepoFile uploads localPath's current content to repoPath in flowRepo, creating or updating it.
func pushRepoFile(localPath, repoPath string) error {
	content, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	api := "repos/" + flowRepo + "/contents/" + repoPath
	sha, _ := ghCmd("api", api, "-q", ".sha").Output()
	enc := base64.StdEncoding.EncodeToString(content)
	args := []string{"api", "-X", "PUT", api, "-f", "message=Update " + repoPath + " from webmaster tools", "-f", "content=" + enc}
	if s := strings.TrimSpace(string(sha)); s != "" {
		args = append(args, "-f", "sha="+s)
	}
	return ghCmd(args...).Run()
}

// siteWorkflowFile is a safe GitHub Actions filename for one submitted site's own workflow.
// Dots in a hostname are fine in a filename, so the site name is used as-is.
func siteWorkflowFile(site string) string {
	return "crawl-site-" + site + ".yml"
}

const siteWorkflowTemplate = `name: Flowsearch crawl - %s
on:
  workflow_dispatch:

jobs:
  crawl:
    runs-on: ubuntu-latest
    timeout-minutes: 60
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - name: build
        working-directory: crawler
        run: CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o sgcrawl-bin ./sgcrawl
      - name: crawl
        working-directory: crawler
        env:
          MYSEARCH_TOKEN: ${{ secrets.MYSEARCH_TOKEN }}
        run: |
          URL=$(cat ../tunnel-url.txt)
          echo "$MYSEARCH_TOKEN" > token.txt
          ./sgcrawl-bin -lang %s -seeds sites/%s.txt \
            -ingest "$URL/api/ingest" -token-file token.txt \
            -workers 500 -hours 1 -maxpages 5000
`

// ensureSiteWorkflow writes (or overwrites) this one site's own workflow file in the repo. Called every
// time the site is submitted/recrawled/gets a sitemap, so the workflow always exists before we run it.
func ensureSiteWorkflow(site, lang string) error {
	yaml := fmt.Sprintf(siteWorkflowTemplate, site, lang, site)
	tmp := "/tmp/flowsite-" + site + ".yml"
	if err := os.WriteFile(tmp, []byte(yaml), 0o644); err != nil {
		return err
	}
	return pushRepoFile(tmp, ".github/workflows/"+siteWorkflowFile(site))
}

// triggerSiteCrawl pushes a one-line seed file for just this site to the repo, makes sure the site's own
// workflow file exists, and runs it - a separate GitHub Actions workflow per site, not per account,
// not a shared one, instead of the old local crawler. line is the seed file's content: usually just the
// site's URL, or "site<TAB>sitemap-address" when a sitemap was sent.
func triggerSiteCrawl(site, lang, line string) {
	go func() {
		tmp := "/tmp/flowsiteseed-" + site + ".txt"
		if err := os.WriteFile(tmp, []byte(line+"\n"), 0o644); err != nil {
			return
		}
		if err := pushRepoFile(tmp, "crawler/sites/"+site+".txt"); err != nil {
			return
		}
		if err := ensureSiteWorkflow(site, lang); err != nil {
			return
		}
		ghCmd("workflow", "run", siteWorkflowFile(site), "--repo", flowRepo).Run()
	}()
}

// takeScreenshot asks Cloudflare Browser Run (a real headless Chrome) for a screenshot of the site and
// keeps it on disk; it answers in a few seconds, no CI job to wait for.
var shotMu sync.Mutex
var shotLast = map[string]time.Time{}

func shotPath(site string) string { return fsShots + "/" + site + ".jxl" }

func takeScreenshot(site string) error {
	img, err := buShot(site)
	if err != nil {
		return err
	}
	saved := false
	if bot, d := botCheckShot(img); bot {
		copyImg, cerr := savedCopyShot("https://" + site + "/")
		if cerr != nil {
			return fmt.Errorf("%w (distance %.3f) %v", errBotCheck, d, cerr)
		}
		img, saved = copyImg, true
	}
	var out, errb bytes.Buffer
	enc := exec.Command("cjxl", "-", "-", "-q", "90", "-e", "7") // PNG from the browser in, JPEG XL out
	enc.Stdin, enc.Stdout, enc.Stderr = bytes.NewReader(img), &out, &errb
	if err := enc.Run(); err != nil || out.Len() == 0 {
		return fmt.Errorf("JPEG XL encoding failed: %v %s", err, strings.TrimSpace(errb.String()))
	}
	os.MkdirAll(fsShots + "", 0o755)
	if err := os.WriteFile(shotPath(site), out.Bytes(), 0o644); err != nil {
		return err
	}
	markSavedCopy(fsShots + "/"+site+".wb", saved)
	return nil
}

func screenshotURL(site string) string {
	return "/webmaster/shot?site=" + url.QueryEscape(site)
}

// wmShot serves the latest screenshot of a site.
func wmShot(w http.ResponseWriter, r *http.Request) {
	site, ok := normalizeSite(r.FormValue("site"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(shotPath(site))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "image/jxl")
	w.Header().Set("Cache-Control", "no-cache")
	io.Copy(w, f)
}

// wmScreenshot triggers a fresh screenshot of the current site.
func wmScreenshot(w http.ResponseWriter, r *http.Request) {
	back := func(m string) {
		http.Redirect(w, r, "/webmaster?view=live&site="+url.QueryEscape(r.FormValue("site"))+"&msg="+url.QueryEscape(m), http.StatusSeeOther)
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
	shotMu.Lock()
	busy := !shotLast[user].IsZero()
	shotLast[user] = time.Now()
	shotMu.Unlock()
	if busy {
		back("Your last screenshot is still being taken - wait for it to finish.")
		return
	}
	defer func() { shotMu.Lock(); delete(shotLast, user); shotMu.Unlock() }()
	if err := takeScreenshot(site); err != nil {
		back("The screenshot of " + site + " did not work (" + err.Error() + ").")
		return
	}
	back("Fresh screenshot of " + site + ".")
}

// wmLiveWS is a tiny own WebSocket (reuses wsFrame from main.go), one per open "Live" tab: every 500ms it
// sends the current page count for that site, no hub/broadcast needed since each connection has its own ticker.
func wmLiveWS(w http.ResponseWriter, r *http.Request) {
	site, ok := normalizeSite(r.URL.Query().Get("site"))
	if !ok {
		http.Error(w, "bad site", 400)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || key == "" {
		http.Error(w, "WebSocket only", 400)
		return
	}
	hj, ok2 := w.(http.Hijacker)
	if !ok2 {
		http.Error(w, "no hijack", 500)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Time{})
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	if brw.Flush() != nil {
		return
	}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for range tick.C {
		_, pages := ix.HostsWithSuffix(site)
		b, _ := json.Marshal(map[string]int{"pages": pages})
		if _, err := conn.Write(wsFrame(b)); err != nil {
			return
		}
	}
}

// dropDoneSeed removes site's line from a done_seeds.tsv (first column) so the crawler treats it as due again.
func dropDoneSeed(path, site string) {
	wmMu.Lock()
	defer wmMu.Unlock()
	f, err := os.Open(path)
	if err != nil {
		return
	}
	var keep []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		col, _, _ := strings.Cut(l, "\t")
		if col != site {
			keep = append(keep, l)
		}
	}
	f.Close()
	os.WriteFile(path, []byte(strings.Join(keep, "\n")+"\n"), 0o644)
}

// ---- proving ownership: a code derived from the site name and a secret only we know ----

func ownerToken(user, site string) string {
	m := hmac.New(sha256.New, secretKey())
	m.Write([]byte("owner:" + user + ":" + site))
	return "sg225-" + hex.EncodeToString(m.Sum(nil))[:24]
}

// safeClient never connects to this machine or the local network, so it cannot be turned against them.
var safeClient = &http.Client{
	Timeout: 8 * time.Second,
	Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second, Control: func(network, addr string, c syscall.RawConn) error {
		host, _, _ := net.SplitHostPort(addr)
		ip := net.ParseIP(host)
		if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return errors.New("not a public address")
		}
		return nil
	}}).DialContext},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		return nil
	},
}

// verifyOwner looks for the code at /sg225-verify.txt, or anywhere on the front page (a meta tag, a comment ...).
func verifyOwner(user, site string) bool {
	tok := ownerToken(user, site)
	for _, u := range []string{"https://" + site + "/sg225-verify.txt", "http://" + site + "/sg225-verify.txt", "https://" + site + "/", "https://www." + site + "/", "http://" + site + "/"} {
		resp, err := safeClient.Get(u)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == 200 && strings.Contains(string(b), tok) {
			return true
		}
	}
	return false
}

// ---- pages ----

// siteIcon is the site's own icon from Google's favicon service. No-referrer: Google must not learn which Flowsearch page it was asked from.
func siteIcon(site, cls string) string {
	return `<img class="sico ` + cls + `" src="https://www.google.com/s2/favicons?domain=` + url.QueryEscape(site) + `&sz=64" width=40 height=40 alt="" referrerpolicy="no-referrer" loading="lazy" onerror="this.style.visibility='hidden'">`
}

var distinctMu sync.Mutex
var distinctCache = map[string]struct {
	at  time.Time
	ids []uint32
}{}

// distinctPages lists the stored pages of a site where the same page stored again under other sort or filter
// options in its address (?s=17&o=d, ?s=6&o=d ...) counts once. A page without a query wins over its variants.
func distinctPages(site string) []uint32 {
	site = strings.TrimPrefix(site, "www.")
	distinctMu.Lock()
	if c, ok := distinctCache[site]; ok && time.Since(c.at) < 60*time.Second {
		distinctMu.Unlock()
		return c.ids
	}
	distinctMu.Unlock()
	all, _ := ix.HostDocPage(site, 0, 1<<30)
	type page struct {
		id  uint32
		key string
		q   bool
	}
	var ps []page
	for _, id := range all {
		d, ok := ix.Doc(id)
		if !ok {
			continue
		}
		rec := load(d)
		if rec.URL == "" { // an erased page
			continue
		}
		u, err := url.Parse(strings.ReplaceAll(rec.URL, "&amp;", "&"))
		if err != nil {
			continue
		}
		ps = append(ps, page{id, d.Host + "\x00" + u.Path + "\x00" + strings.Join(sgindex.Tokens(rec.Title), " "), u.RawQuery != ""})
	}
	seen := map[string]bool{}
	var out []uint32
	for _, withQuery := range []bool{false, true} {
		for _, p := range ps {
			if p.q != withQuery || seen[p.key] {
				continue
			}
			seen[p.key] = true
			out = append(out, p.id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	distinctMu.Lock()
	distinctCache[site] = struct {
		at  time.Time
		ids []uint32
	}{time.Now(), out}
	distinctMu.Unlock()
	return out
}

func wmPageHTML(title, body string) string {
	css := `<style>
html:has(.wmg){color-scheme:dark;--bg:#1a1b1e;--bg2:#222326;--card:#26272b;--ink:#e8eaed;--dim:#9aa0a6;--line:#38393d;--acc:#8ab4f8;--acc2:#c58af8;--acc-ink:#1a1b1e;--link:#8ab4f8;--glow:#2a3a52;--hover:#303136}
.wm{max-width:1180px;margin:0 auto;font-family:"Google Sans",Roboto,system-ui,sans-serif;color:var(--ink);padding-bottom:30px}
.wm h1{font-size:28px;font-weight:400;margin:0 0 6px;letter-spacing:0}
.wm h2{font-size:16px;font-weight:500;margin:0 0 10px}
.wm .lead{color:var(--dim);font-size:16px;margin:0 0 20px;line-height:1.5}
.wm a{color:var(--link);transition:color .12s ease}
.wm .box,.wm .panel{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:20px 24px;margin:0 0 18px;transition:border-color .2s ease,box-shadow .2s ease}
.wm .box:hover,.wm .panel:hover{border-color:var(--border2,#4a4b50);box-shadow:0 8px 24px -16px rgba(138,180,248,.25)}
.wm .box p,.wm .panel p{margin:6px 0 14px;color:var(--dim);line-height:1.55}
.wm form{display:flex;gap:10px;flex-wrap:wrap}
.wm form.stack{flex-direction:column}
.wm form[hidden]{display:none}
.wm input[type=text],.wm input[type=password],.wm select{flex:1;min-width:220px;background:var(--bg2);border:1px solid var(--line);color:var(--ink);border-radius:8px;padding:13px 15px;font:inherit;transition:border-color .12s ease,box-shadow .12s ease}
.wm input:focus,.wm select:focus{outline:none;border-color:var(--acc);box-shadow:0 0 0 3px rgba(138,180,248,.18)}
.wm button,.wm .btn{background:linear-gradient(135deg,var(--acc),var(--acc2));color:var(--acc-ink);border:0;border-radius:20px;padding:10px 24px;font:inherit;font-weight:500;cursor:pointer;text-decoration:none;display:inline-block;transition:transform .1s ease,box-shadow .15s ease,filter .15s ease;box-shadow:0 4px 14px -6px rgba(138,180,248,.5)}
.wm button:hover,.wm .btn:hover{filter:brightness(1.08);box-shadow:0 6px 18px -6px rgba(138,180,248,.6);transform:translateY(-1px)}
.wm button:active,.wm .btn:active{transform:translateY(0)}
.wm button.alt,.wm .btn.alt{background:var(--card);color:var(--acc);border:1px solid var(--line);box-shadow:none}
.wm button.alt:hover{background:var(--glow);box-shadow:none;transform:none}
.wm .msg{background:var(--glow);border:1px solid var(--acc);border-radius:10px;padding:12px 16px;margin:0 0 16px}
.wm .stat{display:flex;gap:12px;flex-wrap:wrap;margin:12px 0}.wm .stat div{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:12px 18px}
.wm .stat b{display:block;font-size:28px;font-weight:400;background:linear-gradient(135deg,var(--acc),var(--acc2));-webkit-background-clip:text;background-clip:text;color:transparent}.wm .stat small{color:var(--dim)}
.wm ul.pages{list-style:none;margin:10px 0 0;padding:0}.wm ul.pages li{padding:12px 0;border-top:1px solid var(--line);overflow-wrap:anywhere;transition:background .12s ease}.wm ul.pages li:hover{background:var(--hover)}
.wm code{background:var(--hover);border-radius:4px;padding:1px 6px}.wm pre{margin:10px 0 0;background:var(--hover);border-radius:10px;padding:13px 16px;overflow-x:auto}
.gsign{min-height:62vh;display:grid;place-items:center}
.gsigncard{width:min(450px,100%);background:var(--card);border:1px solid var(--line);border-radius:28px;padding:40px 40px 34px;box-shadow:0 20px 50px -24px rgba(0,0,0,.6)}
.gsigncard .glogo{font-size:26px;font-weight:700;letter-spacing:-.02em;margin-bottom:14px}.gsigncard .glogo span{background:linear-gradient(135deg,var(--acc),var(--acc2));-webkit-background-clip:text;background-clip:text;color:transparent}
.gsigncard h1{font-size:24px;text-align:left;margin:0 0 6px}
.gsigncard .sub{color:var(--dim);margin:0 0 24px}
.gsigncard .switch{margin:18px 0 0;display:flex;justify-content:space-between;align-items:center;gap:12px}
.gsigncard .switch a{font-weight:500;text-decoration:none;cursor:pointer}
.gtop{display:flex;align-items:center;gap:14px;padding:6px 0 20px;flex-wrap:wrap}
.gtop select{flex:none;min-width:260px;max-width:100%}
.gtop .sp{flex:1}
.gtop .me{display:flex;align-items:center;gap:10px;color:var(--dim);font-size:14px}
.gtop .av{width:34px;height:34px;border-radius:50%;background:linear-gradient(135deg,var(--acc),var(--acc2));color:var(--acc-ink);display:grid;place-items:center;font-weight:500}
.gbody{display:grid;grid-template-columns:220px minmax(0,1fr);gap:28px}
.gside{display:flex;flex-direction:column;gap:2px;align-self:start}
.gside a{padding:11px 18px;border-radius:0 24px 24px 0;color:var(--ink);text-decoration:none;font-weight:500;font-size:15px;transition:background .12s ease,color .12s ease}
.gside a:hover{background:var(--hover)}
.gside a.on{background:var(--glow);color:var(--acc)}
.gcards{display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:16px;margin-bottom:18px}
.gstat{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:18px 20px;transition:transform .15s ease,box-shadow .15s ease}
.gstat:hover{transform:translateY(-2px);box-shadow:0 10px 26px -16px rgba(138,180,248,.3)}
.gstat small{color:var(--dim);font-size:13px;display:block;margin-bottom:8px}
.gstat b{font-size:32px;font-weight:400;display:block;line-height:1.1}
.gstat .ok,.panel .ok{color:#81c995}.gstat .bad,.panel .bad{color:#f28b82}
table.gt{width:100%;border-collapse:collapse}
table.gt th{text-align:left;font-weight:500;color:var(--dim);font-size:13px;padding:10px 8px;border-bottom:1px solid var(--line)}
table.gt td{padding:11px 8px;border-bottom:1px solid var(--line);overflow-wrap:anywhere;font-size:14px;vertical-align:top}
table.gt tr:hover td{background:var(--hover)}
table.gt td small{display:block;color:var(--dim)}
.addrow input[type=text]{font-size:16px}
@media (max-width:820px){.gbody{grid-template-columns:1fr}.gside{flex-direction:row;overflow-x:auto}.gside a{border-radius:20px;white-space:nowrap}.gsigncard{padding:28px 22px}}
/* second pass: icons, depth and motion */
.gside a{display:flex;align-items:center;gap:12px;position:relative}
.gside a:before{content:"";width:20px;height:20px;flex:none;background:currentColor;opacity:.75;-webkit-mask:var(--ic) center/contain no-repeat;mask:var(--ic) center/contain no-repeat}
.gside a.on:before,.gside a:hover:before{opacity:1}
.gside a.on{background:linear-gradient(90deg,var(--glow),transparent);box-shadow:inset 3px 0 0 var(--acc)}
.gside a[href*="view=overview&"]{--ic:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath fill='black' d='M3 13h8V3H3v10zm0 8h8v-6H3v6zm10 0h8V11h-8v10zm0-18v6h8V3h-8z'/%3E%3C/svg%3E")}
.gside a[href*="view=pages&"]{--ic:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath fill='black' d='M6 2h9l5 5v15H6V2zm8 1.5V8h4.5L14 3.5zM8 12h8v2H8v-2zm0 4h8v2H8v-2z'/%3E%3C/svg%3E")}
.gside a[href*="view=cost&"]{--ic:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath fill='black' d='M12 2a10 10 0 100 20 10 10 0 000-20zm1 15.9V19h-2v-1.1c-1.6-.3-2.7-1.3-2.8-2.9h2c.1.8.7 1.3 1.8 1.3 1 0 1.6-.4 1.6-1.1 0-.7-.5-1-1.9-1.3-1.8-.4-3.3-1-3.3-2.8 0-1.4 1.1-2.4 2.6-2.7V7h2v1.1c1.4.3 2.4 1.2 2.5 2.6h-2c-.1-.7-.6-1.1-1.5-1.1-.9 0-1.4.4-1.4 1 0 .6.5.9 1.8 1.2 2 .5 3.4 1.1 3.4 2.9 0 1.4-1.1 2.5-2.8 2.8z'/%3E%3C/svg%3E")}
.gside a[href*="view=api&"]{--ic:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath fill='black' d='M9.4 16.6L4.8 12l4.6-4.6L8 6l-6 6 6 6 1.4-1.4zm5.2 0l4.6-4.6-4.6-4.6L16 6l6 6-6 6-1.4-1.4z'/%3E%3C/svg%3E")}
.gside a[href*="view=live&"]{--ic:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath fill='black' d='M12 8a4 4 0 100 8 4 4 0 000-8zM6.3 6.3a8 8 0 000 11.4l1.4-1.4a6 6 0 010-8.6L6.3 6.3zm11.4 0l-1.4 1.4a6 6 0 010 8.6l1.4 1.4a8 8 0 000-11.4z'/%3E%3C/svg%3E")}
.gside a[href*="view=browse&"]{--ic:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath fill='black' d='M3 4h18a1 1 0 011 1v14a1 1 0 01-1 1H3a1 1 0 01-1-1V5a1 1 0 011-1zm1 4v10h16V8H4zm2-2.5a.8.8 0 100 1.6.8.8 0 000-1.6zm3 0a.8.8 0 100 1.6.8.8 0 000-1.6z'/%3E%3C/svg%3E")}
.gside a[href*="view=sitemaps&"]{--ic:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath fill='black' d='M12 2l3 3h-2v3h6a2 2 0 012 2v3h2l-3 3-3-3h2v-3h-6v3h2l-3 3-3-3h2v-3H5v3h2l-3 3-3-3h2v-3a2 2 0 012-2h6V5H9l3-3z'/%3E%3C/svg%3E")}
.gside a[href*="view=robots&"]{--ic:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath fill='black' d='M12 2a2 2 0 011 3.7V7h4a3 3 0 013 3v7a3 3 0 01-3 3H7a3 3 0 01-3-3v-7a3 3 0 013-3h4V5.7A2 2 0 0112 2zM9 11.5a1.5 1.5 0 100 3 1.5 1.5 0 000-3zm6 0a1.5 1.5 0 100 3 1.5 1.5 0 000-3zM9 17h6v1.5H9V17z'/%3E%3C/svg%3E")}
.gside a[href*="view=notifications&"]{--ic:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath fill='black' d='M12 22a2 2 0 002-2h-4a2 2 0 002 2zm6-6V11a6 6 0 00-5-5.9V4a1 1 0 10-2 0v1.1A6 6 0 006 11v5l-2 2v1h16v-1l-2-2z'/%3E%3C/svg%3E")}
.gside a[href*="view=removals&"]{--ic:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath fill='black' d='M6 7h12l-1 13H7L6 7zm3-4h6l1 2h4v2H4V5h4l1-2z'/%3E%3C/svg%3E")}

.gtop .btn.alt{padding:9px 18px}
.wm h1{font-size:30px;font-weight:500;letter-spacing:-.4px;margin-bottom:8px}
.wm h1:after{content:"";display:block;width:44px;height:3px;border-radius:3px;margin-top:10px;background:linear-gradient(90deg,var(--acc),var(--acc2))}
.wm h2{font-weight:600;letter-spacing:.1px}
.wm .box,.wm .panel{background:linear-gradient(180deg,#2a2b30,var(--card));box-shadow:0 1px 0 #ffffff08 inset,0 10px 30px -22px #000}
.gstat{background:linear-gradient(180deg,#2c2d32,var(--card));position:relative;overflow:hidden}
.gstat:before{content:"";position:absolute;left:0;top:0;right:0;height:3px;background:linear-gradient(90deg,var(--acc),var(--acc2));opacity:.8}
.gstat b{font-weight:500;letter-spacing:-.5px}
table.gt{border-radius:10px;overflow:hidden}
table.gt th{text-transform:uppercase;font-size:11px;letter-spacing:.6px;background:#00000018}
table.gt td a{text-decoration:none;font-weight:500}table.gt td a:hover{text-decoration:underline}
.wm input[type=text]::placeholder,.wm input[type=password]::placeholder{color:#767b82}
.wm button:focus-visible,.wm .btn:focus-visible,.gside a:focus-visible{outline:2px solid var(--acc);outline-offset:2px}
.wm .msg{border-left:4px solid var(--acc)}
#gsection>*{animation:wmIn .28s ease both}
#gsection>*:nth-child(2){animation-delay:.04s}#gsection>*:nth-child(3){animation-delay:.08s}#gsection>*:nth-child(n+4){animation-delay:.12s}
@keyframes wmIn{from{opacity:0;transform:translateY(8px)}to{opacity:1;transform:none}}
@media (prefers-reduced-motion:reduce){#gsection>*{animation:none}.wm button,.wm .btn,.gstat{transition:none}}
@media (max-width:820px){.gside a{padding:9px 14px}.gside a.on{box-shadow:none;background:var(--glow)}}

/* robots.txt tab */
.rbscore{display:flex;align-items:center;gap:18px;margin-bottom:14px}
.ring{--c:#81c995;width:76px;height:76px;border-radius:50%;background:conic-gradient(var(--c) calc(var(--p)*1%),#ffffff14 0);display:grid;place-items:center;position:relative;flex:none}
.ring:before{content:"";position:absolute;inset:7px;border-radius:50%;background:var(--card)}
.ring.warn{--c:#fdd663}.ring.bad{--c:#f28b82}
.ring b,.ring small{position:relative;grid-area:1/1}.ring b{font-size:24px;font-weight:500;transform:translateY(-4px)}.ring small{font-size:11px;color:var(--dim);transform:translateY(14px)}
ul.issues{list-style:none;margin:0;padding:0;display:flex;flex-direction:column;gap:8px}
ul.issues li{display:flex;gap:12px;align-items:flex-start;padding:11px 13px;border-radius:10px;background:var(--bg2);border:1px solid var(--line);line-height:1.5}
ul.issues li i{flex:none;width:22px;height:22px;border-radius:50%;display:grid;place-items:center;font-style:normal;font-weight:700;font-size:13px;margin-top:1px}
ul.issues li.good i{background:#81c99533;color:#81c995}ul.issues li.good i:before{content:"\2713"}
ul.issues li.bad i{background:#f28b8233;color:#f28b82}ul.issues li.bad i:before{content:"!"}
.fixbox{margin-top:14px}.fixbox summary{cursor:pointer;font-weight:500;color:var(--acc)}
.rbres{display:flex;align-items:center;gap:12px;margin-top:14px;padding:12px 14px;border-radius:10px;background:var(--bg2);border:1px solid var(--line)}
.rbres .w{font-weight:600;padding:3px 12px;border-radius:99px}.rbres.ok .w{background:#81c99530;color:#81c995}.rbres.bad .w{background:#f28b8230;color:#f28b82}
.chips{display:flex;gap:8px;flex-wrap:wrap;margin-top:12px}
.chip{padding:5px 12px;border-radius:99px;background:var(--bg2);border:1px solid var(--line);text-decoration:none;font-family:ui-monospace,Menlo,monospace;font-size:13px}
.chip:hover{border-color:var(--acc);background:var(--glow)}
.pill{padding:2px 10px;border-radius:99px;font-size:12px;font-weight:600}.pill.good{background:#81c99530;color:#81c995}.pill.bad{background:#f28b8230;color:#f28b82}
table.rbrules td:first-child{width:56px;color:var(--dim)}
.rbfile{counter-reset:l;font:13px/1.65 ui-monospace,Menlo,Consolas,monospace;background:var(--bg2);border:1px solid var(--line);border-radius:10px;padding:10px 0;overflow-x:auto;max-height:520px;overflow-y:auto}
.rbfile .ln{display:block;counter-increment:l;padding:0 14px 0 0;white-space:pre}
.rbfile .ln:before{content:counter(l);display:inline-block;width:48px;padding-right:14px;text-align:right;color:#6b7075;user-select:none}
.rbfile .c{color:#7d8590;font-style:italic}.rbfile .k{color:#c58af8}
.rbfile .d-disallow .k{color:#f28b82}.rbfile .d-allow .k{color:#81c995}.rbfile .d-sitemap .k{color:#8ab4f8}.rbfile .d-user-agent .k{color:#fdd663}

/* third pass: glow, glass cards, better inputs (no layout changes) */
html:has(.wmg){background:var(--bg)}
html:has(.wmg) body{background:radial-gradient(900px 520px at 12% -8%,#2b3d5c66,transparent 70%),radial-gradient(820px 480px at 92% 0,#4b2f6c40,transparent 70%),var(--bg) no-repeat}
html:has(.wmg) *{scrollbar-width:thin;scrollbar-color:#4a4c53 transparent}
.wm{font-family:Inter,"Google Sans",Roboto,system-ui,sans-serif}
.wm h1{background:linear-gradient(90deg,#fff 30%,#b9d0ff);-webkit-background-clip:text;background-clip:text;color:transparent}
.wm .lead{max-width:72ch}
.wm .box,.wm .panel{background:linear-gradient(180deg,#ffffff0b,#ffffff03),var(--card);border:1px solid #ffffff14;border-radius:16px;padding:22px 26px;box-shadow:0 1px 0 #ffffff0d inset,0 20px 44px -30px #000}
.wm .box:hover,.wm .panel:hover{border-color:#8ab4f85a;box-shadow:0 1px 0 #ffffff0d inset,0 24px 50px -28px #8ab4f855}
.gstat{border-radius:16px;border:1px solid #ffffff14;background:linear-gradient(180deg,#ffffff0b,#ffffff03),var(--card)}
.gstat b{font-variant-numeric:tabular-nums}
.wm input[type=text],.wm input[type=password],.wm select{background:#17181b;border-radius:12px;padding:14px 16px}
.wm select{appearance:none;-webkit-appearance:none;padding-right:42px;background-image:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath fill='%239aa0a6' d='M7 10l5 5 5-5z'/%3E%3C/svg%3E");background-repeat:no-repeat;background-position:right 12px center;background-size:22px;cursor:pointer}
.wm button,.wm .btn{background:linear-gradient(135deg,#9cc2ff,#c58af8);font-weight:600;letter-spacing:.2px;padding:11px 24px}
.wm button.alt,.wm .btn.alt{background:#ffffff0a;border:1px solid #ffffff1f;color:var(--ink)}
.wm button.alt:hover,.wm .btn.alt:hover{background:#8ab4f81f;border-color:#8ab4f870}
.wm code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:.9em;background:#ffffff12;border:1px solid #ffffff0f}
.wm pre{background:#14151a;border:1px solid #ffffff12;font:13px/1.6 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.gside a{border-radius:12px;margin-right:6px}
.gside a.on{background:linear-gradient(90deg,#8ab4f82a,#c58af81a);box-shadow:none;color:#fff}
.gside a.on:before{color:var(--acc);opacity:1}
table.gt td{padding:13px 12px}
table.gt tr:hover td{background:#ffffff08}
.gsigncard{box-shadow:0 0 0 1px #ffffff12,0 30px 80px -34px #8ab4f866;background:linear-gradient(180deg,#ffffff0d,#ffffff03),var(--card)}
.wm .msg{border-radius:12px;background:linear-gradient(90deg,#8ab4f830,#8ab4f80a)}
.wm .stat div{border-radius:14px;transition:transform .15s ease}.wm .stat div:hover{transform:translateY(-2px)}
.wm ul.pages li{padding:13px 10px;border-radius:8px}
.wm a{text-underline-offset:3px}

/* v2: a completely new look - near-black, lime and teal, top dock instead of a sidebar, bento cards */
html:has(.wmg){--bg:#090b0e;--bg2:#0e1217;--card:#10151b;--ink:#eef3f8;--dim:#8794a6;--line:#212a35;--acc:#b8ff5a;--acc2:#35e0cf;--acc-ink:#0a0f08;--link:#8ff0e6;--glow:#1a2a1b;--hover:#161d25;background:#090b0e}
html:has(.wmg) body{background:radial-gradient(720px 360px at 50% -140px,#b8ff5a24,transparent 70%),radial-gradient(circle at 1px 1px,#ffffff10 1px,transparent 0) 0 0/26px 26px,#090b0e}
html:has(.wmg) .top{background:#090b0ef0;border-bottom:1px solid #212a35}
.wm{max-width:1120px;font-family:ui-rounded,"SF Pro Rounded","Segoe UI Variable",Inter,system-ui,sans-serif}
.gtop{padding:22px 0 18px;gap:12px}
.gtop select{font-size:18px;font-weight:700;min-width:300px;border-radius:16px;background-color:#10151b}
.gtop .av{background:linear-gradient(135deg,var(--acc),var(--acc2));font-weight:800}
.gbody{display:block}
.gside{flex-direction:row;gap:6px;padding:6px;margin:0 0 28px;background:#10151bcc;border:1px solid var(--line);border-radius:20px;overflow-x:auto;scrollbar-width:none;position:static;align-self:auto}
.gside::-webkit-scrollbar{display:none}
.gside a{flex:none;padding:10px 16px;border-radius:14px;margin:0;font-size:14px;color:var(--dim);gap:9px;box-shadow:none;background:none}
.gside a:hover{background:#ffffff0d;color:var(--ink)}
.gside a.on{background:var(--acc);color:var(--acc-ink);box-shadow:0 8px 26px -10px #b8ff5a99}
.gside a.on:before{color:inherit;opacity:1}
.wm h1{font-size:42px;font-weight:750;letter-spacing:-1.2px;line-height:1.05;background:none;-webkit-background-clip:border-box;background-clip:border-box;color:var(--ink);-webkit-text-fill-color:currentColor;margin-bottom:10px}
.wm h1:after{width:56px;height:4px;background:var(--acc);box-shadow:0 0 18px var(--acc)}
.wm .lead{font-size:17px}
.hero{display:flex;justify-content:space-between;align-items:flex-end;gap:20px;flex-wrap:wrap;margin-bottom:24px}
.hero .eyebrow{display:inline-block;text-transform:uppercase;letter-spacing:.14em;font-size:12px;font-weight:750;color:var(--acc);margin-bottom:12px}
.hero h1{margin:0;font-size:46px;overflow-wrap:anywhere}.hero h1:after{display:none}
.hero .lead{margin:10px 0 0}
.herostat{background:var(--card);border:1px solid var(--line);border-radius:22px;padding:18px 28px;text-align:right;min-width:200px}
.herostat b{display:block;font-size:50px;line-height:1;font-weight:750;letter-spacing:-2px;color:var(--acc);font-variant-numeric:tabular-nums}
.herostat small{color:var(--dim);text-transform:uppercase;letter-spacing:.12em;font-size:11px;font-weight:700}
.wm .box,.wm .panel{background:var(--card);border:1px solid var(--line);border-radius:22px;padding:24px 28px;box-shadow:none}
.wm .box:hover,.wm .panel:hover{border-color:#b8ff5a55;box-shadow:0 0 0 1px #b8ff5a1f,0 26px 60px -38px #b8ff5a77}
.wm h2{font-size:13px;text-transform:uppercase;letter-spacing:.14em;color:var(--dim);font-weight:750;display:flex;align-items:center;gap:10px;margin-bottom:14px}
.wm h2:before{content:"";width:8px;height:8px;border-radius:3px;background:var(--acc);box-shadow:0 0 12px var(--acc);flex:none}
.gcards{grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:16px}
.gcards:has(.gstat:nth-child(3)) .gstat:first-child{grid-column:span 2}
.gstat{border-radius:22px;padding:22px 24px;background:var(--card);border:1px solid var(--line)}
.gstat:before{height:2px;background:linear-gradient(90deg,var(--acc),transparent);opacity:1}
.gstat small{text-transform:uppercase;letter-spacing:.12em;font-size:11px;font-weight:750}
.gstat b{font-size:44px;font-weight:750;letter-spacing:-1.5px}
.wm button,.wm .btn{background:var(--acc);color:var(--acc-ink);border-radius:14px;font-weight:750;padding:12px 24px;box-shadow:0 12px 30px -16px #b8ff5aaa}
.wm button:hover,.wm .btn:hover{filter:none;background:#caff7d;transform:translateY(-1px)}
.wm button.alt,.wm .btn.alt{background:transparent;color:var(--ink);border:1px solid #2c3745;box-shadow:none}
.wm button.alt:hover,.wm .btn.alt:hover{background:#ffffff0d;border-color:var(--acc)}
.wm input[type=text],.wm input[type=password],.wm select{background-color:#0b0f14;border:1px solid #263140;border-radius:14px}
.wm input:focus,.wm select:focus{border-color:var(--acc);box-shadow:0 0 0 4px #b8ff5a22}
.wm a{color:var(--link)}
table.gt th{letter-spacing:.12em;font-weight:750;color:var(--dim);background:none;border-bottom:1px solid var(--line)}
table.gt td{border-bottom:1px solid #1a222c}
table.gt tr:hover td{background:#ffffff07}
.wm .msg{border-left:4px solid var(--acc);background:linear-gradient(90deg,#b8ff5a1f,transparent);border-radius:14px}
.gsigncard{border-radius:30px;box-shadow:0 0 0 1px #b8ff5a22,0 40px 90px -40px #b8ff5a66}
.wm code{background:#ffffff10;color:#d9ffa8}
.rbfile .k{color:var(--acc2)}
.herotitle{display:flex;align-items:center;gap:20px;min-width:0}
.sico{width:40px;height:40px;border-radius:12px;background:#ffffff14;padding:7px;object-fit:contain;flex:none;border:1px solid var(--line)}
.sico.big{width:72px;height:72px;border-radius:20px;padding:12px}
.gtop form{display:flex;align-items:center;gap:10px}
.smitem{border:1px solid var(--line);border-radius:14px;margin-top:10px;background:var(--bg2);overflow:hidden}
.smitem summary{display:flex;align-items:center;gap:12px;padding:12px 14px;cursor:pointer;list-style:none}
.smitem summary::-webkit-details-marker{display:none}
.smitem summary:hover{background:#ffffff08}
.smitem[open] summary{border-bottom:1px solid var(--line)}
.smaddr{flex:1;min-width:0;overflow-wrap:anywhere;font-family:ui-monospace,Menlo,Consolas,monospace;font-size:13px}
.smcount{color:var(--dim);font-size:13px;white-space:nowrap}
.smitem table.gt{margin:0}.smitem table.gt td{padding:9px 14px;font-size:13px}
.smhead{display:flex;justify-content:space-between;align-items:flex-start;gap:16px;flex-wrap:wrap;margin-bottom:6px}
@media(max-width:820px){.wm h1,.hero h1{font-size:32px}.herostat{text-align:left;width:100%}.gcards:has(.gstat:nth-child(3)) .gstat:first-child{grid-column:auto}.gtop select{min-width:0;width:100%}}
</style>`
	return page(title+" - Flowsearch", "", css+`<div class="wm wmg">`+body+`</div>`+wmNavJS)
}

// wmNavJS makes clicking a sidebar tab or changing the site dropdown fetch just that view's content
// instead of doing a full page reload - same URLs, same back/forward behaviour, just no flash/reload.
const wmNavJS = `<script>(function(){
  var nav=document.getElementById('gnav'), section=document.getElementById('gsection'), selectBox=document.getElementById('gselect');
  if(!nav||!section)return;
  function runScripts(el){
    // innerHTML never executes <script> tags inside it, so re-create each one by hand.
    el.querySelectorAll('script').forEach(function(old){
      var s=document.createElement('script');
      for(var i=0;i<old.attributes.length;i++)s.setAttribute(old.attributes[i].name,old.attributes[i].value);
      s.textContent=old.textContent;
      old.parentNode.replaceChild(s,old);
    });
  }
  function swap(html){
    var d=JSON.parse(html);
    document.title=d.title;
    nav.innerHTML=d.nav;
    section.innerHTML=d.section;
    if(selectBox)selectBox.innerHTML=d.select;
    bind();
    runScripts(section);
  }
  function go(url,push){
    fetch(url,{headers:{'X-Partial':'1'}}).then(function(r){
      if(!r.ok)throw 0;
      return r.text();
    }).then(function(t){
      swap(t);
      if(push)history.pushState({wm:1},'',url);
    }).catch(function(){location.href=url});
  }
  function bind(){
    nav.querySelectorAll('a').forEach(function(a){
      a.onclick=function(e){e.preventDefault();go(a.getAttribute('href'),true)};
    });
    if(selectBox){
      var f=selectBox.querySelector('form');
      if(f)f.onsubmit=function(e){
        e.preventDefault();
        var p=new URLSearchParams(new FormData(f));
        go('/webmaster?'+p.toString(),true);
      };
    }
  }
  bind();
  window.addEventListener('popstate',function(){go(location.pathname+location.search,false)});
})();</script>`

func wmHome(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	msg := ""
	if m := r.URL.Query().Get("msg"); m != "" {
		msg = `<div class=msg>` + html.EscapeString(m) + `</div>`
	}
	user := currentUser(r)
	if user == "" {
		fmt.Fprint(w, wmPageHTML("Webmaster tools", `<div class=gsign><div class=gsigncard>
<div class=glogo>SG<span>225</span></div>
<h1 id=gh>Log in</h1><p class=sub>to use Webmaster tools</p>`+msg+`
<form class=stack method=post action=/webmaster/login id=f-login><input type=text name=user placeholder="username" autocomplete=username aria-label=Username><input type=password name=password placeholder="password" autocomplete=current-password aria-label=Password><div class=switch><a data-t=signup>Create account</a><button>Log in</button></div></form>
<form class=stack method=post action=/webmaster/signup id=f-signup hidden><input type=text name=user placeholder="choose a username" autocomplete=username aria-label="New username"><input type=password name=password placeholder="password" autocomplete=new-password aria-label=Password><input type=password name=password2 placeholder="the same password again" autocomplete=new-password aria-label="Password again"><p style="margin:0;color:var(--dim);font-size:14px">A name, and a password of at least 8 characters. No email address needed.</p><div class=switch><a data-t=login>Log in instead</a><button>Create account</button></div></form>
</div></div>
<script>document.querySelectorAll('.gsigncard [data-t]').forEach(a=>a.onclick=()=>{var s=a.dataset.t==='signup';document.getElementById('f-login').hidden=s;document.getElementById('f-signup').hidden=!s;document.getElementById('gh').textContent=s?document.getElementById('gh').dataset.s||'Create account':document.getElementById('gh').dataset.l||'Log in'});document.getElementById('gh').dataset.s='Create account';document.getElementById('gh').dataset.l='Log in';(function(){var m=decodeURIComponent(location.search);if(/choose|3 to 24|8 char|different|Could not make/i.test(m)){var t=document.querySelector('[data-t=signup]');if(t)t.click()}})()</script>`))
		return
	}
	csrf := csrfFor(user)
	sites := userSites(user)
	view := r.URL.Query().Get("view")
	cur := ""
	if q, ok := normalizeSite(r.URL.Query().Get("site")); ok && hasSite(user, q) {
		cur = q
	} else if len(sites) > 0 {
		cur = sites[0].S
	}
	if cur == "" {
		view = "add"
	}
	if view != "pages" && view != "removals" && view != "add" && view != "sitemaps" && view != "robots" && view != "live" && view != "browse" && view != "notifications" && view != "cost" && view != "api" {
		view = "overview"
	}
	var opts strings.Builder
	for _, o := range sites {
		sel := ""
		if o.S == cur {
			sel = " selected"
		}
		opts.WriteString(fmt.Sprintf(`<option value="%s"%s>%s</option>`, html.EscapeString(o.S), sel, html.EscapeString(o.S)))
	}
	nav := func(v, label string) string {
		on := ""
		if v == view {
			on = ` class=on`
		}
		return fmt.Sprintf(`<a href="/webmaster?view=%s&site=%s"%s>%s</a>`, v, url.QueryEscape(cur), on, label)
	}
	initial := strings.ToUpper(string([]rune(user)[:1]))
	var main strings.Builder
	distinctIDs := distinctPages(cur)
	pages := len(distinctIDs)
	owned, hidden := false, isBlocked(cur)
	for _, o := range sites {
		if o.S == cur {
			owned = o.V
		}
	}
	switch view {
	case "add":
		main.WriteString(`<h1>Add a site</h1><p class=lead>Flowsearch crawls up to 5,000 pages of it, usually within a minute, and only keeps pages that are in English. It follows your <code>robots.txt</code>.</p>
<div class=panel><form class=addrow method=post action=/webmaster/submit><input type=hidden name=csrf value="` + csrf + `"><input type=text name=url placeholder="https://example.com" aria-label="Your site address"><button>Add</button></form></div>
<div class=panel><h2>Keep the crawler out</h2><p>Add this to your <code>robots.txt</code> and Flowsearch will not crawl your site:</p><pre>User-agent: MySearchBot
Disallow: /</pre></div>`)
	case "pages":
		pg, _ := strconv.Atoi(r.URL.Query().Get("p"))
		if pg < 1 {
			pg = 1
		}
		const per = 50
		total := len(distinctIDs)
		from, to := (pg-1)*per, pg*per
		if from > total {
			from = total
		}
		if to > total {
			to = total
		}
		ids := distinctIDs[from:to]
		var rows strings.Builder
		for _, id := range ids {
			d, ok := ix.Doc(id)
			if !ok {
				continue
			}
			rec := load(d)
			if rec.URL == "" { // an erased page
				continue
			}
			rows.WriteString(fmt.Sprintf(`<tr><td><a href="%s" target=_blank rel="noopener noreferrer">%s</a><small>%s</small></td></tr>`, html.EscapeString(rec.URL), html.EscapeString(firstNonEmpty(rec.Title, rec.URL)), html.EscapeString(rec.URL)))
		}
		if rows.Len() == 0 {
			rows.WriteString(`<tr><td>No pages of this site are in Flowsearch yet.</td></tr>`)
		}
		last := (total + per - 1) / per
		pager := ""
		if last > 1 {
			link := func(n int, label string) string {
				return `<a class=btn href="/webmaster?view=pages&site=` + url.QueryEscape(cur) + `&p=` + strconv.Itoa(n) + `">` + label + `</a>`
			}
			pager = `<div class=panel style="display:flex;gap:10px;align-items:center;flex-wrap:wrap">`
			if pg > 1 {
				pager += link(pg-1, "Previous")
			}
			pager += `<span>Page ` + strconv.Itoa(pg) + ` of ` + commas(last) + `</span>`
			if pg < last {
				pager += link(pg+1, "Next")
			}
			pager += `</div>`
		}
		main.WriteString(`<h1>Indexed pages</h1><p class=lead>` + html.EscapeString(cur) + ` · ` + commas(total) + ` pages indexed, ` + strconv.Itoa(per) + ` to a page.</p>
<div class=panel style="display:flex;gap:10px;flex-wrap:wrap">
<form method=post action=/webmaster/recrawl><input type=hidden name=csrf value="` + csrf + `"><input type=hidden name=site value="` + html.EscapeString(cur) + `"><button>Recrawl now</button></form>
<form method=post action=/webmaster/recrawl><input type=hidden name=csrf value="` + csrf + `"><input type=hidden name=site value="` + html.EscapeString(cur) + `"><input type=hidden name=lang value="de"><button class=alt>Crawl for German people</button></form>
<form method=post action=/webmaster/recrawl-url style="display:flex;gap:8px;flex:1;min-width:260px"><input type=hidden name=csrf value="` + csrf + `"><input type=hidden name=site value="` + html.EscapeString(cur) + `"><input name=url placeholder="https://` + html.EscapeString(cur) + `/page" required style="flex:1"><button>Request this page</button></form>
</div>
` + pager + `<div class=panel><table class=gt><tr><th>Page</th></tr>` + rows.String() + `</table></div>` + pager)
	case "cost":
		main.WriteString(wmCostHTML(r, cur))
	case "api":
		main.WriteString(wmapiTab(r, user, csrf))
	case "live":
		main.WriteString(fmt.Sprintf(`<h1>Live indexing</h1><p class=lead>%s is indexing right now. This updates every 500ms over its own WebSocket.</p>
<div class=panel><div class=kpi><small>Pages indexed</small><b id=wmlive>%s</b></div></div>
<div class=panel>
<h2>What the crawler sees</h2>
<p class=lead style="margin-bottom:10px">A real screenshot, taken by an actual Chrome browser - not a mockup.</p>
<div class=browserframe>
  <div class=browserbar><span class=dot></span><span class=dot></span><span class=dot></span><span class=addr>%s</span></div>
  <img class=browserimg src="%s" onerror="this.closest('.panel').querySelector('.browsererr').hidden=false;this.hidden=true" alt="screenshot of %s">
  <p class=browsererr hidden>No screenshot yet - take one below.</p>
</div>
<form method=post action=/webmaster/screenshot style="margin-top:10px;display:inline-block"><input type=hidden name=csrf value="`+csrf+`"><input type=hidden name=site value="`+html.EscapeString(cur)+`"><button>Take screenshot now</button></form>
<form method=post action=/webmaster/ai-explore style="margin-top:10px;margin-left:10px;display:inline-block"><input type=hidden name=csrf value="`+csrf+`"><input type=hidden name=site value="`+html.EscapeString(cur)+`"><button>&#129302; Let AI explore this site</button></form>
<button type=button id=elevatorBtn style="margin-top:10px;margin-left:10px">&#127925; Elevator music</button>
<audio id=elevatorAudio src="/static/elevator.mp3" loop preload=none></audio>
<p style="margin-top:8px;font-size:12px;color:var(--dim)">"Local Forecast - Elevator" by Kevin MacLeod (incompetech.com), licensed under <a href="https://creativecommons.org/licenses/by/3.0/" target=_blank rel=noopener>CC BY 3.0</a></p>
<script>(function(){
  var btn=document.getElementById('elevatorBtn'),au=document.getElementById('elevatorAudio');
  if(!btn||!au)return;
  btn.onclick=function(){
    if(au.paused){au.play();btn.textContent='⏸ Stop elevator music'}
    else{au.pause();btn.textContent='\u{1F3B5} Elevator music'}
  };
})();</script>
</div>
<style>.browserframe{border:1px solid var(--line);border-radius:10px;overflow:hidden;background:#1b1b1b}
.browserbar{background:#2d2d2d;padding:8px 12px;display:flex;align-items:center;gap:6px}
.browserbar .dot{width:10px;height:10px;border-radius:50%%;background:#555;display:inline-block}
.browserbar .addr{margin-left:10px;background:#1b1b1b;color:#ccc;border-radius:6px;padding:4px 10px;font-size:13px;flex:1}
.browserimg{display:block;width:100%%;height:auto}
.browsererr{padding:30px;text-align:center;color:var(--dim)}</style>
<script>
(function(){
  var site=%q;
  var proto=location.protocol==='https:'?'wss:':'ws:';
  function connect(){
    var ws=new WebSocket(proto+'//'+location.host+'/webmaster/ws?site='+encodeURIComponent(site));
    ws.onmessage=function(e){
      try{document.getElementById('wmlive').textContent=JSON.parse(e.data).pages.toLocaleString()}catch(err){}
    };
    ws.onclose=function(){setTimeout(connect,1000)};
  }
  connect();
})();
</script>`, html.EscapeString(cur), commas(pages), html.EscapeString(cur), screenshotURL(cur), html.EscapeString(cur), cur))
	case "notifications":
		main.WriteString(wmNotificationsView(user))
	case "browse":
		main.WriteString(wmStreamView(cur))
	case "robots":
		main.WriteString(robotsView(cur, strings.TrimSpace(r.URL.Query().Get("test")), csrf))
	case "sitemaps":
		var list strings.Builder
		for _, m := range sitemapsOf(user, cur) {
			list.WriteString(`<tr><td><a href="` + html.EscapeString(m) + `" target=_blank rel="noopener noreferrer">` + html.EscapeString(m) + `</a></td></tr>`)
		}
		if list.Len() == 0 {
			list.WriteString(`<tr><td>No sitemap sent yet.</td></tr>`)
		}
		main.WriteString(`<h1>Sitemaps</h1><p class=lead>A sitemap is a file that lists the pages of ` + html.EscapeString(cur) + `. Send its address and Flowsearch reads it and crawls the pages in it, usually within a minute.</p>
<div class=panel><form class=addrow method=post action=/webmaster/sitemap><input type=hidden name=csrf value="` + csrf + `"><input type=hidden name=site value="` + html.EscapeString(cur) + `"><input type=text name=sitemap placeholder="https://` + html.EscapeString(cur) + `/sitemap.xml" aria-label="Sitemap address"><button>Send sitemap</button></form></div>
<div class=panel><h2>Sitemaps you sent</h2><table class=gt>` + list.String() + `</table></div>` + robotsSitemapsPanel(cur, csrf))
	case "removals":
		state := `<span class=ok>Shown in results</span>`
		if hidden {
			state = `<span class=bad>Removed from results</span>`
		}
		main.WriteString(`<h1>Removals</h1><p class=lead>Take ` + html.EscapeString(cur) + ` out of Flowsearch, or bring it back. You have to show that the site is yours.</p>
<div class=gcards><div class=gstat><small>Visibility</small><b>` + state + `</b></div></div>
<div class=panel><p>Put a code on your site, then press a button. The next page walks you through it.</p><a class=btn href="/webmaster/owner?site=` + url.QueryEscape(cur) + `">Remove or restore</a></div>`)
	default:
		own := `<span class=bad>Not verified</span>`
		if owned {
			own = `<span class=ok>Verified</span>`
		}
		vis := `<span class=ok>Shown</span>`
		if hidden {
			vis = `<span class=bad>Removed</span>`
		}
		var recent strings.Builder
		if pages > 0 {
			a := run("site:"+cur, 1, 5, false)
			for _, x := range a.Results {
				recent.WriteString(fmt.Sprintf(`<tr><td><a href="%s" target=_blank rel="noopener noreferrer">%s</a><small>%s</small></td></tr>`, html.EscapeString(x.URL), html.EscapeString(firstNonEmpty(x.Title, x.URL)), html.EscapeString(x.URL)))
			}
		}
		if recent.Len() == 0 {
			recent.WriteString(`<tr><td>No pages of this site are in Flowsearch yet.</td></tr>`)
		}
		main.WriteString(`<div class=hero><div class=herotitle>` + siteIcon(cur, "big") + `<div><span class=eyebrow>Overview</span><h1>` + html.EscapeString(cur) + `</h1><p class=lead>How Flowsearch sees your site today.</p></div></div><div class=herostat><b>` + commas(pages) + `</b><small>pages indexed</small></div></div>
<div class=gcards><div class=gstat><small>Indexed pages</small><b>` + commas(pages) + `</b></div><div class=gstat><small>Ownership</small><b>` + own + `</b></div><div class=gstat><small>In results</small><b>` + vis + `</b></div></div>
` + siteHistorySVG(cur) + `
<div class=panel><h2>Some of your pages</h2><table class=gt>` + recent.String() + `</table><p style="margin:14px 0 0"><a href="/webmaster?view=pages&site=` + url.QueryEscape(cur) + `">See all indexed pages</a></p></div>`)
	}
	selectHTML := ""
	if len(sites) > 0 {
		selectHTML = `<form method=get action=/webmaster><input type=hidden name=view value="` + view + `">` + siteIcon(cur, "") + `<select name=site aria-label="Your site" onchange="this.form.submit()">` + opts.String() + `</select></form>`
	}
	notifLabel := "Notifications"
	if n := unreadCount(user); n > 0 {
		notifLabel = fmt.Sprintf("Notifications (%d)", n)
	}
	navHTML := nav("overview", "Overview") + nav("pages", "Pages") + nav("cost", "Cost") + nav("api", "API") + nav("live", "Live") + nav("browse", "Try website") + nav("sitemaps", "Sitemaps") + nav("robots", "robots.txt") + nav("notifications", notifLabel) + nav("removals", "Removals")
	if r.Header.Get("X-Partial") == "1" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		b, _ := json.Marshal(map[string]string{
			"title":   "Webmaster tools - " + cur,
			"nav":     navHTML,
			"select":  selectHTML,
			"section": main.String(),
		})
		w.Write(b)
		return
	}
	fmt.Fprint(w, wmPageHTML("Webmaster tools", fmt.Sprintf(`<div class=gtop><span id=gselect>%s</span><a class="btn alt" href="/webmaster?view=add">Add a site</a><span class=sp></span><span class=me><span class=av>%s</span>%s</span><form method=post action=/webmaster/logout><button class=alt>Log out</button></form></div>%s
<div class=gbody><nav class=gside id=gnav>%s</nav><section id=gsection>%s</section></div>`,
		selectHTML, html.EscapeString(initial), html.EscapeString(user), msg, navHTML, main.String())))
}

func wmSubmit(w http.ResponseWriter, r *http.Request) {
	back := func(m string) { http.Redirect(w, r, "/webmaster?msg="+url.QueryEscape(m), http.StatusSeeOther) }
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/webmaster", http.StatusSeeOther)
		return
	}
	user := currentUser(r)
	if user == "" || !csrfOK(r, user) {
		back("Please log in first.")
		return
	}
	if wmLimited(r, 1000000) {
		back("That was a lot of submissions in an hour. Please try again later.")
		return
	}
	if len(userSites(user)) >= maxSites {
		back("An account can add up to 120 sites.")
		return
	}
	site, ok := normalizeSite(r.FormValue("url"))
	if !ok {
		back("That doesn't look like a website address. Try something like example.com.")
		return
	}
	if isBlocked(site) {
		back(site + " was removed from Flowsearch by its owner. The owner can bring it back with “Remove or restore your site”.")
		return
	}
	// it must really be a public website that answers
	resp, err := safeClient.Get("https://" + site + "/")
	if err != nil {
		resp, err = safeClient.Get("http://" + site + "/")
	}
	if err != nil {
		back("Flowsearch could not reach " + site + ". Is the address right, and is the site public?")
		return
	}
	resp.Body.Close()
	if !hasSite(user, site) {
		recordSite(user, site, false)
	}
	sp := userSubmittedPath(user, isDE(r))
	if fileHas(sp, site) {
		back(site + " is already on the list. It is crawled again about once a week.")
		return
	}
	appendLine(sp, site)
	lang := "any" // webmaster-submitted sites: no language filter
	triggerSiteCrawl(site, lang, site)
	back("Thank you. " + site + " is queued: a GitHub Actions crawl run is starting for it now, and the pages show up in search as they are found. Check progress with “Check your site”.")
}

func wmCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if currentUser(r) == "" {
		wmGo(w, r, "Please log in first.")
		return
	}
	site, ok := normalizeSite(r.URL.Query().Get("site"))
	if !ok {
		wmGo(w, r, "That doesn't look like a website address.")
		return
	}
	hosts, pages := ix.HostsWithSuffix(site)
	var status []string
	if isBlocked(site) {
		status = append(status, "Removed from results by its owner.")
	}
	if fileHas(submittedPath, site) || fileHas(submittedDePath, site) {
		status = append(status, "Submitted: it is on the list of sites Flowsearch crawls.")
	}
	if len(status) == 0 {
		status = append(status, "Not submitted. If pages show below, they were found by the main crawler.")
	}
	var list strings.Builder
	if pages > 0 {
		a := run("site:"+site, 1, 10, false)
		list.WriteString(`<ul class=pages>`)
		for _, x := range a.Results {
			list.WriteString(fmt.Sprintf(`<li><a href="%s">%s</a><br><small style="color:var(--dim)">%s</small></li>`, html.EscapeString(x.URL), html.EscapeString(x.Title), html.EscapeString(x.URL)))
		}
		list.WriteString(`</ul>`)
	} else {
		list.WriteString(`<p>No pages of this site are in Flowsearch yet.</p>`)
	}
	fmt.Fprint(w, wmPageHTML("Check "+site, fmt.Sprintf(`<h1>%s</h1><p class=lead>%s</p>
<div class=stat><div><b>%s</b><small>pages indexed</small></div><div><b>%d</b><small>address(es), like www.%s</small></div></div>
%s
<p><a class=btn href="/webmaster/owner?site=%s">Remove or restore this site</a> &nbsp; <a href="/webmaster">Back</a></p>`,
		html.EscapeString(site), html.EscapeString(strings.Join(status, " ")), commas(pages), hosts, html.EscapeString(site), list.String(), url.QueryEscape(site))))
}

func wmOwner(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	user := currentUser(r)
	if user == "" {
		wmGo(w, r, "Please log in first.")
		return
	}
	site, ok := normalizeSite(r.FormValue("site"))
	if !ok {
		wmGo(w, r, "That doesn't look like a website address.")
		return
	}
	tok := ownerToken(user, site)
	msg := ""
	if r.Method == http.MethodPost {
		action := r.FormValue("action")
		switch {
		case !csrfOK(r, user):
			msg = `<div class=msg>The page expired. Please try again.</div>`
		case wmLimited(r, 12):
			msg = `<div class=msg>Too many tries in an hour. Please wait a while.</div>`
		case action != "remove" && action != "restore":
			msg = `<div class=msg>Unknown action.</div>`
		case !verifyOwner(user, site):
			msg = `<div class=msg>Flowsearch could not find the code on ` + html.EscapeString(site) + `. Put it in place as described below, then try again.</div>`
		case action == "remove":
			recordSite(user, site, true)
			if !fileHas(blockedPath, site) {
				appendLine(blockedPath, site)
			}
			loadBlocked()
			msg = `<div class=msg>Done. ` + html.EscapeString(site) + ` no longer appears in Flowsearch results.</div>`
		default:
			recordSite(user, site, true)
			wmMu.Lock()
			var keep []string
			if f, err := os.Open(blockedPath); err == nil {
				sc := bufio.NewScanner(f)
				for sc.Scan() {
					if l := strings.TrimSpace(sc.Text()); l != "" && l != site {
						keep = append(keep, l)
					}
				}
				f.Close()
			}
			sort.Strings(keep)
			os.WriteFile(blockedPath, []byte(strings.Join(keep, "\n")+"\n"), 0o644)
			wmMu.Unlock()
			loadBlocked()
			msg = `<div class=msg>Done. ` + html.EscapeString(site) + ` can appear in Flowsearch results again.</div>`
		}
	}
	state := "currently shown in results"
	if isBlocked(site) {
		state = "currently removed from results"
	}
	fmt.Fprint(w, wmPageHTML("Owner of "+site, fmt.Sprintf(`<h1>%s</h1><p class=lead>This site is %s. To change that, show that you own it.</p>%s
<div class=box><h2>1. Put this code on your site</h2><p>Either create the file <code>/sg225-verify.txt</code> containing only the code, or put the code anywhere on your front page, for example in a comment:</p>
<pre>%s</pre><pre>&lt;!-- %s --&gt;</pre></div>
<div class=box><h2>2. Choose</h2><p>Flowsearch looks for the code when you press a button.</p>
<form method=post action=/webmaster/owner><input type=hidden name=csrf value="%s"><input type=hidden name=site value="%s"><button name=action value=remove>Remove from results</button><button class=alt name=action value=restore>Show in results again</button></form></div>
<p><a href="/webmaster">Back</a></p>`, html.EscapeString(site), state, msg, tok, tok, csrfFor(user), html.EscapeString(site))))
}

func registerWebmaster() {
	loadBlocked()
	loadSubmittedHosts()
	registerWebmasterAPI()
	go weeklyShots()
	http.HandleFunc("/webmaster", wmHome)
	http.HandleFunc("/webmaster/submit", wmSubmit)
	http.HandleFunc("/webmaster/signup", wmSignup)
	http.HandleFunc("/webmaster/login", wmLogin)
	http.HandleFunc("/webmaster/logout", wmLogout)
	http.HandleFunc("/webmaster/check", wmCheck)
	http.HandleFunc("/webmaster/owner", wmOwner)
	http.HandleFunc("/webmaster/sitemap", wmSitemap)
	http.HandleFunc("/webmaster/recrawl", wmRecrawl)
	http.HandleFunc("/webmaster/sitemap-scan", wmSitemapScan)
	http.HandleFunc("/webmaster/recrawl-url", wmRecrawlURL)
	http.HandleFunc("/webmaster/ws", wmLiveWS)
	http.HandleFunc("/webmaster/screenshot", wmScreenshot)
	http.HandleFunc("/webmaster/shot", wmShot)
	http.HandleFunc("/webmaster/browse/ws", wmBrowseWS)
	http.HandleFunc("/static/elevator.mp3", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, fsHome + "/web/static/elevator.mp3")
	})
}

// wmRecrawl triggers a fresh GitHub Actions run for this one site's own workflow.
func wmRecrawl(w http.ResponseWriter, r *http.Request) {
	back := func(m string) {
		http.Redirect(w, r, "/webmaster?site="+url.QueryEscape(r.FormValue("site"))+"&msg="+url.QueryEscape(m), http.StatusSeeOther)
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
	lang := "any" // webmaster-submitted sites: no language filter by default
	note := site + " will be recrawled: a GitHub Actions run is starting now."
	if r.FormValue("lang") == "de" {
		// only when the webmaster explicitly asks for it with the German button - never automatic,
		// even if they happen to be looking at the German side of webmaster tools.
		lang = "de"
		note = site + " will be recrawled for German pages only: a GitHub Actions run is starting now."
	}
	triggerSiteCrawl(site, lang, site)
	back(note)
}

// ---------------------------------------------------------------- accounts

var (
	accountsPath = fsData + "/accounts.jsonl"
	ownedPath    = fsData + "/owned.jsonl"
)

const maxSites = 120

var userRe = regexp.MustCompile(`^[a-z0-9_-]{3,24}$`)

func secretKey() []byte {
	k, _ := os.ReadFile(fsSecrets + "/mysearch_token")
	return k
}

// pbkdf2 is PBKDF2-HMAC-SHA256: it makes a password hash slow to guess.
func pbkdf2(pw, salt []byte, iter int) []byte {
	prf := hmac.New(sha256.New, pw)
	prf.Write(salt)
	prf.Write([]byte{0, 0, 0, 1})
	u := prf.Sum(nil)
	out := append([]byte(nil), u...)
	for i := 1; i < iter; i++ {
		prf.Reset()
		prf.Write(u)
		u = prf.Sum(u[:0])
		for j := range out {
			out[j] ^= u[j]
		}
	}
	return out
}

type account struct {
	U string `json:"u"`
	S string `json:"s"`
	H string `json:"h"`
	T int64  `json:"t"`
}

func findAccount(name string) *account {
	f, err := os.Open(accountsPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var a account
		if json.Unmarshal(sc.Bytes(), &a) == nil && a.U == name {
			return &a
		}
	}
	return nil
}

const pbkdfRounds = 210000

func createAccount(name, pw string) error {
	wmMu.Lock()
	defer wmMu.Unlock()
	if findAccount(name) != nil {
		return errors.New("that name is taken")
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	b, _ := json.Marshal(account{U: name, S: base64.RawStdEncoding.EncodeToString(salt), H: base64.RawStdEncoding.EncodeToString(pbkdf2([]byte(pw), salt, pbkdfRounds)), T: time.Now().Unix()})
	f, err := os.OpenFile(accountsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	f.Write(append(b, '\n'))
	return nil
}

func checkLogin(name, pw string) bool {
	a := findAccount(name)
	salt := make([]byte, 16)
	want := make([]byte, 32)
	if a != nil {
		salt, _ = base64.RawStdEncoding.DecodeString(a.S)
		want, _ = base64.RawStdEncoding.DecodeString(a.H)
	}
	got := pbkdf2([]byte(pw), salt, pbkdfRounds) // computed even for an unknown name, so the time does not give it away
	return a != nil && subtle.ConstantTimeCompare(got, want) == 1
}

// the session cookie is user|expiry|signature: nothing is stored on our side
func sign(msg string) string {
	m := hmac.New(sha256.New, secretKey())
	m.Write([]byte("session:" + msg))
	return hex.EncodeToString(m.Sum(nil))[:40]
}

func setSession(w http.ResponseWriter, r *http.Request, user string) {
	exp := time.Now().Add(30 * 24 * time.Hour)
	msg := user + "|" + strconv.FormatInt(exp.Unix(), 10)
	http.SetCookie(w, &http.Cookie{Name: "wm", Value: msg + "|" + sign(msg), Path: "/webmaster", Expires: exp, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" || r.Header.Get("Cf-Visitor") != ""})
}

func currentUser(r *http.Request) string {
	c, err := r.Cookie("wm")
	if err != nil {
		return ""
	}
	p := strings.Split(c.Value, "|")
	if len(p) != 3 {
		return ""
	}
	exp, err := strconv.ParseInt(p[1], 10, 64)
	if err != nil || exp < time.Now().Unix() || subtle.ConstantTimeCompare([]byte(sign(p[0]+"|"+p[1])), []byte(p[2])) != 1 || findAccount(p[0]) == nil {
		return ""
	}
	return p[0]
}

func csrfFor(user string) string {
	m := hmac.New(sha256.New, secretKey())
	m.Write([]byte("csrf:" + user))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

func csrfOK(r *http.Request, user string) bool {
	return subtle.ConstantTimeCompare([]byte(r.FormValue("csrf")), []byte(csrfFor(user))) == 1
}

// ---- which sites an account has added or proved it owns ----

type ownedRow struct {
	U string `json:"u"`
	S string `json:"s"`
	V bool   `json:"v"` // ownership proved
}

func userSites(user string) []ownedRow {
	latest := map[string]ownedRow{}
	var order []string
	if f, err := os.Open(ownedPath); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var o ownedRow
			if json.Unmarshal(sc.Bytes(), &o) == nil && o.U == user {
				if _, ok := latest[o.S]; !ok {
					order = append(order, o.S)
				}
				if prev, ok := latest[o.S]; ok && prev.V {
					o.V = true
				}
				latest[o.S] = o
			}
		}
		f.Close()
	}
	var out []ownedRow
	for _, s := range order {
		out = append(out, latest[s])
	}
	return out
}

func recordSite(user, site string, verified bool) {
	b, _ := json.Marshal(ownedRow{user, site, verified})
	appendLine(ownedPath, string(b))
}

func hasSite(user, site string) bool {
	for _, o := range userSites(user) {
		if o.S == site || strings.HasSuffix(site, "."+o.S) { // a subdomain belongs to the site it is under
			return true
		}
	}
	return false
}

// ---- sign up, log in, log out ----

func wmGo(w http.ResponseWriter, r *http.Request, m string) {
	http.Redirect(w, r, "/webmaster?msg="+url.QueryEscape(m), http.StatusSeeOther)
}

func wmSignup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/webmaster", http.StatusSeeOther)
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.FormValue("user")))
	pw := r.FormValue("password")
	switch {
	case wmLimited(r, 6):
		wmGo(w, r, "Too many tries from your address. Please wait an hour.")
	case !userRe.MatchString(name):
		wmGo(w, r, "Choose a name of 3 to 24 letters, numbers, - or _.")
	case len(pw) < 8 || len(pw) > 200:
		wmGo(w, r, "The password needs at least 8 characters.")
	case pw != r.FormValue("password2"):
		wmGo(w, r, "The two passwords are different.")
	default:
		if err := createAccount(name, pw); err != nil {
			wmGo(w, r, "Could not make the account: "+err.Error()+".")
			return
		}
		setSession(w, r, name)
		wmGo(w, r, "Welcome, "+name+". Your account is ready.")
	}
}

func wmLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/webmaster", http.StatusSeeOther)
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.FormValue("user")))
	if wmLimited(r, 30) || !checkLogin(name, r.FormValue("password")) {
		wmGo(w, r, "Wrong name or password.")
		return
	}
	setSession(w, r, name)
	http.Redirect(w, r, "/webmaster", http.StatusSeeOther)
}

func wmLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "wm", Value: "", Path: "/webmaster", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/webmaster", http.StatusSeeOther)
}

func _unused() { _ = binary.BigEndian }

var sitemapsPath = fsData + "/sitemaps.jsonl"

type sitemapRow struct {
	U string `json:"u"` // account
	S string `json:"s"` // site
	M string `json:"m"` // sitemap address
	T int64  `json:"t"`
}

func sitemapsOf(user, site string) []string {
	var out []string
	f, err := os.Open(sitemapsPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r sitemapRow
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.U == user && r.S == site {
			out = append(out, r.M)
		}
	}
	return out
}

// wmSitemap takes a sitemap address for one of the account's sites, checks that it is on that site (the server
// downloads nothing: GitHub Actions reads the sitemap), and hands it to the crawler as "site<TAB>sitemap" in the list of sites to crawl.
func wmSitemap(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	site, ok := normalizeSite(r.FormValue("site"))
	back := func(m string) {
		http.Redirect(w, r, "/webmaster?view=sitemaps&site="+url.QueryEscape(site)+"&msg="+url.QueryEscape(m), http.StatusSeeOther)
	}
	if r.Method != http.MethodPost || user == "" || !csrfOK(r, user) {
		http.Redirect(w, r, "/webmaster", http.StatusSeeOther)
		return
	}
	if !ok || !hasSite(user, site) {
		back("Add the site first.")
		return
	}
	mu, err := url.Parse(strings.TrimSpace(r.FormValue("sitemap")))
	if err != nil || (mu.Scheme != "http" && mu.Scheme != "https") || mu.Host == "" || len(mu.String()) > 300 {
		back("That doesn't look like a sitemap address.")
		return
	}
	if h, base := strings.TrimPrefix(strings.ToLower(mu.Hostname()), "www."), strings.TrimPrefix(site, "www."); h != base && !strings.HasSuffix(h, "."+base) {
		back("The sitemap has to be on " + site + " or one of its subdomains.")
		return
	}
	mu.Fragment = ""
	sp := userSubmittedPath(user, isDE(r))
	line := site + "\t" + mu.String()
	if !fileHas(sp, line) {
		appendLine(sp, line)
	}
	row, _ := json.Marshal(sitemapRow{user, site, mu.String(), time.Now().Unix()})
	appendLine(sitemapsPath, string(row))
	lang := "any" // webmaster-submitted sites: no language filter
	triggerSiteCrawl(site, lang, line)
	back("Sitemap received. A GitHub Actions run is starting now: it reads the sitemap and crawls the pages in it.")
}


// wmStreamView is the "Try website" tab: a real Chrome on Cloudflare Browser Run, streamed as video over a WebSocket.
func wmStreamView(site string) string {
	return fmt.Sprintf(`<h1>Try website</h1><p class=lead>Opens %s in a real Chrome browser and streams it to you live, like a video. Click, scroll and type like in a normal browser. A session ends after 5 minutes, or 90 seconds without a click or key.</p>
<div class=panel>
<div class=row style="margin-bottom:10px">
  <button type=button id=sbBack class=alt>&#8592;</button><button type=button id=sbReload class=alt>&#8635;</button>
  <input type=text id=sbUrl value="https://%s" style="flex:1" aria-label="Address">
  <button type=button id=sbGo>Go</button>
  <a class=btn href="/webmaster?view=browse&site=%s">New session</a>
</div>
<div class=browserframe>
  <div class=browserbar><span class=dot></span><span class=dot></span><span class=dot></span><span class=addr id=sbBar>%s</span></div>
  <div style="position:relative"><canvas id=sbCv width=1280 height=720 tabindex=0 style="display:block;width:100%%;height:auto;background:#111;outline:none;cursor:default"></canvas>
  <div id=sbMsg style="position:absolute;inset:0;display:flex;align-items:center;justify-content:center;color:#ccc;text-align:center;padding:20px;pointer-events:none">Connecting...</div></div>
</div>
<p style="margin-top:8px;font-size:12px;color:var(--dim)">Click the video to focus it, then type. Only %s and its subdomains can be opened.</p>
</div>
<style>.browserframe{border:1px solid var(--line);border-radius:10px;overflow:hidden;background:#1b1b1b}
.browserbar{background:#2d2d2d;padding:8px 12px;display:flex;align-items:center;gap:6px}
.browserbar .dot{width:10px;height:10px;border-radius:50%%;background:#555;display:inline-block}
.browserbar .addr{margin-left:10px;background:#1b1b1b;color:#ccc;border-radius:6px;padding:4px 10px;font-size:13px;flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}</style>
<script>
(function(){
  var cv=document.getElementById('sbCv'), cx=cv.getContext('2d'), msg=document.getElementById('sbMsg');
  var url=document.getElementById('sbUrl'), bar=document.getElementById('sbBar');
  var ws=new WebSocket((location.protocol==='https:'?'wss:':'ws:')+'//'+location.host+'/webmaster/browse/ws?site='+encodeURIComponent(%q)); ws.binaryType='blob';
  var next=null, busy=false;
  function paint(){
    if(busy||!next) return; busy=true; var b=next; next=null;
    createImageBitmap(b).then(function(bm){ cx.drawImage(bm,0,0,cv.width,cv.height); bm.close&&bm.close(); msg.style.display='none'; }).catch(function(){}).then(function(){ busy=false; paint(); });
  }
  ws.onmessage=function(e){
    if(typeof e.data==='string'){
      var m=JSON.parse(e.data);
      if(m.t==='url'){ url.value=m.url; bar.textContent=m.url; }
      else if(m.t==='status'){ msg.textContent=m.m; msg.style.display=m.m?'flex':'none'; }
      else if(m.t==='end'){ msg.textContent='Session ended. Press New session.'; msg.style.display='flex'; }
    } else { next=e.data; paint(); }
  };
  ws.onclose=function(){ if(msg.style.display==='none'){ msg.textContent='Disconnected. Press New session.'; msg.style.display='flex'; } };
  function send(o){ if(ws.readyState===1) ws.send(JSON.stringify(o)); }
  function frac(e){ var r=cv.getBoundingClientRect(); return {x:Math.min(1,Math.max(0,(e.clientX-r.left)/r.width)), y:Math.min(1,Math.max(0,(e.clientY-r.top)/r.height))}; }
  var lastMove=0;
  cv.addEventListener('mousemove',function(e){ var t=Date.now(); if(t-lastMove<40) return; lastMove=t; var p=frac(e); send({t:'move',x:p.x,y:p.y}); });
  cv.addEventListener('mousedown',function(e){ cv.focus(); var p=frac(e); send({t:'down',x:p.x,y:p.y}); e.preventDefault(); });
  window.addEventListener('mouseup',function(e){ if(!down) return; });
  var down=false; cv.addEventListener('mousedown',function(){down=true}); window.addEventListener('mouseup',function(e){ if(down){ down=false; var p=frac(e); send({t:'up',x:p.x,y:p.y}); } });
  cv.addEventListener('wheel',function(e){ e.preventDefault(); var p=frac(e); send({t:'wheel',x:p.x,y:p.y,dy:e.deltaY}); },{passive:false});
  cv.addEventListener('paste',function(e){ e.preventDefault(); var t=(e.clipboardData||window.clipboardData).getData('text'); if(t) send({t:'text',k:t}); });
  cv.addEventListener('keydown',function(e){
    if((e.ctrlKey||e.metaKey)&&!e.altKey&&(e.key==='a'||e.key==='A')){ e.preventDefault(); send({t:'selectall'}); return; }
    if(e.ctrlKey||e.metaKey||e.altKey) return;
    e.preventDefault();
    var named=['Enter','Backspace','Tab','Escape','Delete','ArrowLeft','ArrowRight','ArrowUp','ArrowDown','Home','End','PageUp','PageDown'];
    if(e.key.length===1||named.indexOf(e.key)>=0) send({t:'key',k:e.key});
  });
  document.getElementById('sbGo').onclick=function(){ send({t:'nav',url:url.value}); };
  url.addEventListener('keydown',function(e){ if(e.key==='Enter') send({t:'nav',url:url.value}); });
  document.getElementById('sbBack').onclick=function(){ send({t:'back'}); };
  document.getElementById('sbReload').onclick=function(){ send({t:'reload'}); };
})();
</script>`, html.EscapeString(site), html.EscapeString(site), url.QueryEscape(site), html.EscapeString(site), html.EscapeString(site), site)
}
