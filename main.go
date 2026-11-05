// mysearch-ingest: receives crawled pages from the Codespace crawler and appends them to a JSON-lines file.
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"mysearch/sgpack"
	"mysearch/sgseen"
)

// bwLimiter is our own token-bucket bandwidth limiter (no x/time/rate), so many parallel GitHub
// Actions crawlers hitting /api/ingest at once can't saturate this box's internet connection.
type bwLimiter struct {
	mu       sync.Mutex
	tokens   float64
	capacity float64
	rate     float64 // bytes/second refill rate
	last     time.Time
}

func newBWLimiter(bytesPerSecond, burstBytes float64) *bwLimiter {
	return &bwLimiter{tokens: burstBytes, capacity: burstBytes, rate: bytesPerSecond, last: time.Now()}
}

// wait blocks until n bytes' worth of tokens are available, refilling as time passes.
func (b *bwLimiter) wait(n int) {
	need := float64(n)
	for {
		b.mu.Lock()
		now := time.Now()
		b.tokens += now.Sub(b.last).Seconds() * b.rate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = now
		if b.tokens >= need {
			b.tokens -= need
			b.mu.Unlock()
			return
		}
		short := need - b.tokens
		b.mu.Unlock()
		time.Sleep(time.Duration(short/b.rate*float64(time.Second)) + 10*time.Millisecond)
	}
}

// 1 MB/s sustained (about 8 Mbit/s), 5 MB burst (the burst must be at least the 4 MB request limit) - keeps the crawl from choking the box's Wi-Fi.
var ingestLimiter = newBWLimiter(1<<20, 256<<10)

// at most 100 pages (sites) a second are accepted, however fast the crawler sends them
var pageLimiter = newBWLimiter(100, 1) // burst of 1: one page every 10 ms, never a lump

// Daily data budget: once this many bytes have come in today (UTC), /api/ingest answers 429 until tomorrow.
// The crawler stops on a 429, and budget-out holds today's date so the timer and watchdog do not restart it.
const dailyBudget = 0 // 0 = no daily limit (only the speed cap applies); for example 3 << 30 = 3 GB a day

var (
	budgetMu   sync.Mutex
	budgetDay  string
	budgetUsed int64
	budgetSave time.Time
)

var budgetFile = fsHome + "/budget.json"
var budgetOutFile = fsHome + "/budget-out"

func budgetLoad() {
	var st struct {
		Day  string `json:"day"`
		Used int64  `json:"used"`
	}
	if b, err := os.ReadFile(budgetFile); err == nil && json.Unmarshal(b, &st) == nil {
		budgetDay, budgetUsed = st.Day, st.Used
	}
}

// budgetAdd counts n bytes for today and reports whether today's budget is already used up (before adding).
func budgetAdd(n int64) (exhausted bool) {
	budgetMu.Lock()
	defer budgetMu.Unlock()
	today := time.Now().UTC().Format("2006-01-02")
	if budgetDay != today {
		budgetDay, budgetUsed = today, 0
	}
	if dailyBudget > 0 && budgetUsed >= dailyBudget {
		os.WriteFile(budgetOutFile, []byte(today+"\n"), 0o644)
		return true
	}
	budgetUsed += n
	if time.Since(budgetSave) > 10*time.Second {
		budgetSave = time.Now()
		b, _ := json.Marshal(map[string]interface{}{"day": budgetDay, "used": budgetUsed, "budget": dailyBudget})
		os.WriteFile(budgetFile, b, 0o644)
	}
	return false
}

// Stored is one line of pages4.jsonl (short keys): the text is packed (see package sgpack) and cut to 2,000 characters.
type Stored struct {
	URL         string `json:"u"`
	Title       string `json:"t"`
	Description string `json:"d,omitempty"`
	Z           string `json:"z"`
	Source      string `json:"s,omitempty"`
}

// ---- recrawl: pages of SUBMITTED sites may be stored again when their text changed.
// Every other site keeps the old rule (an address is stored once). For a submitted site, the fingerprint of the stored text of
// each address is kept in recrawl-hashes.jsonl; a page that arrives again with a different fingerprint (or one never seen
// before) is stored as a new version, and the search shows only the newest version of an address.
var (
	recrawlPath   = fsData + "/recrawl-hashes.jsonl"
	submittedPath = fsData + "/submitted.txt"
)

var (
	rcMu       sync.Mutex
	rcHash     = map[uint64]uint64{}
	rcHosts    = map[string]bool{}
	rcHostsMod time.Time
	rcFile     *os.File
)

func fnv64(s string) uint64 { h := fnv.New64a(); h.Write([]byte(s)); return h.Sum64() }

func loadRecrawl() {
	if f, err := os.Open(recrawlPath); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var r struct{ U, C string }
			if json.Unmarshal(sc.Bytes(), &r) == nil {
				u, e1 := strconv.ParseUint(r.U, 16, 64)
				c, e2 := strconv.ParseUint(r.C, 16, 64)
				if e1 == nil && e2 == nil {
					rcHash[u] = c
				}
			}
		}
		f.Close()
	}
	rcFile, _ = os.OpenFile(recrawlPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}

func rememberHash(uh, ch uint64) {
	rcMu.Lock()
	defer rcMu.Unlock()
	rcHash[uh] = ch
	if rcFile != nil {
		fmt.Fprintf(rcFile, "{\"U\":\"%016x\",\"C\":\"%016x\"}\n", uh, ch)
	}
}

// submittedHost says whether a host (or a site above it) is on a list of submitted sites: the combined lists and every
// user's own list. The lists are read again at most once a minute.
func submittedHost(host string) bool {
	rcMu.Lock()
	defer rcMu.Unlock()
	if time.Since(rcHostsMod) > time.Minute {
		m := map[string]bool{}
		read := func(path string) {
			b, err := os.ReadFile(path)
			if err != nil {
				return
			}
			for _, l := range strings.Split(string(b), "\n") {
				l = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(strings.SplitN(l, "\t", 2)[0]), "www."))
				if l != "" {
					m[l] = true
				}
			}
		}
		read(submittedPath)
		read(fsData + "/submitted-de.txt")
		for _, dir := range []string{fsData + "/submitted-users", fsData + "/submitted-users-de"} {
			if es, err := os.ReadDir(dir); err == nil {
				for _, e := range es {
					if !e.IsDir() {
						read(dir + "/" + e.Name())
					}
				}
			}
		}
		rcHosts, rcHostsMod = m, time.Now()
	}
	for h := host; h != ""; {
		if rcHosts[h] {
			return true
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			break
		}
		h = h[i+1:]
	}
	return false
}

func hostOfURL(u string) string {
	pu, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(pu.Hostname(), "www."))
}

type Page struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Text        string `json:"text"`
	Source      string `json:"source"`
	Crawled     int64  `json:"crawled"`
	Received    int64  `json:"received"`
}

var (
	mu    sync.Mutex
	set   *sgseen.Set // addresses already stored, on disk
	out   *os.File
	token string

	outSize int64 // size of pages file after the last whole line
)

// repairTail cuts off an unfinished last line (left by a crash or a full disk) so new lines start clean.
func repairTail(path string) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return
	}
	last := make([]byte, 1)
	f.ReadAt(last, st.Size()-1)
	if last[0] == '\n' {
		return
	}
	// look back for the last newline
	size := st.Size()
	buf := make([]byte, 1<<20)
	start := size - int64(len(buf))
	if start < 0 {
		start = 0
		buf = buf[:size]
	}
	n, _ := f.ReadAt(buf, start)
	cut := int64(0)
	if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
		cut = start + int64(i) + 1
	}
	log.Printf("pages file ended in an unfinished line: cutting %d bytes", size-cut)
	f.Truncate(cut)
	f.Sync()
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func normalize(raw string) (string, bool) {
	u, err := url.Parse(strings.ReplaceAll(strings.TrimSpace(raw), "&amp;", "&")) // a sitemap address copied with its HTML escapes
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	u.Fragment = ""
	return u.String(), true
}

func authed(r *http.Request) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func ingest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, 405)
		return
	}
	if !authed(r) {
		http.Error(w, `{"error":"bad token"}`, 401)
		return
	}
	// read the upload in small pieces at the allowed speed (counting the bytes that really cross the network),
	// so the data arrives steadily instead of in bursts; a gzip-packed upload is unpacked afterwards
	var wire bytes.Buffer
	chunk := make([]byte, 32<<10)
	lr := io.LimitReader(r.Body, 64<<20) // a gzip-packed batch of 10,000 pages is about 8 MB
	var err error
	for {
		n, rerr := lr.Read(chunk)
		if n > 0 {
			ingestLimiter.wait(n)
			wire.Write(chunk[:n])
		}
		if rerr != nil {
			if rerr != io.EOF {
				err = rerr
			}
			break
		}
	}
	body := wire.Bytes()
	if err == nil && r.Header.Get("Content-Encoding") == "gzip" {
		var zr *gzip.Reader
		if zr, err = gzip.NewReader(bytes.NewReader(body)); err == nil {
			body, err = io.ReadAll(io.LimitReader(zr, 256<<20))
		}
	}
	if err != nil {
		http.Error(w, `{"error":"read failed"}`, 400)
		return
	}
	if budgetAdd(int64(wire.Len())) {
		w.Header().Set("Retry-After", "3600")
		http.Error(w, `{"error":"daily data limit reached"}`, 429)
		return
	}
	// one page per request is the normal case; a JSON list is accepted too
	var pages []Page
	if t := strings.TrimSpace(string(body)); strings.HasPrefix(t, "[") {
		err = json.Unmarshal(body, &pages)
	} else {
		var p Page
		err = json.Unmarshal(body, &p)
		pages = []Page{p}
	}
	if err != nil {
		http.Error(w, `{"error":"bad json"}`, 400)
		return
	}
	// a big batch (thousands of pages) is answered at once and stored in the background: storing runs at the page speed limit, and a proxy
	// in front of this API (the Cloudflare tunnel) gives up with HTTP 524 after 100 seconds. A full queue answers 503: the sender tries again.
	if len(pages) > 200 {
		select {
		case ingestQ <- pages:
			json.NewEncoder(w).Encode(map[string]int{"queued": len(pages)})
		default:
			w.Header().Set("Retry-After", "30")
			http.Error(w, `{"error":"busy, try again later"}`, 503)
		}
		return
	}
	added, updated, dup, bad, perr := processPages(pages)
	if perr != nil {
		http.Error(w, `{"error":"could not save"}`, 500)
		return
	}
	json.NewEncoder(w).Encode(map[string]int{"added": added, "updated": updated, "duplicates": dup, "rejected": bad, "total": set.Len()})
}

// the batches waiting to be stored (at most 8 at a time; one worker stores them one after the other)
var ingestQ = make(chan []Page, 8)

func init() {
	go func() {
		for pages := range ingestQ {
			t0 := time.Now()
			added, updated, dup, bad, err := processPages(pages)
			log.Printf("batch of %d pages stored in %s: %d added, %d updated, %d duplicates, %d rejected, error %v", len(pages), time.Since(t0).Round(time.Second), added, updated, dup, bad, err)
			mu.Lock()
			total := set.Len()
			mu.Unlock()
			saveBatch(batchRec{At: time.Now().UnixMilli(), Pages: len(pages), Added: added, Updated: updated, Dup: dup, Bad: bad, Ms: time.Since(t0).Milliseconds(), Total: total})
		}
	}()
}

// every batch that was stored is written down in /media/data/mysearch/batches.json (all batches, oldest first), which the /live page reads
type batchRec struct {
	Seq     int   `json:"seq"`
	At      int64 `json:"at"` // when the storing ended, milliseconds
	Pages   int   `json:"pages"`
	Added   int   `json:"added"`
	Updated int   `json:"updated"`
	Dup     int   `json:"duplicates"`
	Bad     int   `json:"rejected"`
	Ms      int64 `json:"ms"`    // how long storing took
	Total   int   `json:"total"` // pages in the index afterwards
}

var batchFile = fsData + "/batches.json"

var (
	batchMu  sync.Mutex
	batchLog []batchRec
)

func loadBatches() {
	if b, err := os.ReadFile(batchFile); err == nil {
		json.Unmarshal(b, &batchLog)
	}
}

func saveBatch(r batchRec) {
	batchMu.Lock()
	defer batchMu.Unlock()
	r.Seq = 1
	if n := len(batchLog); n > 0 {
		r.Seq = batchLog[n-1].Seq + 1
	}
	batchLog = append(batchLog, r)
	b, err := json.Marshal(batchLog)
	if err != nil {
		return
	}
	tmp := batchFile + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, batchFile)
	}
}

// processPages stores pages (at the page speed limit) and says how many were added, updated, duplicates and rejected
func processPages(pages []Page) (added, updated, dup, bad int, err error) {
	mu.Lock()
	defer mu.Unlock()
	for _, p := range pages {
		mu.Unlock() // wait for this page's turn without holding the lock
		pageLimiter.wait(1)
		mu.Lock()
		u, ok := normalize(p.URL)
		if !ok || strings.TrimSpace(p.Title+p.Text) == "" {
			bad++
			continue
		}
		submitted := submittedHost(hostOfURL(u))
		uh := fnv64(u)
		ch := fnv64(clip(p.Title, 200) + "\n" + clip(p.Description, 300) + "\n" + clip(p.Text, 2000))
		if !set.Add(u) {
			if !submitted {
				dup++
				continue
			}
			rcMu.Lock()
			old, known := rcHash[uh]
			rcMu.Unlock()
			if known && old == ch {
				dup++ // the page has not changed since it was stored
				continue
			}
			updated++ // changed (or never fingerprinted): store it again as a new version
		}
		line, _ := json.Marshal(Stored{URL: u, Title: clip(p.Title, 200), Description: clip(p.Description, 300),
			Z: sgpack.PackText(clip(p.Text, 2000)), Source: clip(p.Source, 20)})
		// one whole line in one write; if it fails half way (disk full ...) the half line is cut off again,
		// so the next page can never be glued onto it
		b := append(line, '\n')
		if n, werr := out.Write(b); werr != nil || n != len(b) {
			out.Truncate(outSize)
			return added, updated, dup, bad, io.ErrShortWrite
		}
		outSize += int64(len(b))
		added++
		if submitted {
			rememberHash(uh, ch)
		}
	}
	return added, updated, dup, bad, nil
}

func stats(w http.ResponseWriter, r *http.Request) {
	if !authed(r) {
		http.Error(w, `{"error":"bad token"}`, 401)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	json.NewEncoder(w).Encode(map[string]int{"pages": set.Len()})
}

func main() {
	loadBatches()
	budgetLoad()
	loadRecrawl()
	data := os.Getenv("MYSEARCH_DATA")
	if data == "" {
		data = fsData
	}
	tb, err := os.ReadFile(fsSecrets + "/mysearch_token")
	if err != nil || len(strings.TrimSpace(string(tb))) < 20 {
		log.Fatal("missing or short " + fsSecrets + "/mysearch_token")
	}
	token = strings.TrimSpace(string(tb))
	path := data + "/pages4.jsonl"
	set, err = sgseen.Open(data, path)
	if err != nil {
		log.Fatal(err)
	}
	repairTail(path)
	out, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	if st, err := out.Stat(); err == nil {
		outSize = st.Size()
	}
	http.HandleFunc("/api/ingest", ingest)
	http.HandleFunc("/api/forum/known", forumKnown)
	http.HandleFunc("/api/forum/posts", forumPosts)
	http.HandleFunc("/api/amazon/known", amazonKnown)
	http.HandleFunc("/api/amazon/products", amazonProducts)
	http.HandleFunc("/api/roblox/known", robloxKnown)
	http.HandleFunc("/api/roblox/games", robloxGames)
	contribInit()
	http.HandleFunc("/api/contrib/", contribRouter) // open crawling: contributors' tokens, tickets and uploads (contrib.go)
	http.HandleFunc("/api/stats", stats)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	log.Printf("ingest API on %s, %d addresses known (on disk), pages in %s", ingestAddr, set.Len(), path)
	log.Fatal((&http.Server{Addr: ingestAddr, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 3 * time.Minute}).ListenAndServe())
}
