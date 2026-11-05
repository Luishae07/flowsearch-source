// amzcrawl crawls amazon.de product pages (/dp/) in a real browser (Browser Use), because Amazon answers a plain request from
// this server with its block page. It opens the Bestseller pages, collects product links, then visits them one after another
// (a short pause between) and reads the title and the first bullet points. Every page is saved at once: into the list file
// of the "Surprise me: Amazon Germany products" button and into the index. robots.txt of amazon.de allows /dp/ and /gp/bestsellers.
// It is a separate program, so restarting the web server does not stop it:  amzcrawl -n 100
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"hash/crc32"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var listFile = fsData + "/amazon-de-products.jsonl"

var asinRe = regexp.MustCompile(`/dp/([A-Z0-9]{10})`)

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func js(c *cdpConn, expr string, out any) error {
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
	n := flag.Int("n", 100, "how many product pages to save")
	pause := flag.Duration("pause", 200*time.Millisecond, "pause between product pages")
	settle := flag.Duration("settle", 500*time.Millisecond, "wait after a page has loaded")
	shard := flag.Int("shard", 0, "with -shards: this crawler's number (0 up)")
	shards := flag.Int("shards", 1, "how many crawlers run at once; each takes the products whose ID hashes to its number")
	flag.Parse()
	log.SetFlags(log.Ltime)

	id, cdpURL, err := buCreate()
	if err != nil {
		log.Fatal(err)
	}
	// tell the web server's sweeper (it stops every browser it does not know) that this one is in use
	os.MkdirAll(fsRun + "/flowsearch-browsers", 0o755)
	os.WriteFile(fsRun + "/flowsearch-browsers/"+id, nil, 0o644)
	defer os.Remove(fsRun + "/flowsearch-browsers/" + id)
	defer buStop(id)
	wsURL, err := buPageWS(cdpURL)
	if err != nil {
		log.Fatal(err)
	}
	loaded := make(chan struct{}, 1)
	c, err := newCDP(wsURL, func(m string, _ json.RawMessage) {
		if m == "Page.loadEventFired" {
			select {
			case loaded <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		log.Fatal(err)
	}
	defer c.close()
	buSetup(c)
	c.call("Network.setExtraHTTPHeaders", map[string]any{"headers": map[string]string{"Accept-Language": "de-DE,de;q=0.9"}})
	c.call("Emulation.setLocaleOverride", map[string]any{"locale": "de-DE"})
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
	if err := visit("https://www.amazon.de/gp/bestsellers"); err != nil {
		log.Fatal(err)
	}
	all := collect()
	var cats []string
	seenCat := map[string]bool{}
	for _, h := range all {
		h = strings.SplitN(h, "?", 2)[0]
		if i := strings.Index(h, "/ref="); i > 0 {
			h = h[:i]
		}
		if strings.HasPrefix(h, "https://www.amazon.de/gp/bestsellers/") && !seenCat[h] && len(cats) < 12 {
			seenCat[h] = true
			cats = append(cats, h)
		}
	}
	for _, cat := range cats {
		if len(order) >= *n*2 {
			break
		}
		if visit(cat) == nil {
			collect()
		}
		time.Sleep(1500 * time.Millisecond)
	}
	log.Printf("%d product links collected from %d pages", len(order), len(cats)+1)

	have := map[string]bool{} // pages saved by an earlier run are not visited again
	if f, err := os.Open(listFile); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var v struct{ U string }
			if json.Unmarshal(sc.Bytes(), &v) == nil {
				have[v.U] = true
			}
		}
		f.Close()
	}
	listF, err := os.OpenFile(listFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	defer listF.Close()
	token, _ := os.ReadFile(fsSecrets + "/mysearch_token")

	saved, skipped := 0, 0
	for _, asin := range order {
		if saved >= *n {
			break
		}
		u := "https://www.amazon.de/dp/" + asin
		if have[u] || (*shards > 1 && int(crc32.ChecksumIEEE([]byte(asin))%uint32(*shards)) != *shard) {
			continue
		}
		if visit(u) != nil {
			continue
		}
		var p struct {
			Title string `json:"title"`
			Desc  string `json:"desc"`
			Page  string `json:"page"`
		}
		var raw string
		if e := js(c, `JSON.stringify({title:(document.getElementById('productTitle')||{}).innerText||'', desc:(document.getElementById('feature-bullets')||{}).innerText||'', page:document.title+' '+location.href})`, &raw); e != nil {
			log.Printf("%s: reading the page failed: %v", asin, e)
		}
		json.Unmarshal([]byte(raw), &p)
		title := strings.Join(strings.Fields(p.Title), " ")
		if title == "" {
			skipped++
			log.Printf("%s: no product title; page is %q", asin, clip(p.Page, 140))
			if skipped >= 8 && saved == 0 {
				log.Fatal("eight pages in a row without a product: Amazon is probably blocking the browser; stopping")
			}
			continue
		}
		bullets := strings.Join(strings.Fields(p.Desc), " ")
		desc := clip(bullets, 300)
		line, _ := json.Marshal(map[string]string{"u": u, "t": clip(title, 200), "d": desc})
		listF.Write(append(line, '\n'))
		body, _ := json.Marshal(map[string]string{"url": u, "title": clip(title, 200), "description": desc, "text": clip(title+". Amazon.de. "+bullets, 2000), "source": "amazon"})
		if req, e := http.NewRequest("POST", "http://127.0.0.1:8811/api/ingest", bytes.NewReader(body)); e == nil {
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
			req.Header.Set("Content-Type", "application/json")
			if resp, e := http.DefaultClient.Do(req); e == nil {
				resp.Body.Close()
			}
		}
		saved++
		log.Printf("saved %d/%d: %s", saved, *n, clip(title, 70))
		time.Sleep(*pause)
	}
	log.Printf("done: %d saved, %d skipped", saved, skipped)
}
