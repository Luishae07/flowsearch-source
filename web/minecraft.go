package main

// /minecraft: the Minecraft PvP videos Flowsearch holds, sorted by Jev into kinds (montage, tutorial, tier list, fight, other) by
// classify_minecraft.py; filter chips, a mace-only switch and two Surprise me buttons.

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

var minecraftFile = fsData + "/minecraft-videos.jsonl"

type mcVideo struct {
	U    string `json:"u"`
	T    string `json:"t"`
	D    string `json:"d"`
	Kind string `json:"kind"`
	Mace bool   `json:"mace"`
}

var (
	mcMu    sync.Mutex
	mcMtime time.Time
	mcData  []mcVideo
)

func minecraftVideos() []mcVideo {
	mcMu.Lock()
	defer mcMu.Unlock()
	st, err := os.Stat(minecraftFile)
	if err != nil {
		return nil
	}
	if !st.ModTime().Equal(mcMtime) {
		var out []mcVideo
		if f, err := os.Open(minecraftFile); err == nil {
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for sc.Scan() {
				var v mcVideo
				if json.Unmarshal(sc.Bytes(), &v) == nil && strings.HasPrefix(v.U, "https://www.youtube.com/watch?v=") {
					out = append(out, v)
				}
			}
			f.Close()
		}
		mcData, mcMtime = out, st.ModTime()
	}
	return mcData
}

// mcAsList turns videos into the list format of the Surprise me buttons.
func mcAsList(mace bool) []listItem {
	var out []listItem
	for _, v := range minecraftVideos() {
		if !mace || v.Mace {
			out = append(out, listItem{v.U, v.T, v.D})
		}
	}
	return out
}

var mcKinds = []struct{ id, label string }{{"montage", "Montages"}, {"tutorial", "Tutorials"}, {"tierlist", "Tier lists"}, {"comparison", "Comparisons"}, {"fight", "Fights"}, {"other", "Other"}}

func minecraftPage(w http.ResponseWriter, r *http.Request) {
	all := minecraftVideos()
	kind := r.URL.Query().Get("kind")
	mace := r.URL.Query().Get("mace") == "1"
	counts := map[string]int{}
	var list []mcVideo
	for _, v := range all {
		if mace && !v.Mace {
			continue
		}
		counts[v.Kind]++
		if kind == "" || v.Kind == kind {
			list = append(list, v)
		}
	}
	const per = 24
	pg, _ := strconv.Atoi(r.URL.Query().Get("p"))
	last := max(1, (len(list)+per-1)/per)
	pg = min(max(pg, 1), last)
	href := func(k string, m bool, p int) string {
		v := url.Values{}
		if k != "" {
			v.Set("kind", k)
		}
		if m {
			v.Set("mace", "1")
		}
		if p > 1 {
			v.Set("p", strconv.Itoa(p))
		}
		if len(v) == 0 {
			return "/minecraft"
		}
		return "/minecraft?" + v.Encode()
	}
	chip := func(label, k string, n int, on bool) string {
		cls := "chip"
		if on {
			cls += " on"
		}
		return fmt.Sprintf(`<a class="%s" href="%s">%s <small>%d</small></a> `, cls, html.EscapeString(href(k, mace, 1)), label, n)
	}
	nmace := 0
	for _, v := range all {
		if v.Mace {
			nmace++
		}
	}
	var b strings.Builder
	b.WriteString(`<style>.mcg{display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:16px;margin-top:18px}.mcv{display:block;border-radius:20px;overflow:hidden;color:var(--ink);text-decoration:none;border:1px solid rgba(255,255,255,.16);background:var(--card);box-shadow:0 14px 34px -26px rgba(0,0,0,.8);transition:transform .18s,border-color .18s}.mcv:hover{transform:translateY(-3px);border-color:var(--acc)}.mcv img{display:block;width:100%;height:135px;object-fit:cover;background:#8883}.mcv div{padding:11px 14px;font-size:14.5px;line-height:1.35}.mcv small{color:var(--dim)}.mcf{display:flex;flex-wrap:wrap;gap:8px;margin:20px 0 0}.pgr{display:flex;gap:12px;align-items:center;justify-content:center;margin-top:26px;color:var(--dim);flex-wrap:wrap}</style><main class="wrap" style="max-width:1100px">`)
	b.WriteString(pageHead("Minecraft PvP", fmt.Sprintf("%d videos about Minecraft fights between players, sorted by type. %d of them are about the mace.", len(all), nmace)))
	b.WriteString(`<p><a class=btn href="/surprise?site=youtube.com&kind=mace">Surprise me: mace PvP</a> <a class="btn ghost" href="/surprise?site=youtube.com&kind=minecraft">Surprise me: any PvP</a></p><div class=mcf>`)
	total := 0
	for _, c := range counts {
		total += c
	}
	b.WriteString(chip("All", "", total, kind == ""))
	for _, k := range mcKinds {
		b.WriteString(chip(k.label, k.id, counts[k.id], kind == k.id))
	}
	maceCls := "chip"
	if mace {
		maceCls += " on"
	}
	fmt.Fprintf(&b, `<a class="%s" href="%s">Mace only</a></div>`, maceCls, html.EscapeString(href(kind, !mace, 1)))
	b.WriteString(`<div class=mcg>`)
	label := map[string]string{}
	for _, k := range mcKinds {
		label[k.id] = k.label
	}
	for _, v := range list[min((pg-1)*per, len(list)):min(pg*per, len(list))] {
		thumb := "/flowcdn/yt/" + url.PathEscape(strings.TrimPrefix(v.U, "https://www.youtube.com/watch?v=")) + ".jxl"
		fmt.Fprintf(&b, `<a class=mcv href="%s" rel="noopener noreferrer"><img src="%s" alt="" loading=lazy width=240 height=135><div>%s<br><small>%s</small></div></a>`,
			html.EscapeString(v.U), html.EscapeString(thumb), html.EscapeString(clip(v.T, 90)), html.EscapeString(label[v.Kind]))
	}
	b.WriteString(`</div>`)
	if len(list) == 0 {
		b.WriteString(`<p class=psub>No videos here yet.</p>`)
	}
	if last > 1 {
		b.WriteString(`<div class=pgr>`)
		if pg > 1 {
			fmt.Fprintf(&b, `<a class=pbtn href="%s">&larr; Previous</a>`, html.EscapeString(href(kind, mace, pg-1)))
		}
		fmt.Fprintf(&b, `<span>Page %d of %d</span>`, pg, last)
		if pg < last {
			fmt.Fprintf(&b, `<a class=pbtn href="%s">Next &rarr;</a>`, html.EscapeString(href(kind, mace, pg+1)))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</main>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, page("Minecraft PvP - Flowsearch", "", b.String()))
}
