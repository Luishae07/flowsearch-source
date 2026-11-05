package main

// The web server no longer loads the index. The index lives in its own service (sgindexd, systemd unit mysearch-index, 127.0.0.1:18820) that
// stays up; this client has the same methods the web server always called on the index, and every one is a small request to that service.
// So the web server starts in about a second, and the index is not loaded again when it restarts.

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"mysearch/sgindex"
)

const indexURL = "http://127.0.0.1:18820"

type indexClient struct {
	hc     *http.Client
	Notify chan struct{} // gets a value when the index has grown (the page counter and the live page wait on it)

	mu      sync.Mutex
	st      idxStats
	stAt    time.Time
	recent  []sgindex.Recent
	recAt   time.Time

	docMu sync.Mutex
	docs  map[uint32]sgindex.Doc // pages already asked for (a search looks at 300 of them: they are fetched together, not one after the other)
}

type idxStats struct {
	NDocs      int    `json:"ndocs"`
	Segments   int    `json:"segments"`
	Characters uint64 `json:"characters"`
	Terms      int    `json:"terms"`
	Sites      int    `json:"sites"`
}

// newIndexClient waits (up to 5 minutes) until the index service answers, then starts watching it for growth.
func newIndexClient() *indexClient {
	c := &indexClient{hc: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConns: 64, MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second}}, Notify: make(chan struct{}, 1), docs: map[uint32]sgindex.Doc{}}
	for i := 0; i < 300; i++ {
		if c.get("/stats", nil, &c.st) == nil {
			break
		}
		if i == 0 {
			log.Printf("waiting for the index service (mysearch-index) on %s ...", indexURL)
		}
		time.Sleep(time.Second)
	}
	c.stAt = time.Now()
	go func() { // tell the live page the moment the index grows (checked 10 times a second)
		last := c.st.NDocs
		for range time.Tick(100 * time.Millisecond) {
			var s idxStats
			if c.get("/stats", nil, &s) != nil {
				continue
			}
			c.mu.Lock()
			c.st, c.stAt = s, time.Now()
			c.mu.Unlock()
			if s.NDocs != last {
				last = s.NDocs
				select {
				case c.Notify <- struct{}{}:
				default:
				}
			}
		}
	}()
	return c
}

func (c *indexClient) get(path string, q url.Values, out any) error {
	u := indexURL + path
	if q != nil {
		u += "?" + q.Encode()
	}
	resp, err := c.hc.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *indexClient) stats() idxStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.stAt) > 300*time.Millisecond {
		var s idxStats
		if c.get("/stats", nil, &s) == nil {
			c.st, c.stAt = s, time.Now()
		}
	}
	return c.st
}

func (c *indexClient) Run(func([]sgindex.Doc)) {} // the index service keeps reading the new pages

func (c *indexClient) NDocs() int         { return c.stats().NDocs }
func (c *indexClient) Segments() int      { return c.stats().Segments }
func (c *indexClient) Characters() uint64 { return c.stats().Characters }
func (c *indexClient) Terms() int         { return c.stats().Terms }
func (c *indexClient) Sites() int         { return c.stats().Sites }

func (c *indexClient) RecentList() []sgindex.Recent {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.recAt) > 300*time.Millisecond {
		var r []sgindex.Recent
		if c.get("/recent", nil, &r) == nil {
			c.recent, c.recAt = r, time.Now()
		}
	}
	return c.recent
}

func (c *indexClient) Search(q string) (groups [][]string, hits []sgindex.Hit, total int) {
	var r struct {
		Groups [][]string    `json:"groups"`
		Hits   []sgindex.Hit `json:"hits"`
		Total  int           `json:"total"`
	}
	if err := c.get("/search", url.Values{"q": {q}}, &r); err != nil {
		log.Printf("index service: search failed: %v", err)
		return nil, nil, 0
	}
	ids := make([]uint32, len(r.Hits))
	for i, h := range r.Hits {
		ids[i] = h.ID
	}
	c.prefetch(ids) // the web server goes through every hit: all of them are fetched at once, so the search is not 300 small requests in a row
	return r.Groups, r.Hits, r.Total
}

// fetchDoc asks the index service for one page's entry
func (c *indexClient) fetchDoc(id uint32) (sgindex.Doc, bool) {
	var r struct {
		Doc sgindex.Doc `json:"doc"`
		OK  bool        `json:"ok"`
	}
	if c.get("/doc", url.Values{"id": {strconv.Itoa(int(id))}}, &r) != nil {
		return sgindex.Doc{}, false
	}
	if r.OK {
		c.docMu.Lock()
		if len(c.docs) > 200000 {
			c.docs = map[uint32]sgindex.Doc{}
		}
		c.docs[id] = r.Doc
		c.docMu.Unlock()
	}
	return r.Doc, r.OK
}

// prefetch fetches the entries of these pages (up to 1000) with 32 requests at a time, for the ones that are not known yet
func (c *indexClient) prefetch(ids []uint32) {
	if len(ids) > 1000 {
		ids = ids[:1000]
	}
	var todo []uint32
	c.docMu.Lock()
	for _, id := range ids {
		if _, ok := c.docs[id]; !ok {
			todo = append(todo, id)
		}
	}
	c.docMu.Unlock()
	if len(todo) == 0 {
		return
	}
	var wg sync.WaitGroup
	ch := make(chan uint32)
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range ch {
				c.fetchDoc(id)
			}
		}()
	}
	for _, id := range todo {
		ch <- id
	}
	close(ch)
	wg.Wait()
}

func (c *indexClient) Doc(id uint32) (sgindex.Doc, bool) {
	c.docMu.Lock()
	d, ok := c.docs[id]
	c.docMu.Unlock()
	if ok {
		return d, true
	}
	return c.fetchDoc(id)
}

func (c *indexClient) HostDocPage(domain string, offset, limit int) ([]uint32, int) {
	var r struct {
		IDs   []uint32 `json:"ids"`
		Total int      `json:"total"`
	}
	if c.get("/hostdocpage", url.Values{"domain": {domain}, "offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)}}, &r) != nil {
		return nil, 0
	}
	c.prefetch(r.IDs) // the callers look at every page of the list
	return r.IDs, r.Total
}

func (c *indexClient) Suggest(prefix string, limit int) []string {
	var r []string
	c.get("/suggest", url.Values{"prefix": {prefix}, "limit": {strconv.Itoa(limit)}}, &r)
	return r
}

func (c *indexClient) SpellFix(word string) string {
	var r string
	c.get("/spellfix", url.Values{"word": {word}}, &r)
	return r
}

func (c *indexClient) HostsWithSuffix(domain string) (hosts, pages int) {
	var r struct{ Hosts, Pages int }
	c.get("/hostswithsuffix", url.Values{"domain": {domain}}, &r)
	return r.Hosts, r.Pages
}

// HostsWhere: pred is "en" (.com and .org sites) or "de" (.de .at .ch .li); the index service has both rules
func (c *indexClient) HostsWhere(pred string, k int) (hosts, pages int, top []sgindex.HostCount) {
	var r struct {
		Hosts int                `json:"hosts"`
		Pages int                `json:"pages"`
		Top   []sgindex.HostCount `json:"top"`
	}
	c.get("/hostswhere", url.Values{"pred": {pred}, "k": {strconv.Itoa(k)}}, &r)
	return r.Hosts, r.Pages, r.Top
}

func (c *indexClient) HostPagesAny(host string) int {
	var r int
	c.get("/hostpagesany", url.Values{"host": {host}}, &r)
	return r
}
