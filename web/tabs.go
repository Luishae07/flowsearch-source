package main

// The Videos and Shopping tabs of a search: Videos looks through the YouTube videos Flowsearch holds, Shopping through the
// amazon.de product pages. Both search the lists of the Surprise me buttons (a normal search cannot list them: its site filter
// works on the best matches for the words, and other sites crowd these pages out).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
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
)

// tabNav is the row of tabs under the search box; tab is "", "videos" or "shopping".
func tabNav(q, tab string) string {
	one := func(label, t string) string {
		href := "/search?q=" + url.QueryEscape(q)
		if t != "" {
			href += "&tab=" + t
		}
		cls := ""
		if t == tab {
			cls = ` class=on aria-current=true`
		}
		return `<a` + cls + ` href="` + href + `">` + label + `</a>`
	}
	return `<nav class="seg2 stabs" style="margin:0 0 16px" aria-label="Kind of results">` + one("All", "") + one("Videos", "videos") + one("Shopping", "shopping") + one("Roblox", "roblox") + one("Forums", "forums") + one("Stack Overflow", "stack") + `</nav>`
}

// amzItem is a product with what the Amazon crawl (cmd/amzdata) read from its page.
type amzItem struct {
	U, T, D, Price, Rating, Reviews, Brand, Img, Site string
}

var amazonDataFile = fsData + "/amazon-products-data.jsonl"

var (
	amzMu    sync.Mutex
	amzMtime time.Time
	amzData  []amzItem
)

// amazonProducts are the crawled products with price and more, then the plain list's products that the crawl does not have.
func amazonProducts() []amzItem {
	amzMu.Lock()
	defer amzMu.Unlock()
	st, err := os.Stat(amazonDataFile)
	if err == nil && !st.ModTime().Equal(amzMtime) {
		var out []amzItem
		have := map[string]int{} // a later line for the same product (a price update) replaces the earlier one
		if f, err := os.Open(amazonDataFile); err == nil {
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for sc.Scan() {
				var v amzItem
				var raw map[string]string
				if json.Unmarshal(sc.Bytes(), &raw) != nil || raw["u"] == "" {
					continue
				}
				v = amzItem{raw["u"], raw["t"], raw["d"], raw["price"], raw["rating"], raw["reviews"], raw["brand"], raw["img"], raw["site"]}
				if i, ok := have[v.U]; ok {
					out[i] = v
					continue
				}
				have[v.U] = len(out)
				out = append(out, v)
			}
			f.Close()
		}
		amzData, amzMtime = out, st.ModTime()
	}
	out := append([]amzItem(nil), amzData...)
	have := map[string]bool{}
	for _, v := range out {
		have[v.U] = true
	}
	_ = have // only products with a price are shown: the old plain list (no prices) is no longer mixed in
	kept := out[:0]
	for _, v := range out {
		if strings.TrimSpace(v.Price) != "" {
			kept = append(kept, v)
		}
	}
	return kept
}

// amazonStore is the visitor's Amazon store for the Shopping tab: "com", "de" or "both". ?store= changes it and is remembered in
// a cookie in their own browser; without either, the German site gets .de and every other visitor .com.
func amazonStore(w http.ResponseWriter, r *http.Request) string {
	valid := func(v string) bool { return v == "com" || v == "de" || v == "both" }
	if v := r.URL.Query().Get("store"); valid(v) {
		http.SetCookie(w, &http.Cookie{Name: "amazon_store", Value: v, Path: "/", MaxAge: 365 * 86400, SameSite: http.SameSiteLaxMode})
		return v
	}
	if c, err := r.Cookie("amazon_store"); err == nil && valid(c.Value) {
		return c.Value
	}
	if isDE(r) {
		return "de"
	}
	return "com"
}

// storeSwitch is the bar above the shopping results: how many products on the left, a segmented control for the store on the right.
func storeSwitch(q, store, count string) string {
	one := func(label, v string) string {
		cls := ""
		if v == store {
			cls = ` class=on aria-current=true`
		}
		return `<a` + cls + ` href="/search?q=` + url.QueryEscape(q) + `&tab=shopping&store=` + v + `">` + label + `</a>`
	}
	return `<div class=shoptools><span class=meta style="margin:0">` + count + `</span><span class=seg role=group aria-label="Amazon store">` +
		one("🇺🇸 amazon.com", "com") + one("🇩🇪 amazon.de", "de") + one("Both", "both") + `</span></div>`
}

// dvdSetting: the bouncing "pages indexed" banner of the home page is off unless the visitor turned it on in Settings (cookie dvd=on).
func dvdSetting(w http.ResponseWriter, r *http.Request) bool {
	if v := r.URL.Query().Get("dvd"); v == "on" || v == "off" {
		http.SetCookie(w, &http.Cookie{Name: "dvd", Value: v, Path: "/", MaxAge: 365 * 86400, SameSite: http.SameSiteLaxMode})
		return v == "on"
	}
	c, err := r.Cookie("dvd")
	return err == nil && c.Value == "on"
}

// wordSet is the set of words in a text, lower case.
func wordSet(text string) map[string]bool {
	m := map[string]bool{}
	for _, w := range tabTerms(text) {
		m[w] = true
	}
	return m
}

func tabTerms(q string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if len([]rune(w)) >= 2 {
			out = append(out, w)
		}
	}
	return out
}

func tabSearch(w http.ResponseWriter, r *http.Request, q, tab string) {
	var items []listItem
	store := ""
	var amz = map[string]amzItem{} // the product behind an address, for its price and rating
	label, host := "videos", "www.youtube.com"
	if tab == "shopping" {
		label, host = "products", "www.amazon"
		store = amazonStore(w, r)
		for _, a := range amazonProducts() {
			if (store == "com" && a.Site != "amazon.com") || (store == "de" && a.Site != "amazon.de") {
				continue
			}
			items = append(items, listItem{a.U, a.T, a.D + " " + a.Brand})
			amz[a.U] = a
		}
	} else {
		items = youtubeVideos()
	}
	kept := items[:0:0]
	for _, it := range items {
		if !adultText(it.T) && !adultText(it.D) {
			kept = append(kept, it)
		}
	}
	items = kept
	terms := tabTerms(q)
	type hit struct {
		it    listItem
		score int
	}
	var hits []hit
	phrase := strings.Join(terms, " ")
	for _, it := range items {
		titleWords, restWords := wordSet(it.T), wordSet(it.D)
		score, all := 0, true
		for _, t := range terms {
			switch {
			case titleWords[t] || titleWords[t+"s"]:
				score += 3
			case restWords[t] || restWords[t+"s"]:
				score++
			default:
				all = false // whole words only: "mac" must not match "machine"
			}
		}
		if len(terms) > 1 && strings.Contains(" "+strings.Join(tabTerms(it.T), " ")+" ", " "+phrase+" ") {
			score += 6 // the words together, in order, in the title: "mac mini"
		}
		if score > 0 && (all || len(terms) > 3) { // every word must appear, unless the search is long
			hits = append(hits, hit{it, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })

	per := 10
	if tab == "shopping" {
		per = 24 // cards in a grid
	}
	pg, _ := strconv.Atoi(r.URL.Query().Get("p"))
	last := max(1, (len(hits)+per-1)/per)
	pg = min(max(pg, 1), last)
	var b strings.Builder
	b.WriteString(tabNav(q, tab))
	if tab == "shopping" {
		count := fmt.Sprintf("%s %s", commas(len(hits)), label)
		b.WriteString(shopCSS + storeSwitch(q, store, count))
	}
	if len(hits) == 0 {
		fmt.Fprintf(&b, `<div class=empty><h2>No %s found for “%s”</h2><p>Flowsearch holds %s %s. Try fewer words.</p></div>`, label, html.EscapeString(q), commas(len(items)), label)
	} else if tab != "shopping" {
		fmt.Fprintf(&b, `<p class=meta>%s %s from %s</p>`, commas(len(hits)), label, commas(len(items)))
	}
	if tab == "shopping" {
		b.WriteString(`<div class=pgrid>`)
	}
	for _, h := range hits[min((pg-1)*per, len(hits)):min(pg*per, len(hits))] {
		if tab == "shopping" {
			b.WriteString(shopCard(h.it, amz[h.it.U]))
			continue
		}
		thumb := ""
		if tab != "shopping" {
			if i := strings.Index(h.it.U, "watch?v="); i >= 0 {
				thumb = `<img class=shot src="/flowcdn/yt/` + url.PathEscape(h.it.U[i+8:]) + `.jxl" alt="" loading=lazy width=160 height=100>`
			}
		}
		site, shown, extra := strings.TrimPrefix(host, "www."), h.it.D, ""
		if a, ok := amz[h.it.U]; ok {
			if u, err := url.Parse(a.U); err == nil {
				host, site = u.Host, strings.TrimPrefix(u.Host, "www.")
			}
			shown = a.D
			if a.Img != "" && strings.HasPrefix(a.Img, "https://") {
				thumb = `<img class=shot src="` + html.EscapeString(amazonPicture(a.Img)) + `" alt="" loading=lazy width=100 height=100 referrerpolicy=no-referrer style="object-fit:contain;width:100px;height:100px;background:#fff">`
			}
			var facts []string
			if a.Price != "" {
				facts = append(facts, "<b>"+html.EscapeString(a.Price)+"</b>")
			}
			if a.Rating != "" {
				r := a.Rating
				if a.Reviews != "" {
					r += " (" + a.Reviews + ")"
				}
				facts = append(facts, html.EscapeString(r))
			}
			if a.Brand != "" {
				facts = append(facts, html.EscapeString(clip(a.Brand, 60)))
			}
			if len(facts) > 0 {
				extra = `<p class=s>` + strings.Join(facts, " · ") + `</p>`
			}
		}
		crumb := strings.TrimPrefix(strings.TrimPrefix(h.it.U, "https://"), host)
		fmt.Fprintf(&b, `<article class=r>%s<div class=site>%s<b>%s</b><i>%s</i></div><a class=t href="%s" rel="noopener noreferrer">%s</a>%s<p class=s>%s</p></article>`,
			thumb, avatar(host), html.EscapeString(site), html.EscapeString(clip(crumb, 70)),
			html.EscapeString(h.it.U), html.EscapeString(h.it.T), extra, html.EscapeString(clip(shown, 220)))
	}
	if tab == "shopping" {
		b.WriteString(`</div>` + shopJS)
	}
	if last > 1 {
		link := func(n int, text string) string {
			return fmt.Sprintf(`<a class=pbtn href="/search?q=%s&tab=%s&p=%d">%s</a>`, url.QueryEscape(q), tab, n, text)
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
	fmt.Fprint(w, page(q+" - "+label+" - Flowsearch", q, b.String()))
}

const shopCSS = `<style>
.pgrid{display:grid;grid-template-columns:repeat(auto-fill,minmax(190px,1fr));gap:16px;margin:10px 0 18px}
.pcard{display:flex;flex-direction:column;gap:7px;padding:12px;border:1px solid var(--line);border-radius:14px;background:var(--card);text-decoration:none;color:inherit;transition:transform .12s,box-shadow .12s}
.pcard:hover{transform:translateY(-2px);box-shadow:0 8px 20px rgba(0,0,0,.2)}
.pmain{display:flex;flex-direction:column;gap:7px;text-decoration:none;color:inherit;flex:1}
.pact{display:flex;align-items:center;gap:8px;flex-wrap:wrap;min-height:26px}
.pupd{font:inherit;font-size:12px;cursor:pointer;border:1px solid var(--line);background:none;color:var(--dim);border-radius:99px;padding:3px 11px}
.pupd:hover:not(:disabled){color:var(--link);border-color:var(--link)}
.pupd:disabled{opacity:.6;cursor:default}
.pstate{font-size:12px;color:var(--dim)}
.pstate.ok{color:#3aa76d}.pstate.bad{color:#d9534f}
.pimg{height:150px;display:grid;place-items:center;background:#fff;border-radius:10px;overflow:hidden;color:#9aa;font-size:13px}
.pimg img{max-width:100%;max-height:150px;object-fit:contain}
.pprice{font-size:21px;font-weight:700;line-height:1.1}
.pprice.none{font-size:13px;font-weight:400;color:var(--dim)}
.prate{font-size:13px;color:var(--dim)}.prate b{color:#f5a623;font-weight:700}
.pbrand{font-size:11.5px;color:var(--dim);text-transform:uppercase;letter-spacing:.05em;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.ptitle{font-size:15px;line-height:1.3;color:var(--link);display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;overflow:hidden}
.shoptools{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap;margin:4px 0 14px}
.seg{display:inline-flex;padding:3px;border:1px solid var(--line);border-radius:999px;background:var(--card)}
.seg a{padding:6px 15px;border-radius:999px;font-size:14px;color:var(--dim);text-decoration:none;white-space:nowrap;transition:background .12s,color .12s}
.seg a:hover{color:var(--link)}
.seg a.on{background:var(--acc);color:var(--acc-ink);font-weight:600}
.psize{font-size:12.5px;font-weight:600;color:var(--acc);}
.psite{font-size:11px;padding:1px 8px;border-radius:99px;border:1px solid var(--line);align-self:flex-start;color:var(--dim);margin-top:auto}
</style>`

var (
	laptopWord   = regexp.MustCompile(`(?i)laptop|notebook|netbook|ultrabook|macbook|chromebook|googlebook|pixelbook|galaxy book|surface laptop|surface book|thinkpad|thinkbook|ideapad|yoga slim|legion (?:pro|slim|5|7)|latitude|inspiron|xps \d|alienware (?:m|x)\d|elitebook|probook|zbook|omnibook|spectre x360|envy x360|pavilion (?:x360|aero|plus)|zenbook|vivobook|rog (?:zephyrus|strix scar|flow)|tuf gaming [afk]\d|swift (?:go|edge|x|lite)|aspire (?:\d|go|vero)|predator (?:helios|triton)|razer blade|lg gram|matebook|magicbook|honor magicbook|msi (?:stealth|katana|raider|cyborg|thin|titan)`)
	screenInches = regexp.MustCompile(`(?i)\b(\d{1,2}(?:[.,]\d{1,2})?)\s?-?\s?(?:inch(?:es)?|zoll|["”″])`)
)

// laptopInches reads the screen size out of a laptop's name ("MacBook Air 13.6-inch", "Notebook 15,6 Zoll", 14.2"). It is empty
// when the name is not a laptop's or gives no size between 10 and 18.5 inches.
func laptopInches(title string) string {
	if !laptopWord.MatchString(title) || laptopAccessory.MatchString(title) {
		return ""
	}
	for _, m := range screenInches.FindAllStringSubmatch(title, -1) {
		n, err := strconv.ParseFloat(strings.Replace(m[1], ",", ".", 1), 64)
		if err == nil && n >= 10 && n <= 18.5 {
			return strconv.FormatFloat(n, 'f', -1, 64)
		}
	}
	return ""
}

// laptopAccessory marks chargers, cases, screen protectors and the like: their names mention a laptop and its size, but are not laptops.
var laptopAccessory = regexp.MustCompile(`(?i)\b(charger|adapter|adaptor|case|sleeve|cover|protector|privacy|skin|stand|bag|backpack|dock|docking|hub|cable|keyboard|mouse|sticker|decal|battery|screen filter|cooling|riser|tray|desk)\b|\b(for|compatible with|fits|replacement for)\b[^,|-]{0,25}(laptop|macbook|notebook|chromebook)`)

var ratingNum = regexp.MustCompile(`[0-9]+[.,][0-9]`)

// shopCard is one product as a card: image, price, rating, brand, title and the site it is on.
func shopCard(it listItem, a amzItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<div class=pcard data-u="%s"><a class=pmain href="%s" rel="noopener noreferrer">`, html.EscapeString(it.U), html.EscapeString(it.U))
	if strings.HasPrefix(a.Img, "https://") {
		fmt.Fprintf(&b, `<div class=pimg><img src="%s" alt="" loading=lazy referrerpolicy=no-referrer></div>`, html.EscapeString(amazonPicture(a.Img)))
	} else {
		b.WriteString(`<div class=pimg>no image</div>`)
	}
	if a.Price != "" && priceLooksWrong(it.T, a.Price) {
		b.WriteString(`<div class="pprice none">price unclear · update it</div>`)
	} else if a.Price != "" {
		fmt.Fprintf(&b, `<div class=pprice>%s</div>`, html.EscapeString(a.Price))
	} else {
		b.WriteString(`<div class="pprice none">price on Amazon</div>`)
	}
	if n := ratingNum.FindString(a.Rating); n != "" {
		reviews := strings.Trim(strings.TrimSpace(a.Reviews), "()")
		if reviews != "" {
			reviews = " · " + reviews
		}
		fmt.Fprintf(&b, `<div class=prate><b>★ %s</b>%s</div>`, html.EscapeString(n), html.EscapeString(reviews))
	}
	if in := laptopInches(it.T); in != "" {
		fmt.Fprintf(&b, `<div class=psize>💻 %s-inch screen</div>`, html.EscapeString(in))
	}
	if a.Brand != "" {
		fmt.Fprintf(&b, `<div class=pbrand>%s</div>`, html.EscapeString(clip(a.Brand, 40)))
	}
	fmt.Fprintf(&b, `<div class=ptitle>%s</div>`, html.EscapeString(it.T))
	site := a.Site
	if site == "" {
		if u, err := url.Parse(it.U); err == nil {
			site = strings.TrimPrefix(u.Host, "www.")
		}
	}
	fmt.Fprintf(&b, `<span class=psite>%s</span></a><div class=pact><button class=pupd type=button title="Read the current price from Amazon">↻ Update price</button><span class=pstate></span></div></div>`, html.EscapeString(site))
	return b.String()
}

// apiAmazon: how many Amazon products Flowsearch holds (per store) and the 12 saved last, for the Live page's Amazon tab.
func apiAmazon(w http.ResponseWriter, r *http.Request) {
	all := amazonProducts()
	com, de := 0, 0
	for _, a := range all {
		if a.Site == "amazon.com" {
			com++
		} else {
			de++
		}
	}
	type item struct {
		U     string `json:"u"`
		T     string `json:"t"`
		Price string `json:"price"`
		Site  string `json:"site"`
	}
	recent := []item{}
	amzMu.Lock() // the crawl appends to the file, so the data list ends with the newest
	data := amzData
	amzMu.Unlock()
	for i := len(data) - 1; i >= 0 && len(recent) < 12; i-- {
		if adultText(data[i].T) || adultText(data[i].D) {
			continue
		}
		recent = append(recent, item{data[i].U, clip(data[i].T, 100), data[i].Price, data[i].Site})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	priced := 0
	amzMu.Lock()
	for _, a := range amzData {
		if a.Price != "" {
			priced++
		}
	}
	amzMu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{"total": len(all), "com": com, "de": de, "priced": priced, "recent": recent})
}

// matchItems ranks a list of pages for a search the way the Videos and Shopping tabs do: whole words only, every word must
// appear (unless the search is long), words in the title count more, the words together in the title count most.
func matchItems(items []listItem, q string) []listItem {
	terms := tabTerms(q)
	phrase := strings.Join(terms, " ")
	type scored struct {
		it    listItem
		score int
	}
	var hits []scored
	for _, it := range items {
		titleWords, restWords := wordSet(it.T), wordSet(it.D)
		score, all := 0, true
		for _, t := range terms {
			switch {
			case titleWords[t] || titleWords[t+"s"]:
				score += 3
			case restWords[t] || restWords[t+"s"]:
				score++
			default:
				all = false
			}
		}
		if len(terms) > 1 && strings.Contains(" "+strings.Join(tabTerms(it.T), " ")+" ", " "+phrase+" ") {
			score += 6
		}
		if score > 0 && (all || len(terms) > 3) {
			hits = append(hits, scored{it, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]listItem, len(hits))
	for i, h := range hits {
		out[i] = h.it
	}
	return out
}

// apiShopping: GET /api/shopping?q=mac+mini&store=com|de|both&page=1  - products with price, rating and brand as JSON (10 a page).
func apiShopping(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	store := r.URL.Query().Get("store")
	if store != "com" && store != "de" {
		store = "both"
	}
	var items []listItem
	by := map[string]amzItem{}
	for _, a := range amazonProducts() {
		if (store == "com" && a.Site != "amazon.com") || (store == "de" && a.Site != "amazon.de") {
			continue
		}
		items = append(items, listItem{a.U, a.T, a.D + " " + a.Brand})
		by[a.U] = a
	}
	hits := matchItems(items, q)
	pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pg = max(pg, 1)
	from, to := min((pg-1)*10, len(hits)), min(pg*10, len(hits))
	type item struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Price   string `json:"price,omitempty"`
		Rating  string `json:"rating,omitempty"`
		Reviews string `json:"reviews,omitempty"`
		Brand   string `json:"brand,omitempty"`
		Store   string `json:"store"`
		Inches  string `json:"laptop_screen_inches,omitempty"`
		Image   string `json:"image,omitempty"`
	}
	res := []item{}
	for _, h := range hits[from:to] {
		a := by[h.U]
		res = append(res, item{Title: a.T, URL: a.U, Price: a.Price, Rating: a.Rating, Reviews: a.Reviews, Brand: a.Brand, Store: a.Site, Inches: laptopInches(a.T), Image: a.Img})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]any{"query": q, "store": store, "total": len(hits), "page": pg, "results": res})
}

// apiVideos: GET /api/videos?q=minecraft+pvp&page=1 - YouTube videos Flowsearch holds, as JSON (10 a page).
func apiVideos(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	hits := matchItems(youtubeVideos(), q)
	pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pg = max(pg, 1)
	from, to := min((pg-1)*10, len(hits)), min(pg*10, len(hits))
	type item struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Description string `json:"description,omitempty"`
		Thumbnail   string `json:"thumbnail,omitempty"`
	}
	res := []item{}
	for _, h := range hits[from:to] {
		thumb := ""
		if i := strings.Index(h.U, "watch?v="); i >= 0 {
			thumb = "https://i.ytimg.com/vi/" + h.U[i+8:] + "/mqdefault.jpg"
		}
		res = append(res, item{h.T, h.U, clip(h.D, 300), thumb})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]any{"query": q, "total": len(hits), "page": pg, "results": res})
}

var toyWord = regexp.MustCompile(`(?i)lern|kinder|kids|child|toy|spielzeug|learning|vtech|leapfrog`)

// priceValue reads the number of a price text ("1.299,00 €", "$1,299.00", "7,86 €").
func priceValue(p string) float64 {
	m := regexp.MustCompile(`[0-9][0-9.,]*`).FindString(p)
	if m == "" {
		return -1
	}
	if i, j := strings.LastIndex(m, ","), strings.LastIndex(m, "."); i >= 0 && j >= 0 {
		if i > j {
			m = strings.ReplaceAll(strings.ReplaceAll(m, ".", ""), ",", ".")
		} else {
			m = strings.ReplaceAll(m, ",", "")
		}
	} else if i >= 0 {
		if len(m)-i-1 <= 2 {
			m = strings.Replace(m, ",", ".", 1)
		} else {
			m = strings.ReplaceAll(m, ",", "")
		}
	}
	v, err := strconv.ParseFloat(strings.TrimRight(m, ".,"), 64)
	if err != nil {
		return -1
	}
	return v
}

// priceLooksWrong catches a price that cannot be right: a real laptop (not a charger, a case or a toy) under 60, or a price of 0.
func priceLooksWrong(title, price string) bool {
	v := priceValue(price)
	if v == 0 {
		return true
	}
	return v > 0 && v < 60 && laptopWord.MatchString(title) && !laptopAccessory.MatchString(title) && !toyWord.MatchString(title)
}
