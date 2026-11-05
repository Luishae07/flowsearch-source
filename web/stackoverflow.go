package main

// The Stack Overflow tab: questions read through Stack Exchange's own API (cmd: selfhost/p/so_month.py + so_to_jsonl.py), shown in the
// search results (tab=stack). stackoverflow-questions.jsonl has one line per question. The text is CC BY-SA 4.0, so every card
// names the author and links back to the question.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var stackFile = fsData + "/stackoverflow-questions.jsonl"

type stackQ struct {
	U, T, D, Author, ALink string
	Tags                   []string
	Score, Answers         int
	Answered, Closed       bool
	Created                int64
}

var (
	stackMu    sync.Mutex
	stackMtime time.Time
	stackData  []stackQ
)

func stackQuestions() []stackQ {
	stackMu.Lock()
	defer stackMu.Unlock()
	st, err := os.Stat(stackFile)
	if err != nil {
		return nil
	}
	if !st.ModTime().Equal(stackMtime) {
		var out []stackQ
		if f, err := os.Open(stackFile); err == nil {
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for sc.Scan() {
				var raw struct {
					U, T, D, Author, ALink string
					Tags                   []string
					Score, Answers         int
					Answered, Closed       bool
					Created                int64
				}
				// the file's keys are lower case
				var m map[string]json.RawMessage
				if json.Unmarshal(sc.Bytes(), &m) != nil {
					continue
				}
				get := func(k string, v any) { json.Unmarshal(m[k], v) }
				get("u", &raw.U)
				get("t", &raw.T)
				get("d", &raw.D)
				get("author", &raw.Author)
				get("alink", &raw.ALink)
				get("tags", &raw.Tags)
				get("score", &raw.Score)
				get("answers", &raw.Answers)
				get("answered", &raw.Answered)
				get("closed", &raw.Closed)
				get("created", &raw.Created)
				if raw.U == "" {
					continue
				}
				out = append(out, stackQ(raw))
			}
			f.Close()
		}
		stackData, stackMtime = out, st.ModTime()
	}
	return stackData
}

const stackCSS = `<style>
.sq{padding:12px 14px;margin:0 0 12px;border:1px solid var(--line);border-radius:14px;background:var(--card)}
.sq h3{margin:0 0 4px;font-size:17px;line-height:1.3;font-weight:600}.sq h3 a{color:var(--link);text-decoration:none}.sq h3 a:hover{text-decoration:underline}
.sq p{margin:0 0 8px;font-size:14px;line-height:1.5;color:var(--dim)}
.sqm{display:flex;gap:12px;flex-wrap:wrap;align-items:center;font-size:12.5px;color:var(--dim)}
.sqm b{color:var(--ink,inherit)}.sqm .ok{color:#3fb26b}
.sqt{display:flex;gap:6px;flex-wrap:wrap;margin:0 0 8px}.sqt span{font-size:11.5px;padding:1px 9px;border-radius:99px;border:1px solid var(--line);color:var(--dim)}
.sqc{font-size:11.5px;color:var(--dim);margin-top:6px}.sqc a{color:inherit}
</style>`

func stackCard(q stackQ) string {
	var b strings.Builder
	b.WriteString(`<article class=sq>`)
	fmt.Fprintf(&b, `<h3><a href="%s" rel="noopener noreferrer">%s</a></h3>`, html.EscapeString(q.U), html.EscapeString(q.T))
	if q.D != "" {
		d := q.D
		if r := []rune(d); len(r) > 260 {
			d = string(r[:260]) + "…"
		}
		fmt.Fprintf(&b, `<p>%s</p>`, html.EscapeString(d))
	}
	if len(q.Tags) > 0 {
		b.WriteString(`<div class=sqt>`)
		for _, t := range q.Tags {
			fmt.Fprintf(&b, `<span>%s</span>`, html.EscapeString(t))
		}
		b.WriteString(`</div>`)
	}
	ans := fmt.Sprintf("<b>%d</b> answers", q.Answers)
	if q.Answered {
		ans = `<span class=ok>✓ ` + ans + `</span>`
	}
	fmt.Fprintf(&b, `<div class=sqm><span><b>%d</b> votes</span><span>%s</span><span>%s</span></div>`, q.Score, ans, time.Unix(q.Created, 0).UTC().Format("2 Jan 2006"))
	by := html.EscapeString(q.Author)
	if q.ALink != "" && q.Author != "" {
		by = fmt.Sprintf(`<a href="%s" rel="noopener noreferrer">%s</a>`, html.EscapeString(q.ALink), by)
	}
	if by == "" {
		by = "a Stack Overflow user"
	}
	fmt.Fprintf(&b, `<div class=sqc>Asked by %s on <a href="%s" rel="noopener noreferrer">Stack Overflow</a> · <a href="https://creativecommons.org/licenses/by-sa/4.0/" rel="noopener noreferrer">CC BY-SA 4.0</a></div></article>`, by, html.EscapeString(q.U))
	return b.String()
}

func stackSearch(w http.ResponseWriter, r *http.Request, q string) {
	all := stackQuestions()
	by := map[string]stackQ{}
	items := make([]listItem, 0, len(all))
	for _, s := range all {
		if adultText(s.T) || adultText(s.D) {
			continue
		}
		items = append(items, listItem{s.U, s.T + " " + strings.Join(s.Tags, " "), s.D})
		by[s.U] = s
	}
	hits := matchItems(items, q)
	const per = 20
	pg, _ := strconv.Atoi(r.URL.Query().Get("p"))
	last := max(1, (len(hits)+per-1)/per)
	pg = min(max(pg, 1), last)
	var b strings.Builder
	b.WriteString(tabNav(q, "stack"))
	b.WriteString(stackCSS)
	if len(hits) == 0 {
		fmt.Fprintf(&b, `<div class=empty><h2>No Stack Overflow questions found for “%s”</h2><p>Flowsearch holds %s recent questions. Try fewer words.</p></div>`, html.EscapeString(q), commas(len(all)))
	} else {
		fmt.Fprintf(&b, `<p class=meta>%s questions from %s</p>`, commas(len(hits)), commas(len(all)))
		for _, h := range hits[min((pg-1)*per, len(hits)):min(pg*per, len(hits))] {
			b.WriteString(stackCard(by[h.U]))
		}
	}
	if last > 1 {
		link := func(n int, text string) string {
			return fmt.Sprintf(`<a class=pbtn href="/search?q=%s&tab=stack&p=%d">%s</a>`, url.QueryEscape(q), n, text)
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
	fmt.Fprint(w, page(q+" - Stack Overflow - Flowsearch", q, b.String()))
}
