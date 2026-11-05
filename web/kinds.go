package main

// Result kind chips. Jev answers one pick-one question for each of the best 30 results of a search: what kind of page is it (tutorial,
// reference, news, forum, product, opinion, other)? The answer is kept per page address (forums-cache.jsonl), so a page is judged once.
// /api/kinds hands the labels to the results page, which tags each result and shows a row of chips with counts; a chip opens the same
// search with &kind=... so that only the pages of that kind are listed.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const kindWindow = 30

var kindCriteria = map[string]string{
	"tutorial":  "A how-to, guide, tutorial or walkthrough that teaches how to do something step by step",
	"reference": "Documentation, a manual, an encyclopedia entry, a specification or another reference page",
	"news":      "A news article, an announcement or a blog post about something recent",
	"forum":     "A forum thread, a question and answer page or a discussion between people",
	"product":   "A shop, a product page, a product review or a comparison for buying something",
	"opinion":   "An opinion piece, an essay or a personal blog post",
	"other":     "Anything else: a home page, a tool, an app, a list of links",
}

func kindParam(kind string) string {
	if _, ok := kindCriteria[kind]; ok && kind != "" {
		return "&kind=" + url.QueryEscape(kind)
	}
	return ""
}

// kindsOf labels the first kindWindow hits ("" where Jev could not say).
func kindsOf(hits []hit) []string {
	n := min(len(hits), kindWindow)
	out := make([]string, n)
	var todo []int
	for i := 0; i < n; i++ {
		var k string
		if fcGet("kind|"+hits[i].rec.URL, &k) && k != "" {
			out[i] = k
		} else {
			todo = append(todo, i)
		}
	}
	if len(todo) == 0 || jevKeyValue() == "" {
		return out
	}
	var st strings.Builder
	st.WriteString("Web pages from a search engine:\n")
	qs := map[string]any{}
	for j, i := range todo {
		r := hits[i].rec
		fmt.Fprintf(&st, "[%d] %s | %s | %s\n", j+1, clipRunes(r.Title, 100), clipRunes(r.URL, 80), clipRunes(r.Description, 150))
		qs[fmt.Sprintf("y%d", j+1)] = map[string]any{
			"type":         "choice",
			"instructions": fmt.Sprintf("What kind of page is [%d]?", j+1),
			"criteria":     kindCriteria,
		}
	}
	body, _ := json.Marshal(map[string]any{"model": jevModel, "state": st.String(), "questions": qs})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", jevURL, bytes.NewReader(body))
	if err != nil {
		return out
	}
	req.Header.Set("Authorization", "Bearer "+jevKeyValue())
	req.Header.Set("Content-Type", "application/json")
	resp, err := jevClient.Do(req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	var res struct {
		Answers map[string]struct {
			Choice string `json:"choice"`
		} `json:"answers"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&res) != nil {
		return out
	}
	for j, i := range todo {
		if k := res.Answers[fmt.Sprintf("y%d", j+1)].Choice; kindCriteria[k] != "" {
			out[i] = k
			fcPut("kind|"+hits[i].rec.URL, k)
		}
	}
	return out
}

// filterKind keeps the pages of one kind among the best kindWindow hits.
func filterKind(hits []hit, kind string) []hit {
	ks := kindsOf(hits)
	var out []hit
	for i, k := range ks {
		if k == kind {
			out = append(out, hits[i])
		}
	}
	return out
}

func apiKinds(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 200 || adultQuery(q) || strings.Contains(strings.ToLower(q), "site:") {
		fmt.Fprint(w, `{"counts":{},"items":[]}`)
		return
	}
	if limited(r) {
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":"too many requests"}`)
		return
	}
	_, hits, _, _, _ := search(q, isDE(r))
	ks := kindsOf(hits)
	type item struct {
		U string `json:"u"`
		K string `json:"k"`
	}
	counts := map[string]int{}
	items := []item{}
	for i, k := range ks {
		if k != "" {
			counts[k]++
			items = append(items, item{hits[i].rec.URL, k})
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"counts": counts, "items": items})
}

// kindJS is added to the results page: it asks /api/kinds, tags the results and shows the chips.
const kindJS = `<style>.kchips{display:flex;gap:8px;flex-wrap:wrap;margin:0 0 14px}.kchips a{padding:5px 13px;border:1px solid var(--line);border-radius:99px;font-size:13.5px;color:var(--dim);text-decoration:none;background:var(--card)}.kchips a:hover{color:var(--link)}.kchips a.on{background:var(--acc);color:var(--acc-ink);border-color:var(--acc);font-weight:600}.ktag{margin-left:8px;font-size:11px;font-weight:600;padding:1px 8px;border-radius:99px;background:rgba(128,128,128,.18);color:var(--dim)}</style><script>(function(){
  var lay=document.querySelector('.layout');if(!lay)return;
  var q=lay.dataset.q,cur=new URLSearchParams(location.search).get('kind')||'';
  if(!q||/site:/i.test(q))return;
  fetch('/api/kinds?q='+encodeURIComponent(q)).then(function(r){return r.json()}).then(function(d){
    var names={tutorial:'Tutorials',reference:'Reference',news:'News',forum:'Forums',product:'Products',opinion:'Opinion',other:'Other'};
    var map={};(d.items||[]).forEach(function(i){map[i.u]=i.k});
    document.querySelectorAll('article.r').forEach(function(a){
      var t=a.querySelector('a.t'),k=t&&map[t.getAttribute('href')];
      if(k&&k!=='other'){var s=document.createElement('span');s.className='ktag';s.textContent=names[k]||k;var site=a.querySelector('.site');if(site)site.appendChild(s)}
    });
    var keys=Object.keys(d.counts||{}).filter(function(k){return d.counts[k]>0});
    if(keys.length<2&&!cur)return;
    var nav=document.createElement('nav');nav.className='kchips';nav.setAttribute('aria-label','Kind of page');
    function chip(label,k,n){var a=document.createElement('a');a.href='/search?q='+encodeURIComponent(q)+(k?'&kind='+k:'');a.textContent=label+(n?' '+n:'');if(k===cur)a.className='on';nav.appendChild(a)}
    chip('All','',0);
    keys.sort(function(a,b){return d.counts[b]-d.counts[a]}).forEach(function(k){chip(names[k]||k,k,d.counts[k])});
    var meta=document.querySelector('p.meta');
    if(meta)meta.insertAdjacentElement('afterend',nav);
  }).catch(function(){});
})();</script>`
