// amzdata crawls amazon.com or amazon.de product pages (/dp/) in a real browser (Browser Use), because Amazon answers a plain request from
// this server with its block page. It opens the Bestseller pages, collects product links, then visits them one after another
// (a short pause between) and reads the title and the first bullet points. Every page is saved at once: into the list file
// of the "Surprise me: Amazon Germany products" button and into the index. robots.txt of amazon.de allows /dp/ and /gp/bestsellers.
// Unlike amzcrawl it also reads the price, star rating, number of reviews, brand and main image, and keeps them in
// amazon-products-data.jsonl (the Shopping tab shows them). It is a separate program, so restarting the web server does not
// stop it:  amzdata -site amazon.com -n 100
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"hash/crc32"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	dataFile = fsData + "/amazon-products-data.jsonl"
	deList   = fsData + "/amazon-de-products.jsonl" // the amazon.de button also gets the new pages
)

// flushBatch sends the pages that are waiting; set once the crawl has started, and also called when the program is stopped.
var flushBatch = func() {}

var asinRe = regexp.MustCompile(`/dp/([A-Z0-9]{10})`)

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func js(c cdpCaller, expr string, out any) error {
	r, err := c.call("Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true})
	if err != nil {
		return err
	}
	var d struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	json.Unmarshal(r, &d)
	return json.Unmarshal(d.Result.Value, out)
}

func main() {
	site := flag.String("site", "amazon.de", "amazon.com or amazon.de")
	pages := flag.Int("pages", 1, "with -search: how many result pages to read (about 16 to 30 products each)")
	save := flag.Bool("save", false, "with -search: also save the results as products (the Shopping tab and the index)")
	searchQ := flag.String("search", "", "search Amazon live for this (a real browser opens amazon.<site>/s?k=...) and print the first results")
	bulk := flag.Bool("bulk", false, "send the pages to the index in batches of 500, one request each, instead of one request per page (for the crawl of all Amazon pages)")
	shuffle := flag.Bool("shuffle", false, "with -collect: a random choice of categories; with -queue: the product numbers in random order (the daily crawl uses both, so each day looks at other products)")
	collectOnly := flag.Bool("collect", false, "only collect product addresses from many listing pages (Bestsellers, New releases, Movers, Most wished for, with their categories) into -queue, then stop")
	queue := flag.String("queue", "", "a file of product numbers (ASIN), one per line: -collect writes it, the crawlers read it instead of collecting")
	refresh := flag.Bool("refresh", false, "visit the products of this site that are already in amazon-products-data.jsonl and write their new price, rating... to amazon-products-data.jsonl.<site>.refreshed (nothing is collected)")
	n := flag.Int("n", 100, "how many product pages to save")
	pause := flag.Duration("pause", 100*time.Millisecond, "pause between product pages")
	settle := flag.Duration("settle", 300*time.Millisecond, "wait after a page has loaded")
	shard := flag.Int("shard", 0, "with -shards: this crawler's number (0 up)")
	shards := flag.Int("shards", 1, "how many crawlers run at once; each takes the products whose ID hashes to its number")
	minutes := flag.Int("minutes", 0, "stop visiting products after this many minutes and send what is waiting (0: no limit)")
	firefoxFlag := flag.Bool("firefox", false, "use a Firefox started on this machine (a GitHub Actions runner, geckodriver) instead of a Browser Use browser")
	chromeFlag := flag.Bool("chrome", false, "use a Chrome started on this machine (a GitHub Actions runner) instead of a Browser Use browser")
	remote := flag.String("remote", "", "the address of the Flowsearch ingest: send the products there (and ask it which product numbers are new) instead of writing files")
	flag.Parse()
	log.SetFlags(log.Ltime)
	chromeMode = *chromeFlag
	firefoxMode = *firefoxFlag
	if *minutes > 0 {
		amzDeadline = time.Now().Add(time.Duration(*minutes) * time.Minute)
	}
	remoteURL = strings.TrimRight(*remote, "/")
	remoteSite = *site

	time.Sleep(time.Duration(*shard) * 2 * time.Second) // the crawlers start one after another, so Browser Use is not asked for 10 browsers at once
	var cdpURL string
	var stopBrowser func()
	var err error
	loaded := make(chan struct{}, 1)
	onEvent := func(m string, _ json.RawMessage) {
		if m == "Page.loadEventFired" {
			select {
			case loaded <- struct{}{}:
			default:
			}
		}
	}
	var ffc *ffConn
	if firefoxMode { // a Firefox started on this machine (a GitHub Actions runner), driven over WebDriver
		ffLang := "de-DE,de;q=0.9,en;q=0.5"
		if *site == "amazon.com" {
			ffLang = "en-US,en;q=0.9"
		}
		if ffc, err = newFFConn(ffLang, onEvent); err != nil {
			log.Fatal(err)
		}
		stopBrowser = ffc.stop
	} else if chromeMode { // a Chrome started on this machine
		if stopBrowser, cdpURL, err = chromeStart(); err != nil {
			log.Fatal(err)
		}
	} else {
		var id string
		if id, cdpURL, err = buCreate(); err != nil {
			log.Fatal(err)
		}
		// tell the web server's sweeper (it stops every browser it does not know) that this one is in use
		os.MkdirAll(fsRun + "/flowsearch-browsers", 0o755)
		os.WriteFile(fsRun + "/flowsearch-browsers/"+id, nil, 0o644)
		stopBrowser = func() {
			os.Remove(fsRun + "/flowsearch-browsers/" + id)
			buStop(id)
		}
	}
	defer stopBrowser()
	sig := make(chan os.Signal, 1) // when it is stopped (kill), the browser is closed too, so it does not keep running and billing
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		flushBatch()
		stopBrowser()
		os.Exit(1)
	}()
	var c cdpCaller
	if firefoxMode {
		c = ffc
	} else {
		wsURL, err := buPageWS(cdpURL)
		if err != nil {
			log.Fatal(err)
		}
		cc, err := newCDP(wsURL, onEvent)
		if err != nil {
			log.Fatal(err)
		}
		c = cc
	}
	defer c.close()
	buSetup(c)
	// the price is in the page's HTML: do not load pictures, fonts or videos, so a page is ready much sooner
	c.call("Network.setBlockedURLs", map[string]any{"urls": []string{"*.jpg*", "*.jpeg*", "*.png*", "*.gif*", "*.webp*", "*.svg*", "*.ico*", "*.woff*", "*.ttf*", "*.mp4*", "*.m3u8*", "*.webm*", "*/images/G/*", "*fls-na.amazon*", "*unagi*", "*sellers-prod*"}})
	lang, locale := "de-DE,de;q=0.9", "de-DE"
	if *site == "amazon.com" {
		lang, locale = "en-US,en;q=0.9", "en-US"
	}
	c.call("Network.setExtraHTTPHeaders", map[string]any{"headers": map[string]string{"Accept-Language": lang}})
	if *site == "amazon.de" { // from a US address amazon.de shows dollars; ask for euros and German delivery
		for _, ck := range [][2]string{{"i18n-prefs", "EUR"}, {"lc-acbde", "de_DE"}} {
			c.call("Network.setCookie", map[string]any{"name": ck[0], "value": ck[1], "domain": ".amazon.de", "path": "/", "secure": true})
		}
	}
	c.call("Emulation.setLocaleOverride", map[string]any{"locale": locale})
	base := "https://www." + *site
	visit := func(u string) error {
		select {
		case <-loaded:
		default:
		}
		if _, e := c.call("Page.navigate", map[string]any{"url": u}); e != nil {
			return e
		}
		select {
		case <-loaded:
		case <-time.After(25 * time.Second):
		}
		time.Sleep(*settle)
		return nil
	}

	// readPage reads what the page that is open now shows
	type pageData struct {
		Title   string `json:"title"`
		Desc    string `json:"desc"`
		Price   string `json:"price"`
		Rating  string `json:"rating"`
		Reviews string `json:"reviews"`
		Brand   string `json:"brand"`
		Img     string `json:"img"`
		Page    string `json:"page"`
	}
	readPage := func() (p pageData) {
		var raw string
		if e := js(c, `JSON.stringify({
			title: (document.getElementById('productTitle')||{}).innerText||'',
			desc: (document.getElementById('feature-bullets')||{}).innerText||'',
			price: (function(){
				var root = null, sels = ['#corePrice_feature_div .a-price:not(.a-text-price)', '#corePriceDisplay_desktop_feature_div .a-price:not(.a-text-price)', '#apex_desktop .a-price:not(.a-text-price)', '.priceToPay .a-price', '#buybox .a-price:not(.a-text-price)'];
				for (var i = 0; i < sels.length && !root; i++) root = document.querySelector(sels[i]); // the buy box price first
				if (!root) { // some pages lay the box out differently: take the first plain price that is not in a carousel, a recommendation or a list of other products
					var all = document.querySelectorAll('.a-price:not(.a-text-price)');
					for (var k = 0; k < all.length && !root; k++) {
						if (!all[k].closest('[id*="sims"],[id*="similar"],[id*="carousel"],[class*="carousel"],[id*="sp_detail"],[id*="rhf"],[id*="anonCarousel"],[id*="bundle"],[data-component-type]')) root = all[k];
					}
				}
				if (!root) return '';
				var q = function(s){ return ((root.querySelector(s)||{}).textContent||'').trim(); };
				var sym = q('.a-price-symbol'), whole = q('.a-price-whole').replace(/[^0-9]/g,''), frac = q('.a-price-fraction').replace(/[^0-9]/g,'');
				if (!whole) { return q('.a-offscreen'); }
				var de = location.hostname.endsWith('.de');
				var num = de ? whole + ',' + (frac||'00') : whole + '.' + (frac||'00');
				return de ? num + ' ' + (sym||'€') : (sym||'$') + num;
			})(),
			rating: (document.getElementById('acrPopover')||{}).title||'',
			reviews: (document.getElementById('acrCustomerReviewText')||{}).innerText||'',
			brand: (document.getElementById('bylineInfo')||{}).innerText||'',
			img: (document.getElementById('landingImage')||{}).src||'',
			page: document.title+' '+location.href})`, &raw); e != nil {
			log.Printf("reading the page failed: %v", e)
		}
		json.Unmarshal([]byte(raw), &p)
		return p
	}

	// visitProduct opens a product page and returns as soon as its title (and its price) are on the page, not when the browser has
	// finished loading every last script: that is what takes the seconds. It checks that the page is the one asked for.
	visitProduct := func(u, asin string) error {
		if _, e := c.call("Page.navigate", map[string]any{"url": u}); e != nil {
			return e
		}
		deadline := time.Now().Add(15 * time.Second)
		var titleAt, doneAt time.Time
		for time.Now().Before(deadline) {
			var raw string
			js(c, `JSON.stringify({t: !!document.getElementById('productTitle'), p: !!document.querySelector('.a-price'), u: location.href, rs: document.readyState})`, &raw)
			var st struct {
				T, P bool
				U    string
				Rs   string
			}
			json.Unmarshal([]byte(raw), &st)
			if strings.Contains(st.U, asin) {
				if st.T && st.P {
					return nil
				}
				if st.T && titleAt.IsZero() {
					titleAt = time.Now()
				}
				if !titleAt.IsZero() && time.Since(titleAt) > 1500*time.Millisecond { // a product page with no price
					return nil
				}
				if st.Rs == "complete" && !st.T {
					if doneAt.IsZero() {
						doneAt = time.Now()
					} else if time.Since(doneAt) > 1200*time.Millisecond { // loaded, and it is not a product page
						return nil
					}
				}
			}
			time.Sleep(150 * time.Millisecond)
		}
		return nil
	}
	const links = `JSON.stringify([...document.querySelectorAll('a[href]')].map(a=>a.href))`
	asins := map[string]bool{}
	var order []string
	collect := func() (all []string) {
		var raw string
		if js(c, links, &raw) != nil {
			return nil
		}
		json.Unmarshal([]byte(raw), &all)
		for _, h := range all {
			if m := asinRe.FindStringSubmatch(h); m != nil && !asins[m[1]] {
				asins[m[1]] = true
				order = append(order, m[1])
			}
		}
		return all
	}
	if *refresh {
		out, err := os.OpenFile(dataFile+"."+*site+".refreshed", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatal(err)
		}
		defer out.Close()
		f, err := os.Open(dataFile)
		if err != nil {
			log.Fatal(err)
		}
		var rows []map[string]string
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var r map[string]string
			if json.Unmarshal(sc.Bytes(), &r) == nil && r["site"] == *site && r["u"] != "" {
				rows = append(rows, r)
			}
		}
		f.Close()
		done := 0
		for _, r := range rows {
			if visit(r["u"]) != nil {
				continue
			}
			p := readPage()
			one := func(s string) string { return strings.Join(strings.Fields(s), " ") }
			if strings.TrimSpace(p.Title) == "" {
				log.Printf("%s: no product title, kept as it was; page is %q", r["u"], clip(p.Page, 120))
				continue
			}
			r["t"], r["d"] = clip(one(p.Title), 200), clip(one(p.Desc), 300)
			r["price"], r["rating"], r["reviews"], r["brand"], r["img"] = one(p.Price), one(p.Rating), one(p.Reviews), one(p.Brand), p.Img
			line, _ := json.Marshal(r)
			out.Write(append(line, '\n'))
			done++
			log.Printf("refreshed %d/%d: %s | %s", done, len(rows), clip(r["t"], 40), r["price"])
			time.Sleep(*pause)
		}
		log.Printf("done: %d of %d refreshed", done, len(rows))
		return
	}
	if *searchQ != "" {
		// like the search box of the site: a first visit to the front page, then the address it would make (k, crid, sprefix)
		visit(base + "/")
		const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		crid := make([]byte, 13)
		for i := range crid {
			crid[i] = letters[int(time.Now().UnixNano()/int64(i+7))%len(letters)]
		}
		for pageNo := 1; pageNo <= *pages; pageNo++ {
			kq := url.QueryEscape(*searchQ)
			pageParam := ""
			if pageNo > 1 {
				pageParam = "&page=" + strconv.Itoa(pageNo)
			}
			if err := visit(base + "/s?k=" + kq + pageParam + "&crid=" + string(crid) + "&sprefix=" + kq + "%2Caps%2C" + strconv.Itoa(300+len(*searchQ)*13) + "&ref=nb_sb_noss"); err != nil {
				log.Printf("page %d: %v", pageNo, err)
				break
			}
			var raw string
			if e := js(c, `JSON.stringify([...document.querySelectorAll('[data-component-type="s-search-result"]')].slice(0, 30).map(e => ({
			asin: e.getAttribute('data-asin') || '',
			title: ((e.querySelector('[data-cy="title-recipe"]') || {}).innerText || (e.querySelector('img.s-image') || {}).alt || (e.querySelector('h2') || {}).innerText || '').replace(/\s+/g, ' ').trim(),
			price: ((e.querySelector('.a-price .a-offscreen') || {}).textContent || '').trim(),
			rating: ((e.querySelector('.a-icon-alt') || {}).textContent || '').trim(),
			img: ((e.querySelector('img.s-image') || {}).src || ''),
			reviews: ((e.querySelector('a[href*="#customerReviews"] span.s-underline-text, span.s-underline-text') || {}).innerText || '').trim(),
			sponsored: /Sponsored|Gesponsert/i.test((e.innerText || '').slice(0, 250))
		})).filter(r => r.asin && r.title))`, &raw); e != nil {
				log.Printf("page %d: %v", pageNo, e)
				break
			}
			var res []struct {
				Asin, Title, Price, Rating, Reviews, Img string
				Sponsored                                bool
			}
			json.Unmarshal([]byte(raw), &res)
			if len(res) == 0 {
				var page string
				js(c, `document.title + ' ' + location.href`, &page)
				log.Printf("page %d: no results read; the page is %q", pageNo, clip(page, 140))
				break
			}
			if *save {
				have := map[string]bool{}
				if f, err := os.Open(dataFile); err == nil {
					sc := bufio.NewScanner(f)
					sc.Buffer(make([]byte, 1<<20), 1<<20)
					for sc.Scan() {
						var v struct {
							U string `json:"u"`
						}
						if json.Unmarshal(sc.Bytes(), &v) == nil {
							have[v.U] = true
						}
					}
					f.Close()
				}
				df, err := os.OpenFile(dataFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
				if err != nil {
					log.Fatal(err)
				}
				defer df.Close()
				token, _ := os.ReadFile(fsSecrets + "/mysearch_token")
				saved := 0
				for _, r := range res {
					u := base + "/dp/" + r.Asin
					if have[u] {
						continue
					}
					desc := strings.TrimSpace(r.Price + " " + r.Rating)
					line, _ := json.Marshal(map[string]string{"u": u, "t": clip(r.Title, 200), "d": desc, "price": r.Price, "rating": r.Rating, "reviews": r.Reviews, "brand": "", "img": r.Img, "site": *site})
					df.Write(append(line, '\n'))
					body, _ := json.Marshal(map[string]string{"url": u, "title": clip(r.Title, 200), "description": desc, "text": clip(r.Title+". "+*site+". "+desc, 2000), "source": "amazon"})
					if req, e := http.NewRequest("POST", "http://127.0.0.1:8811/api/ingest", bytes.NewReader(body)); e == nil {
						req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
						req.Header.Set("Content-Type", "application/json")
						if resp, e := http.DefaultClient.Do(req); e == nil {
							resp.Body.Close()
						}
					}
					saved++
				}
				log.Printf("saved %d of %d results as products (the others were already saved)", saved, len(res))
			}
			for i, r := range res {
				ad := ""
				if r.Sponsored {
					ad = " [ad]"
				}
				fmt.Printf("%2d. %-10s %-6s %s%s\n    %s/dp/%s\n", i+1, r.Price, strings.Fields(r.Rating + " -")[0], clip(r.Title, 100), ad, base, r.Asin)
			}
			time.Sleep(1500 * time.Millisecond)
		}
		return
	}
	if *collectOnly {
		if *queue == "" {
			log.Fatal("-collect needs -queue FILE")
		}
		qf, err := os.OpenFile(*queue, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatal(err)
		}
		defer qf.Close()
		scroll := func() { // the listings load more products as the page is scrolled
			for i := 0; i < 5; i++ {
				c.call("Runtime.evaluate", map[string]any{"expression": "window.scrollTo(0, document.body.scrollHeight)"})
				time.Sleep(500 * time.Millisecond)
			}
		}
		added := 0
		gather := func() []string {
			scroll()
			before := len(order)
			all := collect()
			for _, a := range order[before:] {
				qf.WriteString(a + "\n")
				added++
			}
			return all
		}
		for _, kind := range []string{"bestsellers", "new-releases", "movers-and-shakers", "most-wished-for"} {
			if visit(base+"/gp/"+kind) != nil {
				continue
			}
			all := gather()
			var cats []string
			seenCat := map[string]bool{}
			for _, h := range all {
				h = strings.SplitN(h, "?", 2)[0]
				if i := strings.Index(h, "/ref="); i > 0 {
					h = h[:i]
				}
				if strings.HasPrefix(h, base+"/gp/"+kind+"/") && !seenCat[h] {
					seenCat[h] = true
					cats = append(cats, h)
				}
			}
			lim := 30
			if *shuffle {
				rand.Shuffle(len(cats), func(i, j int) { cats[i], cats[j] = cats[j], cats[i] })
				lim = 14
			}
			if len(cats) > lim {
				cats = cats[:lim]
			}
			for _, cat := range cats {
				if visit(cat) == nil {
					gather()
				}
				log.Printf("%s: %d product numbers collected so far", kind, added)
			}
		}
		log.Printf("done: %d product numbers in %s", added, *queue)
		return
	}
	var cats []string
	if *queue != "" { // the product numbers were collected before; this crawler takes its share
		qf, err := os.Open(*queue)
		if err != nil {
			log.Fatal(err)
		}
		sc := bufio.NewScanner(qf)
		for sc.Scan() {
			if a := strings.TrimSpace(sc.Text()); len(a) == 10 && !asins[a] {
				asins[a] = true
				order = append(order, a)
			}
		}
		qf.Close()
		if *shuffle {
			rand.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		}
	} else {
		if err := visit(base + "/gp/bestsellers"); err != nil {
			log.Fatal(err)
		}
		all := collect()
		seenCat := map[string]bool{}
		for _, h := range all {
			h = strings.SplitN(h, "?", 2)[0]
			if i := strings.Index(h, "/ref="); i > 0 {
				h = h[:i]
			}
			if strings.HasPrefix(h, base+"/gp/bestsellers/") && !seenCat[h] && len(cats) < 30 {
				seenCat[h] = true
				cats = append(cats, h)
			}
		}
		for _, cat := range cats {
			if len(order) >= *n*4 {
				break
			}
			if visit(cat) == nil {
				collect()
			}
			time.Sleep(1500 * time.Millisecond)
		}
		log.Printf("%d product links collected from %d pages", len(order), len(cats)+1)
	}

	have := map[string]bool{} // pages saved by an earlier run are not visited again
	if f, err := os.Open(dataFile); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var v struct {
				U string `json:"u"`
			}
			if json.Unmarshal(sc.Bytes(), &v) == nil {
				have[v.U] = true
			}
		}
		f.Close()
	}
	skipFile := fsHome + "/asin-skipped-" + *site + ".txt" // pages that are gone or have no price: not visited again
	if f, err := os.Open(skipFile); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			have[base+"/dp/"+strings.TrimSpace(sc.Text())] = true
		}
		f.Close()
	}
	skipF, _ := os.OpenFile(skipFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if skipF != nil {
		defer skipF.Close()
	}
	remember := func(asin string) {
		if remoteURL != "" { // -remote: the skipped product numbers go to the ingest with the products
			rSkipped = append(rSkipped, asin)
			return
		}
		if skipF != nil {
			skipF.WriteString(asin + "\n")
		}
	}
	var listF, deF *os.File
	if remoteURL == "" { // with -remote nothing is written to files here
		listF, err = os.OpenFile(dataFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatal(err)
		}
		defer listF.Close()
		deF, err = os.OpenFile(deList, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatal(err)
		}
		defer deF.Close()
	}
	token, _ := os.ReadFile(fsSecrets + "/mysearch_token")

	saved, skipped, streak := 0, 0, 0
	// send posts pages to the index in one request; in bulk mode that is 500 pages at a time (and what is left when the crawl ends or is stopped)
	send := func(pages []map[string]string) {
		if remoteURL != "" { // -remote: the ingest makes the index text from the products itself
			return
		}
		var body []byte
		if len(pages) == 1 {
			body, _ = json.Marshal(pages[0])
		} else {
			body, _ = json.Marshal(pages)
		}
		if req, e := http.NewRequest("POST", "http://127.0.0.1:8811/api/ingest", bytes.NewReader(body)); e == nil {
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
			req.Header.Set("Content-Type", "application/json")
			if resp, e := http.DefaultClient.Do(req); e == nil {
				resp.Body.Close()
			} else {
				log.Printf("sending %d pages to the index failed: %v", len(pages), e)
			}
		}
	}
	var batch []map[string]string
	flushBatch = func() {
		remoteFlush() // -remote: the products that are waiting go to the ingest
		if len(batch) > 0 {
			log.Printf("sending a batch of %d pages to the index in one request", len(batch))
			send(batch)
			batch = nil
		}
	}
	defer flushBatch()
	if remoteURL != "" { // -remote: only the product numbers the ingest does not have yet are visited
		before := len(order)
		order = remoteUnknown(order)
		log.Printf("%d of %d product numbers are new", len(order), before)
	}
	for _, asin := range order {
		if saved >= *n {
			break
		}
		if !amzDeadline.IsZero() && time.Now().After(amzDeadline) {
			log.Printf("the time limit of %d minutes is up", *minutes)
			break
		}
		u := base + "/dp/" + asin
		if have[u] || (*shards > 1 && int(crc32.ChecksumIEEE([]byte(asin))%uint32(*shards)) != *shard) {
			continue
		}
		if visitProduct(u, asin) != nil {
			continue
		}
		p := readPage()
		title := strings.Join(strings.Fields(p.Title), " ")
		if title == "" && (strings.Contains(p.Page, "Page Not Found") || strings.Contains(p.Page, "404")) { // a product that is gone: no sign of blocking
			remember(asin)
			skipped++
			continue
		}
		if title == "" {
			skipped++
			streak++
			if streak >= 15 { // fifteen pages in a row without a product: Amazon is probably blocking the browser
				if chromeMode { // on a GitHub runner waiting does not help (the address stays the same): stop, what was found is sent
					log.Printf("%d pages in a row without a product: Amazon blocks this runner; stopping (page: %q)", streak, clip(p.Page, 140))
					return
				}
				log.Printf("%d pages in a row without a product; pausing 5 minutes", streak)
				time.Sleep(5 * time.Minute)
				streak = 0
			}
			log.Printf("%s: no product title; page is %q", asin, clip(p.Page, 140))
			continue
		}
		one := func(s string) string { return strings.Join(strings.Fields(s), " ") }
		streak = 0
		bullets, price, brand := one(p.Desc), one(p.Price), one(p.Brand)
		if price == "" { // only products with a price are kept
			skipped++
			log.Printf("%s: no price, not saved", asin)
			remember(asin)
			continue
		}
		desc := clip(bullets, 300)
		line, _ := json.Marshal(map[string]string{"u": u, "t": clip(title, 200), "d": desc, "price": price, "rating": one(p.Rating), "reviews": one(p.Reviews), "brand": brand, "img": p.Img, "site": *site})
		if remoteURL != "" { // -remote: the product waits for the next batch of 100 to the ingest
			rProducts = append(rProducts, map[string]string{"u": u, "t": clip(title, 200), "d": desc, "price": price, "rating": one(p.Rating), "reviews": one(p.Reviews), "brand": brand, "img": p.Img, "site": *site})
			if len(rProducts) >= 100 {
				remoteFlush()
			}
		} else {
			listF.Write(append(line, '\n'))
			if *site == "amazon.de" {
				l2, _ := json.Marshal(map[string]string{"u": u, "t": clip(title, 200), "d": desc})
				deF.Write(append(l2, '\n'))
			}
		}
		text := title + ". " + *site + ". " + brand + " " + price + " " + one(p.Rating) + " " + bullets
		page := map[string]string{"url": u, "title": clip(title, 200), "description": clip(price+" "+brand+" "+bullets, 300), "text": clip(text, 2000), "source": "amazon"}
		if *bulk {
			batch = append(batch, page)
			if len(batch) >= 500 {
				flushBatch()
			}
		} else {
			send([]map[string]string{page}) // one request per page
		}
		saved++
		log.Printf("saved %d/%d: %s | %s | %s", saved, *n, clip(title, 50), price, one(p.Rating))
		time.Sleep(*pause)
	}
	log.Printf("done: %d saved, %d skipped", saved, skipped)
}
