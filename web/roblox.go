package main

// The Roblox tab: the games the Roblox crawler (cmd/rbxcrawl, a real browser) has read, in the search results (tab=roblox) and on the
// Live page. roblox-games.jsonl has one line per game: title, creator, genre, players online, visits, favorites, description, picture.

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var robloxFile = fsData + "/roblox-games.jsonl"

type rbxGame struct {
	U, T, D, Creator, Genre, Active, Visits, Favorites, Img string
}

var (
	rbxMu    sync.Mutex
	rbxMtime time.Time
	rbxData  []rbxGame
)

func robloxGames() []rbxGame {
	rbxMu.Lock()
	defer rbxMu.Unlock()
	st, err := os.Stat(robloxFile)
	if err != nil {
		return nil
	}
	if !st.ModTime().Equal(rbxMtime) {
		var out []rbxGame
		have := map[string]bool{}
		if f, err := os.Open(robloxFile); err == nil {
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for sc.Scan() {
				var raw map[string]string
				if json.Unmarshal(sc.Bytes(), &raw) != nil || raw["u"] == "" || have[raw["u"]] {
					continue
				}
				have[raw["u"]] = true
				out = append(out, rbxGame{raw["u"], raw["t"], raw["d"], raw["creator"], raw["genre"], raw["active"], raw["visits"], raw["favorites"], raw["img"]})
			}
			f.Close()
		}
		rbxData, rbxMtime = out, st.ModTime()
	}
	return rbxData
}

// rbxNum reads "13,211" or "472.5M+" as a number (for sorting).
func rbxNum(s string) float64 {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), ",", ""))
	s = strings.TrimSuffix(s, "+")
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1e3, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = 1e6, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "B"):
		mult, s = 1e9, strings.TrimSuffix(s, "B")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v * mult
}

const rbxCSS = `<style>
.rgrid{display:grid;grid-template-columns:repeat(auto-fill,minmax(210px,1fr));gap:16px;margin:12px 0 18px}
.rcard{display:flex;flex-direction:column;gap:6px;padding:10px;border:1px solid var(--line);border-radius:14px;background:var(--card);text-decoration:none;color:inherit;transition:transform .12s,box-shadow .12s}
.rcard:hover{transform:translateY(-2px);box-shadow:0 8px 20px rgba(0,0,0,.2)}
.rimg{aspect-ratio:1/1;border-radius:10px;overflow:hidden;background:#222;display:grid;place-items:center;color:#889;font-size:13px}
.rimg img{width:100%;height:100%;object-fit:cover}
.rtitle{font-size:15px;line-height:1.3;color:var(--link);display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
.rby{font-size:12.5px;color:var(--dim)}
.rstats{display:flex;gap:10px;flex-wrap:wrap;font-size:12.5px;color:var(--dim)}.rstats b{color:var(--ink,inherit)}
.rgenre{font-size:11px;padding:1px 8px;border-radius:99px;border:1px solid var(--line);align-self:flex-start;color:var(--dim)}
</style>`

// flowcdnKey is the file name (without .jxl) of a picture in flowcdn: a short hash of the picture's original address.
func flowcdnKey(src string) string {
	h := sha1.Sum([]byte(src))
	return hex.EncodeToString(h[:8])
}

// flowcdnRoblox hands out the Roblox pictures we keep as JPEG XL: /flowcdn/roblox/<hash>.jxl, cached by browsers for a year.
func flowcdnRoblox(w http.ResponseWriter, r *http.Request) {
	k := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/flowcdn/roblox/"), ".jxl")
	if len(k) != 16 || strings.Trim(k, "0123456789abcdef") != "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jxl")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, fsCDN + "/roblox/"+k+".jxl")
}

func rbxCard(g rbxGame) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<a class=rcard href="%s" rel="noopener noreferrer">`, html.EscapeString(g.U))
	if strings.HasPrefix(g.Img, "https://") {
		src := g.Img // Roblox's own CDN, until our copy has been made
		k := flowcdnKey(g.Img)
		if _, err := os.Stat(fsCDN + "/roblox/" + k + ".jxl"); err == nil {
			src = "/flowcdn/roblox/" + k + ".jxl"
		}
		fmt.Fprintf(&b, `<div class=rimg><img src="%s" alt="" loading=lazy referrerpolicy=no-referrer></div>`, html.EscapeString(src))
	} else {
		b.WriteString(`<div class=rimg>no picture</div>`)
	}
	fmt.Fprintf(&b, `<div class=rtitle>%s</div>`, html.EscapeString(g.T))
	if g.Creator != "" {
		fmt.Fprintf(&b, `<div class=rby>by %s</div>`, html.EscapeString(g.Creator))
	}
	var st []string
	if g.Active != "" {
		st = append(st, "<b>"+html.EscapeString(g.Active)+"</b> playing")
	}
	if g.Visits != "" {
		st = append(st, "<b>"+html.EscapeString(g.Visits)+"</b> visits")
	}
	if g.Favorites != "" {
		st = append(st, "♥ "+html.EscapeString(g.Favorites))
	}
	if len(st) > 0 {
		b.WriteString(`<div class=rstats>` + strings.Join(st, " · ") + `</div>`)
	}
	if g.Genre != "" {
		fmt.Fprintf(&b, `<span class=rgenre>%s</span>`, html.EscapeString(g.Genre))
	}
	b.WriteString(`</a>`)
	return b.String()
}

func robloxSearch(w http.ResponseWriter, r *http.Request, q string) {
	games := robloxGames()
	by := map[string]rbxGame{}
	items := make([]listItem, 0, len(games))
	for _, g := range games {
		if adultText(g.T) || adultText(g.D) || adultText(g.Genre) {
			continue
		}
		items = append(items, listItem{g.U, g.T, g.D + " " + g.Creator + " " + g.Genre})
		by[g.U] = g
	}
	hits := matchItems(items, q)
	const per = 24
	pg, _ := strconv.Atoi(r.URL.Query().Get("p"))
	last := max(1, (len(hits)+per-1)/per)
	pg = min(max(pg, 1), last)
	var b strings.Builder
	b.WriteString(tabNav(q, "roblox"))
	b.WriteString(rbxCSS)
	if len(hits) == 0 {
		fmt.Fprintf(&b, `<div class=empty><h2>No Roblox games found for “%s”</h2><p>Flowsearch holds %s Roblox games. Try fewer words.</p></div>`, html.EscapeString(q), commas(len(games)))
	} else {
		fmt.Fprintf(&b, `<p class=meta>%s Roblox games from %s</p><div class=rgrid>`, commas(len(hits)), commas(len(games)))
		for _, h := range hits[min((pg-1)*per, len(hits)):min(pg*per, len(hits))] {
			b.WriteString(rbxCard(by[h.U]))
		}
		b.WriteString(`</div>`)
	}
	if last > 1 {
		link := func(n int, text string) string {
			return fmt.Sprintf(`<a class=pbtn href="/search?q=%s&tab=roblox&p=%d">%s</a>`, url.QueryEscape(q), n, text)
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
	fmt.Fprint(w, page(q+" - Roblox - Flowsearch", q, b.String()))
}

// apiRoblox: for the Live page's Roblox tab: how many games, the newest 12 saved and the 12 with the most players right now.
func apiRoblox(w http.ResponseWriter, r *http.Request) {
	var games []rbxGame
	for _, g := range robloxGames() {
		if !adultText(g.T) && !adultText(g.D) && !adultText(g.Genre) {
			games = append(games, g)
		}
	}
	type item struct {
		U       string `json:"u"`
		T       string `json:"t"`
		Creator string `json:"creator"`
		Active  string `json:"active"`
		Visits  string `json:"visits"`
	}
	mk := func(g rbxGame) item { return item{g.U, clip(g.T, 80), clip(g.Creator, 40), g.Active, g.Visits} }
	recent := []item{}
	for i := len(games) - 1; i >= 0 && len(recent) < 12; i-- {
		recent = append(recent, mk(games[i]))
	}
	top := append([]rbxGame(nil), games...)
	sort.SliceStable(top, func(i, j int) bool { return rbxNum(top[i].Active) > rbxNum(top[j].Active) })
	best := []item{}
	for i := 0; i < len(top) && i < 12; i++ {
		best = append(best, mk(top[i]))
	}
	players := 0.0
	for _, g := range games {
		players += rbxNum(g.Active)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{"games": len(games), "players": int(players), "recent": recent, "top": best})
}
