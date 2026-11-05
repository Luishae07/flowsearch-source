package main

// The Forums tab: only discussion posts and forum threads from the search results: Lemmy posts, and sites that are forums
// (forum./discuss./community. hosts, Hacker News, Reddit ...).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var forumHosts = []string{"news.ycombinator.com", "reddit.com", "quora.com", "lobste.rs", "tildes.net", "slashdot.org", "discourse.org"}

var lemmyPostsFile = fsData + "/lemmy-posts.jsonl"

type lemmyComment struct {
	A string `json:"a"`
	T string `json:"t"`
	S int    `json:"s"`
}
type lemmyPost struct {
	U         string         `json:"u"`
	T         string         `json:"t"`
	Body      string         `json:"body"`
	Instance  string         `json:"instance"`
	Community string         `json:"community"`
	Name      string         `json:"name"`
	Score     int            `json:"score"`
	Comments  int            `json:"comments"`
	Published string         `json:"published"`
	Top       []lemmyComment `json:"top"`
	Platform  string         `json:"platform"` // "" = Lemmy, "discourse"
}

var (
	lpMu    sync.Mutex
	lpMtime time.Time
	lpData  = map[string]lemmyPost{}
)

// lemmyDetails: what the Lemmy crawl read about each post (score, comment count, top comments), by the post's address.
func lemmyDetails() map[string]lemmyPost {
	lpMu.Lock()
	defer lpMu.Unlock()
	st, err := os.Stat(lemmyPostsFile)
	if err != nil {
		return lpData
	}
	if !st.ModTime().Equal(lpMtime) {
		m := map[string]lemmyPost{}
		if f, err := os.Open(lemmyPostsFile); err == nil {
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for sc.Scan() {
				var p lemmyPost
				if json.Unmarshal(sc.Bytes(), &p) == nil && p.U != "" {
					m[p.U] = p
				}
			}
			f.Close()
		}
		lpData, lpMtime = m, st.ModTime()
	}
	return lpData
}

func isForum(host, source string) bool {
	if source == "lemmy" {
		return true
	}
	h := strings.TrimPrefix(strings.ToLower(host), "www.")
	for _, f := range forumHosts {
		if h == f || strings.HasSuffix(h, "."+f) {
			return true
		}
	}
	for _, p := range []string{"forum.", "forums.", "discuss.", "community.", "board.", "boards.", "talk.", "bbs.", "lemmy.", "feddit.", "kbin."} {
		if strings.HasPrefix(h, p) {
			return true
		}
	}
	return false
}

// platform names the kind of forum a post comes from, for the filter chips and the coloured label.
func platform(host, source string) string {
	h := strings.TrimPrefix(strings.ToLower(host), "www.")
	switch {
	case source == "lemmy" || strings.HasPrefix(h, "lemmy.") || strings.HasPrefix(h, "feddit.") || strings.HasPrefix(h, "kbin."):
		return "Lemmy"
	case h == "news.ycombinator.com":
		return "Hacker News"
	case h == "reddit.com" || strings.HasSuffix(h, ".reddit.com"):
		return "Reddit"
	}
	return "Other forums"
}

var platformColor = map[string]string{"Lemmy": "#16a066", "Hacker News": "#ff6600", "Reddit": "#ff4500", "Other forums": "#7b6fd6"}

const forumCSS = `<style>
.fchips{display:flex;gap:8px;flex-wrap:nowrap;overflow-x:auto;scrollbar-width:none;margin:4px 0 12px;padding:2px 0}
.fchips::-webkit-scrollbar{display:none}
.fchips a{flex:0 0 auto;padding:6px 14px;border:1px solid var(--line);border-radius:999px;font-size:14px;color:var(--dim);text-decoration:none;background:var(--card);white-space:nowrap;transition:background .12s,color .12s}
.fchips a:hover{color:var(--link)}.fchips a.on{background:var(--acc);color:var(--acc-ink);border-color:var(--acc);font-weight:600}
.fchips small{opacity:.7;margin-left:5px}
.fsort{display:inline-flex;border:1px solid var(--line);border-radius:12px;overflow:hidden;background:var(--card);margin:2px 0 12px}
.fsort a{padding:7px 16px;font-size:14px;color:var(--dim);text-decoration:none;border-right:1px solid var(--line)}.fsort a:last-child{border-right:0}
.fsort a:hover{color:var(--link)}.fsort a.on{background:var(--acc);color:var(--acc-ink);font-weight:600}
.fcard{display:grid;grid-template-columns:54px minmax(0,1fr);gap:0 12px;padding:14px 16px;margin:0 0 12px;border:1px solid var(--line);border-radius:16px;background:var(--card);transition:transform .12s,box-shadow .12s,border-color .12s}
.fcard:hover{transform:translateY(-2px);box-shadow:0 10px 24px rgba(0,0,0,.2);border-color:var(--acc)}
.fvote{display:flex;flex-direction:column;align-items:center;gap:1px;padding-top:2px;color:var(--dim);font-size:12px}
.fvote i{font-style:normal;font-size:15px;line-height:1;color:var(--acc)}.fvote b{font-size:16px;color:var(--ink,inherit)}
.fmain{min-width:0}
.fhead{display:flex;align-items:center;gap:7px;flex-wrap:wrap;font-size:12.5px;color:var(--dim);margin-bottom:5px}
.fhead .av{width:18px;height:18px}
.fplat{display:inline-flex;align-items:center;gap:5px;color:#fff;font-weight:700;font-size:11px;padding:2px 9px 2px 5px;border-radius:99px;letter-spacing:.02em}.fplat img{border-radius:4px;background:#fff;padding:1px}
.fcomm{font-weight:600;padding:1px 9px;border-radius:99px;background:rgba(128,128,128,.14);color:var(--ink,inherit)}
.fname{opacity:.7}.fago{margin-left:auto;opacity:.75;white-space:nowrap}
.ftitle{display:block;font-size:18px;line-height:1.3;color:var(--link);text-decoration:none;font-weight:600;overflow-wrap:anywhere}
.ftitle:hover{text-decoration:underline}
.fsnip{margin:6px 0 0;font-size:14.5px;line-height:1.5;color:var(--dim);display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
.fstats{display:flex;gap:14px;flex-wrap:wrap;margin:9px 0 0;font-size:13px;color:var(--dim)}.fstats b{color:var(--ink,inherit)}
.fbest{margin:11px 0 0;padding:10px 14px;border-radius:14px 14px 14px 4px;background:rgba(76,141,255,.10);border:1px solid rgba(76,141,255,.35)}
.fbest.nosol{background:none;border:1px dashed var(--line);opacity:.85}
.fbest .lab{font-size:11px;font-weight:700;letter-spacing:.05em;text-transform:uppercase;color:var(--acc);margin-bottom:5px}
.fbest.nosol .lab{color:var(--dim)}
.who{display:flex;align-items:center;gap:7px;font-size:12.5px;color:var(--dim)}.who b{color:var(--ink,inherit)}
.cico{border-radius:50%;vertical-align:-3px;margin-right:5px}.fav.pic img{border-radius:50%}.fhead .av.pic img{border-radius:6px}
.fav{display:inline-flex;align-items:center;justify-content:center;width:22px;height:22px;border-radius:50%;background:rgba(128,128,128,.25);color:var(--dim);flex:none}
.fbest p,.fc p{margin:5px 0 0;font-size:14.5px;line-height:1.5;overflow-wrap:anywhere}
.fcom{margin:10px 0 0}
.fcom summary{cursor:pointer;font-size:13.5px;color:var(--link);list-style:none;display:inline-flex;gap:6px;align-items:center;padding:4px 0}
.fcom summary::-webkit-details-marker{display:none}.fcom summary::before{content:"▸";transition:transform .15s}.fcom[open] summary::before{transform:rotate(90deg)}
.fc{margin:8px 0 0;padding:8px 12px;border-left:3px solid var(--line);background:rgba(128,128,128,.07);border-radius:0 12px 12px 0}
.fc.isbest{border-left-color:var(--acc);background:rgba(76,141,255,.08)}.fc .star{font-size:10.5px;font-weight:700;color:var(--acc);margin-left:4px}
.fempty{padding:26px 8px;text-align:center}.fempty h2{margin:0 0 6px}.fempty p{color:var(--dim);margin:0 0 14px}
@media(max-width:560px){.fcard{grid-template-columns:1fr}.fvote{flex-direction:row;gap:6px;margin-bottom:6px}.ftitle{font-size:17px}.fago{margin-left:0}}
.vblock .fcard{display:block}.vblock .fvote,.vblock .fago{display:none}
</style>`

// wordMatches: does the text have the word, or a form of it? Exact, a plural, or a longer word that starts with it (for words of 4 letters or more,
// so "mapping" finds "maps" and "map" finds "maps" but "ai" does not find "air").
func wordMatches(words map[string]bool, t string) bool {
	if words[t] || words[t+"s"] || words[t+"es"] {
		return true
	}
	if len(t) >= 4 {
		for w := range words {
			if len(w) > len(t) && strings.HasPrefix(w, t) {
				return true
			}
		}
		if strings.HasSuffix(t, "s") && words[strings.TrimSuffix(t, "s")] {
			return true
		}
	}
	return false
}

// markTerms: the text, escaped for the page. (The search words used to be highlighted; Jev now picks the best comment instead.)
func markTerms(text string, terms []string) string { return html.EscapeString(text) }

type forumHit struct {
	p        lemmyPost
	score    float64
	inTitle  bool
	inBody   bool
	inCommts bool
}

// forumsSearch: the Forums tab shows only the posts that have a comments dropdown, that is the Lemmy posts the Lemmy crawl has read
// (with score, comment count and top comments). It searches the title, the text, the community and the TOP COMMENTS, understands plurals and
// word forms, ranks by match and by how well the post did, and can be sorted (best, newest, top, most comments) and narrowed to a community.
func forumsSearch(w http.ResponseWriter, r *http.Request, q string) {
	terms := tabTerms(q)
	comm := r.URL.Query().Get("c")
	sortBy := r.URL.Query().Get("s")
	need := len(terms) // every word must be somewhere in the post; a long search needs at least half of them
	if len(terms) >= 4 {
		need = (len(terms) + 1) / 2
	}
	// the index picks the best few hundred posts for these words without reading the whole file; the careful scoring below works on those
	ageCh := make(chan string, 1) // Jev/clef: new or old posts? asked while the posts are found and judged
	go func() { ageCh <- jevForumAge(q) }()
	fx := forumIndex()
	cand := fx.search(terms, need, sortBy, comm, 400)
	total := fx.count()
	type docWords struct {
		p           lemmyPost
		title, body map[string]bool
		comm        map[string]bool
		comments    []map[string]bool
	}
	var docs []docWords
	for _, p := range cand.Posts {
		d := docWords{p: p, title: wordSet(p.T), body: wordSet(p.Body), comm: wordSet(p.Community + " " + p.Name)}
		for _, c := range p.Top {
			d.comments = append(d.comments, wordSet(c.T))
		}
		docs = append(docs, d)
	}
	// how rare is each word among the posts? a rare word ("organic") says much more than a common one ("google")
	idf := map[string]float64{}
	for _, t := range terms {
		idf[t] = math.Log(float64(total+1)/float64(cand.DF[t]+1)) + 1
	}
	var hits []forumHit
	for _, d := range docs {
		h := forumHit{p: d.p}
		matched, main := 0, 0
		for _, t := range terms {
			got := false
			w := idf[t]
			if wordMatches(d.title, t) {
				h.score += 10 * w // the title says what the post is about
				h.inTitle, got, main = true, true, main+1
			} else if wordMatches(d.comm, t) {
				h.score += 3 * w
				got, main = true, main+1
			} else if wordMatches(d.body, t) {
				h.score += 3 * w
				h.inBody, got, main = true, true, main+1
			}
			n := 0
			for _, m := range d.comments {
				if wordMatches(m, t) {
					n++
				}
			}
			if n > 0 {
				h.score += math.Min(float64(n), 2) * w * 0.25 // comments only add a little
				h.inCommts, got = true, true
			}
			if got {
				matched++
			}
		}
		if matched < need || h.score == 0 {
			continue
		}
		h.score *= float64(matched) / float64(len(terms)) // the more of the words, the better
		h.score *= 0.4 + 0.6*float64(main)/float64(len(terms)) // and the more of them in the title, text or community, not only in the comments
		// words that sit together, in order ("organic maps"), in the title or the text
		tt, bt := " "+strings.Join(tabTerms(d.p.T), " ")+" ", " "+strings.Join(tabTerms(d.p.Body), " ")+" "
		for i := 0; i+1 < len(terms); i++ {
			pair := " " + terms[i] + " " + terms[i+1] + " "
			if strings.Contains(tt, pair) {
				h.score += 6 * (idf[terms[i]] + idf[terms[i+1]]) / 2
			} else if strings.Contains(bt, pair) {
				h.score += 3 * (idf[terms[i]] + idf[terms[i+1]]) / 2
			}
		}
		h.score += math.Log1p(math.Max(0, float64(d.p.Score)))*0.5 + math.Log1p(float64(d.p.Comments))*0.3 // a post that did well ranks a little higher
		hits = append(hits, h)
	}
	// narrow to a community
	counts := cand.CommCounts
	if comm != "" {
		keep := hits[:0:0]
		for _, h := range hits {
			if h.p.Community == comm {
				keep = append(keep, h)
			}
		}
		hits = keep
	}
	// the order: chosen with the chips, or by match (and by Jev's idea of the age the search wants)
	mode := ""
	switch sortBy {
	case "new":
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].p.Published > hits[j].p.Published })
	case "top":
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].p.Score > hits[j].p.Score })
	case "comments":
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].p.Comments > hits[j].p.Comments })
	default:
		sortBy = ""
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
		if r.URL.Query().Get("debug") == "1" { // a look at the match scores before Jev judges them
			for i, h := range hits {
				if i < 8 || h.p.U == "https://lemmy.ml/post/19251943" {
					log.Printf("forums debug #%d score=%.1f title=%v | %s", i+1, h.score, h.inTitle, clip(h.p.T, 60))
				}
			}
		}
		forumsDebug = r.URL.Query().Get("debug") == "1"
		hits = jevJudgeForums(q, hits)
		// Jev: does this search want new or old posts? only a nudge among posts that match, never the main order
		if mode = <-ageCh; mode != "any" && len(hits) > 1 {
			byDate := append([]forumHit(nil), hits...)
			sort.SliceStable(byDate, func(i, j int) bool { return byDate[i].p.Published < byDate[j].p.Published })
			rank := map[string]float64{}
			for i, h := range byDate {
				rank[h.p.U] = float64(i) / float64(len(byDate)-1) // 0 = oldest, 1 = newest
			}
			for i := range hits {
				r := rank[hits[i].p.U]
				if mode == "old" {
					r = 1 - r
				}
				hits[i].score *= 1 + 0.25*r
			}
			sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
		}
	}
	if r.URL.Query().Get("debug") == "1" {
		for i, h := range hits {
			if i < 6 {
				log.Printf("forums final #%d score=%.2f | %s", i+1, h.score, clip(h.p.T, 60))
			}
		}
	}
	if r.URL.Query().Get("json") == "1" { // the same ranked posts as JSON (the MCP server reads them): ?json=1&n=10&p=1, with q, s and c as on the page
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		n = min(max(n, 1), 30)
		pj, _ := strconv.Atoi(r.URL.Query().Get("p"))
		from := (max(pj, 1) - 1) * n
		type jpost struct {
			URL       string         `json:"url"`
			Title     string         `json:"title"`
			Instance  string         `json:"instance"`
			Community string         `json:"community"`
			Score     int            `json:"score"`
			Comments  int            `json:"comments"`
			Published string         `json:"published"`
			Body      string         `json:"body"`
			Top       []lemmyComment `json:"top"`
		}
		out := []jpost{}
		for i := from; i < len(hits) && i < from+n; i++ {
			p := hits[i].p
			top := p.Top
			if len(top) > 2 {
				top = top[:2]
			}
			out = append(out, jpost{p.U, p.T, p.Instance, p.Community, p.Score, p.Comments, p.Published, clip(p.Body, 400), top})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"query": q, "total": len(hits), "posts": out})
		return
	}
	const per = 10
	pg, _ := strconv.Atoi(r.URL.Query().Get("p"))
	last := max(1, (len(hits)+per-1)/per)
	pg = min(max(pg, 1), last)
	href := func(c, so string, p int) string {
		v := url.Values{"q": {q}, "tab": {"forums"}}
		if c != "" {
			v.Set("c", c)
		}
		if so != "" {
			v.Set("s", so)
		}
		if p > 1 {
			v.Set("p", strconv.Itoa(p))
		}
		return "/search?" + v.Encode()
	}
	var b strings.Builder
	b.WriteString(tabNav(q, "forums") + forumCSS)
	if len(hits) == 0 && comm == "" {
		fmt.Fprintf(&b, `<div class="empty fempty"><h2>No forum posts found for “%s”</h2><p>This tab searches the %s Lemmy posts that Flowsearch has read, with their top comments. Try fewer words, or one of these:</p><div class=fchips style="justify-content:center;flex-wrap:wrap">%s</div></div>`, html.EscapeString(q), commas(total), forumSuggestions(r))
	} else {
		// sort chips
		b.WriteString(`<div class=fsort>`)
		chip := func(label, so string) {
			cls := ""
			if so == sortBy {
				cls = ` class=on`
			}
			fmt.Fprintf(&b, `<a%s href="%s">%s</a>`, cls, html.EscapeString(href(comm, so, 1)), label)
		}
		chip("Best match", "")
		chip("Newest", "new")
		chip("Top", "top")
		chip("Most comments", "comments")
		b.WriteString(`</div>`)
		// community chips: the five with the most posts for this search
		type cc struct {
			c string
			n int
		}
		var cl []cc
		for c, n := range counts {
			cl = append(cl, cc{c, n})
		}
		sort.Slice(cl, func(i, j int) bool { return cl[i].n > cl[j].n || (cl[i].n == cl[j].n && cl[i].c < cl[j].c) })
		if len(cl) > 1 || comm != "" {
			b.WriteString(`<div class=fchips>`)
			cls := ""
			if comm == "" {
				cls = ` class=on`
			}
			fmt.Fprintf(&b, `<a%s href="%s">All communities</a>`, cls, html.EscapeString(href("", sortBy, 1)))
			for i, c := range cl {
				if i >= 6 {
					break
				}
				cls := ""
				if c.c == comm {
					cls = ` class=on`
				}
				fmt.Fprintf(&b, `<a%s href="%s">%s<small>%d</small></a>`, cls, html.EscapeString(href(c.c, sortBy, 1)), html.EscapeString(c.c), c.n)
			}
			b.WriteString(`</div>`)
		}
		note := ""
		switch mode {
		case "new":
			note = ` · this search looks for recent posts, so newer ones rank a little higher`
		case "old":
			note = ` · this search looks for older posts, so older ones rank a little higher`
		}
		fmt.Fprintf(&b, `<p class=meta>%s posts with comments, from %s%s</p>`, commas(cand.Total), commas(total), note)
	}
	color := platformColor["Lemmy"]
	pageHits := hits[min((pg-1)*per, len(hits)):min(pg*per, len(hits))]
	var pagePosts []lemmyPost
	for _, h := range pageHits {
		pagePosts = append(pagePosts, h.p)
	}
	best := jevBestComments(q, pagePosts) // Jev: the best comment of each post for this search
	if cap, ok := r.Context().Value(askKey{}).(*askCapture); ok { // "Ask the forums" only wants the ranked posts and the picked comments
		cap.hits, cap.best = pageHits, best
		return
	}
	for _, h := range pageHits {
		p := h.p
		platName, platIcon := "Lemmy", "join-lemmy.org"
		if p.Platform == "discourse" {
			platName, platIcon = "Discourse", "discourse.org"
		} else if p.Platform == "flowforum" {
			platName, platIcon = "Flowforum", p.Instance
		}
		head := `<span class=fplat style="background:` + color + `"><img src="/flowcdn/fav/` + platIcon + `.jxl" width=14 height=14 alt="" onerror="this.remove()">` + platName + `</span>` + instanceLogo(p.Instance, p.U) + `<b>` + html.EscapeString(p.Instance) + `</b>`
		if p.Community != "" {
			head += `<span class=fcomm>` + communityIcon(p) + html.EscapeString(p.Community) + `</span>`
			if p.Name != "" && !strings.EqualFold(p.Name, strings.TrimPrefix(p.Community, "c/")) {
				head += `<span class=fname>` + html.EscapeString(clip(p.Name, 40)) + `</span>`
			}
		}
		if ago := forumAgo(p.Published); ago != "" {
			head += `<span class=fago>` + ago + `</span>`
		}
		extra := `<div class=fstats><span>💬 <b>` + strconv.Itoa(p.Comments) + `</b> comments</span>`
		if len(p.Published) >= 10 {
			extra += `<span>` + html.EscapeString(p.Published[:10]) + `</span>`
		}
		extra += `</div>`
		bi, hasBest := best[p.U]
		if hasBest && bi >= 0 && bi < len(p.Top) { // Jev's pick, right under the post
			c := p.Top[bi]
			extra += `<div class=fbest><div class=lab>Best comment for your search</div><div class=who>` + forumAvatarI(p, c.A) + `<b>` + html.EscapeString(c.A) + `</b> · ▲ ` + strconv.Itoa(c.S) + `</div><p>` + html.EscapeString(clip(c.T, 320)) + `</p></div>`
		} else if hasBest && len(p.Top) > 0 { // Jev: none of the comments answers the search; say so, and show the most upvoted one
			top := p.Top[0]
			for _, c := range p.Top {
				if c.S > top.S {
					top = c
				}
			}
			extra += `<div class="fbest nosol"><div class=lab>No comment answers this search · most upvoted comment</div><div class=who>` + forumAvatarI(p, top.A) + `<b>` + html.EscapeString(top.A) + `</b> · ▲ ` + strconv.Itoa(top.S) + `</div><p>` + html.EscapeString(clip(top.T, 260)) + `</p></div>`
		}
		extra += `<details class=fcom><summary>Top comments (` + strconv.Itoa(len(p.Top)) + ` of ` + strconv.Itoa(p.Comments) + `)</summary>`
		for j, c := range p.Top {
			cls, star := "fc", ""
			if hasBest && j == bi {
				cls, star = "fc isbest", `<span class=star>BEST</span>`
			}
			extra += `<div class="` + cls + `"><div class=who>` + forumAvatarI(p, c.A) + `<b>` + html.EscapeString(c.A) + `</b> · ▲ ` + strconv.Itoa(c.S) + star + `</div><p>` + html.EscapeString(c.T) + `</p></div>`
		}
		extra += `</details>`
		snip := ""
		if strings.TrimSpace(p.Body) != "" {
			snip = `<p class=fsnip>` + markTerms(clip(p.Body, 300), terms) + `</p>`
		}
		fmt.Fprintf(&b, `<article class=fcard><div class=fvote><i>▲</i><b>%s</b></div><div class=fmain><div class=fhead>%s</div><a class=ftitle href="%s" rel="noopener noreferrer">%s</a>%s%s</div></article>`,
			humanVotes(p.Score), head, html.EscapeString(p.U), markTerms(firstNonEmpty(p.T, p.U), terms), snip, extra)
	}
	if last > 1 {
		link := func(n int, text string) string {
			return fmt.Sprintf(`<a class=pbtn href="%s">%s</a>`, html.EscapeString(href(comm, sortBy, n)), text)
		}
		b.WriteString(`<nav class=pager aria-label=Pages>`)
		if pg > 1 {
			b.WriteString(link(pg-1, "&larr;"))
		}
		fmt.Fprintf(&b, `<span>%d / %d</span>`, pg, last)
		if pg < last {
			b.WriteString(link(pg+1, "&rarr;"))
		}
		b.WriteString(`</nav>`)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, page(q+" - Forums - Flowsearch", q, b.String()))
}

// humanVotes: 1234 -> 1.2k
func humanVotes(n int) string {
	if n >= 1000 || n <= -1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return strconv.Itoa(n)
}

const personSVG = `<img src="/flowcdn/vendor/icons/person.svg" width=14 height=14 alt="">`

// forumAvatarI: the commenter's profile picture once it is known (it is fetched in the background after the first search that
// shows it), else a round person icon.
func forumAvatarI(p lemmyPost, name string) string {
	if src := lemmyIcon("u", p.Instance, name, p.U); src != "" {
		return `<span class="fav pic" title="` + html.EscapeString(name) + `">` + iconImg(src, 22, "") + `</span>`
	}
	return `<span class=fav title="` + html.EscapeString(name) + `">` + personSVG + `</span>`
}

// instanceLogo: the instance's own icon once known, else the site's favicon through flowcdn.
func instanceLogo(inst, postURL string) string {
	if src := lemmyIcon("i", inst, "", postURL); src != "" {
		return `<span class="av pic">` + iconImg(src, 22, "") + `</span>`
	}
	return avatar(inst)
}

func communityIcon(p lemmyPost) string {
	if src := lemmyIcon("c", p.Instance, strings.TrimPrefix(p.Community, "c/"), p.U); src != "" {
		return iconImg(src, 16, "cico")
	}
	return ""
}

// forumAgo: "3 days ago" for a date like 2026-10-27T12:30:00Z.
func forumAgo(published string) string {
	t, err := time.Parse(time.RFC3339, published)
	if err != nil {
		if len(published) >= 19 {
			t, err = time.Parse("2006-01-02T15:04:05", published[:19])
		}
		if err != nil {
			return ""
		}
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return "just now"
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	case d < 730*24*time.Hour:
		return fmt.Sprintf("%d months ago", int(d.Hours()/24/30))
	}
	return fmt.Sprintf("%d years ago", int(d.Hours()/24/365))
}

// forumSuggestions: a few searches that are known to have answers (the 100 searches of forums_warm.py), as chips.
func forumSuggestions(r *http.Request) string {
	b, _ := os.ReadFile(fsHome + "/forums-warm-words.txt")
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	rand.Shuffle(len(lines), func(i, j int) { lines[i], lines[j] = lines[j], lines[i] })
	var out strings.Builder
	for i, l := range lines {
		if i >= 6 || strings.TrimSpace(l) == "" {
			break
		}
		out.WriteString(`<a href="/search?tab=forums&q=` + url.QueryEscape(l) + `">` + html.EscapeString(l) + `</a>`)
	}
	return out.String()
}
