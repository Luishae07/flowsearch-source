// Flowsearch (mysearch-web): the search site and API over the pages the crawler sent (pages.jsonl).
// The index is on disk (package sgindex, JSON lines only) and follows the file, so new pages are searchable within a second.
package main

import (
	"context"
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"html"
	"io"
	"math/rand"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"mysearch/sgindex"
	"mysearch/sgpack"
)

var pagesFile = fsData + "/pages4.jsonl"

type record struct {
	URL         string `json:"u"`
	Title       string `json:"t"`
	Description string `json:"d"`
	Text        string `json:"x"`
	Z           string `json:"z"`
	Source      string `json:"s"`
}

// full returns the page text whether it is stored packed or plain.
func (r record) full() string {
	if r.Z != "" {
		if t, err := sgpack.UnpackText(r.Z); err == nil {
			return t
		}
	}
	return r.Text
}

var (
	ix      *indexClient // the index itself lives in the index service (indexclient.go)
	file    *os.File
	started = time.Now()
)

type hit struct {
	id    uint32
	score float64
	d     sgindex.Doc
	rec   record // title, address, description; the text stays packed until needed
}

func search(q string, de bool) (terms []string, hits []hit, total int, jevMs int64, cut int) {
	groups, raw, tot := ix.Search(q)
	total = tot
	for _, g := range groups {
		terms = append(terms, g...)
	}
	if len(raw) == 0 {
		return
	}
	var words, qt []string
	for _, f := range strings.Fields(q) {
		if !strings.HasPrefix(strings.ToLower(f), "site:") {
			words = append(words, f)
		}
	}
	for _, t := range sgindex.Tokens(strings.Join(words, " ")) {
		if !sgindex.IsStop(t) {
			qt = append(qt, t)
		}
	}
	if len(qt) == 1 && !strings.Contains(strings.ToLower(q), "site:") { // the site's own front page may not be among the best text matches: add it
		have := map[uint32]bool{}
		for _, h := range raw {
			have[h.ID] = true
		}
		added := 0
		tlds := []string{"com", "org", "net", "io", "dev"}
		sites := make([][]sgindex.Hit, len(tlds)) // the five searches run at the same time (they took about 80 ms each, one after the other)
		var swg sync.WaitGroup
		for ti, tld := range tlds {
			swg.Add(1)
			go func(ti int, tld string) {
				defer swg.Done()
				_, sites[ti], _ = ix.Search("site:" + qt[0] + "." + tld)
			}(ti, tld)
		}
		swg.Wait()
		for ti := range tlds {
			sraw := sites[ti]
			for k, sh := range sraw {
				if k >= 200 || added >= 12 {
					break
				}
				if have[sh.ID] {
					continue
				}
				if sd, ok := ix.Doc(sh.ID); ok {
					if sr := load(sd); sr.URL != "" && frontPathRe.MatchString(urlPath(sr.URL)) {
						sh.Score = raw[0].Score
						raw = append(raw, sh)
						have[sh.ID] = true
						added++
					}
				}
			}
		}
	}
	for i, h := range raw {
		d, ok := ix.Doc(h.ID)
		if !ok {
			continue
		}
		rec := load(d)
		if rec.URL == "" { // an erased page: nothing to show
			continue
		}
		if isBlocked(d.Host) || adultPage(d.Host, rec.Title, rec.Description) || (de && !sgpack.GermanURL(rec.URL) && !sgpack.IsGermanText(rec.URL, rec.Title, rec.Description, rec.full())) || (!de && !isEnglish(rec) && !isSubmittedHost(d.Host)) {
			continue
		}
		sc := h.Score
		tt := map[string]bool{}
		for _, w := range sgindex.Tokens(rec.Title) {
			tt[w] = true
		}
		all := len(groups) > 0
		for _, g := range groups {
			found := false
			for _, t := range g {
				if tt[t] {
					found = true
				}
			}
			if !found {
				all = false
			}
		}
		if all { // every word is in the title
			sc *= 1.5
		}
		if depth := strings.Count(strings.Trim(urlPath(rec.URL), "/"), "/"); depth > 0 {
			sc /= 1 + 0.03*float64(depth)
		}
		if isFrontPageOf(d.Host, urlPath(rec.URL), qt) { // the search is the site's name: its own front page comes first
			sc *= 3
		}
		if len(qt) >= 2 && i < 40 { // the words together, in order
			if strings.Contains(strings.Join(sgindex.Tokens(rec.Title+" "+rec.Description+" "+rec.full()), " "), strings.Join(qt, " ")) {
				sc *= 1.4
			}
		}
		sc *= quality(qt, allASCII(q), d.Host, rec)
		hits = append(hits, hit{h.ID, sc, d, rec})
	}
	// a page that was stored more than once (a recrawl found it changed): only the newest version is shown
	newest := map[string]int{}
	for i, h := range hits {
		if j, ok := newest[h.rec.URL]; !ok || h.id > hits[j].id {
			newest[h.rec.URL] = i
		}
	}
	latest := hits[:0]
	for i, h := range hits {
		if newest[h.rec.URL] == i {
			latest = append(latest, h)
		}
	}
	hits = latest
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	// the same title twice (a mirror, or www and non-www) is shown once
	// (pages of one site that share a title, like a site with a single title everywhere, all stay)
	seenTitle := map[string]string{}
	seenVariant := map[string]bool{}
		seenHostTitle := map[string]bool{} // site + title seen: tag, archive and author pages with one shared title are shown once
	seenPost := map[string]bool{} // the same post cross-posted to many servers: same title and same opening text
	uniq := hits[:0]
	for _, h := range hits {
		t := strings.Join(sgindex.Tokens(h.rec.Title), " ")
		// the same title on www.x and x is one site shown twice; another subdomain (ed2k.x) is its own result
		if host, ok := seenTitle[t]; len(t) > 12 && ok && host != h.d.Host && strings.TrimPrefix(host, "www.") == strings.TrimPrefix(h.d.Host, "www.") {
			continue
		}
		if _, ok := seenTitle[t]; !ok {
			seenTitle[t] = h.d.Host
		}
		// the same page with other sorting or filter options in its address is shown once
		if len(t) > 0 {
			k := h.d.Host + "\x00" + urlPath(h.rec.URL) + "\x00" + t
			if seenVariant[k] {
				continue
			}
			seenVariant[k] = true
			}
			if hk := h.d.Host + "\x00" + t; len(t) > 0 && !strings.Contains(strings.ToLower(q), "site:") { // browsing a site with site: keeps every page
				if seenHostTitle[hk] {
					continue
				}
				seenHostTitle[hk] = true
		}
		if len(t) > 12 {
			if body := strings.Join(sgindex.Tokens(clip(firstNonEmpty(h.rec.full(), h.rec.Description), 200)), " "); len(body) > 40 {
				if k := t + "\x00" + body; seenPost[k] {
					continue
				} else {
					seenPost[k] = true
				}
			}
		}
		uniq = append(uniq, h)
	}
	hits = uniq
	if len(hits) > 300 {
		hits = hits[:300]
	}
	// no site fills the whole page: each extra result from the same host counts for less
	out := make([]hit, 0, len(hits))
	perHost := map[string]int{}
	rest := append([]hit(nil), hits...)
	for len(out) < 100 && len(rest) > 0 {
		best, bi := -1.0, 0
		for i, h := range rest {
			adj := h.score / (1 + 0.35*float64(perHost[h.d.Host]))
			if adj > best {
				best, bi = adj, i
			}
		}
		perHost[rest[bi].d.Host]++
		out = append(out, rest[bi])
		rest = append(rest[:bi], rest[bi+1:]...)
	}
	out, jevMs, cut = jevReorder(q, out)
	return terms, out, total, jevMs, cut
}

// jevReorder lets Jev judge the first 30 results of a search. A result that Jev says the searcher would not care about
// at all (keep below 0.25 and good below 0.2) is removed, as long as the best result and at least two stay; the rest are put in the order Jev prefers, keeping 30%
// of the normal order so one odd answer cannot throw a good result to the bottom. If Jev does not answer in time,
// nothing changes. It returns the new list, how long Jev took (0: not used) and how many results were removed.
func jevReorder(q string, out []hit) ([]hit, int64, int) {
	n := min(len(out), jevWindow)
	if n < 2 || strings.Contains(strings.ToLower(q), "site:") {
		return out, 0, 0
	}
	t0 := time.Now()
	items := make([]rrItem, n)
	for i := 0; i < n; i++ {
		items[i] = rrItem{out[i].rec.Title, out[i].rec.URL, firstNonEmpty(out[i].rec.Description, out[i].rec.Text)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	good, keep, dup, err := jevScores(ctx, q, items)
	if err != nil {
		return out, 0, 0
	}
	type ranked struct {
		h          hit
		v          float64
		good, keep float64
	}
	r := make([]ranked, 0, n)
	cut := 0
	for i := 0; i < n; i++ {
		if i > 0 && dup[i] >= 0.7 { // the same post or story as a result above it: shown once
			cut++
			continue
		}
		r = append(r, ranked{out[i], 0.7*good[i] + 0.3*(1-float64(i)/float64(n)), good[i], keep[i]})
	}
	sort.SliceStable(r, func(a, b int) bool { return r[a].v > r[b].v })
	kept := make([]hit, 0, len(out))
	low := 0
	for i, x := range r {
		if x.keep < 0.25 && x.good < 0.2 && i >= 1 && len(r)-low > 2 { // Jev says it is nonsense for this search; the best result and at least two stay
			low++
			cut++
			continue
		}
		kept = append(kept, x.h)
	}
	kept = append(kept, out[n:]...)
	return kept, max(1, time.Since(t0).Milliseconds()), cut
}

func urlPath(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return u.Path
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

// highlight escapes text for the page. The query words are no longer marked.
func highlight(text string, terms []string) string {
	return html.EscapeString(text)
}

func snippetOf(r record, terms []string) string {
	text := r.Text
	if len(text) > 0 {
		low := strings.ToLower(text)
		at := -1
		for _, t := range terms {
			if i := strings.Index(low, t); i >= 0 && (at < 0 || i < at) {
				at = i
			}
		}
		if at >= 0 && (r.Description == "" || !containsAny(strings.ToLower(r.Description), terms)) {
			start := at - 80
			if start < 0 {
				start = 0
			}
			for start > 0 && !utf8Start(text[start]) {
				start--
			}
			end := start + 220
			if end > len(text) {
				end = len(text)
			}
			for end < len(text) && !utf8Start(text[end]) {
				end++
			}
			s := text[start:end]
			if start > 0 {
				s = "… " + s
			}
			if end < len(text) {
				s += " …"
			}
			return s
		}
	}
	if r.Description != "" {
		return r.Description
	}
	if len(text) > 220 {
		return text[:220] + " …"
	}
	return text
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func containsAny(s string, terms []string) bool {
	for _, t := range terms {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

func load(d sgindex.Doc) (r record) {
	buf := make([]byte, d.N)
	if _, err := file.ReadAt(buf, d.Off); err == nil {
		json.Unmarshal(buf, &r)
	}
	return
}

type result struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Host        string `json:"host"`
	Description string `json:"description"`
	Snippet     string `json:"snippet"`
	Source      string `json:"source"`
}

// card is the box shown beside the results when the best match is a wiki article.
type card struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Text        string `json:"text"`
	URL         string `json:"url"`
	Host        string `json:"host"`
}

type answer struct {
	Query      string   `json:"query"`
	Total      int      `json:"total"` // how many pages match
	Page       int      `json:"page"`
	PerPage    int      `json:"per_page"`
	Pages      int      `json:"pages"` // pages of results you can open (at most 10)
	TookMs     int64    `json:"took_ms"`
	JevCut     int      `json:"jev_removed,omitempty"` // results among the first 10 that Jev removed as nonsense for this search
	JevMs      int64    `json:"jev_ms,omitempty"` // how long Jev took to order the top results (0: it did not)
	Results    []result `json:"results"`
	DidYouMean string   `json:"did_you_mean,omitempty"`
	Related    []string `json:"related,omitempty"`
	Narrow     []string `json:"narrow,omitempty"`
	terms      []string
}

func run(q string, page, per int, de bool) answer { return runWith(q, page, per, de, "") }

// runWith is run for one kind of page ("" = all): only the pages Jev labelled with that kind among the best 30.
func runWith(q string, page, per int, de bool, kind string) answer {
	t0 := time.Now()
	terms, hits, total, jevMs, jevCut := search(q, de)
	if kind != "" {
		hits = filterKind(hits, kind)
		total = len(hits)
	}
	a := answer{Query: q, Total: total, JevMs: jevMs, JevCut: jevCut, Page: page, PerPage: per, terms: terms, Results: []result{}}
	a.Pages = (len(hits) + per - 1) / per
	if a.Pages > 10 {
		a.Pages = 10
	}
	from := (page - 1) * per
	for i := from; i < len(hits) && i < from+per; i++ {
		h := hits[i]
		r := h.rec
		r.Text = r.full()
		a.Results = append(a.Results, result{URL: r.URL, Title: firstNonEmpty(r.Title, r.URL), Host: h.d.Host, Description: cleanSnippet(r.Description), Snippet: cleanSnippet(snippetOf(r, terms)), Source: r.Source})
	}
	var words []string
	site := ""
	for _, f := range strings.Fields(q) {
		if strings.HasPrefix(strings.ToLower(f), "site:") {
			site = f
		} else {
			words = append(words, f)
		}
	}
	qt := map[string]bool{}
	for _, t := range sgindex.Tokens(strings.Join(words, " ")) {
		qt[t] = true
	}
	if page == 1 && len(hits) > 0 {
		// related searches: words that turn up in the best titles
		count := map[string]int{}
		hostSeen := map[string]bool{}
		for i := 0; i < len(hits) && i < 12; i++ {
			for _, w := range sgindex.Tokens(hits[i].rec.Title) {
				if len(w) >= 4 && !qt[w] && !sgindex.IsStop(w) {
					count[w]++
				}
			}
			if h := strings.TrimPrefix(hits[i].d.Host, "www."); !hostSeen[h] && len(a.Narrow) < 4 && site == "" {
				hostSeen[h] = true
				a.Narrow = append(a.Narrow, h)
			}
		}
		type wc struct {
			w string
			n int
		}
		var l []wc
		for w, n := range count {
			if n >= 2 {
				l = append(l, wc{w, n})
			}
		}
		sort.Slice(l, func(i, j int) bool {
			if l[i].n != l[j].n {
				return l[i].n > l[j].n
			}
			return l[i].w < l[j].w
		})
		for i := 0; i < len(l) && i < 6; i++ {
			a.Related = append(a.Related, strings.TrimSpace(strings.Join(words, " ")+" "+l[i].w))
		}
	}
	if total < 5 && len(words) > 0 && len(words) <= 4 && len(q) <= 60 { // few results: maybe a typo
		fixed, changed := []string{}, false
		for _, t := range sgindex.Tokens(strings.Join(words, " ")) {
			if alt := ix.SpellFix(t); alt != "" {
				fixed, changed = append(fixed, alt), true
			} else {
				fixed = append(fixed, t)
			}
		}
		if changed {
			a.DidYouMean = strings.TrimSpace(strings.Join(fixed, " ") + " " + site)
		}
	}
	a.TookMs = time.Since(t0).Milliseconds()
	return a
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// ---- limits, pages, handlers ----

var (
	rmu  sync.Mutex
	hitT = map[string][]time.Time{}
)

func limited(r *http.Request) bool {
	ip := clientIP(r)
	rmu.Lock()
	defer rmu.Unlock()
	now := time.Now()
	h := hitT[ip][:0]
	for _, t := range hitT[ip] {
		if now.Sub(t) < time.Minute {
			h = append(h, t)
		}
	}
	h = append(h, now)
	hitT[ip] = h
	return len(h) > 120
}

func params(r *http.Request) (string, int, int) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		q = q[:200]
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 10 {
		page = 10
	}
	per, _ := strconv.Atoi(r.URL.Query().Get("per"))
	if per < 1 || per > 50 {
		per = 10
	}
	return q, page, per
}

type hostCount struct {
	Host  string `json:"host"`
	Pages int    `json:"pages"`
}

func thisRun() int {
	n, _ := currentRun(runsNow(), ix.NDocs())
	return n
}

func statsJSON() map[string]any {
	var top []hostCount
	_, _, hs := ix.HostsWhere("en", 15)
	for _, h := range hs {
		top = append(top, hostCount{h.Host, h.Pages})
	}
	return map[string]any{"pages": ix.NDocs(), "sites": ix.Sites(), "terms": ix.Terms(), "characters": ix.Characters(), "segments": ix.Segments(), "pages_per_minute": perMinute(), "pages_per_hour": perHour(), "pages_this_run": thisRun(), "top_sites": top}
}

const css = `:root{color-scheme:dark light;--bg:#0b0d12;--bg2:#11141b;--card:#151923;--line:#232938;--ink:#eaedf4;--dim:#8b95a8;--acc:#ffb347;--acc-ink:#1a1204;--link:#9bcbff;--glow:rgba(255,179,71,.12);--aero1:#1f5fc4;--aero2:#0b2e6e;--aero-glass:rgba(255,255,255,.08)}
@media (prefers-color-scheme:light){:root{--bg:#f6f4ef;--bg2:#fffdf8;--card:#fff;--line:#e3dfd6;--ink:#1b1a17;--dim:#6b665c;--acc:#c96a00;--acc-ink:#fff;--link:#1553b3;--glow:rgba(201,106,0,.12)}}
*{box-sizing:border-box}
html{background:var(--bg)}
body{margin:0;min-height:100vh;color:var(--ink);font:16px/1.55 "Segoe UI",system-ui,-apple-system,Roboto,sans-serif;background:radial-gradient(700px 420px at 12% -120px,rgba(31,95,196,.38),transparent 70%),radial-gradient(900px 420px at 88% -140px,var(--glow),transparent 70%),var(--bg);-webkit-font-smoothing:antialiased}
a{color:var(--link)}
.wrap{max-width:1100px;margin:0 auto;padding:28px 20px 80px}
.top{position:sticky;top:0;z-index:5;background:linear-gradient(to bottom,color-mix(in srgb,var(--aero1) 55%,transparent),color-mix(in srgb,var(--aero2) 70%,transparent) 90%),color-mix(in srgb,var(--bg) 60%,transparent);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);border-bottom:1px solid color-mix(in srgb,var(--aero1) 60%,var(--line));box-shadow:0 1px 0 rgba(255,255,255,.25) inset,0 2px 8px rgba(0,0,0,.25)}
.bar{max-width:1100px;margin:0 auto;display:grid;grid-template-columns:1fr minmax(0,760px) 1fr;align-items:center;gap:20px;padding:12px 20px}
.logo{display:inline-flex;align-items:center;text-decoration:none;color:var(--ink);font-weight:800;letter-spacing:-.02em;font-size:21px;flex:none}
.logo.colorful{font-family:"Comic Sans MS","Segoe UI",system-ui,sans-serif;font-size:26px;font-weight:700}
.hero .logo.colorful{font-size:52px}
.dvd{position:fixed;top:0;left:0;z-index:40;pointer-events:none;font-weight:800;font-size:15px;letter-spacing:.04em;padding:8px 14px;border-radius:6px;white-space:nowrap;background:rgba(0,0,0,.35);color:#ffb347;text-shadow:0 1px 3px rgba(0,0,0,.5)}
.links{display:flex;gap:18px;flex:none;justify-self:end}.links a{color:var(--dim);text-decoration:none;font-size:14px}.links a:hover{color:var(--ink)}
.sbox{position:relative;flex:1;min-width:0;display:block;margin:0}
.sbox input{width:100%;background:var(--card);border:1px solid var(--line);color:var(--ink);border-radius:15px;padding:13px 104px 13px 20px;font:inherit;font-size:18px;transition:border-color .15s,box-shadow .15s}
.sbox input::placeholder{color:var(--dim)}
.sbox input:focus{outline:none;border-color:var(--acc);box-shadow:0 0 0 4px var(--glow)}
.sbox button{position:absolute;right:5px;top:5px;bottom:5px;padding:0 17px;border-radius:10px;border:1px solid color-mix(in srgb,var(--aero1) 60%,black);background:linear-gradient(to bottom,color-mix(in srgb,var(--aero1) 70%,white) 0%,var(--aero1) 45%,var(--aero2) 100%);color:#fff;font:inherit;font-weight:700;cursor:pointer;box-shadow:0 1px 0 rgba(255,255,255,.5) inset,0 2px 4px rgba(0,0,0,.3)}
.sbox button:hover{filter:brightness(1.1)}
.sbox button:active{filter:brightness(.9);box-shadow:0 1px 2px rgba(0,0,0,.4) inset}
.top{top:10px;background:none;backdrop-filter:none;-webkit-backdrop-filter:none;border:0;box-shadow:none;pointer-events:none}
.top .bar{grid-template-columns:auto minmax(0,640px) auto;justify-content:center;padding:0 14px}
.top .bar>*{pointer-events:auto}
.top .logo,.top .links,.top .sbox{background:color-mix(in srgb,var(--card) 38%,transparent);backdrop-filter:blur(16px) saturate(1.7);-webkit-backdrop-filter:blur(16px) saturate(1.7);border:1px solid rgba(255,255,255,.28);box-shadow:0 8px 24px -10px rgba(0,0,0,.45),0 1px 0 rgba(255,255,255,.35) inset}
.top .logo{padding:5px 16px;border-radius:99px}
.top .links{padding:9px 18px;border-radius:99px}
.top .sbox{border-radius:99px}
.top .sbox input{background:transparent;border:0;border-radius:99px;padding:13px 104px 13px 22px;position:relative;z-index:3}
.top .sbox input:focus{box-shadow:none}
.top .sbox:focus-within{outline:2px solid var(--acc);outline-offset:1px}
.top .sbox button{z-index:3}
.lg-on .top .sbox{background:none;backdrop-filter:none;-webkit-backdrop-filter:none;box-shadow:none;border-color:transparent}
.glassPane{position:absolute;inset:0;pointer-events:none}
.top .sbox .glassPane{z-index:4}
.hero .sbox .glassPane{z-index:2}
.top .sbox input,.top .sbox button{z-index:5}
@media(max-width:900px){.top{top:0;background:var(--bg);background:linear-gradient(to bottom,var(--bg) 78%,transparent)}.top .bar{justify-content:space-between;padding:8px 12px 14px}.top .sbox{flex-basis:100%}}
@media(min-width:901px){body:has(.top)::before{content:"";position:fixed;left:0;right:0;top:0;height:92px;z-index:3;pointer-events:none;background:linear-gradient(to bottom,var(--bg) 30%,transparent)}}
article.r{padding:18px 22px;margin:0 0 14px;border:1px solid var(--line);border-radius:20px;background:var(--card);background:color-mix(in srgb,var(--card) 84%,transparent);box-shadow:0 10px 30px -22px rgba(0,0,0,.6),0 1px 0 rgba(255,255,255,.06) inset;transition:transform .18s,border-color .18s,box-shadow .18s}
article.r:hover{transform:translateY(-2px);border-color:var(--acc);border-color:color-mix(in srgb,var(--acc) 45%,var(--line));box-shadow:0 18px 40px -24px rgba(0,0,0,.7),0 1px 0 rgba(255,255,255,.08) inset}
.r+.r{border-top:1px solid var(--line)}
article.r .t{font-size:21px;font-weight:600}
article.r .s{font-size:16px;opacity:.82}
.hero .sbox{z-index:2;max-width:700px;border-radius:99px;background:var(--card);background:color-mix(in srgb,var(--card) 38%,transparent);backdrop-filter:blur(18px) saturate(1.7);-webkit-backdrop-filter:blur(18px) saturate(1.7);border:1px solid rgba(255,255,255,.3);box-shadow:0 14px 40px -16px rgba(0,0,0,.55),0 1px 0 rgba(255,255,255,.35) inset}
.hero .sbox input{background:transparent;border:0;border-radius:99px;padding:19px 128px 19px 30px;font-size:19px;box-shadow:none;position:relative;z-index:3}
.hero .sbox input:focus{box-shadow:none}
.hero .sbox:focus-within{outline:2px solid var(--acc);outline-offset:2px}
.hero .sbox button{z-index:3;right:8px;top:8px;bottom:8px;padding:0 26px;border-radius:99px}
.lg-on .hero .sbox{background:none;backdrop-filter:none;-webkit-backdrop-filter:none;box-shadow:none;border-color:transparent}
.tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(190px,1fr));gap:14px;max-width:860px;margin:38px auto 0;text-align:left}
.tile{display:block;padding:18px 20px 20px;border-radius:24px;border:1px solid var(--line);border-color:rgba(255,255,255,.18);background:var(--card);background:color-mix(in srgb,var(--card) 62%,transparent);backdrop-filter:blur(14px) saturate(1.5);-webkit-backdrop-filter:blur(14px) saturate(1.5);color:var(--ink);text-decoration:none;box-shadow:0 14px 34px -24px rgba(0,0,0,.7),0 1px 0 rgba(255,255,255,.12) inset;transition:transform .18s,border-color .18s}
.tile:hover{transform:translateY(-3px);border-color:var(--acc)}
.tile svg{color:var(--acc);display:block;margin-bottom:10px}
.tile b{display:block;font-size:18px;margin-bottom:4px}
.tile span{display:block;color:var(--dim);font-size:14.5px;line-height:1.45}
.lucky{margin-top:26px}
main.wrap h1{font-size:34px;letter-spacing:-.02em}
.scard,.changelog li,.kp{border-radius:22px;border-color:rgba(255,255,255,.16);background:var(--card);background:color-mix(in srgb,var(--card) 62%,transparent);backdrop-filter:blur(14px) saturate(1.4);-webkit-backdrop-filter:blur(14px) saturate(1.4);box-shadow:0 14px 34px -26px rgba(0,0,0,.7),0 1px 0 rgba(255,255,255,.08) inset}
.sugg{border-radius:22px;border-color:rgba(255,255,255,.2);background:var(--card);background:color-mix(in srgb,var(--card) 78%,transparent);backdrop-filter:blur(22px) saturate(1.5);-webkit-backdrop-filter:blur(22px) saturate(1.5)}
.pbtn{border-radius:99px;padding:8px 17px}
.chip{backdrop-filter:blur(10px);-webkit-backdrop-filter:blur(10px)}
code,pre{border-radius:12px}
.foot{padding-top:14px;line-height:1.9}
button:focus-visible,a:focus-visible{outline:2px solid var(--acc);outline-offset:2px;border-radius:6px}
.hero{text-align:center;padding-top:10vh}
.hero .logo{font-size:56px;gap:10px}.hero .logo b{font-size:44px;border-radius:14px;padding:0 16px}.hero .logo span{font-size:50px}
.hero .tag{color:var(--dim);margin:14px auto 30px;max-width:30em}
.hero .sbox{max-width:660px;margin:0 auto}
.hero .sbox input{padding:17px 112px 17px 24px;font-size:18px;border-radius:20px;box-shadow:0 8px 30px -14px rgba(0,0,0,.5)}
.hero .sbox button{right:7px;top:7px;bottom:7px;border-radius:14px;padding:0 22px}
.hint{display:inline-block;border:1px solid var(--line);border-radius:6px;padding:0 6px;font:12px ui-monospace,Menlo,monospace;color:var(--dim);margin-left:4px}
.try{margin:26px 0 0;color:var(--dim);font-size:14px}
.try h4,.sites h4{margin:0 0 10px;font-size:12px;font-weight:600;letter-spacing:.08em;text-transform:uppercase;color:var(--dim)}
.chips{display:flex;flex-wrap:wrap;gap:8px;justify-content:center}
.chip{display:inline-block;background:linear-gradient(to bottom,color-mix(in srgb,var(--card) 90%,white 8%),var(--card));border:1px solid var(--line);border-radius:999px;padding:6px 16px;font-size:15.5px;color:var(--ink);text-decoration:none;transition:border-color .15s,transform .15s;box-shadow:0 1px 0 rgba(255,255,255,.15) inset}
.chip:hover{border-color:var(--aero1)}
.chip:hover{border-color:var(--acc);transform:translateY(-1px)}.chip small{color:var(--dim);margin-left:6px}
.sites{margin-top:38px}.lucky{margin:16px 0 0;text-align:center}
.stats{color:var(--dim);font-size:14px;margin-top:34px}.stats b{color:var(--ink);font-variant-numeric:tabular-nums}
.meta{color:var(--dim);font-size:15px;margin:0 0 4px}
.results{width:100%;min-width:0}
.layout{display:grid;grid-template-columns:minmax(0,700px) minmax(0,340px);gap:48px;justify-content:center;align-items:start}
.side{min-width:0}
.kp{position:sticky;top:92px;background:var(--card);border:1px solid var(--line);border-radius:18px;padding:20px 22px;margin-top:6px;animation:in .3s ease both}.kp::after{content:"";display:block;clear:both}
.kimg{display:block;width:100%;max-height:210px;border-radius:12px;margin:2px 0 14px;object-fit:cover}
.kp h2{margin:8px 0 2px;font-size:25px;line-height:1.25;letter-spacing:-.01em}.kp .kd{color:var(--dim);margin:0 0 10px;font-size:14px}.kp p{margin:8px 0;font-size:16.5px;opacity:.92}.kp a{font-size:15.5px}
.dym{margin:0 0 10px;font-size:19px}.dym a{text-decoration:none}.dym b{font-weight:700}
.narrow{color:var(--dim);font-size:15px;margin:0 0 4px}.narrow a{text-decoration:none}
.related{margin-top:36px;text-align:center}.related h4{margin:0 0 10px;font-size:12px;font-weight:600;letter-spacing:.08em;text-transform:uppercase;color:var(--dim)}.chips2{display:flex;flex-wrap:wrap;gap:9px;justify-content:center}
.sugg{position:absolute;z-index:20;left:0;right:0;top:100%;margin:8px 0 0;padding:6px;list-style:none;background:var(--card);border:1px solid var(--line);border-radius:16px;box-shadow:0 18px 40px -16px rgba(0,0,0,.55)}
.sugg li{padding:11px 16px;border-radius:10px;cursor:pointer;text-align:left;font-size:17.5px}.sugg li.on,.sugg li:hover{background:var(--glow)}
.pbtn.cur{background:var(--acc);border-color:var(--acc);color:var(--acc-ink)}
.r{padding:23px 0;animation:in .28s ease both}.r .shot{float:right;width:160px;height:100px;object-fit:cover;margin:2px 0 8px 16px;border:1px solid var(--line);border-radius:10px}@media(max-width:560px){.r .shot{width:96px;height:60px}}.r+.r{border-top:1px solid var(--line)}
@keyframes in{from{opacity:0;transform:translateY(5px)}to{opacity:1;transform:none}}
.site{display:flex;align-items:center;gap:11px;font-size:15px;color:var(--dim);margin-bottom:7px;min-width:0}
.av{flex:none;position:relative;overflow:hidden;width:28px;height:28px;border-radius:8px;display:grid;place-items:center;font-size:13px;font-weight:700;color:#fff}
.av img{position:absolute;inset:0;width:100%;height:100%;object-fit:contain;padding:3px;background:var(--card);border-radius:8px}
.site b{color:var(--ink);font-weight:600;flex:none}.site i{font-style:normal;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0}
.pill{flex:none;border:1px solid var(--line);border-radius:999px;padding:0 9px;font-size:12px;letter-spacing:.03em}
.t{display:inline-block;font-size:23px;line-height:1.3;color:var(--link);text-decoration:none;overflow-wrap:anywhere;font-weight:500}.r .t{display:inline}
.t:hover{text-decoration:underline;text-underline-offset:3px}
.s{margin:8px 0 0;font-size:17px;line-height:1.6;color:var(--ink);opacity:.84;overflow-wrap:anywhere;display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;overflow:hidden}
mark{background:var(--glow);box-shadow:inset 0 -2px 0 var(--acc);color:inherit;padding:0 1px;border-radius:2px}
.pager{display:flex;gap:8px;align-items:center;flex-wrap:wrap;justify-content:center;margin-top:30px}
.pbtn{border:1px solid var(--line);background:var(--card);color:var(--ink);text-decoration:none;border-radius:12px;padding:8px 15px;min-width:44px;font-size:16px;text-align:center;font-weight:600;transition:border-color .15s}
.pbtn:hover{border-color:var(--acc)}
.empty{text-align:center;padding:54px 10px;color:var(--dim)}.empty h2{color:var(--ink);margin:0 0 8px;font-size:22px}
.empty ul{display:inline-block;text-align:left;margin:14px 0 0;padding-left:20px}
.foot{max-width:1100px;margin:0 auto;padding:0 20px 40px;justify-content:center;color:var(--dim);font-size:13px;display:flex;gap:16px;flex-wrap:wrap}
.foot a{color:var(--dim)}
.changelog{list-style:none;padding:0;margin:20px 0;display:flex;flex-direction:column;gap:14px;max-width:700px}
.changelog li{display:flex;gap:16px;padding:12px 16px;background:var(--card);border:1px solid var(--line);border-radius:10px}
.changelog time{color:var(--acc);font-size:13px;white-space:nowrap;min-width:90px}
.scard{max-width:500px;background:var(--card);border:1px solid var(--line);border-radius:10px;padding:6px 18px;margin:18px 0}
.srow{display:flex;align-items:center;gap:10px;padding:10px 0;border-bottom:1px solid var(--line)}
.srow:last-child{border-bottom:0}
.sdot{width:9px;height:9px;border-radius:50%;flex:none}
.slabel{margin-left:auto;color:var(--dim)}
.changelog a{color:var(--ink);text-decoration:none}
.changelog a:hover{color:var(--acc);text-decoration:underline}
.sub{color:var(--dim);margin:0 0 22px}.grid{display:flex;flex-wrap:wrap;gap:8px;margin-top:8px}
code,pre{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:2px 6px;font-size:14px}pre{padding:12px;overflow-x:auto}
@media (max-width:900px){.layout{display:flex;flex-direction:column}.side{order:-1}.kp{position:static}.bar{display:flex;flex-wrap:wrap;gap:12px}.bar .sbox{order:3;flex-basis:100%}.links{margin-left:auto}}
@media (max-width:640px){.bar{flex-wrap:wrap;gap:10px}.sbox{order:3;flex-basis:100%}.links{margin-left:auto}.hero{padding-top:6vh}.hero .logo{font-size:44px}.hero .logo b{font-size:34px}.hero .logo span{font-size:40px}.t{font-size:21px}.s{font-size:16.5px}}
@media (prefers-reduced-motion:reduce){*{animation:none!important;transition:none!important}}`

const uiJS = `(function(){
  const konami=[38,38,40,40,37,39,37,39,66,65];let ki=0;
  document.addEventListener('keydown',e=>{
    ki=(e.keyCode===konami[ki])?ki+1:0;
    if(ki===konami.length){
      ki=0;
      document.body.style.transition='transform 1.2s';
      document.body.style.transform='rotate(360deg)';
      setTimeout(()=>{document.body.style.transform=''},1200);
      const t=document.getElementById('tagline');
      if(t)t.textContent='You found the Konami code. Nothing else happens. We made our own search engine, not a game studio.';
    }
  });
})();
(function(){
  const tl=document.getElementById('tagline');
  if(tl){
    const lines=['Ever felt like the internet isn\'t fun anymore? We made our own.','A search engine we made ourselves: its own crawler, its own index, its own ranking.','3M+ pages. Zero databases. One very stubborn server.','Built from scratch because we felt like it.','No ads, no tracking, no SQLite - just us and some JSONL files.'];
    tl.textContent=lines[Math.floor(Math.random()*lines.length)];
  }
})();
(function(){
  const el=document.getElementById('dvd');if(!el)return;
  let x=40,y=40,vx=2.2,vy=1.7,hue=0;
  function step(){
    const w=el.offsetWidth,h=el.offsetHeight;
    const maxX=innerWidth-w,maxY=innerHeight-h;
    x+=vx;y+=vy;
    const minSpeed=0.8;
    const hitX=x<=0||x>=maxX,hitY=y<=0||y>=maxY;
    const oldX=x,oldY=y;
    if(hitX)x=Math.max(0,Math.min(x,maxX));
    if(hitY)y=Math.max(0,Math.min(y,maxY));
    if(hitX&&!hitY){
      vx=-vx;
      if(Math.random()<0.95){
        const distY=vy<0?oldY:maxY-oldY;
        const time=maxX/Math.abs(vx);
        vy=Math.sign(vy)*Math.max(minSpeed,distY/time);
      }
    }
    if(hitY&&!hitX){
      vy=-vy;
      if(Math.random()<0.95){
        const distX=vx<0?oldX:maxX-oldX;
        const time=maxY/Math.abs(vy);
        vx=Math.sign(vx)*Math.max(minSpeed,distX/time);
      }
    }
    if(hitX&&hitY){vx=-vx;vy=-vy}
    hue=(hue+1.5)%360;
    el.style.color='hsl('+hue+',100%,65%)';
    el.style.transform='translate('+x+'px,'+y+'px)';
    requestAnimationFrame(step);
  }
  requestAnimationFrame(step);
})();
(function(){
  const inp=document.querySelector('.sbox input');if(!inp)return;
  const box=inp.closest('.sbox');const ul=document.createElement('ul');ul.className='sugg';ul.hidden=true;box.append(ul);
  let items=[],sel=-1,timer=0,last='';
  function hide(){ul.hidden=true;sel=-1}
  function render(){ul.textContent='';items.forEach((s,i)=>{const li=document.createElement('li');li.textContent=s;li.dataset.i=i;if(i===sel)li.className='on';ul.append(li)});ul.hidden=!items.length}
  async function ask(){
    const q=inp.value;if(q.trim().length<2){items=[];render();return}
    if(q===last)return;last=q;
    try{const r=await fetch('/api/suggest?q='+encodeURIComponent(q));items=await r.json()}catch(e){items=[]}
    sel=-1;render();
  }
  inp.addEventListener('input',()=>{clearTimeout(timer);timer=setTimeout(ask,70)});
  inp.addEventListener('keydown',e=>{
    if(ul.hidden)return;
    if(e.key==='ArrowDown'){e.preventDefault();sel=(sel+1)%items.length;render()}
    else if(e.key==='ArrowUp'){e.preventDefault();sel=(sel-1+items.length)%items.length;render()}
    else if(e.key==='Enter'&&sel>=0){e.preventDefault();inp.value=items[sel];box.requestSubmit()}
    else if(e.key==='Escape')hide();
  });
  ul.addEventListener('mousedown',e=>{const li=e.target.closest('li');if(li){e.preventDefault();inp.value=items[+li.dataset.i];box.requestSubmit()}});
  inp.addEventListener('blur',()=>setTimeout(hide,150));
})();
document.addEventListener('keydown',e=>{
  const t=document.activeElement&&document.activeElement.tagName;
  if(e.key==='/'&&t!=='INPUT'&&t!=='TEXTAREA'){e.preventDefault();const i=document.querySelector('.sbox input');if(i){i.focus();i.select()}}
  if(e.key==='Escape'&&t==='INPUT')document.activeElement.blur();
});`

const svgIcon = `<link rel=icon href=/favicon.svg><link rel=search type="application/opensearchdescription+xml" title="Flowsearch" href=/opensearch.xml>`

func shell(title, inner string) string {
	return fmt.Sprintf(`<!doctype html><html lang=en><head><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1"><meta name=color-scheme content="dark light"><title>%s</title>%s<style>%s</style></head><body>%s<footer class=foot><span>Flowsearch</span><a href=/live>Live stats</a><a href=/screenshots>Screenshots</a><a href=/wall>Screenshot wall</a><a href=/quiz>Guess the website</a><a href=/compare>Compare</a><a href=/tools>Tools</a><a href=/more>More</a><a href=/looks>Search by looks</a><a href=/minecraft>Minecraft PvP</a><a href=/settings>Settings</a><a href=/news>News</a><a href=/privacy>Privacy</a><a href=/status>Status</a><a href=/classic>Classic look</a><a href=/api>For developers</a><a href=/webmaster>For site owners</a><a href=/de>Deutsch</a><span>an independent search engine</span></footer><script>%s</script></body></html>`,
		html.EscapeString(title), svgIcon, css+uiCSS, inner, uiJS)
}

var logoColors = []string{"#4285F4", "#EA4335", "#FBBC05", "#4285F4", "#34A853", "#EA4335", "#4285F4", "#34A853", "#EA4335", "#FBBC05"}

func logoHTML() string {
	var b strings.Builder
	b.WriteString(`<a class="logo colorful" href=/ aria-label="Flowsearch home">`)
	for i, ch := range "Flowsearch" {
		fmt.Fprintf(&b, `<span style="color:%s">%c</span>`, logoColors[i%len(logoColors)], ch)
	}
	b.WriteString(`</a>`)
	return b.String()
}

func searchForm(q, placeholder string, focus bool) string {
	af := ""
	if focus {
		af = " autofocus"
	}
	return fmt.Sprintf(`<form class=sbox action=/search role=search><input name=q value="%s" aria-label=Search placeholder="%s" autocomplete=off spellcheck=false%s><button>Search</button></form>`,
		html.EscapeString(q), html.EscapeString(placeholder), af)
}

// page is the layout for every page except the home page: a sticky header with a small search box.
func page(title, q, body string) string {
	return shell(title, `<header class=top><div class=bar>`+logoHTML()+searchForm(q, "Search Flowsearch", q == "")+`<nav class=links><a href=/chat>AI mode</a><a href="/surprise?pick=page">Surprise me</a><a href=/more>More</a><a href=/settings>Settings</a></nav></div></header><main class=wrap>`+body+`</main>`)
}

func hueOf(host string) int { return int(crc32.ChecksumIEEE([]byte(host)) % 360) }

var shotsDir = fsMedia + "/flowsearch-shots" // website screenshots sent by the flowsearch-shots GitHub Action

func avatar(host string) string {
	h := strings.TrimPrefix(host, "www.")
	r, _ := utf8.DecodeRuneInString(h)
	// the site's own icon from Google's favicon service sits on top; if it cannot load, the colored initial shows.
	// the icon comes from our own flowcdn (fetched and converted once by this server), so Google never sees who looked at which site
	return fmt.Sprintf(`<span class=av style="background:hsl(%d 52%% 42%%)" aria-hidden=true>%s<img src="/flowcdn/fav/%s.jxl" width=22 height=22 alt="" loading=lazy onerror="this.remove()"></span>`,
		hueOf(h), html.EscapeString(strings.ToUpper(string(r))), url.QueryEscape(h))
}

func homeBody(r *http.Request, withDVD bool, maxSites int) string {
	st := statsJSON()
	var chips, tries strings.Builder
	topSites := st["top_sites"].([]hostCount)
	if isDE(r) { // the German home page lists German sites
		topSites = nil
		_, _, hs := ix.HostsWhere("de", 15)
		for _, h := range hs {
			topSites = append(topSites, hostCount{h.Host, h.Pages})
		}
	}
	if maxSites > 0 && len(topSites) > maxSites {
		topSites = topSites[:maxSites]
	}
	for _, h := range topSites {
		if adultHost(h.Host) {
			continue
		}
		chips.WriteString(fmt.Sprintf(`<a class=chip href="/search?q=%s">%s<small>%s</small></a>`, url.QueryEscape("site:"+h.Host), html.EscapeString(h.Host), humanN(h.Pages)))
	}
	for _, q := range []string{"linux kernel", "python asyncio", "site:man7.org mmap", "lua table", "firefox"} {
		tries.WriteString(fmt.Sprintf(`<a class=chip href="/search?q=%s">%s</a>`, url.QueryEscape(q), html.EscapeString(q)))
	}
	dvd := ""
	if withDVD {
		dvd = `<div id=dvd class=dvd>OVER ` + pagesBanner(st["pages"].(int)) + ` PAGES INDEXED</div>`
	}
	return dvd + `<main class="wrap hero">` + logoHTML() +
		`<p class=tag id=tagline>Search the web without being tracked. Ask a question and get an answer with sources.</p>` +
		searchForm("", "Search the web", true) +
		homeTiles + `<form class=lucky action=/surprise method=get id=sformstyle="display:flex;gap:8px;justify-content:center;flex-wrap:wrap;align-items:center"><select name=pick id=spick aria-label="What to be surprised with" style="padding:9px 12px;border-radius:10px;max-width:100%"><option value=page>A random page</option><option value=amazon>Amazon (amazon.com)</option><option value=products>Amazon Germany products</option><option value=youtube>YouTube video</option><option value=music>Random music</option><option value=mace>Minecraft mace PvP</option><option value=minecraft>Minecraft PvP</option><option value=connector>Claude MCP connector</option><option value=site>A site I type…</option></select><input name=site id=ssite placeholder="a site, like wikipedia.org" aria-label="Site" autocomplete=off spellcheck=false hidden style="padding:8px 12px;border-radius:10px;min-width:200px"><button class=chip type=submit>Surprise me</button><a class=chip href=/surprise-bookmarklet>Get the "Another one" button</a></form><script>(function(){var s=document.getElementById('spick'),i=document.getElementById('ssite');function f(){i.hidden=s.value!=='site';if(!i.hidden)i.focus()}s.addEventListener('change',f);try{var k=localStorage.getItem('surprise_pick');if(k&&s.querySelector('option[value='+k+']'))s.value=k;f()}catch(e){}document.getElementById('sform').addEventListener('submit',function(){try{localStorage.setItem('surprise_pick',s.value)}catch(e){}if(s.value!=='site')i.disabled=true})})()</script>` +
		`<div class=try><h4>Try searching for</h4><div class=chips>` + tries.String() + `</div></div>` + forumsTrendingHTML() +
		fmt.Sprintf(`<div class=sites><h4>Popular websites</h4><div class=chips>%s</div></div>`, chips.String()) +
		fmt.Sprintf(`<p class=stats><b>%s</b> pages from <b>%s</b> websites · press <span class=hint>/</span> to start searching</p></main>`, humanN(st["pages"].(int)), humanN(st["sites"].(int)))
}

func home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, shell("Flowsearch", homeBody(r, dvdSetting(w, r), 0)+glassJS))
}

// pagesBanner rounds the number of pages down for the banner: 56961612 -> "56 MILLION", 640000 -> "640 THOUSAND".
func pagesBanner(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%d MILLION", n/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%d THOUSAND", n/1000)
	}
	return strconv.Itoa(n)
}

// humanN writes 1234567 as 1.2M.
func humanN(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return strconv.Itoa(n)
}

func commas(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func searchPage(w http.ResponseWriter, r *http.Request) {
	q, pg, per := params(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if q == "" {
		http.Redirect(w, r, "/", 302)
		return
	}
	if limited(r) {
		w.WriteHeader(429)
		fmt.Fprint(w, page(q, q, `<div class=empty><h2>Slow down a little</h2><p>That was a lot of searches in a minute. Try again in a moment.</p></div>`))
		return
	}
	if lq := strings.ToLower(strings.TrimSpace(q)); lq == "do a barrel roll" || lq == "askew" || lq == "tilt" {
		fmt.Fprint(w, shell(q+" - Flowsearch", `<main class="wrap hero"><p class=tag>Alright, alright. 🙄</p><p class=stats>You found it. There's nothing else here though, go search for something real.</p><p class=lucky><a class=chip href=/>Back</a></p></main><style>body{animation:barrelroll 0.03s 1}@keyframes barrelroll{from{transform:rotate(0)}to{transform:rotate(360deg)}}</style>`))
		return
	}
	if adultQuery(q) {
		fmt.Fprint(w, page(q+" - Flowsearch", q, adultQueryPage(q)))
		return
	}
	if t := r.URL.Query().Get("tab"); t == "roblox" {
		robloxSearch(w, r, q)
		return
	} else if t == "forums" {
		forumsSearch(w, r, q)
		return
	} else if t == "stack" {
		stackSearch(w, r, q)
		return
	} else if t == "videos" || t == "shopping" {
		tabSearch(w, r, q, t)
		return
	}
	vch := make(chan string, 1)
	go func() { vch <- jevVertical(q) }()
	kind := r.URL.Query().Get("kind")
	a := adultScrub(runWith(q, pg, per, isDE(r), kind))
	link := func(query string, page int) string {
		if page > 1 {
			return fmt.Sprintf("/search?q=%s&page=%d", url.QueryEscape(query), page) + kindParam(kind)
		}
		return "/search?q=" + url.QueryEscape(query) + kindParam(kind)
	}
	scard := ""
	if pg == 1 && kind == "" && !strings.Contains(strings.ToLower(q), "site:") {
		scard = spaceCard(q) // a planet, the Moon or the Sun: NASA's map and a few facts
		if scard == "" {
			scard = instantCard(q) // an instant answer: a calculation, a unit, the time in a city, dice, a colour, a password ...
		}
	}
	var b strings.Builder
	if a.DidYouMean != "" {
		b.WriteString(fmt.Sprintf(`<p class=dym>Did you mean <a href="%s"><b>%s</b></a>?</p>`, link(a.DidYouMean, 1), html.EscapeString(a.DidYouMean)))
	}
	if a.Total == 0 {
		b.WriteString(scard)
		b.WriteString(`<div class=empty><h2>Nothing found for “` + html.EscapeString(q) + `”</h2><p>Some things to try:</p><ul><li>check the spelling, or use fewer words</li><li>search one site: <code>site:ffmpeg.org codec</code></li><li>try a more general word</li></ul></div>`)
	} else {
		about := "About "
		if a.Total < 100 {
			about = ""
		}
		b.WriteString(tabNav(q, ""))
		b.WriteString(fmt.Sprintf(`<p class=meta>%s%s results · %.3f seconds%s</p>`, about, commas(a.Total), float64(a.TookMs)/1000, jevNote(a.JevMs, a.JevCut)))
		if len(a.Narrow) > 1 {
			b.WriteString(`<p class=narrow>Narrow to `)
			for i, h := range a.Narrow {
				if i > 0 {
					b.WriteString(" · ")
				}
				b.WriteString(fmt.Sprintf(`<a href="%s">%s</a>`, link(q+" site:"+h, 1), html.EscapeString(h)))
			}
			b.WriteString(`</p>`)
		}
		b.WriteString(scard)
	}
	vblock := ""
	if vk := <-vch; vk != "" && pg == 1 && a.Total > 0 && !strings.Contains(strings.ToLower(q), "site:") {
		vblock = verticalBlock(r, q, vk)
	}
	for ri, x := range a.Results {
		if ri == 4 && vblock != "" { // like Google: after the first web results
			b.WriteString(vblock)
			vblock = ""
		}
		host := strings.TrimPrefix(x.Host, "www.")
		crumb := ""
		if u, err := url.Parse(x.URL); err == nil {
			for _, seg := range strings.Split(strings.Trim(u.Path, "/"), "/") {
				if seg != "" {
					crumb += " › " + seg
				}
			}
			if u.RawQuery != "" {
				crumb += " › ?…"
			}
			if len(crumb) > 70 {
				crumb = crumb[:70] + "…"
			}
		}
		pill := ""
		if x.Source == "mediawiki" {
			pill = `<span class=pill>wiki</span>`
		}
		pill += `<a href="/site/` + url.PathEscape(host) + `" title="About this website: its screenshot, its pages, a new screenshot" style="margin-left:8px;color:var(--dim);text-decoration:none;font-size:14px">ⓘ</a>` // the way to the page of the website
		// the preview shows the page the result opens; until that one has been taken, the site's front page (if there is one)
		shot := ""
		pk := pageShotKey(x.URL)
		if pageShotFile(pk) != "" {
			shot = `<img class=shot src="/pshot/` + pk + `.jxl" alt="" loading=lazy width=160 height=100` + savedCopyTitle(pageShotDir+"/"+pk+".wb") + `>`
		} else {
			if shotFile(host) != "" {
				shot = `<img class=shot src="/shot/` + url.PathEscape(host) + `.jxl" alt="" loading=lazy width=160 height=100` + savedCopyTitle(fsShots + "/"+host+".wb") + `>`
			}
			queueResultShot(x.URL, host, x.Title, x.Snippet) // take this very page in the background, for the next search
		}
		b.WriteString(fmt.Sprintf(`<article class=r>`+shot+`<div class=site>%s<b>%s</b><i>%s</i>%s</div><a class=t href="%s" rel="noopener noreferrer">%s</a><p class=s>%s</p></article>`,
			avatar(x.Host), html.EscapeString(host), html.EscapeString(crumb), pill, html.EscapeString(x.URL), highlight(x.Title, a.terms), highlight(x.Snippet, a.terms)))
	}
	b.WriteString(vblock) // fewer than 5 web results: the box comes last
	if a.Pages > 1 {
		b.WriteString(`<nav class=pager aria-label=Pages>`)
		if pg > 1 {
			b.WriteString(fmt.Sprintf(`<a class=pbtn href="%s">&larr;</a>`, link(q, pg-1)))
		}
		for i := 1; i <= a.Pages; i++ {
			if i == pg {
				b.WriteString(fmt.Sprintf(`<span class="pbtn cur" aria-current=page>%d</span>`, i))
			} else {
				b.WriteString(fmt.Sprintf(`<a class=pbtn href="%s">%d</a>`, link(q, i), i))
			}
		}
		if pg < a.Pages {
			b.WriteString(fmt.Sprintf(`<a class=pbtn href="%s">&rarr;</a>`, link(q, pg+1)))
		}
		b.WriteString(`</nav>`)
	}
	if len(a.Related) > 0 {
		b.WriteString(`<div class=related><h4>Related searches</h4><div class=chips2>`)
		for _, rq := range a.Related {
			b.WriteString(fmt.Sprintf(`<a class=chip href="%s">%s</a>`, link(rq, 1), html.EscapeString(rq)))
		}
		b.WriteString(`</div></div>`)
	}
	main := `<div class=results>` + b.String() + `</div>`
	// the Wikipedia card is fetched after the page has loaded, so a slow answer never delays the results
	cardJS := ""
	if pg == 1 && !strings.Contains(strings.ToLower(q), "site:") {
		cardJS = `<script>(function(){
  const lay=document.querySelector('.layout');if(!lay)return;
  fetch('/api/card?q='+encodeURIComponent(lay.dataset.q)).then(r=>r.json()).then(c=>{
    if(!c.found)return;
    const a=document.createElement('aside');a.className='kp';
    const pill=document.createElement('span');pill.className='pill';pill.textContent='Wikipedia';
    const h=document.createElement('h2');h.textContent=c.title;
    a.append(pill,h);
    if(c.description){const d=document.createElement('p');d.className='kd';d.textContent=c.description;a.append(d)}
    if(c.thumb){const i=document.createElement('img');i.src=c.thumb;i.alt='';i.className='kimg';i.referrerPolicy='no-referrer';i.loading='lazy';i.onerror=()=>i.remove();a.append(i)}
    const p=document.createElement('p');p.textContent=c.extract;a.append(p);
    const l=document.createElement('a');l.href=c.url;l.target='_blank';l.rel='noopener noreferrer';l.textContent='Read on Wikipedia \u2192';a.append(l);
    lay.querySelector('.side').append(a);
  }).catch(()=>{});
})();</script>`
	}
	mapJS := ""
	if pg == 1 && !strings.Contains(strings.ToLower(q), "site:") {
		mapJS = `<link rel=stylesheet href="/flowcdn/vendor/leaflet/leaflet.min.css">
<script src="/flowcdn/vendor/leaflet/leaflet.min.js"></script>
<style>.mapcard{padding:0;overflow:hidden}.mapcard .mm{height:220px;width:100%}.mapcard .mm .leaflet-tile-pane{filter:invert(1) hue-rotate(180deg) brightness(0.95) contrast(0.9)}.mapcard .mn{padding:12px 16px;font-size:14px;color:var(--dim)}</style>
<script>(function(){
  const lay=document.querySelector('.layout');if(!lay)return;
  fetch('/api/geocode?q='+encodeURIComponent(lay.dataset.q)).then(r=>r.json()).then(g=>{
    if(!g.found||typeof L==='undefined')return;
    const a=document.createElement('aside');a.className='kp mapcard';
    const div=document.createElement('div');div.className='mm';a.append(div);
    const n=document.createElement('div');n.className='mn';n.textContent=g.name;a.append(n);
    lay.querySelector('.side').prepend(a);
    const lat=parseFloat(g.lat),lon=parseFloat(g.lon);
    const map=L.map(div,{zoomControl:false,attributionControl:false}).setView([lat,lon],12);
    // the OpenStreetMap tiles come from our own maps app (its disk cache), inverted via CSS for a dark look
    L.tileLayer('/flowcdn/tiles/{z}/{x}/{y}.png',{maxZoom:19,attribution:'© OpenStreetMap'}).addTo(map);
    L.marker([lat,lon]).addTo(map);
  }).catch(()=>{});
})();</script>`
	}
	aiJS := ""
	if pg == 1 && !strings.Contains(strings.ToLower(q), "site:") {
		aiJS = `<style>.ai{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:14px 18px;margin:0 0 18px}.ai h4{margin:0 0 8px;font-size:12px;letter-spacing:.08em;text-transform:uppercase;color:var(--dim);font-weight:600}.ai p{margin:0 0 10px;line-height:1.55}.ai pre{background:var(--bg);border:1px solid var(--line);border-radius:10px;padding:10px 12px;overflow-x:auto;font:13px/1.5 ui-monospace,Menlo,monospace;margin:8px 0}.ai code{background:var(--bg);border-radius:5px;padding:1px 5px;font:13px ui-monospace,Menlo,monospace}.ai pre code{background:none;padding:0}.ai .aitext p:last-child{margin-bottom:6px}.ai .srcs{display:flex;flex-wrap:wrap;gap:6px 14px;font-size:13px;color:var(--dim)}.ai .srcs a{color:var(--link);text-decoration:none}.ai .srcs a:hover{text-decoration:underline}.aioff{font-size:13px;color:var(--dim);margin:0 0 12px}.aioff a,.aitog{color:var(--dim)}.aitog{float:right;font-size:12px;letter-spacing:0;text-transform:none;font-weight:400}.ai small{display:block;color:var(--dim);font-size:12px;margin-top:8px}</style><script>(function(){
  const lay=document.querySelector('.layout');if(!lay)return;
  // the AI summary can be turned off (kept in this browser); when it is off nothing is asked from the AI
  let aioff=false;try{aioff=localStorage.getItem('ai')==='off'}catch(e){}
  const flip=v=>{try{localStorage.setItem('ai',v)}catch(e){}location.reload()};
  if(aioff){
    const o=document.createElement('p');o.className='aioff';o.append('AI summary is off \u00b7 ');
    const t=document.createElement('a');t.href='#';t.textContent='turn on';t.onclick=e=>{e.preventDefault();flip('on')};o.append(t);
    const res=lay.querySelector('.results');res.insertBefore(o,res.firstChild);return;
  }
  const clean=t=>t.replace(/\s*\[\d+\]/g,'');
  const BT=String.fromCharCode(96),FENCE=BT+BT+BT;
  // code between triple backticks becomes a box, code between single backticks a small inline box
  let p=null;
  const render=t=>{
    p.textContent='';
    t.split(FENCE).forEach((seg,i)=>{
      if(i%2){const pre=document.createElement('pre'),c=document.createElement('code');c.textContent=seg.replace(/^[a-zA-Z0-9+#_-]*\n/,'').replace(/\n$/,'');pre.append(c);p.append(pre)}
      else seg.split(/\n{2,}/).forEach(par=>{
        if(!par.trim())return;
        const q=document.createElement('p');
        par.split(BT).forEach((x,j)=>{if(j%2){const c=document.createElement('code');c.textContent=x;q.append(c)}else q.append(document.createTextNode(x))});
        p.append(q);
      });
    });
  };
  fetch('/api/ai?q='+encodeURIComponent(lay.dataset.q)+(document.documentElement.lang==='de'?'&lang=de':'')).then(async r=>{
    const rd=r.body.getReader(),dec=new TextDecoder();let buf='',txt='',fin=false;
    const box=src=>{
      const a=document.createElement('section');a.className='ai';
      const h=document.createElement('h4');h.textContent='AI summary';
      const tg=document.createElement('a');tg.className='aitog';tg.href='#';tg.textContent='turn off';tg.onclick=e=>{e.preventDefault();flip('off')};h.append(tg);
      p=document.createElement('div');p.className='aitext';
      const s=document.createElement('div');s.className='srcs';
      for(const x of src){const l=document.createElement('a');l.href=x.url;l.target='_blank';l.rel='noopener noreferrer';l.textContent=x.title.length>40?x.title.slice(0,40)+'\u2026':x.title;s.append(l)}
      const m=document.createElement('small');m.textContent='Written by AI. It can be wrong.';
      a.append(h,p,s,m);
      // typewriter: the text is shown slowly, even when the AI has already finished
      let n=0;const tm=setInterval(()=>{const t=clean(txt);if(n<t.length){n=Math.min(t.length,n+1+Math.floor((t.length-n)/120));render(t.slice(0,n))}else if(fin)clearInterval(tm)},28);
      const res=lay.querySelector('.results');res.insertBefore(a,res.firstChild);
    };
    for(;;){
      const {done,value}=await rd.read();if(done)break;
      buf+=dec.decode(value,{stream:true});
      let i;while((i=buf.indexOf('\n'))>=0){
        const l=buf.slice(0,i);buf=buf.slice(i+1);if(!l)continue;
        const c=JSON.parse(l);
        if(c.sources&&!p)box(c.sources);
        if(c.delta&&p)txt+=c.delta;
      }
    }
    fin=true;
  }).catch(()=>{});
})();</script>`
	}
	fmt.Fprint(w, page(q+" - Flowsearch", q, `<div class=layout data-q="`+html.EscapeString(q)+`">`+main+`<div class=side></div></div>`+cardJS+mapJS+aiJS+kindJS+safeJS+glassJS))
}

var (
	smu  sync.Mutex
	sHit = map[string][]time.Time{}
)

func suggestLimited(r *http.Request) bool {
	if warmAuth(r) {
		return false
	}
	ip := clientIP(r)
	smu.Lock()
	defer smu.Unlock()
	now := time.Now()
	h := sHit[ip][:0]
	for _, t := range sHit[ip] {
		if now.Sub(t) < time.Minute {
			h = append(h, t)
		}
	}
	h = append(h, now)
	sHit[ip] = h
	return len(h) > 900
}

// apiSuggest completes the last word of what is being typed.
func apiSuggest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	out := []string{}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	words := strings.Fields(q)
	if len(words) > 0 && len(q) <= 100 && !adultQuery(q) && !suggestLimited(r) {
		last := strings.ToLower(words[len(words)-1])
		if !strings.HasPrefix(last, "site:") {
			prefix := strings.Join(words[:len(words)-1], " ")
			for _, c := range ix.Suggest(last, 8) {
				s := c
				if prefix != "" {
					s = prefix + " " + c
				}
				if s != strings.ToLower(q) && !adultQuery(s) {
					out = append(out, s)
				}
			}
		}
	}
	json.NewEncoder(w).Encode(out)
}

var wikiClient = &http.Client{Timeout: 4 * time.Second}
var geoClient = &http.Client{Timeout: 4 * time.Second}

// apiGeocode asks OpenStreetMap's Nominatim whether the query looks like a place (address, city,
// country...). Used to show a dark-mode OSM map card next to search results.
func apiGeocode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=600")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 100 || strings.Contains(strings.ToLower(q), "site:") || adultQuery(q) || suggestLimited(r) {
		fmt.Fprint(w, `{"found":false}`)
		return
	}
	if !jevIsPlace(q) { // the search is not about a place: no map
		fmt.Fprint(w, `{"found":false}`)
		return
	}
	req, _ := http.NewRequest("GET", "https://nominatim.openstreetmap.org/search?format=jsonv2&limit=1&q="+url.QueryEscape(q), nil)
	req.Header.Set("User-Agent", "Flowsearch/1.0 (own search engine, see-flowsearch.example)")
	resp, err := geoClient.Do(req)
	if err != nil {
		fmt.Fprint(w, `{"found":false}`)
		return
	}
	defer resp.Body.Close()
	var results []struct {
		Lat         string `json:"lat"`
		Lon         string `json:"lon"`
		DisplayName string  `json:"display_name"`
		Importance  float64 `json:"importance"`
	}
	if json.NewDecoder(resp.Body).Decode(&results) != nil || len(results) == 0 || results[0].Importance < 0.2 { // a barely known place is probably a wrong match
		fmt.Fprint(w, `{"found":false}`)
		return
	}
	b, _ := json.Marshal(map[string]any{
		"found": true,
		"lat":   results[0].Lat,
		"lon":   results[0].Lon,
		"name":  results[0].DisplayName,
	})
	w.Write(b)
}

// apiCard asks the separate sgwiki service for the Wikipedia article that fits the query.
func apiCard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=600")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 100 || strings.Contains(strings.ToLower(q), "site:") || adultQuery(q) || suggestLimited(r) {
		fmt.Fprint(w, `{"found":false}`)
		return
	}
	resp, err := wikiClient.Get("http://127.0.0.1:8813/card?q=" + url.QueryEscape(q))
	if err != nil {
		fmt.Fprint(w, `{"found":false}`)
		return
	}
	defer resp.Body.Close()
	io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

func apiSearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	q, pg, per := params(r)
	if q == "" {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":"missing q"}`)
		return
	}
	if limited(r) {
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":"too many requests"}`)
		return
	}
	if adultQuery(q) {
		json.NewEncoder(w).Encode(answer{Query: q, Results: []result{}})
		return
	}
	json.NewEncoder(w).Encode(adultScrub(run(q, pg, per, isDE(r))))
}

func apiStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(statsJSON())
}

const apiCSS = `<style>
.api{max-width:860px;margin:0 auto}
.api h1{font-size:34px;letter-spacing:-.02em;margin:6px 0 6px}
.api .lead{color:var(--dim);font-size:18px;margin:0 0 20px;max-width:38em}
.facts{display:flex;flex-wrap:wrap;gap:10px;margin:0 0 26px}
.facts span{background:var(--card);background:color-mix(in srgb,var(--card) 55%,transparent);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);border:1px solid rgba(255,255,255,.18);border-radius:999px;padding:6px 16px;font-size:14px;color:var(--dim)}
.facts b{color:var(--ink)}
.ep{background:var(--card);background:color-mix(in srgb,var(--card) 62%,transparent);backdrop-filter:blur(16px) saturate(1.5);-webkit-backdrop-filter:blur(16px) saturate(1.5);border:1px solid rgba(255,255,255,.16);border-radius:24px;padding:22px 24px;margin:0 0 18px;box-shadow:0 18px 44px -30px rgba(0,0,0,.8),0 1px 0 rgba(255,255,255,.1) inset}
.ep h2{display:flex;align-items:center;gap:12px;flex-wrap:wrap;margin:0 0 6px;font-size:20px;font-weight:600}
.verb{font:700 12px ui-monospace,Menlo,monospace;letter-spacing:.05em;background:var(--acc);color:var(--acc-ink);border-radius:7px;padding:3px 9px}
.verb.ws{background:#3ecf8e;color:#06281a}
.ep .path{font:600 17px ui-monospace,SFMono-Regular,Menlo,monospace}
.ep p{margin:6px 0 12px;color:var(--dim)}
.ep table{width:100%;border-collapse:collapse;margin:0 0 14px;font-size:15px}
.ep th{text-align:left;font-size:12px;letter-spacing:.07em;text-transform:uppercase;color:var(--dim);font-weight:600;padding:0 10px 6px 0}
.ep td{padding:7px 10px 7px 0;border-top:1px solid var(--line);vertical-align:top}
.ep td:first-child{font:600 14px ui-monospace,Menlo,monospace;white-space:nowrap}
.code{position:relative;margin:0 0 12px}
.code pre{margin:0;padding:14px 16px;background:var(--bg);border:1px solid var(--line);border-radius:12px;overflow-x:auto;font:14px/1.55 ui-monospace,SFMono-Regular,Menlo,monospace;white-space:pre}
.copy{position:absolute;top:8px;right:8px;background:var(--card);color:var(--dim);border:1px solid var(--line);border-radius:8px;padding:3px 10px;font:12px system-ui;cursor:pointer}
.copy:hover{color:var(--ink);border-color:var(--acc)}
.try{display:flex;gap:8px;margin:4px 0 10px;flex-wrap:wrap}
.try input{flex:1;min-width:200px;background:var(--bg);border:1px solid var(--line);color:var(--ink);border-radius:99px;padding:10px 18px;font:inherit}
.try input:focus{outline:none;border-color:var(--acc)}
.try button{background:var(--acc);color:var(--acc-ink);border:0;border-radius:99px;padding:0 24px;font:inherit;font-weight:700;cursor:pointer}
.out{max-height:340px;overflow:auto}
.api details{margin:0 0 14px}.api summary{cursor:pointer;color:var(--dim);font-size:14px}
</style>`

const apiJS = `<script>
(function(){
  const base=location.origin;
  document.querySelectorAll('[data-base]').forEach(e=>{e.textContent=e.textContent.replace('WSORIGIN',base.replace(/^http/,'ws')).replace('ORIGIN',base)});
  document.querySelectorAll('.code').forEach(c=>{
    const b=document.createElement('button');b.className='copy';b.textContent='Copy';
    b.onclick=()=>{navigator.clipboard.writeText(c.querySelector('pre').textContent).then(()=>{b.textContent='Copied';setTimeout(()=>b.textContent='Copy',1200)})};
    c.append(b);
  });
  const q=document.getElementById('tryq'),go=document.getElementById('trygo'),out=document.getElementById('tryout');
  async function run(){
    out.textContent='Loading…';
    try{const r=await fetch('/api/search?per=3&q='+encodeURIComponent(q.value));const j=await r.json();out.textContent=JSON.stringify(j,null,2)}
    catch(e){out.textContent='Request failed'}
  }
  go.onclick=run;q.addEventListener('keydown',e=>{if(e.key==='Enter')run()});
  run();
})();
</script>`

func apiDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body := apiCSS + `<div class=api>
` + pageHead("Flowsearch for developers", "Search, suggestions and live statistics as plain JSON. No key, no sign-up.") + `
<div class=facts><span><b>120</b> searches a minute per address</span><span>JSON over <b>HTTPS</b> or HTTP</span><span><b>CORS</b> open: call it from any page</span><span>English pages only</span></div>

<section class=ep>
<h2><span class=verb>GET</span><span class=path>/api/search</span></h2>
<p>Find pages. Results are ranked best first. Put <code>site:example.com</code> in the query to search one site.</p>
<table><tr><th>Parameter</th><th>Meaning</th></tr>
<tr><td>q</td><td>The search. Required, up to 200 characters.</td></tr>
<tr><td>page</td><td>Page of results, 1 to 10. Default 1.</td></tr>
<tr><td>per</td><td>Results per page, 1 to 50. Default 10.</td></tr></table>
<div class=code><pre data-base>curl "ORIGIN/api/search?q=linux+kernel&amp;per=3"</pre></div>
<div class=try><input id=tryq value="linux kernel" aria-label="Try a search"><button id=trygo>Try it</button></div>
<div class="code out"><pre id=tryout>…</pre></div>
<details><summary>What the fields mean</summary><table>
<tr><td>total</td><td>How many pages match.</td></tr><tr><td>pages</td><td>Pages of results you can open (at most 10).</td></tr>
<tr><td>took_ms</td><td>How long the search took.</td></tr><tr><td>results[].snippet</td><td>The text around the match.</td></tr>
<tr><td>did_you_mean</td><td>A better spelling, when few pages match.</td></tr><tr><td>related</td><td>Related searches.</td></tr></table></details>
</section>

<section class=ep>
<h2><span class=verb>GET</span><span class=path>/api/suggest</span></h2>
<p>Complete the last word of what is being typed. Returns up to 8 suggestions.</p>
<table><tr><th>Parameter</th><th>Meaning</th></tr><tr><td>q</td><td>What has been typed so far.</td></tr></table>
<div class=code><pre data-base>curl "ORIGIN/api/suggest?q=kern"
["kernel","kernels", ...]</pre></div>
</section>

<section class=ep>
<h2><span class=verb>GET</span><span class=path>/api/card</span></h2>
<p>The Wikipedia-style summary card for a search, from our own wiki. <code>found</code> is false when there is nothing that fits.</p>
<table><tr><th>Parameter</th><th>Meaning</th></tr><tr><td>q</td><td>The search.</td></tr></table>
<div class=code><pre data-base>curl "ORIGIN/api/card?q=linux+kernel"
{"found":true,"title":"Linux kernel","extract":"...","thumb":"...","url":"..."}</pre></div>
</section>

<section class=ep>
<h2><span class=verb>GET</span><span class=path>/api/stats</span></h2>
<p>How big the index is and how fast it is growing: pages, sites, words, pages per minute and per hour, pages this run and the biggest sites.</p>
<div class=code><pre data-base>curl "ORIGIN/api/stats"</pre></div>
</section>

<section class=ep>
<h2><span class="verb ws">WS</span><span class=path>/ws/stats</span></h2>
<p>The same numbers pushed live over a WebSocket, a few bytes the moment pages are added. This is what the <a href=/live>Live</a> page uses.</p>
<div class=code><pre data-base>const ws = new WebSocket("WSORIGIN/ws/stats");
ws.onmessage = e => console.log(JSON.parse(e.data));</pre></div>
</section>
</div>` + apiJS
	fmt.Fprint(w, page("API - Flowsearch", "", body))
}

func main() {
	var err error
	file, err = os.OpenFile(pagesFile, os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	ix = newIndexClient() // the index is already loaded in the index service: nothing to wait for here
	registerWebmaster()
	registerBrowse()
	registerNotify()
	go rateKeeper()
	log.Printf("Flowsearch: %d pages in the on-disk index, listening on :8812", ix.NDocs())
	http.HandleFunc("/", home)
	http.HandleFunc("/mobile", mobilePage)
	http.HandleFunc("/search", searchPage)
	http.HandleFunc("/surprise", surprise)
	http.HandleFunc("/surprise-bookmarklet", surpriseBookmarklet)
	http.HandleFunc("/api/kinds", apiKinds)
	http.HandleFunc("/api/unsafe", apiUnsafe)
	go broadcaster()
	http.HandleFunc("/ws/stats", wsStats)
	http.HandleFunc("/ws/hosts", wsHosts)
	http.HandleFunc("/live", livePage2) // the new design (live2.go); the old livePage stays in this file, unused
	http.HandleFunc("/api/crawlrun", apiCrawlRun)
	http.HandleFunc("/api/batches", apiBatches)
	http.HandleFunc("/api", apiDocs)
	http.HandleFunc("/api/search", apiSearch)
	http.HandleFunc("/internal/llms", internalLlms)
	http.HandleFunc("/internal/screenshot", internalRetake)
	http.HandleFunc("/internal/amazon-urls", internalAmazonURLs)
	http.HandleFunc("/api/suggest", apiSuggest)
	http.HandleFunc("/api/card", apiCard)
	http.HandleFunc("/api/geocode", apiGeocode)
	http.HandleFunc("/api/ai", apiAI)
	http.HandleFunc("/api/ai-warm", apiAIWarm)
	http.HandleFunc("/api/stats", apiStats)
	http.HandleFunc("/news", newsPage)
	http.HandleFunc("/privacy", privacyPage)
	http.HandleFunc("/classic", win7Page)
	http.HandleFunc("/api/news", apiNews)
	seedNewsIfEmpty()
	favicon := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		fmt.Fprint(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#ffb347"/><text x="32" y="38" font-family="Arial" font-weight="700" font-size="22" text-anchor="middle" fill="#1b1206">Fl</text></svg>`)
	}
	http.HandleFunc("/opensearch.xml", func(w http.ResponseWriter, r *http.Request) {
		scheme := "https"
		if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
			scheme = "http"
		}
		base := scheme + "://" + r.Host
		w.Header().Set("Content-Type", "application/opensearchdescription+xml; charset=utf-8")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<OpenSearchDescription xmlns="http://a9.com/-/spec/opensearch/1.1/">
  <ShortName>Flowsearch</ShortName>
  <Description>Our own search engine: own crawler, own index, own ranking.</Description>
  <InputEncoding>UTF-8</InputEncoding>
  <Image height="64" width="64" type="image/svg+xml">%s/favicon.svg</Image>
  <Url type="text/html" template="%s/search?q={searchTerms}"/>
  <Url type="application/x-suggestions+json" template="%s/api/suggest?q={searchTerms}"/>
  <moz:SearchForm xmlns:moz="http://www.mozilla.org/2006/browser/search/">%s/</moz:SearchForm>
</OpenSearchDescription>`, base, base, base, base)
	})
	http.HandleFunc("/pshot/", func(w http.ResponseWriter, r *http.Request) {
		k := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/pshot/"), ".jxl")
		p := pageShotFile(k)
		if p == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "image/jxl")
		http.ServeFile(w, r, p)
	})
	http.HandleFunc("/shot/", func(w http.ResponseWriter, r *http.Request) {
		n := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/shot/"), ".jxl")
		if n == "" || strings.ContainsAny(n, "/\\") || strings.Contains(n, "..") {
			http.NotFound(w, r)
			return
		}
		p := shotFile(n)
		if p == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "image/jxl")
		http.ServeFile(w, r, p)
	})
	http.HandleFunc("/internal/forums-warm", internalForumsWarm)
	http.HandleFunc("/flowcdn/lemmy/", flowcdnLemmy)
	http.HandleFunc("/api/forums-live", apiForumsLive)
	http.HandleFunc("/ws/forums", wsForums)
	http.HandleFunc("/ask", askPage)
	http.HandleFunc("/chat", chatPage)
	http.HandleFunc("/api/ai-chat", apiAIChat)
	go forumIndex() // the Forums index is built while the server starts, not by the first search
	http.HandleFunc("/looks", looksPage)
	http.HandleFunc("/api/looks", apiLooks)
	http.HandleFunc("/screenshots", screenshotsPage)
	http.HandleFunc("/more", morePage)
	http.HandleFunc("/tools", toolsPage)
	http.HandleFunc("/quiz", quizPage)
	http.HandleFunc("/wall", wallPage)
	http.HandleFunc("/compare", comparePage)
	http.HandleFunc("/site/", sitePage)
	http.HandleFunc("/api/retake", retakePublic)
	http.HandleFunc("/settings", settingsPage)
	http.HandleFunc("/minecraft", minecraftPage)
	http.HandleFunc("/api/youtube", apiYoutube)
	http.HandleFunc("/api/amazon", apiAmazon)
	http.HandleFunc("/api/roblox", apiRoblox)
	http.HandleFunc("/flowcdn/roblox/", flowcdnRoblox)
	http.HandleFunc("/flowcdn/space/", flowcdnSpace)
	http.HandleFunc("/flowcdn/fav/", flowcdnFavicon)
	http.HandleFunc("/flowcdn/vendor/", flowcdnVendor)
	http.HandleFunc("/flowcdn/tiles/", flowcdnTile)
	http.HandleFunc("/flowcdn/yt/", flowcdnYouTube)
	http.HandleFunc("/flowcdn/amz/", flowcdnAmazon)
	http.HandleFunc("/api/shopping", apiShopping)
	http.HandleFunc("/api/price-update", apiPriceUpdate)
	http.HandleFunc("/api/price-status", apiPriceStatus)
	http.HandleFunc("/api/videos", apiVideos)
	http.HandleFunc("/favicon.svg", favicon)
	http.HandleFunc("/favicon.ico", favicon)
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	http.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "User-agent: *\nAllow: /\nDisallow: /webmaster/login\nDisallow: /webmaster/signup\n")
	})
	http.HandleFunc("/status", statusPage)
	addr := os.Getenv("MYSEARCH_ADDR") // behind the front service (mysearch-front) the site listens on its own port, e.g. 127.0.0.1:8830; on its own it keeps :8812
	if addr == "" {
		addr = ":8812"
	}
	log.Printf("Flowsearch is ready on %s", addr)
	log.Fatal((&http.Server{Addr: addr, Handler: langMux(http.DefaultServeMux), ReadHeaderTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second}).ListenAndServe())
}

// ---- live stats over WebSocket (RFC 6455, server to client only, no library) ----

type recentPage struct {
	Title string `json:"title"`
	Host  string `json:"host"`
	URL   string `json:"url"`
}

var (
	hubMu   sync.Mutex
	clients = map[chan []byte]bool{}
)

func short(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// msg is what a live page receives. The numbers are tiny; the two lists are left out unless they changed.
type msg struct {
	P      int          `json:"p"` // pages
	S      int          `json:"s"` // sites
	W      int          `json:"w"` // words
	R      float64      `json:"r"` // pages per second
	T      int64        `json:"t"`
	Recent []recentPage `json:"recent,omitempty"`
	Top    []hostCount  `json:"top,omitempty"`
	PM     int          `json:"pm"`              // pages in the last 60 seconds
	Hist   []int        `json:"hist,omitempty"`  // pages in each of the last 10 minutes, the newest (unfinished) last
	PH     int          `json:"ph"`              // pages in the last hour
	HHist  []int        `json:"hhist,omitempty"` // pages in each of the last 6 hours
	DHist  []int        `json:"dhist,omitempty"` // pages in each of the last 8 days
	PF     int          `json:"pf"`              // which of pm, ph are still "so far"
	Run    int          `json:"run"`             // pages added by the running crawl (or the last one)
	RunOn  bool         `json:"ra"`              // is a crawl running now
	Runs   []runInfo    `json:"runs,omitempty"`  // the last few runs
	De     *deMsg       `json:"de,omitempty"`    // the German tab
	Fo     map[string]int `json:"fo,omitempty"`  // the forum numbers: total, lemmy, discourse, flowforum, languages, instances, communities
}

// ---- pages per minute: one count a second, kept for an hour ----

type mark struct {
	t time.Time
	n int
}

var rk struct {
	mu    sync.Mutex
	marks []mark
}

var ratesPath = fsData + "/index4/rates.jsonl"

// loadRates reads the saved minute marks so the lists survive a restart.
func loadRates() {
	f, err := os.Open(ratesPath)
	if err != nil {
		return
	}
	defer f.Close()
	var keep []mark
	var out []byte
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l struct {
			T int64 `json:"t"`
			N int   `json:"n"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		t := time.Unix(l.T, 0)
		if time.Since(t) < 8*24*time.Hour && l.N <= ix.NDocs() {
			keep = append(keep, mark{t, l.N})
			out = append(out, sc.Bytes()...)
			out = append(out, '\n')
		}
	}
	rk.mu.Lock()
	rk.marks = keep
	rk.mu.Unlock()
	os.WriteFile(ratesPath, out, 0o644) // the file holds only the last 8 days
}

func rateKeeper() {
	loadRates()
	var saved time.Time
	for now := range time.Tick(time.Second) {
		n := ix.NDocs()
		rk.mu.Lock()
		rk.marks = append(rk.marks, mark{now, n})
		rk.mu.Unlock()
		if now.Sub(saved) >= time.Minute {
			saved = now
			if f, err := os.OpenFile(ratesPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				fmt.Fprintf(f, `{"t":%d,"n":%d}`+"\n", now.Unix(), n)
				f.Close()
			}
			rk.mu.Lock() // older than 2 hours: one mark a minute is enough; older than 8 days: gone
			var out []mark
			lastMin := time.Time{}
			for _, m := range rk.marks {
				age := now.Sub(m.t)
				if age > 8*24*time.Hour {
					continue
				}
				if age > 2*time.Hour {
					if mm := m.t.Truncate(time.Minute); mm.Equal(lastMin) {
						continue
					} else {
						lastMin = mm
					}
				}
				out = append(out, m)
			}
			rk.marks = out
			rk.mu.Unlock()
		}
	}
}

// countAt is the page count at time t (from the last mark at or before it). ok is false before the first mark.
func countAt(t time.Time) (int, bool) {
	i := sort.Search(len(rk.marks), func(i int) bool { return rk.marks[i].t.After(t) }) - 1
	if i < 0 {
		return 0, false
	}
	return rk.marks[i].n, true
}

// perSpan is the number of pages added in the last d. If we have not been watching that long yet, it is the
// number since we started watching, and full is false.
func perSpan(d time.Duration) (n int, full bool) {
	rk.mu.Lock()
	defer rk.mu.Unlock()
	if len(rk.marks) < 2 {
		return 0, false
	}
	last := rk.marks[len(rk.marks)-1]
	if old, ok := countAt(last.t.Add(-d)); ok {
		return last.n - old, true
	}
	return last.n - rk.marks[0].n, false
}

func perMinute() int { n, _ := perSpan(time.Minute); return n }
func perHour() int   { n, _ := perSpan(time.Hour); return n }

// partialMask says which of the two figures are still "so far" (1 minute, 2 hour, 4 day).
func partialMask() int {
	m := 0
	for i, d := range []time.Duration{time.Minute, time.Hour} {
		if _, full := perSpan(d); !full {
			m |= 1 << i
		}
	}
	return m
}

// histOf gives the pages added in each of the last n clock units (minutes or hours); the last one is the unit so far.
func histOf(unit time.Duration, n int) []int {
	rk.mu.Lock()
	defer rk.mu.Unlock()
	if len(rk.marks) < 2 {
		return nil
	}
	last := rk.marks[len(rk.marks)-1]
	cur := last.t.Truncate(unit)
	out := make([]int, n)
	for i := 0; i < n; i++ {
		end := cur.Add(time.Duration(i-(n-2)) * unit)
		start := end.Add(-unit)
		if i == n-1 {
			end, start = last.t, cur
		}
		a, okA := countAt(start)
		b, okB := countAt(end)
		if okB && !okA {
			a, okA = rk.marks[0].n, true // we only started watching part-way through
		}
		if okA && okB && b >= a {
			out[i] = b - a
		}
	}
	return out
}

func history() []int     { return histOf(time.Minute, 10) }
func hourHistory() []int { return histOf(time.Hour, 6) }
func dayHistory() []int  { return histOf(24*time.Hour, 8) }

// runInfo is one stretch of crawling: it starts when pages begin to arrive and ends after 10 minutes without any.
type runInfo struct {
	Start  int64 `json:"s"` // milliseconds
	End    int64 `json:"e"`
	Pages  int   `json:"n"`
	Active bool  `json:"a"`
	base   int
}

// runsNow finds the last few runs in the saved counts.
func runsNow() []runInfo {
	rk.mu.Lock()
	defer rk.mu.Unlock()
	marks := rk.marks
	var runs []runInfo
	var cur *runInfo
	var lastGrowth time.Time
	for i := 1; i < len(marks); i++ {
		if marks[i].n <= marks[i-1].n {
			continue
		}
		if cur != nil && marks[i].t.Sub(lastGrowth) > 10*time.Minute {
			runs = append(runs, *cur)
			cur = nil
		}
		if cur == nil {
			cur = &runInfo{Start: marks[i-1].t.UnixMilli(), base: marks[i-1].n}
		}
		cur.Pages = marks[i].n - cur.base
		cur.End = marks[i].t.UnixMilli()
		lastGrowth = marks[i].t
	}
	if cur != nil {
		cur.Active = time.Since(lastGrowth) < 90*time.Second
		runs = append(runs, *cur)
	}
	if len(runs) > 6 {
		runs = runs[len(runs)-6:]
	}
	return runs
}

// currentRun is the page count of the running crawl, or of the last one if nothing is running.
func currentRun(runs []runInfo, pages int) (int, bool) {
	if len(runs) == 0 {
		return 0, false
	}
	last := runs[len(runs)-1]
	if last.Active {
		return pages - last.base, true
	}
	return last.Pages, false
}

// topN returns the k biggest sites.
func topN(k int) []hostCount {
	var top []hostCount
	_, _, hs := ix.HostsWhere("en", k)
	for _, h := range hs {
		if adultHost(h.Host) {
			continue
		}
		top = append(top, hostCount{h.Host, h.Pages})
	}
	return top
}

func recentNow() []recentPage {
	var out []recentPage
	for _, r := range ix.RecentList() {
		if adultPage(r.Host, r.Title, "") {
			continue
		}
		out = append(out, recentPage{r.Title, r.Host, r.URL})
	}
	return out
}

// fullMsg is sent once when a page connects, so it starts with everything.
func fullMsg() []byte {
	fullRuns := runsNow()
	fullRun, fullOn := currentRun(fullRuns, ix.NDocs())
	b, _ := json.Marshal(msg{P: ix.NDocs(), S: ix.Sites(), W: ix.Terms(), T: time.Now().UnixMilli(), Recent: recentNow(), Top: topN(10), PM: perMinute(), Hist: history(), PH: perHour(), HHist: hourHistory(), DHist: dayHistory(), PF: partialMask(), Run: fullRun, RunOn: fullOn, Runs: fullRuns, De: deCompute(), Fo: foSummary()})
	return b
}

// broadcaster sends the small numbers the moment new pages are indexed (and once a second when idle).
// The newest-pages list goes out at most twice a second and only if it changed, the top sites every
// two seconds and only if they changed.
func broadcaster() {
	type sample struct {
		t time.Time
		n int
	}
	var samples []sample
	var recentAt, topAt, histAt, deAt time.Time
	var lastRuns []runInfo
	var recentKey, topKey string
	idle := time.NewTicker(time.Second)
	for {
		select {
		case <-ix.Notify:
			time.Sleep(time.Millisecond)
		case <-idle.C:
		}
		hubMu.Lock()
		n := len(clients)
		hubMu.Unlock()
		if n == 0 {
			continue
		}
		now := time.Now()
		pages, sites, terms := ix.NDocs(), ix.Sites(), ix.Terms()
		var rc []recentPage
		if rl := recentNow(); len(rl) > 0 && rl[len(rl)-1].URL != recentKey && now.Sub(recentAt) >= 500*time.Millisecond {
			recentKey, recentAt = rl[len(rl)-1].URL, now
			rc = rl
		}
		var tp []hostCount
		if now.Sub(topAt) >= 2*time.Second {
			topAt = now
			if t := topN(10); fmt.Sprint(t) != topKey {
				topKey, tp = fmt.Sprint(t), t
			}
		}
		var hh, hhh []int
		var rr []runInfo
		if now.Sub(histAt) >= time.Second {
			histAt = now
			hh, hhh, rr = history(), hourHistory(), runsNow()
			lastRuns = rr
		}
		var de *deMsg
		if now.Sub(deAt) >= time.Second {
			deAt = now
			de = deCompute()
		}
		run, runOn := currentRun(lastRuns, pages)
		samples = append(samples, sample{now, pages})
		for len(samples) > 1 && now.Sub(samples[0].t) > time.Second {
			samples = samples[1:]
		}
		rate := 0.0
		if d := now.Sub(samples[0].t).Seconds(); d > 0.05 {
			rate = float64(pages-samples[0].n) / d
		}
		b, _ := json.Marshal(msg{pages, sites, terms, float64(int(rate*10)) / 10, now.UnixMilli(), rc, tp, perMinute(), hh, perHour(), hhh, dayHistory(), partialMask(), run, runOn, rr, de, foSummary()})
		hubMu.Lock()
		for c := range clients {
			select {
			case c <- b:
			default: // a slow page skips a message
			}
		}
		hubMu.Unlock()
	}
}

func wsFrame(payload []byte) []byte {
	h := []byte{0x81}
	switch n := len(payload); {
	case n < 126:
		h = append(h, byte(n))
	case n < 65536:
		h = append(h, 126, byte(n>>8), byte(n))
	default:
		h = append(h, 127, 0, 0, 0, 0, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	return append(h, payload...)
}

func wsStats(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || key == "" {
		http.Error(w, "WebSocket only", 400)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", 500)
		return
	}
	hubMu.Lock()
	if len(clients) >= 50 {
		hubMu.Unlock()
		http.Error(w, "too many live pages open", 503)
		return
	}
	hubMu.Unlock()
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
	if _, err := conn.Write(wsFrame(fullMsg())); err != nil {
		return
	}
	ch := make(chan []byte, 4)
	hubMu.Lock()
	clients[ch] = true
	hubMu.Unlock()
	defer func() {
		hubMu.Lock()
		delete(clients, ch)
		hubMu.Unlock()
	}()
	done := make(chan struct{})
	go func() { // the browser only ever sends a close; read until then
		defer close(done)
		for {
			h0, err := brw.ReadByte()
			if err != nil {
				return
			}
			h1, err := brw.ReadByte()
			if err != nil {
				return
			}
			n := int64(h1 & 0x7f)
			if n == 126 {
				var b [2]byte
				if _, err := io.ReadFull(brw, b[:]); err != nil {
					return
				}
				n = int64(b[0])<<8 | int64(b[1])
			} else if n == 127 {
				return
			}
			if h1&0x80 != 0 {
				if _, err := io.CopyN(io.Discard, brw, 4); err != nil {
					return
				}
			}
			if _, err := io.CopyN(io.Discard, brw, n); err != nil {
				return
			}
			if h0&0x0f == 8 {
				return
			}
		}
	}()
	for {
		select {
		case b := <-ch:
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Write(wsFrame(b)); err != nil {
				return
			}
		case <-done:
			conn.Write([]byte{0x88, 0x00})
			return
		}
	}
}

const liveJS = `
const $=id=>document.getElementById(id);
const fmt=x=>Math.round(x).toLocaleString('en-US');
let ws,pend=null,upd=0,bytes=0,lastDelay=0,lastP=null,lastPT=null;
const seen=new Set();
function set(id,v){const e=$(id);if(e&&e.textContent!==v)e.textContent=v}
function status(s){const el=$('state');el.dataset.s=s;el.lastChild.textContent=s==='live'?'Live':s==='paused'?'Paused while the tab is hidden':'Reconnecting…'}
function connect(){
  if(document.hidden)return;
  ws=new WebSocket((location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/ws/stats');
  ws.onopen=()=>status('live');
  ws.onclose=()=>{if(!document.hidden){status('reconnecting');setTimeout(connect,1000)}};
  ws.onmessage=e=>{
    upd++;bytes+=e.data.length;
    const m=JSON.parse(e.data);pend=Object.assign(pend||{},m);
    if(m.T)lastDelay=Date.now()-m.T;
    if(typeof m.p==='number'){
      const now=Date.now();
      if(lastP!==null&&lastPT!==null){
        set('p300n','Pages per '+(now-lastPT)+'ms');
        set('p300',Math.max(0,m.p-lastP));
      }
      lastP=m.p;lastPT=now;
    }
  };
}
function fav(host){
  const i=document.createElement('img');
  i.src='/flowcdn/fav/'+encodeURIComponent(host.replace(/^www\./,''))+'.jxl';
  i.width=i.height=16;i.alt='';i.loading='lazy';i.referrerPolicy='no-referrer';i.onerror=()=>i.remove();
  return i;
}
// a column chart that updates its bars in place
function columns(el,vals,labels){
  if(el.children.length!==vals.length){
    el.textContent='';
    vals.forEach(()=>{
      const c=document.createElement('div');c.className='col';
      const v=document.createElement('span');v.className='val';
      const b=document.createElement('div');b.className='cb';
      const l=document.createElement('span');l.className='lbl';
      c.append(v,b,l);el.append(c);
    });
  }
  const mx=Math.max(1,...vals);
  vals.forEach((n,i)=>{
    const c=el.children[i];
    c.classList.toggle('now',i===vals.length-1);
    c.children[0].textContent=fmt(n);
    c.children[1].style.height=Math.max(2,Math.round(100*n/mx))+'%';
    c.children[2].textContent=labels[i];
  });
}
const hm=t=>new Date(t).toLocaleTimeString('en-GB',{hour:'2-digit',minute:'2-digit'});
function draw(d){
  if(d.p!==undefined){
    set('pages',fmt(d.p));set('sites',fmt(d.s));set('terms',fmt(d.w));
    set('rate',fmt(d.r));set('pm',fmt(d.pm));set('ph',fmt(d.ph));set('pday',fmt(d.ph*24));set('pd',fmt(d.run));
    set('pmn','Pages per minute'+((d.pf&1)?' (so far)':''));
    set('phn','Pages per hour'+((d.pf&2)?' (so far)':''));
    set('pdn',d.ra?'Pages this run':'Pages in the last run');
    set('runnote',d.ra?'crawler running':'crawler idle');
    set('lag',Math.max(0,Date.now()-d.t)+' ms');
  }
  if(d.hist){
    const base=Math.floor(d.t/60000)*60000,n=d.hist.length;
    columns($('hist'),d.hist,d.hist.map((_,i)=>hm(base-(n-1-i)*60000)));
  }
  if(d.hhist){
    const base=Math.floor(d.t/3600000)*3600000,n=d.hhist.length;
    columns($('hhist'),d.hhist,d.hhist.map((_,i)=>hm(base-(n-1-i)*3600000)));
  }
  if(d.dhist){
    const day=86400000,base=Math.floor(d.t/day)*day,n=d.dhist.length;
    columns($('dhist'),d.dhist,d.dhist.map((_,i)=>new Date(base-(n-1-i)*day).toLocaleDateString(undefined,{month:'short',day:'numeric'})));
  }
  if(d.runs){
    const h=$('runsList');h.textContent='';const mx=Math.max(1,...d.runs.map(r=>r.n));
    const day=ms=>new Date(ms).toLocaleDateString('en-GB',{weekday:'short'});
    for(const r of d.runs.slice().reverse()){
      const li=document.createElement('li');
      const f=document.createElement('i');f.className='fill';f.style.width=(100*r.n/mx)+'%';
      const a=document.createElement('span');a.className='lt';a.textContent=day(r.s)+' '+hm(r.s)+' to '+hm(r.e)+(r.a?'  ● running':'');
      const b=document.createElement('span');b.className='rt';b.textContent=fmt(r.n);
      li.append(f,a,b);h.append(li);
    }
    if(!d.runs.length){const li=document.createElement('li');li.className='none';li.textContent='No runs yet';h.append(li)}
  }
  if(d.top){
    const t=$('top');t.textContent='';const mx=d.top.length?d.top[0].pages:1;
    for(const s of d.top){
      const li=document.createElement('li');
      const f=document.createElement('i');f.className='fill';f.style.width=(100*s.pages/mx)+'%';
      const a=document.createElement('span');a.className='lt';a.append(fav(s.host),document.createTextNode(' '+s.host));
      const b=document.createElement('span');b.className='rt';b.textContent=fmt(s.pages);
      li.append(f,a,b);t.append(li);
    }
  }
  if(d.recent){
    const r=$('recent');r.textContent='';
    for(const p of d.recent.slice().reverse()){
      const li=document.createElement('li');
      if(!seen.has(p.url)){li.className='new';seen.add(p.url)}
      const a=document.createElement('a');a.href=p.url;a.target='_blank';a.rel='noopener noreferrer';a.textContent=p.title||p.url;
      const h=document.createElement('span');h.className='host';h.textContent=p.host.replace(/^www\./,'');
      li.append(fav(p.host),a,h);r.append(li);
    }
    if(seen.size>500)seen.clear();
  }
  if(d.de)drawDe(d.de);
}
// Lemmy tab: its own WebSocket (/ws/hosts) that pushes the page counts of the Lemmy servers every 50 ms
const LM='0xdd.org.ru,academy.garden,adultswim.fan,aggregatet.org,alien.top,altgag.net,altmedia.house,ani.social,api-dev.rabotaem.app,api.rabotaem.app,ascy.mooo.com,aussie.zone,awful.systems,badatbeing.social,baraza.africa,beehaw.org,belfry.rip,belgae.social,beta.discuss.online,blockchained.world,board.tankie.club,bodli.mven.nl,bolha.forum,breakfast.haus,chachara.club,champserver.net,communick.news,corndog.social,crazypeople.online,crustaceanson.theshelf.top,dev.lemmy.world,devblogs.io,diggita.com,discover.deltanauten.de,discuss.aethelgard.space,discuss.icewind.me,discuss.jacen.moe,discuss.mschae23.de,discuss.online,discuss.tchncs.de,discuss.voidcentury.net,dit.reformed.social,downonthestreet.eu,dullsters.net,enterprise.lemmy.ml,esc.ozar.dev,europe.pub,eventfrontier.com,eviltoast.org,expats.zone,fanaticus.social,fangnet.me,fasheng.ing,fed.dyne.org,feddit.bg,feddit.cl,feddit.dk,feddit.in,feddit.it,feddit.nl,feddit.nu,feddit.org,feddit.uk,fedii.me,feditown.com,fenmou.cyou,fjdk.uk,foros.fediverso.gal,forum.ayom.media,forum.guncadindex.com,forum.storstad.net,forums.rereading.space,fosscad.io,fstab.sh,futurology.today,gearhead.town,geekroom.tech,giesing.dev,gioia.news,go.ontomy.tv,gregtech.eu,group.lt,hackertalks.com,halubilo.social,hardpass.lol,hardware.watch,healthy.community,helpos.ca,herbicide.fallcounty.omg.lol,hexbear.net,hi-fi.community,hikki.team,hilariouschaos.com,hoihoi.superboi.eu.org,indie-ver.se,indiehackers.space,info.prou.be,infosec.pub,isawthat.fyi,jlai.lu,kakera.kintsugi.moe,kbin.thicknahalf.com,kitty.ly,krabb.org,kutsuya.dev,l.7rg1nt.moe,l.dongxi.ca,l.mathers.fr,l.mchome.net,l.roofo.cc,l.sw0.com,l.tta.wtf,lazysoci.al,leaf.dance,lef.li,lem.afiz.org,lem.cochrun.xyz,lem.free.as,lem.permisuan.com,lem.ph3j.com,lem.radiantfig.fyi,lem.serkozh.me,lem.taconuke.club,lem.trashbrain.org,lemdro.id,leminal.space,lemming.megumin.org,lemmit.online,lemmus.org,lemmy-api.ten4ward.social,lemmy.0upti.me,lemmy.0xloc.org,lemmy.100010101.xyz,lemmy.1095.me,lemmy.14042003.xyz,lemmy.4d2.org,lemmy.8bitar.io,lemmy.8th.world,lemmy.963.pm,lemmy.activitypub.academy,lemmy.ahall.se,lemmy.almostadatacenter.social,lemmy.amxl.com,lemmy.anhanga.com.br,lemmy.ansico.dk,lemmy.artificial-ryan.us,lemmy.asc6.org,lemmy.asudox.dev,lemmy.b0tt0m.xyz,lemmy.based.zone,lemmy.beagle.quest,lemmy.belegost.net,lemmy.berinkton.de,lemmy.beru.co,lemmy.bestiver.se,lemmy.bigbertha.tech,lemmy.blahaj.zone,lemmy.bleh.au,lemmy.blehiscool.com,lemmy.blueberryicetea.com,lemmy.bmck.au,lemmy.bosio.info,lemmy.browntown.dev,lemmy.ca,lemmy.cafe,lemmy.caliban.io,lemmy.canthonyscott.io,lemmy.carl.id,lemmy.ch3n2k.com,lemmy.charles.wiki,lemmy.cheese-project.ru,lemmy.chillypenguin.net,lemmy.circenn.win,lemmy.civl.cc,lemmy.conorab.com,lemmy.coupou.fr,lemmy.cringecollective.io,lemmy.criticalbasics.xyz,lemmy.cryonex.net,lemmy.ctrlq.dev,lemmy.curiana.net,lemmy.cyberia9.org,lemmy.darkc0de.one,lemmy.darvit.nl,lemmy.dbzer0.com,lemmy.deadca.de,lemmy.decronym.xyz,lemmy.deedium.nl,lemmy.devr.cc,lemmy.digitalcharon.in,lemmy.dorfrollenspiel.de,lemmy.dougiepeart.co.uk,lemmy.draktis.com,lemmy.drath.ru,lemmy.dryja.dev,lemmy.duckpond.social,lemmy.durstig.online,lemmy.eirides.net,lemmy.eknz.org,lemmy.electricmonk.io,lemmy.encryptionin.space,lemmy.enticedwanderer.com,lemmy.entroversal.com,lemmy.eus,lemmy.evangineer.net,lemmy.fait.ch,lemmy.federate.lol,lemmy.fedi.ovh,lemmy.fedi.zutto.fi,lemmy.fediverse.jp,lemmy.freakaria.com,lemmy.freewilltiger.page,lemmy.fromshado.ws,lemmy.frozeninferno.xyz,lemmy.funami.tech,lemmy.glasgow.social,lemmy.glue.io,lemmy.grey.fail,lemmy.grey.ooo,lemmy.grys.it,lemmy.halstead.host,lemmy.hameteman.com,lemmy.hangdaan.com,lemmy.hkserv.space,lemmy.hogru.ch,lemmy.horwood.cloud,lemmy.igl.ooo,lemmy.ill13.com,lemmy.inbutts.lol,lemmy.jacaranda.club,lemmy.jann.dev,lemmy.jaypg.pw,lemmy.jelliefrontier.net,lemmy.johnnei.org,lemmy.k2pk.com,lemmy.keimai.space,lemmy.keniir.wtf,lemmy.killtime.online,lemmy.kmoneyserver.com,lemmy.kya.moe,lemmy.kzkr.xyz,lemmy.labfox.fr,lemmy.lacasabien.space,lemmy.lama-corp.space,lemmy.lantian.pub,lemmy.linden.social,lemmy.linuxuserspace.show,lemmy.lqx.net,lemmy.lt,lemmy.lu,lemmy.lundgrensjostrom.com,lemmy.lynas.ca,lemmy.mair.io,lemmy.maquis196.io,lemmy.mariusdavid.fr,lemmy.masto.community,lemmy.mats.ooo,lemmy.mayer.rocks,lemmy.mbl.social,lemmy.mebitek.com,lemmy.meissners.me,lemmy.michaelsasser.org,lemmy.minecloud.ro,lemmy.mistyreverie.org,lemmy.mit.edu,lemmy.ml,lemmy.mlaga97.space,lemmy.monster,lemmy.moocloud.party,lemmy.moonling.nl,lemmy.mpgs.cc,lemmy.mrm.one,lemmy.mrpostman.ch,lemmy.muffalings.com,lemmy.my-box.dev,lemmy.nanatay.net,lemmy.neondystopia.world,lemmy.net.au,lemmy.nexus,lemmy.nocturnal.garden,lemmy.nope.ly,lemmy.norbipeti.eu,lemmy.nothavingthis.co.uk,lemmy.nrdblg.de,lemmy.nube-gran.de,lemmy.nx.dedyn.io,lemmy.nz,lemmy.okr765.com,lemmy.onlylans.io,lemmy.openvuture.com,lemmy.organic-server.org,lemmy.oursocial.club,lemmy.patton.id.au,lemmy.paulstevens.org,lemmy.piepjes.net,lemmy.pierre-couy.fr,lemmy.pixelpassport.studio,lemmy.pleasestopfaxing.social,lemmy.pootis.network,lemmy.poudlar.do,lemmy.pt,lemmy.radio,lemmy.randomsonicnet.org,lemmy.reepay.org,lemmy.rhys.sh,lemmy.rochegmr.com,lemmy.runesmite.com,lemmy.sador.me,lemmy.science.social,lemmy.sdf.org,lemmy.secnd.me,lemmy.securitycafe.ca,lemmy.selina.diy,lemmy.services.coupou.fr,lemmy.sgfault.com,lemmy.shiny-task.com,lemmy.shir.one,lemmy.shutes.org,lemmy.sietch.online,lemmy.skoops.social,lemmy.skyjake.fi,lemmy.ssba.com,lemmy.staphup.nl,lemmy.starlightkel.xyz,lemmy.stefanoprenna.com,lemmy.t-rg.ws,lemmy.tauata.com,lemmy.techtailors.net,lemmy.teuto.icu,lemmy.tgxn.net,lemmy.thewooskeys.com,lemmy.tobyvin.dev,lemmy.today,lemmy.transrights.fyi,lemmy.trippy.pizza,lemmy.tserts.com,lemmy.ubergeek77.chat,lemmy.uhhoh.com,lemmy.umany222.net,lemmy.umucat.day,lemmy.uninsane.org,lemmy.unryzer.eu,lemmy.usuck.fyi,lemmy.vg,lemmy.vigenere.nl,lemmy.wald.icu,lemmy.walker.cafe,lemmy.waynetec.us,lemmy.world,lemmy.wtf,lemmy.xeviousx.eu,lemmy.xoynq.com,lemmy.xurxia.net,lemmy.zarioth.com,lemmy.zimage.com,lemmy.zip,lemmy.zwanenburg.info,lemmygrad.ml,lemmyland.com,lemmynal.space,lemy.lol,lemy.nl,level-up.zone,linkage.ds8.zone,links.gayfr.online,links.nadia.moe,links.openriver.net,linux.community,linz.city,literature.cafe,lm.boing.icu,lm.inu.is,lm.mlfh.org,lm.preferlinux.de,lm.tros.org,lmm.azrak.io,lmy.cruzgott.com,lmy.ndu.wtf,lmy.sagf.io,lowtech.li,lt.harding.dev,ly.eok.me,making.awful.systems,maple.social,matchpoint.zone,metacritics.zone,mimiclem.me,mlemapp.org,monero.town,monero.you,moonwater.space,moplemmy.fyi,morbier.foo,mouse.chitanda.moe,mtgzone.com,mujico.org,natur.23.nu,nba.space,news.abolish.capital,news.girolab.foo,news.idlestate.org,nfl.community,no.lastname.nz,nodesphere.site,notizie.bida.im,odin.lanofthedead.xyz,openmic.social,orcas.enjoying.yachts,pawb.social,philosophy.cafe,piefed.diffrint.org,poptalk.scrubbles.tech,poweruser.forum,programming.dev,providence.werewolf.lat,r.nf,r3dnvmbr.xyz,rblind.com,rdrama.co,realbitcoin.cash,reddthat.com,retrolemmy.com,riskeratspizza.com,roanoke.social,rollenspiel.forum,sammich.es,scribe.disroot.org,selfhosted.forum,sfw.community,sh.itjust.works,sha1.nl,shazbot.industries,shredderfood.net,slevin.horse,slrpnk.net,soccer.forum,social.ggbox.fr,social.jears.at,social.jrruethe.info,social.nerdhouse.io,social.p80.se,social.sour.is,social.srx.digital,some.institute,sopuli.xyz,spgrn.com,startrek.website,style.land,sub.community,sublem.com,supernova.place,suppo.fi,swg-empire.de,szmer.info,talk.digerty.com,tech.pr0n.pl,test1.lemmy.ddenis.info,test2.lealternative.net,test2.lemmy.ddenis.info,thelemmy.club,thinktank.brainstormes.org,threads.kyzune.com,tiann.plk.tw,timeperiods.fr,timesink.p3nguin.org,timeyak.com,tkohhh.social,toast.ooo,ttrpg.network,tyckling.se,unreachable.cloud,upvote.au,vger.social,viewfinder.pro,whatcom.social,whemic.xyz,wired.bluemarch.art,yall.theatl.social,ymous.au,zegheteens.nl';
let lws=null,lpend=null,lhist=[];
let lmPrev=null,lmSeen=null;
// the Lemmy tab: its own WebSocket (/ws/forums) pushes the forum numbers and the crawl progress every second
function lmConnect(){
  if(lws)return;
  lws=new WebSocket((location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/ws/forums');
  lws.onmessage=e=>{try{forumsDraw(JSON.parse(e.data))}catch(x){}};
  lws.onclose=()=>{lws=null;if(!$('lmv').hidden)setTimeout(lmConnect,1000)};
}
function lmClose(){if(lws){lws.onclose=null;lws.close();lws=null}}
function forumsDraw(d){
  const now=Date.now();
  set('lmtot',fmt(d.total));set('lmcomm',fmt(d.communities));set('lminst',fmt(d.instances));
  if(lmPrev&&now>lmPrev.t)set('lmrate',(Math.max(0,d.total-lmPrev.n)/((now-lmPrev.t)/1000)).toFixed(1));
  lmPrev={t:now,n:d.total};
  const c=d.crawl||{},st=$('lmcrawl');
  if(c.running){
    const goal=c.goal||0,pct=goal?Math.min(100,100*c.read/goal):0;
    st.textContent='';
    const l1=document.createElement('div');l1.textContent='Crawling now: '+(c.site||'starting')+'  ·  '+fmt(c.read)+(goal?' of '+fmt(goal):'')+' posts read';
    const bar=document.createElement('div');bar.className='lmbar';const f=document.createElement('i');f.style.width=pct+'%';bar.append(f);
    st.append(l1,bar);
  }else st.textContent=c.read?'The last crawl read '+fmt(c.read)+' posts and has finished.':'No crawl running.';
  const ul=$('lmcomms');ul.textContent='';const mx=d.top&&d.top.length?d.top[0].n:1;
  for(const k of d.top||[]){
    const li=document.createElement('li'),f=document.createElement('i');f.className='fill';f.style.width=(100*k.n/mx)+'%';
    const a=document.createElement('span');a.className='lt';a.textContent=k.c;
    const b=document.createElement('span');b.className='rt';b.textContent=fmt(k.n);
    li.append(f,a,b);ul.append(li);
  }
  const nl=$('lmnew');nl.textContent='';
  const seenNow=new Set();
  for(const p of d.newest||[]){
    seenNow.add(p.u);
    const li=document.createElement('li'),a=document.createElement('a');a.href=p.u;a.target='_blank';a.rel='noopener noreferrer';a.textContent=p.t;
    const s=document.createElement('small');s.textContent='  '+p.c+' · '+p.i+' · ▲'+p.s+(p.d?' · '+p.d:'');
    if(lmSeen&&!lmSeen.has(p.u))li.className='lmfresh';
    li.append(a,s);nl.append(li);
  }
  lmSeen=seenNow;
}
let lmLastDraw=0;
function lmDraw(m){
  const now=Date.now();lhist.push({t:now,h:m.h});
  while(lhist.length>1&&now-lhist[0].t>10000)lhist.shift();
  const span=(now-lhist[0].t)/1000,names=Object.keys(m.h).sort((a,b)=>m.h[b]-m.h[a]);
  let tot=0,rtot=0;const mx=Math.max(1,m.h[names[0]]||1);
  for(const k of names){
    const v=m.h[k],r=span>0.5?(v-(lhist[0].h[k]||0))/span:0;tot+=v;rtot+=r;
  }
  // with hundreds of servers, redrawing the whole list every frame would freeze the tab - throttle that part
  if(now-lmLastDraw>=400){
    lmLastDraw=now;
    const ul=$('lmlist');ul.textContent='';
    for(const k of names){
      const v=m.h[k],r=span>0.5?(v-(lhist[0].h[k]||0))/span:0;
      const li=document.createElement('li');
      const f=document.createElement('i');f.className='fill';f.style.width=(100*v/mx)+'%';
      const a=document.createElement('span');a.className='lt';a.append(fav(k),document.createTextNode(' '+k));
      const b=document.createElement('span');b.className='rt';b.textContent=fmt(v)+'  ·  '+r.toFixed(1)+'/s';
      li.append(f,a,b);ul.append(li);
    }
  }
  set('lmtot',fmt(tot));set('lmrate',rtot.toFixed(1));
}
function drawDe(d){
  set('dpages',fmt(d.p));set('dsites',fmt(d.s));set('dps',d.ps);set('dpm',fmt(d.pm));set('dph',fmt(d.ph));
  const t=$('dtop');t.textContent='';const mx=d.top&&d.top.length?d.top[0].pages:1;
  for(const s of d.top||[]){
    const li=document.createElement('li');
    const f=document.createElement('i');f.className='fill';f.style.width=(100*s.pages/mx)+'%';
    const a=document.createElement('span');a.className='lt';a.append(fav(s.host),document.createTextNode(' '+s.host));
    const b=document.createElement('span');b.className='rt';b.textContent=fmt(s.pages);
    li.append(f,a,b);t.append(li);
  }
  const r=$('drecent');r.textContent='';
  for(const p of (d.recent||[]).slice().reverse()){
    const li=document.createElement('li');
    const a=document.createElement('a');a.href=p.url;a.target='_blank';a.rel='noopener noreferrer';a.textContent=p.title||p.url;
    const h=document.createElement('span');h.className='host';h.textContent=p.host.replace(/^www\./,'');
    li.append(fav(p.host),a,h);r.append(li);
  }
  if(!(d.recent||[]).length){const li=document.createElement('li');li.className='none';li.textContent='Noch keine deutschen Seiten';r.append(li)}
}
let rbTimer=null,rws=null,rpend=null,amTimer=null,aws=null,apend=null; // declared before the saved tab is clicked again at load
for(const [b,v] of [['tabAll','allv'],['tabDe','dev'],['tabLm','lmv'],['tabYt','ytv'],['tabAm','amv'],['tabRb','rbv']]){
  $(b).onclick=()=>{
    for(const [b2,v2] of [['tabAll','allv'],['tabDe','dev'],['tabLm','lmv'],['tabYt','ytv'],['tabAm','amv'],['tabRb','rbv']]){$(b2).classList.toggle('on',b2===b);$(v2).hidden=v2!==v}
    if(b==='tabLm')lmConnect();else lmClose();
    if(b==='tabYt')ytLoad();
    if(b==='tabAm')amStart();else amStop();
    if(b==='tabRb')rbStart();else rbStop();
    try{localStorage.setItem('livetab',b)}catch(e){}
  };
}
try{const t=localStorage.getItem('livetab');if(t==='tabDe'||t==='tabLm'||t==='tabYt'||t==='tabAm'||t==='tabRb')$(t).click()}catch(e){}
// the page count comes over the same WebSocket as the other tabs (/ws/hosts, every 50 ms); the games lists are fetched every 4 seconds
function rbStart(){
  rbLoad();clearInterval(rbTimer);rbTimer=setInterval(rbLoad,4000);
  if(rws)return;
  rws=new WebSocket((location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/ws/hosts?h=roblox.com');
  rws.onmessage=e=>{rpend=JSON.parse(e.data)};
  rws.onclose=()=>{rws=null;if(!$('rbv').hidden)setTimeout(rbStart,1000)};
}
function rbStop(){clearInterval(rbTimer);rbTimer=null;if(rws){rws.onclose=null;rws.close();rws=null}}
function rbDraw(m){$('rbpages').textContent=(m.h['roblox.com']||0).toLocaleString('en-GB')}
function rbList(id,list){
  const u=$(id);u.textContent='';
  for(const v of list||[]){const li=document.createElement('li'),a=document.createElement('a');a.href=v.u;a.target='_blank';a.rel='noopener noreferrer';a.textContent=v.t;const s=document.createElement('small');s.textContent='  '+(v.creator?'by '+v.creator+' · ':'')+(v.active?v.active+' playing':'')+(v.visits?' · '+v.visits+' visits':'');li.append(a,s);u.append(li)}
  if(!(list||[]).length){const li=document.createElement('li');li.className='none';li.textContent='No games yet';u.append(li)}
}
function rbLoad(){
  fetch('/api/roblox').then(r=>r.json()).then(d=>{
    const n=x=>(x||0).toLocaleString('en-GB');
    $('rbgames').textContent=n(d.games);$('rbplayers').textContent=n(d.players);
    rbList('rbtop',d.top);rbList('rbrecent',d.recent);
  }).catch(()=>{$('rbgames').textContent='?'});
}
// the counts come over the same WebSocket as the Lemmy tab (/ws/hosts, every 50 ms); the newest products are fetched every 3 seconds
function amStart(){
  amLoad();clearInterval(amTimer);amTimer=setInterval(amLoad,3000);
  if(aws)return;
  aws=new WebSocket((location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/ws/hosts?h=amazon.com,amazon.de');
  aws.onmessage=e=>{apend=JSON.parse(e.data)};
  aws.onclose=()=>{aws=null;if(!$('amv').hidden)setTimeout(amStart,1000)};
}
function amStop(){clearInterval(amTimer);amTimer=null;if(aws){aws.onclose=null;aws.close();aws=null}}
function amDraw(m){
  const n=x=>(x||0).toLocaleString('en-GB'),c=m.h['amazon.com']||0,d=m.h['amazon.de']||0;
  $('amcom').textContent=n(c);$('amde').textContent=n(d);$('amtot').textContent=n(c+d);
}
function amLoad(){
  fetch('/api/amazon').then(r=>r.json()).then(d=>{
    if(!aws){const n=x=>(x||0).toLocaleString('en-GB');$('amtot').textContent=n(d.total);$('amcom').textContent=n(d.com);$('amde').textContent=n(d.de)}
    $('ampriced').textContent=(d.priced||0).toLocaleString('en-GB');
    const u=$('amlist');u.textContent='';
    for(const v of d.recent||[]){const li=document.createElement('li'),a=document.createElement('a');a.href=v.u;a.target='_blank';a.rel='noopener noreferrer';a.textContent=(v.price?v.price+'  ':'')+v.t;const s=document.createElement('small');s.textContent='  '+v.site;li.append(a,s);u.append(li)}
    if(!(d.recent||[]).length){const li=document.createElement('li');li.className='none';li.textContent='No products yet';u.append(li)}
  }).catch(()=>{$('amtot').textContent='?'});
}
function ytLoad(){
  fetch('/api/youtube').then(r=>r.json()).then(d=>{
    $('yttot').textContent=(d.count||0).toLocaleString('en-GB');
    const u=$('ytlist');u.textContent='';
    for(const v of d.sample||[]){const li=document.createElement('li'),a=document.createElement('a');a.href=v.u;a.target='_blank';a.rel='noopener noreferrer';a.textContent=v.t;li.append(a);u.append(li)}
    if(!(d.sample||[]).length){const li=document.createElement('li');li.className='none';li.textContent='No videos yet';u.append(li)}
  }).catch(()=>{$('yttot').textContent='?'});
}
function loop(){if(pend){draw(pend);pend=null}if(lpend){lmDraw(lpend);lpend=null}if(apend){amDraw(apend);apend=null}if(rpend){rbDraw(rpend);rpend=null}requestAnimationFrame(loop)}
document.addEventListener('visibilitychange',()=>{
  if(document.hidden){if(ws){ws.onclose=null;ws.close()}status('paused')}else{status('connecting');connect()}
});
setInterval(()=>{set('mps',upd+' updates/s · '+(bytes/1024).toFixed(1)+' KB/s · delay '+lastDelay+'ms');upd=0;bytes=0;},50);
loop();connect();`

func livePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	extra := `<style>
.wrap{max-width:1000px}
.lhead{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap;margin-bottom:18px}
.lhead h1{margin:0;font-size:30px;letter-spacing:-.02em}
.lright{display:flex;align-items:center;gap:12px;flex-wrap:wrap;color:var(--dim);font-size:13px}
.state{display:inline-flex;align-items:center;gap:8px;border:1px solid var(--line);background:var(--card);border-radius:999px;padding:5px 14px;font-size:14px;color:var(--ink)}
.state i{width:9px;height:9px;border-radius:50%;background:#3ecf8e;animation:pulse 1.6s infinite}
.state[data-s=connecting] i,.state[data-s=reconnecting] i{background:var(--acc);animation:none}
.state[data-s=paused] i{background:var(--dim);animation:none}
@keyframes pulse{0%{box-shadow:0 0 0 0 rgba(62,207,142,.55)}70%{box-shadow:0 0 0 9px rgba(62,207,142,0)}100%{box-shadow:0 0 0 0 rgba(62,207,142,0)}}
.kpis{display:grid;grid-template-columns:repeat(auto-fit,minmax(170px,1fr));gap:12px;margin-bottom:14px}
.kpi{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:16px 18px;min-width:0}
.kpi.big{grid-column:span 2;background:linear-gradient(135deg,var(--card),color-mix(in srgb,var(--acc) 10%,var(--card)))}
.kpi small{display:block;color:var(--dim);font-size:12px;letter-spacing:.06em;text-transform:uppercase}
.kpi b{display:block;font-size:32px;line-height:1.15;font-variant-numeric:tabular-nums;letter-spacing:-.02em;margin-top:4px}
.kpi.big b{font-size:48px;color:var(--acc)}
.kpi em{display:block;font-style:normal;color:var(--dim);font-size:13px;margin-top:3px}
.two{display:grid;grid-template-columns:repeat(auto-fit,minmax(340px,1fr));gap:12px;margin-bottom:12px}
.card{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:16px 18px;min-width:0;margin-bottom:12px}
.two .card{margin-bottom:0}
.card h3{margin:0 0 14px;font-size:14px;letter-spacing:.02em}.card h3 small{color:var(--dim);font-weight:400;margin-left:6px}
.cols{display:flex;align-items:flex-end;gap:8px;height:170px}
.col{flex:1;display:flex;flex-direction:column;align-items:center;justify-content:flex-end;height:100%;min-width:0}
.col .cb{width:100%;min-height:3px;border-radius:8px 8px 3px 3px;background:linear-gradient(180deg,var(--acc),color-mix(in srgb,var(--acc) 50%,transparent));transition:height .35s ease}
.col.now .cb{opacity:.6;background-image:repeating-linear-gradient(135deg,rgba(255,255,255,.25) 0 6px,transparent 6px 12px),linear-gradient(180deg,var(--acc),var(--acc))}
.col .val{font-size:11px;color:var(--dim);margin-bottom:4px;font-variant-numeric:tabular-nums;white-space:nowrap}
.col .lbl{font-size:11px;color:var(--dim);margin-top:6px}
ul.runs,ul.topl,ul.feed{list-style:none;margin:0;padding:0}
.runs li,.topl li{position:relative;margin:5px 0;padding:8px 12px;border-radius:10px;background:color-mix(in srgb,var(--line) 40%,transparent);overflow:hidden;display:flex;gap:10px;align-items:center;justify-content:space-between}
.runs li.none{color:var(--dim)}
.fill{position:absolute;left:0;top:0;bottom:0;background:var(--glow);border-right:2px solid var(--acc);transition:width .4s ease}
.lt,.rt{position:relative}.lt{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;display:flex;align-items:center;gap:6px}.rt{font-variant-numeric:tabular-nums;color:var(--dim);flex:none}
.lt img,.feed img{flex:none;border-radius:4px}
.feed li{display:flex;align-items:center;gap:10px;padding:7px 0;border-bottom:1px solid var(--line);min-width:0}
.feed li:last-child{border:0}
.feed li.new{animation:in .4s ease both}
.feed a{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--link);text-decoration:none}.feed a:hover{text-decoration:underline}
.feed .host{flex:none;color:var(--dim);font-size:13px;max-width:40%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
@media (max-width:640px){.kpi.big{grid-column:span 1}.kpi.big b{font-size:40px}.cols{height:130px}}
.tabs{display:flex;gap:8px;margin:0 0 16px}
.tabs button{background:var(--card);color:var(--dim);border:1px solid var(--line);border-radius:999px;padding:7px 18px;font:inherit;cursor:pointer}
.tabs button.on{background:var(--acc);color:var(--acc-ink);border-color:var(--acc);font-weight:700}
[hidden]{display:none!important}
.lhead{padding:6px 0 2px;margin-bottom:22px}
.lhead h1{font-size:36px}
.lsub{color:var(--dim);margin:2px 0 0;font-size:16px}
.state{background:var(--card);background:color-mix(in srgb,var(--card) 50%,transparent);backdrop-filter:blur(14px) saturate(1.5);-webkit-backdrop-filter:blur(14px) saturate(1.5);border-color:rgba(255,255,255,.2)}
.kpi,.card{border-radius:24px;border:1px solid rgba(255,255,255,.16);background:var(--card);background:color-mix(in srgb,var(--card) 58%,transparent);backdrop-filter:blur(16px) saturate(1.5);-webkit-backdrop-filter:blur(16px) saturate(1.5);box-shadow:0 18px 44px -30px rgba(0,0,0,.8),0 1px 0 rgba(255,255,255,.1) inset}
.kpi.big{background:linear-gradient(135deg,color-mix(in srgb,var(--aero1) 34%,transparent),color-mix(in srgb,var(--acc) 14%,transparent)),var(--card)}
.kpi{padding:18px 20px}.kpi b{font-size:34px}.kpi.big b{font-size:54px}
.card{padding:18px 20px}.card h3{font-size:15px;margin-bottom:16px}
.tabs{display:inline-flex;flex-wrap:wrap;gap:4px;padding:5px;margin:0 0 20px;border-radius:99px;border:1px solid rgba(255,255,255,.2);background:var(--card);background:color-mix(in srgb,var(--card) 50%,transparent);backdrop-filter:blur(14px) saturate(1.5);-webkit-backdrop-filter:blur(14px) saturate(1.5)}
.tabs button{border:0;background:transparent;padding:8px 18px;color:var(--dim)}
.tabs button:hover{color:var(--ink)}
.tabs button.on{background:var(--acc);color:var(--acc-ink);box-shadow:0 4px 14px -6px rgba(0,0,0,.6)}
.col .cb{border-radius:10px 10px 4px 4px}
.runs li,.topl li{border-radius:14px}
.feed li{padding:9px 0}
</style>`
	body := extra + `<style>
.wrap:has(.lv){max-width:1260px}
.lv{display:grid;grid-template-columns:236px minmax(0,1fr);gap:28px;align-items:start}
.rail{position:sticky;top:96px;display:flex;flex-direction:column;gap:14px;padding:22px 16px 18px;border-radius:30px;border:1px solid rgba(255,255,255,.16);background:var(--card);background:color-mix(in srgb,var(--card) 58%,transparent);backdrop-filter:blur(18px) saturate(1.5);-webkit-backdrop-filter:blur(18px) saturate(1.5);box-shadow:0 18px 44px -30px rgba(0,0,0,.8),0 1px 0 rgba(255,255,255,.1) inset}
.rbrand{display:flex;align-items:center;gap:11px;font-size:32px;font-weight:800;letter-spacing:-.035em;padding:0 8px}
.rdot,.pulse{width:11px;height:11px;border-radius:50%;background:#3ecf8e;animation:pulse 1.6s infinite;flex:none}
.rsub{color:var(--dim);font-size:14px;margin:-8px 8px 2px;line-height:1.4}
.rail .tabs{display:flex;flex-direction:column;gap:3px;padding:0;margin:0;border:0;border-radius:0;background:none;backdrop-filter:none;-webkit-backdrop-filter:none}
.rail .tabs button{display:flex;align-items:center;gap:12px;text-align:left;width:100%;padding:11px 14px;border-radius:16px;font-size:15.5px;border:0;background:transparent;color:var(--dim);cursor:pointer;font-family:inherit}
.rail .tabs button svg{flex:none;opacity:.85}
.rail .tabs button:hover{color:var(--ink);background:rgba(128,128,128,.12)}
.rail .tabs button.on{background:var(--acc);color:var(--acc-ink);font-weight:700;box-shadow:0 8px 20px -12px rgba(0,0,0,.7)}
.rconn{display:flex;flex-direction:column;gap:8px;margin-top:4px;padding:14px 8px 0;border-top:1px solid rgba(255,255,255,.14);font-size:12.5px;color:var(--dim)}
.rconn .rmeta{display:flex;flex-direction:column;gap:2px}
.hero2{text-align:center;padding:40px 20px 32px;margin-bottom:14px;border-radius:34px;border:1px solid rgba(255,255,255,.16);background:linear-gradient(145deg,color-mix(in srgb,var(--aero1) 30%,transparent),color-mix(in srgb,var(--acc) 12%,transparent)),var(--card);backdrop-filter:blur(18px) saturate(1.5);-webkit-backdrop-filter:blur(18px) saturate(1.5);box-shadow:0 22px 56px -34px rgba(0,0,0,.85),0 1px 0 rgba(255,255,255,.14) inset}
.h2k{text-transform:uppercase;letter-spacing:.16em;font-size:13px;color:var(--dim)}
.h2n{font-size:clamp(46px,9vw,108px);font-weight:800;letter-spacing:-.045em;line-height:1.04;font-variant-numeric:tabular-nums;background:linear-gradient(135deg,var(--link),var(--acc));-webkit-background-clip:text;background-clip:text;color:transparent;margin:6px 0 2px}
.h2s{color:var(--dim);font-size:16px}
.h2r{margin-top:18px;display:inline-flex;align-items:center;gap:10px;padding:9px 20px;border-radius:99px;border:1px solid rgba(255,255,255,.2);background:rgba(128,128,128,.12);font-size:16px}
.h2r b{font-size:24px;font-variant-numeric:tabular-nums;color:var(--acc);letter-spacing:-.02em}
.pace{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:10px;margin-bottom:14px}
.pace>div{padding:14px 18px;border-radius:22px;border:1px solid rgba(255,255,255,.14);background:var(--card);background:color-mix(in srgb,var(--card) 58%,transparent);backdrop-filter:blur(14px) saturate(1.4);-webkit-backdrop-filter:blur(14px) saturate(1.4)}
.pace small{display:block;color:var(--dim);font-size:12px;letter-spacing:.05em;text-transform:uppercase}
.pace b{display:block;font-size:27px;letter-spacing:-.02em;font-variant-numeric:tabular-nums;margin-top:2px}
.pace em{font-style:normal;color:var(--dim);font-size:12px}
.split{display:grid;grid-template-columns:minmax(0,1.1fr) minmax(0,1fr);gap:14px;align-items:start}
.sidecol{display:flex;flex-direction:column;gap:14px}
.split .card{margin:0}
.tick .feed{max-height:880px;overflow:auto}
@media(max-width:1020px){.lv{grid-template-columns:1fr}.rail{position:static;flex-direction:row;flex-wrap:wrap;align-items:center;gap:10px}.rsub{display:none}.rail .tabs{flex-direction:row;flex-wrap:wrap}.rail .tabs button{width:auto;padding:9px 14px}.rconn{border:0;padding:0;margin:0}.split{grid-template-columns:1fr}.rbrand{font-size:26px}}
</style><div class=lv>
<aside class=rail>
<div class=rbrand><span class=rdot></span>Live</div>
<p class=rsub>Watch Flowsearch learn the web, in real time.</p>
<nav class=tabs aria-label="What to watch">
<button id=tabAll class=on><svg width=18 height=18 viewBox="0 0 24 24" fill=none stroke=currentColor stroke-width=1.9 stroke-linecap=round stroke-linejoin=round aria-hidden=true><rect x=3 y=3 width=7 height=7 rx=1.5 /><rect x=14 y=3 width=7 height=7 rx=1.5 /><rect x=3 y=14 width=7 height=7 rx=1.5 /><rect x=14 y=14 width=7 height=7 rx=1.5 /></svg>Everything</button>
<button id=tabDe><svg width=18 height=18 viewBox="0 0 24 24" fill=none stroke=currentColor stroke-width=1.9 stroke-linecap=round stroke-linejoin=round aria-hidden=true><circle cx=12 cy=12 r=9 /><path d="M3 12h18M12 3c3 3.5 3 14.5 0 18M12 3c-3 3.5-3 14.5 0 18"/></svg>German sites</button>
<button id=tabLm><svg width=18 height=18 viewBox="0 0 24 24" fill=none stroke=currentColor stroke-width=1.9 stroke-linecap=round stroke-linejoin=round aria-hidden=true><path d="M4 5h16v11H9l-5 4z"/></svg>Forums</button>
<button id=tabYt><svg width=18 height=18 viewBox="0 0 24 24" fill=none stroke=currentColor stroke-width=1.9 stroke-linecap=round stroke-linejoin=round aria-hidden=true><rect x=3 y=5 width=18 height=14 rx=3 /><path d="M10 9l5 3-5 3z"/></svg>Videos</button>
<button id=tabAm><svg width=18 height=18 viewBox="0 0 24 24" fill=none stroke=currentColor stroke-width=1.9 stroke-linecap=round stroke-linejoin=round aria-hidden=true><path d="M5 8h14l-1 12H6zM9 8V6a3 3 0 0 1 6 0v2"/></svg>Shopping</button>
<button id=tabRb><svg width=18 height=18 viewBox="0 0 24 24" fill=none stroke=currentColor stroke-width=1.9 stroke-linecap=round stroke-linejoin=round aria-hidden=true><rect x=3 y=8 width=18 height=10 rx=4 /><path d="M8 11v4M6 13h4"/><circle cx=16 cy=12.5 r=.8 /><circle cx=18 cy=14.5 r=.8 /></svg>Games</button></nav>
<div class=rconn><span id=state class=state data-s=connecting><i></i><span>Connecting…</span></span><div class=rmeta><span id=mps></span><span id=lag></span></div></div>
</aside>
<div class=stage>
<div id=allv>
<section class=hero2>
<div class=h2k>Pages Flowsearch knows</div>
<div class=h2n id=pages>0</div>
<div class=h2s><span id=sites>0</span> websites · <span id=terms>0</span> different words</div>
<div class=h2r><span class=pulse></span><b id=rate>0</b> new pages every second</div>
</section>
<section class=pace>
<div><small id=p300n>Pages per message</small><b id=p300>0</b></div>
<div><small id=pmn>Pages per minute</small><b id=pm>0</b></div>
<div><small id=phn>Pages per hour</small><b id=ph>0</b></div>
<div><small>Pages per day</small><b id=pday>0</b></div>
<div><small id=pdn>Pages this run</small><b id=pd>0</b><em id=runnote></em></div>
</section>
<section class=split>
<div class="card tick"><h3>Newest pages <small>as they arrive</small></h3><ul class=feed id=recent></ul></div>
<div class=sidecol>
<div class=card><h3>Pages per minute <small>last 10 minutes</small></h3><div class=cols id=hist></div></div>
<div class=card><h3>Pages per hour <small>last 6 hours</small></h3><div class=cols id=hhist></div></div>
<div class=card><h3>Pages per day <small>last 8 days</small></h3><div class=cols id=dhist></div></div>
<div class=card><h3>Biggest websites</h3><ul class=topl id=top></ul></div>
<div class=card><h3>Collection rounds</h3><ul class=runs id=runsList></ul></div>
</div>
</section>
</div>
<div id=dev hidden>
<section class=kpis>
<div class="kpi big"><small>Deutsche Seiten</small><b id=dpages>0</b><em><span id=dsites>0</span> Websites (.de .at .ch .li)</em></div>
<div class=kpi><small>Seiten pro Sekunde</small><b id=dps>0</b></div>
<div class=kpi><small>Seiten pro Minute</small><b id=dpm>0</b></div>
<div class=kpi><small>Seiten pro Stunde</small><b id=dph>0</b></div>
</section>
<section class=card><h3>Größte deutsche Websites</h3><ul class=topl id=dtop></ul></section>
<section class=card><h3>Neueste deutsche Seiten <small>sobald sie ankommen</small></h3><ul class=feed id=drecent></ul></section>
</div>
<div id=ytv hidden>
<section class=kpis>
<div class="kpi big"><small>YouTube videos</small><b id=yttot>0</b><em>videos Flowsearch knows about</em></div>
<div class=kpi><small>Random video</small><b style="font-size:1rem"><a class=chip href="/surprise?site=youtube.com&kind=video">Surprise me</a></b></div>
</section>
<section class=card><h3>12 random videos <small>reload the tab for new ones</small></h3><ul class=topl id=ytlist></ul></section>
</div>
<div id=amv hidden>
<section class=kpis>
<div class="kpi big"><small>Amazon pages in the index</small><b id=amtot>0</b><em>every Amazon page Flowsearch knows; updates live</em></div>
<div class=kpi><small>amazon.com pages</small><b id=amcom>0</b></div>
<div class=kpi><small>amazon.de pages</small><b id=amde>0</b></div>
<div class=kpi><small>Products with price &amp; rating</small><b id=ampriced>0</b><em>shown in the Shopping tab</em></div>
</section>
<section class=card><h3>Newest products <small>as the crawl saves them</small></h3><ul class=topl id=amlist></ul></section>
</div>
<div id=rbv hidden>
<section class=kpis>
<div class="kpi big"><small>Roblox games read</small><b id=rbgames>0</b><em>with creator, genre, players and visits</em></div>
<div class=kpi><small>Players in these games right now</small><b id=rbplayers>0</b></div>
<div class=kpi><small>Roblox pages in the index</small><b id=rbpages>0</b><em>updates live</em></div>
</section>
<section class=card><h3>Most players right now</h3><ul class=topl id=rbtop></ul></section>
<section class=card><h3>Newest games <small>as the crawl saves them</small></h3><ul class=topl id=rbrecent></ul></section>
</div>
<div id=lmv hidden>
<style>@keyframes lmfresh{from{background:rgba(76,141,255,.35)}to{background:transparent}}.lmfresh{animation:lmfresh 2s ease-out}.lmbar{height:8px;border-radius:99px;background:rgba(128,128,128,.25);margin-top:8px;overflow:hidden}.lmbar i{display:block;height:100%;background:var(--acc);transition:width .6s}</style>
<section class=kpis>
<div class="kpi big"><small>Forum posts</small><b id=lmtot>0</b><em>discussion posts and their best comments</em></div>
<div class=kpi><small>Communities</small><b id=lmcomm>0</b></div>
<div class=kpi><small>Servers</small><b id=lminst>0</b></div>
<div class=kpi><small>New posts per second</small><b id=lmrate>0</b></div>
</section>
<section class=card><h3>Collection progress <small>how the forum collection is going</small></h3><div id=lmcrawl>Loading…</div></section>
<section class=card><h3>Biggest communities</h3><ul class=topl id=lmcomms></ul></section>
<section class=card><h3>Just saved <small>live, newest first</small></h3><ul class=feed id=lmnew></ul></section>
</div>
</div></div>
<script>` + liveJS + `</script>`
	fmt.Fprint(w, page("Live - Flowsearch", "", body))
}

// clip cuts text to n characters, adding … when it had to.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// clientIP is the address of whoever is asking. Behind the Cloudflare tunnel every request comes from this box,
// so the real visitor is read from Cloudflare's header, but only when the request really came from the tunnel.
func clientIP(r *http.Request) string {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "127.0.0.1" || ip == "::1" {
		if v := strings.TrimSpace(r.Header.Get("Cf-Connecting-Ip")); v != "" {
			return v
		}
	}
	return ip
}

func allASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

var junkTitles = []string{"just a moment", "access denied", "403 forbidden", "404", "not found", "attention required", "are you a robot", "captcha", "error 4", "page unavailable", "verifying you are human"}

// quality scales a page's score by things that make it a better or worse answer:
// the site's own name in the query, error pages, and pages in another script than the query.
func quality(qt []string, asciiQuery bool, host string, rec record) float64 {
	f := 1.0
	h := strings.ToLower(strings.TrimPrefix(host, "www."))
	labels := strings.Split(h, ".")
	for _, t := range qt {
		for _, l := range labels[:max(1, len(labels)-1)] {
			if len(t) >= 3 && strings.Trim(l, "-") == t {
				f *= 1.35 // "kernel" is in kernel.org
				if u, err := url.Parse(rec.URL); err == nil && strings.Trim(u.Path, "/") == "" {
					f *= 1.25 // and this is that site's front page
				}
				break
			}
		}
	}
	lt := strings.ToLower(rec.Title)
	if strings.TrimSpace(lt) == "" {
		f *= 0.6
	}
	for _, j := range junkTitles {
		if strings.Contains(lt, j) {
			f *= 0.2
			break
		}
	}
	if asciiQuery {
		letters, other := 0, 0
		for _, r := range rec.Title {
			if unicode.IsLetter(r) {
				letters++
				if r > 0x24F { // beyond Latin
					other++
				}
			}
		}
		if letters > 4 && float64(other)/float64(letters) > 0.4 {
			f *= 0.6
		}
	}
	return f
}

// isEnglish: see sgpack.IsEnglish.
func isEnglish(rec record) bool {
	return sgpack.IsEnglish(rec.URL, rec.Title, rec.Description, rec.full())
}

// deHost: German-language sites are told apart by their country domain.
func deHost(host string) bool {
	h := strings.ToLower(host)
	for _, t := range []string{".de", ".at", ".ch", ".li"} {
		if strings.HasSuffix(h, t) {
			return true
		}
	}
	return false
}

func isDE(r *http.Request) bool {
	return r.Header.Get("X-Lang") == "de" || r.URL.Query().Get("lang") == "de"
}

// enSite: the "biggest sites" lists of the English pages only name .com and .org sites.
func enSite(host string) bool {
	h := strings.ToLower(host)
	return strings.HasSuffix(h, ".com") || strings.HasSuffix(h, ".org")
}

// surprise sends you to a page picked at random from the whole index: it jumps to a random place in the pages
// file, skips the rest of that line and takes the next whole one (so every page is about equally likely).
// On the German site only German (.de .at .ch .li) pages are drawn.
func surprise(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("pick") { // the one Surprise me control of the home page: what it is set to
	case "amazon":
		surpriseSite(w, r, "amazon.com", "")
		return
	case "products":
		surpriseSite(w, r, "amazon.de", "products")
		return
	case "youtube":
		surpriseSite(w, r, "youtube.com", "video")
		return
	case "music":
		surpriseSite(w, r, "youtube.com", "music")
		return
	case "mace":
		surpriseSite(w, r, "youtube.com", "mace")
		return
	case "minecraft":
		surpriseSite(w, r, "youtube.com", "minecraft")
		return
	case "connector":
		surpriseSite(w, r, "claude.com", "connector")
		return
	case "site":
		surpriseSite(w, r, r.URL.Query().Get("site"), "")
		return
	case "page": // a random page: falls through to the normal Surprise me below (which shows where it sends you)
		q := r.URL.Query()
		q.Del("site")
		r.URL.RawQuery = q.Encode()
	}
	if site := r.URL.Query().Get("site"); site != "" {
		surpriseSite(w, r, site, r.URL.Query().Get("kind"))
		return
	}
	if f, err := os.Open(pagesFile); err == nil {
		defer f.Close()
		if st, err := f.Stat(); err == nil && st.Size() > 1<<16 {
			german := isDE(r)
			// the last pages shown are kept in a cookie in the visitor's own browser (short hashes, no ID, nothing stored here)
			var seen []string
			if c, err := r.Cookie("surprise_seen"); err == nil {
				seen = strings.Split(c.Value, ".")
			}
			deadline := time.Now().Add(8 * time.Second) // each draw is checked by Jev; never keep the visitor waiting long
			for try := 0; try < 60 && time.Now().Before(deadline); try++ {
				if _, err := f.Seek(rand.Int63n(st.Size()-(1<<16)), io.SeekStart); err != nil {
					break
				}
				br := bufio.NewReaderSize(f, 1<<16)
				if _, err := br.ReadBytes('\n'); err != nil { // the rest of a half line
					continue
				}
				line, err := br.ReadBytes('\n')
				if err != nil {
					continue
				}
				var rec struct {
					U string `json:"u"`
					T string `json:"t"`
					D string `json:"d"`
				}
				if json.Unmarshal(line, &rec) != nil || strings.TrimSpace(rec.T) == "" ||
					!(strings.HasPrefix(rec.U, "http://") || strings.HasPrefix(rec.U, "https://")) {
					continue
				}
				if german {
					host := strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(rec.U, "https://"), "http://"), "/", 2)[0]
					if !deHost(host) {
						continue
					}
				}
				if !surpriseSafe(strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(rec.U, "https://"), "http://"), "/", 2)[0], rec.T, rec.U, rec.D) {
					continue // blocked, adult, or Jev could not say: draw another
				}
				h := fmt.Sprintf("%x", crc32.ChecksumIEEE([]byte(rec.U)))
				dup := false
				for _, x := range seen {
					if x == h {
						dup = true
					}
				}
				if dup {
					continue
				}
				seen = append(seen, h)
				if len(seen) > 30 {
					seen = seen[len(seen)-30:]
				}
				http.SetCookie(w, &http.Cookie{Name: "surprise_seen", Value: strings.Join(seen, "."), Path: "/", MaxAge: 30 * 86400, SameSite: http.SameSiteLaxMode})
				w.Header().Set("Cache-Control", "no-store")
				surpriseGo(w, r, rec.U, rec.T)
				return
			}
		}
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// jevNote is the small line under the result count when Jev ordered the top results.
func jevNote(ms int64, cut int) string {
	if ms == 0 {
		return ""
	}
	if cut > 0 {
		return fmt.Sprintf(" · sorted for relevance in %d ms, %d repeats or unrelated pages hidden", ms, cut)
	}
	return fmt.Sprintf(" · sorted for relevance in %d ms", ms)
}
