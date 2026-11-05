package main

// "Ask the forums": a question in plain words goes through the Forums tab's search, and the page shows the best comment of the top
// threads as answers (the comment the decision model picked for the question), each with the thread it came from.

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
)

type askKey struct{}

type askCapture struct {
	hits []forumHit
	best map[string]int
}

func askPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	form := `<form class=askf action=/ask method=get><input class=fld name=q value="` + html.EscapeString(q) + `" placeholder="Ask real people: my Pi won't boot after an update" autocomplete=off aria-label="Question"><button class=btn type=submit>Ask</button></form>`
	css := `<style>.ask{max-width:780px}
.askf{display:flex;gap:10px;margin:0 0 24px;flex-wrap:wrap}.askf .fld{flex:1;min-width:240px}
.ans{padding:18px 20px;margin:0 0 14px}
.ans .who{display:flex;align-items:center;gap:7px;font-size:13.5px;color:var(--dim)}.ans .who b{color:var(--ink)}
.ans .txt{margin:10px 0 12px;font-size:16.5px;line-height:1.6;overflow-wrap:anywhere;white-space:pre-wrap}
.ans .src{display:block;border-top:1px solid rgba(255,255,255,.14);padding-top:10px;font-size:14px;color:var(--dim);text-decoration:none}.ans .src b{color:var(--link);font-weight:600}
.ans .src:hover b{text-decoration:underline}.ans .n{font-size:11.5px;font-weight:700;letter-spacing:.06em;text-transform:uppercase;color:var(--acc)}
</style>`
	if q == "" {
		fmt.Fprint(w, page("Ask real people - Flowsearch", "", css+`<main class="wrap ask">`+pageHead("Ask real people", "Write your question in plain words. Flowsearch finds forum discussions about it and shows the replies that help.")+form+`</main>`))
		return
	}
	if adultQuery(q) {
		fmt.Fprint(w, page(q+" - Flowsearch", q, adultQueryPage(q)))
		return
	}
	cap := &askCapture{}
	sub := httptest.NewRequest("GET", "/search?tab=forums&q="+url.QueryEscape(q), nil).WithContext(context.WithValue(r.Context(), askKey{}, cap))
	forumsSearch(httptest.NewRecorder(), sub, q)
	var b strings.Builder
	b.WriteString(css + `<main class="wrap ask">` + pageHead("Ask real people", "Replies from forums for “"+html.EscapeString(clip(q, 100))+"”") + form)
	shown := 0
	for _, h := range cap.hits {
		bi, ok := cap.best[h.p.U]
		if !ok || bi < 0 || bi >= len(h.p.Top) || shown >= 3 {
			continue
		}
		c := h.p.Top[bi]
		shown++
		ago := ""
		if a := forumAgo(h.p.Published); a != "" {
			ago = " · " + a
		}
		fmt.Fprintf(&b, `<article class="ans gc"><div class=n>Reply %d</div><div class=who>%s<b>%s</b> · ▲ %d</div><div class=txt>%s</div><a class=src href="%s" rel="noopener noreferrer">From the discussion <b>%s</b><br>%s · %s%s</a></article>`,
			shown, forumAvatarI(h.p, c.A), html.EscapeString(c.A), c.S, html.EscapeString(clip(c.T, 700)), html.EscapeString(h.p.U),
			html.EscapeString(clip(h.p.T, 120)), html.EscapeString(h.p.Community), html.EscapeString(h.p.Instance), ago)
	}
	if shown == 0 {
		b.WriteString(`<div class="empty gc"><h2>No reply answers this</h2><p>The discussions Flowsearch knows have nothing that answers it. Try other words, or look at the normal results.</p></div>`)
	}
	b.WriteString(`<p style="margin-top:18px"><a class="btn ghost" href="/search?tab=forums&q=` + url.QueryEscape(q) + `">All forum results (` + strconv.Itoa(len(cap.hits)) + `+)</a> <a class="btn ghost" href="/search?q=` + url.QueryEscape(q) + `">Search the web for this</a></p></main>`)
	fmt.Fprint(w, page(q+" - Ask real people - Flowsearch", "", b.String()))
}
