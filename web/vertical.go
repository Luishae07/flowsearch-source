package main

// Jev reads every search and decides what else it wants besides web pages: products, forum posts, Roblox games or videos. The web
// results always come; when Jev is sure, a short block of the best three from that tab sits above them (like Google's
// shopping and discussion boxes). There is no switch for it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"
)

type verticalGuess struct {
	kind string
	at   time.Time
}

var (
	vertMu    sync.Mutex
	vertCache = map[string]verticalGuess{}
)

// jevVertical returns "shopping", "forums", "videos" or "" (web results only).
func jevVertical(q string) string {
	key := jevKeyValue()
	if key == "" {
		return ""
	}
	lq := strings.ToLower(strings.TrimSpace(q))
	vertMu.Lock()
	if g, ok := vertCache[lq]; ok && time.Since(g.at) < 6*time.Hour {
		vertMu.Unlock()
		return g.kind
	}
	vertMu.Unlock()
	body, _ := json.Marshal(map[string]any{"model": jevModel, "state": "Search query: " + clipRunes(q, 200), "questions": map[string]any{
		"kind": map[string]any{
			"type":         "choice",
			"instructions": "A search engine shows web results for this query. Which extra box would help the person most above them?",
			"criteria": map[string]string{
				"web":      "None: they want facts, news, an explanation, a website or something else that web pages answer",
				"shopping": "Products: they want to buy something or compare prices, models or gear",
				"forums":   "Forum posts: a problem, a how-to, a bug, an opinion or experience that people discuss and answer in communities",
				"roblox":   "Roblox games: they look for a Roblox game, an obby, tycoon, simulator or something to play on Roblox",
				"videos":   "Videos: a tutorial, a montage, a gameplay or music video, something to watch",
			},
		},
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", jevURL, bytes.NewReader(body))
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := jevClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var out struct {
		Answers map[string]struct {
			Probabilities map[string]float64 `json:"probabilities"`
			Choice        string             `json:"choice"`
		} `json:"answers"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil {
		return ""
	}
	a := out.Answers["kind"]
	kind := ""
	if p := a.Probabilities[a.Choice]; (a.Choice == "shopping" || a.Choice == "forums" || a.Choice == "videos" || a.Choice == "roblox") && (p == 0 || p >= 0.6) {
		kind = a.Choice
	}
	vertMu.Lock()
	if len(vertCache) > 2000 {
		vertCache = map[string]verticalGuess{}
	}
	vertCache[lq] = verticalGuess{kind, time.Now()}
	vertMu.Unlock()
	return kind
}

const vblockCSS = `<style>
.vblock{margin:0 0 16px;padding:10px 12px;border:1px solid var(--line,#8884);border-radius:14px;background:var(--card,transparent)}
.vblock h3{margin:0 0 8px;font-size:13px;font-weight:600;letter-spacing:.01em}
.vblock h3 small{font-size:11px}
.vblock .pgrid,.vblock .rgrid{display:flex;gap:10px;overflow-x:auto;margin:0;padding:2px 2px 6px;scrollbar-width:thin}
.vblock .pcard,.vblock .rcard{flex:0 0 128px;width:128px;padding:6px;gap:3px;border-radius:10px;font-size:12px;box-shadow:none;min-width:0}
.vblock .pcard .pimg,.vblock .rcard img{height:78px;width:100%;object-fit:contain;border-radius:7px}
.vblock .pcard .pimg img{max-height:78px;max-width:100%;object-fit:contain}
.vblock .pcard a,.vblock .rcard{line-height:1.25}
.vblock .pcard .ptitle,.vblock .rcard b,.vblock .rcard .rt{display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;font-size:12px}
.vblock .pprice{font-size:13px}
.vblock .prate{font-size:11px}
.vblock .pcard button,.vblock .pcard .pupd,.vblock .pcard .pmeta,.vblock .pcard .pinch{display:none}
.vblock .fcard{padding:7px 10px;margin:0 0 6px;border-radius:9px;font-size:12px}
.vblock .fcard .ftitle{font-size:13.5px}
.vblock .fcard .fhead{font-size:11px;margin-bottom:2px}
.vblock .fcard .fbest,.vblock .fcard details,.vblock .fcard .fbody,.vblock .fcard p{display:none}
.vblock .fcard .fstats{font-size:11px;margin-top:3px}
.vblock article.r{display:flex;gap:10px;align-items:center;margin:0 0 6px;padding:0}
.vblock article.r .shot{width:80px;height:50px;object-fit:cover;border-radius:6px;flex:none}
.vblock article.r .site,.vblock article.r p{display:none}
.vblock article.r .t{font-size:13.5px}
.vblock>p{margin:4px 0 0;font-size:12px}
</style>`

// verticalBlock renders the first cards of a tab for q by running the tab's own search and cutting its first cards out.
func verticalBlock(r *http.Request, q, kind string) string {
	u := "/search?q=" + url.QueryEscape(q) + "&tab=" + kind
	sub := httptest.NewRequest("GET", u, nil)
	for _, c := range r.Cookies() {
		sub.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	switch kind {
	case "forums":
		forumsSearch(rec, sub, q)
	case "roblox":
		robloxSearch(rec, sub, q)
	default:
		tabSearch(rec, sub, q, kind)
	}
	pg := rec.Body.String()
	var cards []string
	var css, label, wrapOpen, wrapClose string
	switch kind {
	case "forums", "videos":
		marker := "<article class=fcard"
		if kind == "videos" {
			marker = "<article class=r>"
		}
		rest := pg
		for len(cards) < 3 {
			i := strings.Index(rest, marker)
			if i < 0 {
				break
			}
			j := strings.Index(rest[i:], "</article>")
			if j < 0 {
				break
			}
			cards = append(cards, rest[i:i+j+len("</article>")])
			rest = rest[i+j:]
		}
		css, label = "", "Forum posts"
		if kind == "forums" {
			css = forumCSS
		} else {
			label = "Videos"
		}
	case "roblox":
		rest := pg
		for len(cards) < 4 {
			i := strings.Index(rest, "<a class=rcard")
			if i < 0 {
				break
			}
			j := strings.Index(rest[i:], "</a>")
			if j < 0 {
				break
			}
			cards = append(cards, rest[i:i+j+4])
			rest = rest[i+j:]
		}
		css, label = rbxCSS, "Roblox games"
		wrapOpen, wrapClose = "<div class=rgrid>", "</div>"
	case "shopping":
		const m = "<div class=pcard "
		var starts []int
		for off := 0; ; {
			i := strings.Index(pg[off:], m)
			if i < 0 {
				break
			}
			starts = append(starts, off+i)
			off += i + len(m)
		}
		// a card ends where the next one starts; the last card of a grid is not cut (its end is shared with the grid's)
		for k := 0; k+1 < len(starts) && len(cards) < 4; k++ {
			cards = append(cards, pg[starts[k]:starts[k+1]])
		}
		css, label = shopCSS, "Products"
		wrapOpen, wrapClose = "<div class=pgrid>", "</div>"
	}
	if len(cards) == 0 {
		return ""
	}
	more := "/search?q=" + url.QueryEscape(q) + "&tab=" + kind
	return css + vblockCSS + `<section class=vblock><h3>` +
		label + ` for “` + html.EscapeString(clip(q, 50)) + `” <small style="font-weight:400;opacity:.6">· suggested for this search</small></h3>` +
		wrapOpen + strings.Join(cards, "") + wrapClose +
		fmt.Sprintf(`<p style="margin:8px 0 0"><a href="%s">More %s →</a></p></section>`, html.EscapeString(more), strings.ToLower(label))
}
