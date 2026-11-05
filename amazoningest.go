package main

// Amazon products from GitHub Actions. The Amazon crawl (amzdata, in Chrome on a GitHub runner) cannot write the product files itself, so it sends
// them here, to the same public address as the page ingest:
//   POST /api/amazon/known     {"site":"amazon.de","asins":[...]}  -> {"new":[the product numbers this server does not have or has skipped before]}
//   POST /api/amazon/products  {"site":"amazon.de","products":[{u,t,d,price,rating,reviews,brand,img,site}],"skipped":[asin,...]}  (gzip allowed)
// Both need the same Bearer token as /api/ingest. A product is saved to amazon-products-data.jsonl (the Shopping tab), amazon.de products also to
// amazon-de-products.jsonl (the Surprise me button), and the text goes to the search index through the forum queue (forumPageQ).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
)

var (
	amzDataFile = fsData + "/amazon-products-data.jsonl"
	amzDeList   = fsData + "/amazon-de-products.jsonl"
)

var (
	amzMu     sync.Mutex
	amzSet    = map[uint64]struct{}{}
	amzLoaded bool
)

func amzSkipFile(site string) string { return fsHome + "/asin-skipped-" + site + ".txt" }

func amzOK(site string) bool { return site == "amazon.de" || site == "amazon.com" }

// amzLoad reads the product addresses saved before and the skipped product numbers (once). Called with amzMu held.
func amzLoad() {
	if amzLoaded {
		return
	}
	if f, err := os.Open(amzDataFile); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var v struct {
				U string `json:"u"`
			}
			if json.Unmarshal(sc.Bytes(), &v) == nil && v.U != "" {
				amzSet[fkHash(v.U)] = struct{}{}
			}
		}
		f.Close()
	}
	for _, site := range []string{"amazon.de", "amazon.com"} {
		if f, err := os.Open(amzSkipFile(site)); err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				if a := strings.TrimSpace(sc.Text()); a != "" {
					amzSet[fkHash("https://www."+site+"/dp/"+a)] = struct{}{}
				}
			}
			f.Close()
		}
	}
	amzLoaded = true
}

func amazonKnown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !authed(r) {
		http.Error(w, `{"error":"POST with the token"}`, 401)
		return
	}
	body, err := forumBody(r)
	var req struct {
		Site  string   `json:"site"`
		Asins []string `json:"asins"`
	}
	if err != nil || json.Unmarshal(body, &req) != nil || !amzOK(req.Site) || len(req.Asins) > 20000 {
		http.Error(w, `{"error":"bad request"}`, 400)
		return
	}
	amzMu.Lock()
	amzLoad()
	unknown := []string{}
	for _, a := range req.Asins {
		if _, ok := amzSet[fkHash("https://www."+req.Site+"/dp/"+a)]; !ok {
			unknown = append(unknown, a)
		}
	}
	amzMu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{"new": unknown})
}

func amazonProducts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !authed(r) {
		http.Error(w, `{"error":"POST with the token"}`, 401)
		return
	}
	body, err := forumBody(r)
	var req struct {
		Site     string              `json:"site"`
		Products []map[string]string `json:"products"`
		Skipped  []string            `json:"skipped"`
	}
	if err != nil || json.Unmarshal(body, &req) != nil || !amzOK(req.Site) || len(req.Products) > 5000 || len(req.Skipped) > 20000 {
		http.Error(w, `{"error":"bad request"}`, 400)
		return
	}
	amzMu.Lock()
	defer amzMu.Unlock()
	amzLoad()
	var data, de bytes.Buffer
	var pages []Page
	for _, p := range req.Products {
		u := p["u"]
		if !strings.HasPrefix(u, "https://www."+req.Site+"/dp/") || strings.TrimSpace(p["t"]) == "" || strings.TrimSpace(p["price"]) == "" {
			continue
		}
		if _, ok := amzSet[fkHash(u)]; ok {
			continue
		}
		amzSet[fkHash(u)] = struct{}{}
		line, _ := json.Marshal(p)
		data.Write(line)
		data.WriteByte('\n')
		if req.Site == "amazon.de" {
			l2, _ := json.Marshal(map[string]string{"u": u, "t": p["t"], "d": p["d"]})
			de.Write(l2)
			de.WriteByte('\n')
		}
		text := p["t"] + ". " + req.Site + ". " + p["brand"] + " " + p["price"] + " " + p["rating"] + " " + p["d"]
		pages = append(pages, Page{URL: u, Title: clip(p["t"], 200), Description: clip(p["price"]+" "+p["brand"]+" "+p["d"], 300), Text: clip(text, 2000), Source: "amazon"})
	}
	appendTo := func(path string, b []byte) bool {
		if len(b) == 0 {
			return true
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return false
		}
		_, err = f.Write(b)
		f.Close()
		return err == nil
	}
	if !appendTo(amzDataFile, data.Bytes()) || !appendTo(amzDeList, de.Bytes()) {
		http.Error(w, `{"error":"could not save"}`, 500)
		return
	}
	var sk bytes.Buffer
	for _, a := range req.Skipped {
		if len(a) == 10 && !strings.ContainsAny(a, " /\n") {
			sk.WriteString(a + "\n")
			amzSet[fkHash("https://www."+req.Site+"/dp/"+a)] = struct{}{}
		}
	}
	appendTo(amzSkipFile(req.Site), sk.Bytes())
	if len(pages) > 0 {
		select {
		case forumPageQ <- pages:
		default:
		}
	}
	json.NewEncoder(w).Encode(map[string]int{"added": len(pages), "skipped": len(req.Skipped), "waiting": len(forumPageQ)})
}
