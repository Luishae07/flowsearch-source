package main

// "Update price": a button on every product card puts the product in a queue. One worker reads the product page in a real browser
// (Browser Use) and writes the new price, rating and image to the product file; the card shows its place in the line and then the
// new price. Limits, because every update costs a browser visit: 10 clicks a minute from one address, at most 200 waiting,
// 500 updates a day, and a product that was updated is left alone for 10 minutes.

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type priceJob struct {
	State    string // queued, working, done, error
	Err      string
	Price    string
	Rating   string
	Reviews  string
	Finished time.Time
}

var (
	pqMu    sync.Mutex
	pqJobs  = map[string]*priceJob{}
	pqLine  []string
	pqWake  = make(chan struct{}, 1)
	pqDay   string
	pqCount int
	pqRate  = map[string][]time.Time{}
)

const (
	pqMaxWaiting  = 200
	pqPerDay      = 500
	pqCooldown    = 10 * time.Minute
	pqClicksPerMn = 10
)

func init() { go priceWorker() }

func pqPosition(u string) int {
	for i, x := range pqLine {
		if x == u {
			return i + 1
		}
	}
	return 0
}

func pqStatus(u string) map[string]any {
	j := pqJobs[u]
	if j == nil {
		return map[string]any{"state": "idle", "waiting": len(pqLine)}
	}
	m := map[string]any{"state": j.State, "waiting": len(pqLine)}
	if j.State == "queued" {
		m["position"] = pqPosition(u)
	}
	if j.State == "done" {
		m["price"], m["rating"], m["reviews"] = j.Price, j.Rating, j.Reviews
	}
	if j.State == "error" {
		m["error"] = j.Err
	}
	return m
}

// apiPriceUpdate: POST /api/price-update?u=<product address> puts the product in the line (or says where it already is).
func apiPriceUpdate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, 405)
		return
	}
	u := r.URL.Query().Get("u")
	known := false
	for _, a := range amazonProducts() {
		if a.U == u {
			known = true
			break
		}
	}
	if !known {
		http.Error(w, `{"error":"not a product Flowsearch holds"}`, 404)
		return
	}
	ip := clientIP(r)
	now := time.Now()
	pqMu.Lock()
	defer pqMu.Unlock()
	recent := pqRate[ip][:0]
	for _, t := range pqRate[ip] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= pqClicksPerMn {
		pqRate[ip] = recent
		http.Error(w, `{"error":"too many clicks, wait a minute"}`, 429)
		return
	}
	pqRate[ip] = append(recent, now)
	if j := pqJobs[u]; j != nil {
		if j.State == "queued" || j.State == "working" || (j.State == "done" && now.Sub(j.Finished) < pqCooldown) {
			json.NewEncoder(w).Encode(pqStatus(u)) // already waiting, or updated a moment ago
			return
		}
	}
	if today := now.UTC().Format("2006-01-02"); today != pqDay {
		pqDay, pqCount = today, 0
	}
	if len(pqLine) >= pqMaxWaiting || pqCount >= pqPerDay {
		http.Error(w, `{"error":"the line is full for now, try later"}`, 503)
		return
	}
	pqCount++
	pqJobs[u] = &priceJob{State: "queued"}
	pqLine = append(pqLine, u)
	if len(pqJobs) > 5000 { // forget old finished jobs
		for k, j := range pqJobs {
			if j.State == "done" || j.State == "error" {
				delete(pqJobs, k)
			}
		}
	}
	select {
	case pqWake <- struct{}{}:
	default:
	}
	json.NewEncoder(w).Encode(pqStatus(u))
}

// apiPriceStatus: GET /api/price-status?u=<product address>  (without u: just how many are waiting)
func apiPriceStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	pqMu.Lock()
	defer pqMu.Unlock()
	json.NewEncoder(w).Encode(pqStatus(r.URL.Query().Get("u")))
}

// priceWorker takes products from the line one at a time. It keeps one browser while there is work and closes it after 25 idle seconds.
func priceWorker() {
	var br *priceBrowser
	for {
		pqMu.Lock()
		var u string
		if len(pqLine) > 0 {
			u = pqLine[0]
			pqLine = pqLine[1:]
			pqJobs[u].State = "working"
		}
		pqMu.Unlock()
		if u == "" {
			select {
			case <-pqWake:
			case <-time.After(25 * time.Second):
				if br != nil {
					br.close()
					br = nil
				}
			}
			continue
		}
		var res priceResult
		var err error
		for attempt := 0; attempt < 2; attempt++ { // a second try with a fresh browser if the first failed
			if br == nil {
				if br, err = newPriceBrowser(); err != nil {
					break
				}
			}
			if res, err = br.read(u); err == nil {
				break
			}
			br.close()
			br = nil
		}
		pqMu.Lock()
		j := pqJobs[u]
		j.Finished = time.Now()
		if err != nil {
			j.State, j.Err = "error", "could not read the page"
			log.Printf("price update of %s failed: %v", u, err)
		} else {
			j.State, j.Price, j.Rating, j.Reviews = "done", res.Price, res.Rating, res.Reviews
			saveUpdatedProduct(u, res)
			log.Printf("price update of %s: %s", u, res.Price)
		}
		pqMu.Unlock()
		time.Sleep(600 * time.Millisecond)
	}
}

// saveUpdatedProduct appends the new values as a later line of the product file; a later line replaces the earlier one.
func saveUpdatedProduct(u string, res priceResult) {
	var old amzItem
	for _, a := range amazonProducts() {
		if a.U == u {
			old = a
			break
		}
	}
	if old.U == "" {
		return
	}
	pick := func(n, o string) string {
		if strings.TrimSpace(n) != "" {
			return n
		}
		return o
	}
	line, _ := json.Marshal(map[string]string{"u": u, "t": pick(res.Title, old.T), "d": old.D, "price": res.Price, "rating": pick(res.Rating, old.Rating),
		"reviews": pick(res.Reviews, old.Reviews), "brand": pick(res.Brand, old.Brand), "img": pick(res.Img, old.Img), "site": old.Site})
	if f, err := os.OpenFile(amazonDataFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.Write(append(line, '\n'))
		f.Close()
	}
}

type priceResult struct {
	Title, Price, Rating, Reviews, Brand, Img string
}

// priceBrowser is one Browser Use browser kept open while the line has work.
type priceBrowser struct {
	id     string
	c      *cdpConn
	loaded chan struct{}
}

func newPriceBrowser() (*priceBrowser, error) {
	id, cdpURL, err := buCreate()
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*priceBrowser, error) {
		go buStop(id)
		return nil, err
	}
	wsURL, err := buPageWS(cdpURL)
	if err != nil {
		return fail(err)
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
		return fail(err)
	}
	buSetup(c)
	for _, ck := range [][2]string{{"i18n-prefs", "EUR"}, {"lc-acbde", "de_DE"}} { // amazon.de in euros
		c.call("Network.setCookie", map[string]any{"name": ck[0], "value": ck[1], "domain": ".amazon.de", "path": "/", "secure": true})
	}
	return &priceBrowser{id, c, loaded}, nil
}

func (b *priceBrowser) close() {
	b.c.close()
	go buStop(b.id)
}

func (b *priceBrowser) read(u string) (res priceResult, err error) {
	if strings.HasSuffix(strings.SplitN(strings.TrimPrefix(u, "https://"), "/", 2)[0], "amazon.de") {
		b.c.call("Network.setExtraHTTPHeaders", map[string]any{"headers": map[string]string{"Accept-Language": "de-DE,de;q=0.9"}})
		b.c.call("Emulation.setLocaleOverride", map[string]any{"locale": "de-DE"})
	} else {
		b.c.call("Network.setExtraHTTPHeaders", map[string]any{"headers": map[string]string{"Accept-Language": "en-US,en;q=0.9"}})
		b.c.call("Emulation.setLocaleOverride", map[string]any{"locale": "en-US"})
	}
	wait := func() {
		select {
		case <-b.loaded:
		case <-time.After(25 * time.Second):
		}
		time.Sleep(500 * time.Millisecond)
	}
	eval := func(expr string) (map[string]string, error) {
		r, err := b.c.call("Runtime.evaluate", map[string]any{"returnByValue": true, "expression": expr})
		if err != nil {
			return nil, err
		}
		var d struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		json.Unmarshal(r, &d)
		var raw map[string]string
		return raw, json.Unmarshal([]byte(d.Result.Value), &raw)
	}
	one := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	var raw map[string]string
	for try := 0; try < 3; try++ {
		select {
		case <-b.loaded:
		default:
		}
		if _, err = b.c.call("Page.navigate", map[string]any{"url": u}); err != nil {
			return
		}
		wait()
		if raw, err = eval(priceJS); err != nil { // the page was still changing (Amazon redirecting): look again in a moment
			raw, err = map[string]string{"page": "(not readable yet)"}, nil
			time.Sleep(1500 * time.Millisecond)
			continue
		}
		if one(raw["title"]) != "" {
			break
		}
		page := raw["page"]
		switch {
		case strings.Contains(page, "checkpoint"): // Amazon's "Continue shopping" check: click the button, then read the page again
			b.c.call("Runtime.evaluate", map[string]any{"expression": `(function(){var b=[...document.querySelectorAll('button,input[type=submit],a')].find(function(x){return /continue shopping|weiter einkaufen/i.test(x.innerText||x.value||'')});if(b)b.click()})()`})
			wait()
		case strings.Contains(page, "503"), strings.Contains(page, "Sorry"): // a short error: wait and try again
			time.Sleep(2500 * time.Millisecond)
		default:
			try = 3
		}
	}
	if one(raw["title"]) == "" {
		return res, &priceErr{"the page has no product on it; the page is " + clip(one(raw["page"]), 140)}
	}
	return priceResult{one(raw["title"]), one(raw["price"]), one(raw["rating"]), one(raw["reviews"]), one(raw["brand"]), raw["img"]}, nil
}

var errNoProduct = &priceErr{"the page has no product on it"}

type priceErr struct{ s string }

func (e *priceErr) Error() string { return e.s }

// shopJS makes the "Update price" buttons work: click, then follow the place in the line, then show the new price.
const shopJS = `<script>
(function(){
  const post=(u)=>fetch('/api/price-update?u='+encodeURIComponent(u),{method:'POST'}).then(r=>r.json().then(j=>({ok:r.ok,j})));
  const stat=(u)=>fetch('/api/price-status?u='+encodeURIComponent(u)).then(r=>r.json());
  const nth=n=>n+(n%10==1&&n!=11?'st':n%10==2&&n!=12?'nd':n%10==3&&n!=13?'rd':'th');
  function show(card,j){
    const st=card.querySelector('.pstate'),btn=card.querySelector('.pupd');
    st.className='pstate';
    if(j.state==='queued'){btn.disabled=true;st.textContent='In line: '+nth(j.position||1)+' of '+(j.waiting||1)}
    else if(j.state==='working'){btn.disabled=true;st.textContent='Reading Amazon…'}
    else if(j.state==='done'){
      btn.disabled=false;st.className='pstate ok';st.textContent='Updated just now';
      const p=card.querySelector('.pprice');if(p&&j.price){p.textContent=j.price;p.classList.remove('none')}
      const r=card.querySelector('.prate');if(r&&j.rating){const m=j.rating.match(/[0-9]+[.,][0-9]/);if(m)r.innerHTML='<b>★ '+m[0]+'</b>'+(j.reviews?' · '+j.reviews.replace(/[()]/g,''):'')}
    }
    else if(j.state==='error'){btn.disabled=false;st.className='pstate bad';st.textContent=j.error||'failed'}
  }
  function follow(card,u){
    stat(u).then(j=>{show(card,j);if(j.state==='queued'||j.state==='working')setTimeout(()=>follow(card,u),1500)}).catch(()=>{});
  }
  document.addEventListener('click',e=>{
    const btn=e.target.closest('.pupd');if(!btn)return;
    const card=btn.closest('.pcard'),u=card.dataset.u;
    btn.disabled=true;
    post(u).then(({ok,j})=>{
      if(!ok){btn.disabled=false;const st=card.querySelector('.pstate');st.className='pstate bad';st.textContent=j.error||'failed';return}
      show(card,j);follow(card,u);
    }).catch(()=>{btn.disabled=false});
  });
})();
</script>`

// priceJS reads the open product page: title, the buy box price (never an accessory's or a monthly price), rating, reviews, brand, image.
const priceJS = `JSON.stringify({
	title: (document.getElementById('productTitle')||{}).innerText||'',
	price: (function(){
		var root = null, sels = ['#corePrice_feature_div .a-price:not(.a-text-price)', '#corePriceDisplay_desktop_feature_div .a-price:not(.a-text-price)', '#apex_desktop .a-price:not(.a-text-price)', '.priceToPay .a-price', '#buybox .a-price:not(.a-text-price)'];
		for (var i = 0; i < sels.length && !root; i++) root = document.querySelector(sels[i]);
		if (!root) { // some pages lay the box out differently: the first plain price that is not in a carousel, a recommendation or a list of other products
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
	page: document.title+' | '+location.href})`
