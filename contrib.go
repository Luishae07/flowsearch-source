package main

// Open crawling: people run the official crawler on their own machines and send pages to this server. Everything is checked HERE, because a program that runs on
// somebody else's computer can always be changed; the build hash only catches old or accidentally changed copies.
//
//   an operator makes a contributor        POST /api/contribute/admin/create   (the main ingest token)  -> a personal token, shown once
//   a contributor asks for work            POST /api/contribute/ticket         -> a signed ticket with a slice of the seed list and the SHA-256 of that slice
//   the crawler sends its pages            POST /api/contribute/submit         (token + ticket) -> every page must be on a site of the slice
//   the crawler reports that it is done    POST /api/contribute/complete
//   anyone can read                        GET  /api/contribute/policy, GET /api/contribute/stats
//
// The work is a queue. The seed list is cut into slices of sites. A contributor who asks gets the first free slice (the lowest number); a slice whose
// ticket ran out goes back to the front. When no slice is free, the contributors who ask wait in line and are served in the order they came.
//
// The pages of a new contributor are held back (quarantine) until a sample of them has been fetched again by this server (contribverify.go) and matched.
// A contributor whose samples keep matching becomes trusted and their pages are stored at once (and still sampled afterwards).
//
// Files, all in <data folder>/contrib/: seeds.txt (the sites to crawl, one per line), config.json (the settings, all optional), blocklist.txt (hosts that are never
// accepted), contributors.json, assignments.json, quarantine/. The key that signs the tickets is <secrets folder>/contrib_hmac_key (made on first start).

import (
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type contribConfig struct {
	SliceSize       int      `json:"slice_size"`            // sites in one slice
	TicketHours     int      `json:"ticket_hours"`          // how long a ticket is valid
	MaxPagesPerSite int      `json:"max_pages_per_site"`    // the crawler stops a site after this many pages
	MaxPagesTicket  int      `json:"max_pages_per_ticket"`  // most pages one ticket may send
	DelayMs         int      `json:"delay_ms"`              // pause after every page, per site
	Lang            string   `json:"lang"`                  // en, de or any
	DailyCap        int      `json:"daily_cap"`             // most pages one contributor may send in a day
	TrustedBatches  int      `json:"trusted_batches"`       // batches that must match before a contributor's pages are stored at once
	SamplePercent   int      `json:"sample_percent"`        // how much of a batch is fetched again
	MinVersion      string   `json:"min_version"`           // oldest client version that may ask for work
	AllowedClients  []string `json:"allowed_client_sha256"` // build hashes of the official client (empty: any build)
	AllowedCrawlers []string `json:"allowed_crawler_sha256"`
	RecycleDays     int      `json:"recycle_days"`      // a finished slice comes up again after this many days
	IPPerMinute     int      `json:"ip_per_minute"`     // requests one IP address may make to the contributor API in a minute (all kinds together)
	TicketPerMinute int      `json:"ticket_per_minute"` // of those, requests for work
	AdminPerMinute  int      `json:"admin_per_minute"`  // of those, admin calls
	BadAuthLimit    int      `json:"bad_auth_limit"`    // wrong tokens from one IP address within 10 minutes before it is blocked
	BlockMinutes    int      `json:"block_minutes"`     // how long such an address is blocked
}

func defaultContribConfig() contribConfig {
	return contribConfig{SliceSize: 50, TicketHours: 6, MaxPagesPerSite: 200, MaxPagesTicket: 20000, DelayMs: 1000, Lang: "en", DailyCap: 50000,
		TrustedBatches: 3, SamplePercent: 5, MinVersion: "1.0.0", RecycleDays: 30,
		IPPerMinute: 150, TicketPerMinute: 12, AdminPerMinute: 30, BadAuthLimit: 10, BlockMinutes: 15}
}

type contributor struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	TokenHash  string `json:"token_hash"`
	Created    int64  `json:"created"`
	Status     string `json:"status"` // active, revoked or suspended
	OK         int    `json:"ok"`     // sampled pages that matched
	Fail       int    `json:"fail"`   // sampled pages that did not
	BatchesOK  int    `json:"batches_ok"`
	Pages      int64  `json:"pages"` // pages accepted
	Day        string `json:"day"`
	DayPages   int    `json:"day_pages"`
	LastSeen   int64  `json:"last_seen"`
	Client     string `json:"client,omitempty"`
	ClientSHA  string `json:"client_sha256,omitempty"`
	CrawlerSHA string `json:"crawler_sha256,omitempty"`
	recent     []int64
}

func (c *contributor) trust() float64 { return float64(c.OK+1) / float64(c.OK+c.Fail+2) }

func (c *contributor) trusted(cfg contribConfig) bool {
	return c.BatchesOK >= cfg.TrustedBatches && c.trust() >= 0.85
}

type assignment struct {
	Slice   int      `json:"slice"`
	Contrib string   `json:"contributor"`
	Ticket  string   `json:"ticket"`
	Issued  int64    `json:"issued"`
	Expires int64    `json:"expires"`
	State   string   `json:"state"` // open or done
	DoneAt  int64    `json:"done_at,omitempty"`
	Pages   int      `json:"pages"`
	Seeds   []string `json:"seeds"` // the sites of the slice at the time the ticket was made
}

type waiter struct {
	ID   string
	Seen int64
}

type ticketData struct {
	V          int    `json:"v"`
	ID         string `json:"id"`
	Contrib    string `json:"c"`
	Slice      int    `json:"slice"`
	SeedsSHA   string `json:"seeds_sha256"`
	Issued     int64  `json:"issued"`
	Expires    int64  `json:"expires"`
	MaxPages   int    `json:"max_pages"`
	MaxPerSite int    `json:"max_pages_per_site"`
	DelayMs    int    `json:"delay_ms"`
	Lang       string `json:"lang"`
}

var (
	cMu       sync.Mutex
	cfg       = defaultContribConfig()
	cfgAt     time.Time
	contribs  = map[string]*contributor{} // by id
	assigns   = map[int]*assignment{}
	waiters   []waiter
	seedList  []string
	seedAt    time.Time
	hmacKey   []byte
	blockList map[string]bool
	blockAt   time.Time
	cDir      = ""
)

func sha256hex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func contribInit() {
	cDir = fsData + "/contrib"
	os.MkdirAll(cDir+"/quarantine", 0o755)
	keyPath := fsSecrets + "/contrib_hmac_key"
	if b, err := os.ReadFile(keyPath); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		hmacKey, _ = hex.DecodeString(strings.TrimSpace(string(b)))
		if len(hmacKey) < 16 {
			hmacKey = []byte(strings.TrimSpace(string(b)))
		}
	} else {
		k := randHex(32)
		os.MkdirAll(fsSecrets, 0o700)
		if err := os.WriteFile(keyPath, []byte(k+"\n"), 0o600); err != nil {
			log.Printf("contrib: could not save the ticket key (%v); tickets will not survive a restart", err)
		}
		hmacKey, _ = hex.DecodeString(k)
	}
	if b, err := os.ReadFile(cDir + "/contributors.json"); err == nil {
		var l []*contributor
		if json.Unmarshal(b, &l) == nil {
			for _, c := range l {
				contribs[c.ID] = c
			}
		}
	}
	if b, err := os.ReadFile(cDir + "/assignments.json"); err == nil {
		var l []*assignment
		if json.Unmarshal(b, &l) == nil {
			for _, a := range l {
				assigns[a.Slice] = a
			}
		}
	}
	go verifyLoop()
	log.Printf("contrib: %d contributors, %d slices assigned; contributor API on /api/contribute/", len(contribs), len(assigns))
}

func saveJSON(path string, v any) {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, path)
	}
}

// saveContribs and saveAssigns are called with cMu held.
func saveContribs() {
	l := make([]*contributor, 0, len(contribs))
	for _, c := range contribs {
		l = append(l, c)
	}
	sort.Slice(l, func(i, j int) bool { return l[i].Created < l[j].Created })
	saveJSON(cDir+"/contributors.json", l)
}

func saveAssigns() {
	l := make([]*assignment, 0, len(assigns))
	for _, a := range assigns {
		l = append(l, a)
	}
	sort.Slice(l, func(i, j int) bool { return l[i].Slice < l[j].Slice })
	saveJSON(cDir+"/assignments.json", l)
}

// loadConfig, loadSeeds and loadBlock re-read their file at most once a minute. Called with cMu held.
func loadConfig() contribConfig {
	if time.Since(cfgAt) > time.Minute {
		c := defaultContribConfig()
		if b, err := os.ReadFile(cDir + "/config.json"); err == nil {
			json.Unmarshal(b, &c)
		}
		if c.SliceSize < 1 {
			c.SliceSize = 50
		}
		if c.TicketHours < 1 {
			c.TicketHours = 6
		}
		cfg, cfgAt = c, time.Now()
	}
	return cfg
}

func cleanSeed(l string) string {
	l = strings.ToLower(strings.TrimSpace(strings.SplitN(l, "\t", 2)[0]))
	l = strings.TrimPrefix(strings.TrimPrefix(l, "https://"), "http://")
	if i := strings.IndexAny(l, "/?#"); i >= 0 {
		l = l[:i]
	}
	l = strings.TrimPrefix(l, "www.")
	if l == "" || strings.ContainsAny(l, " @:") || !strings.Contains(l, ".") {
		return ""
	}
	return l
}

func loadSeeds() []string {
	if time.Since(seedAt) > time.Minute || seedList == nil {
		var out []string
		seen := map[string]bool{}
		if b, err := os.ReadFile(cDir + "/seeds.txt"); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if s := cleanSeed(l); s != "" && !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
		}
		seedList, seedAt = out, time.Now()
		if out == nil {
			seedList = []string{}
		}
	}
	return seedList
}

func blocked(host string) bool {
	cMu.Lock()
	defer cMu.Unlock()
	if time.Since(blockAt) > time.Minute || blockList == nil {
		m := map[string]bool{}
		if b, err := os.ReadFile(cDir + "/blocklist.txt"); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if s := cleanSeed(l); s != "" {
					m[s] = true
				}
			}
		}
		blockList, blockAt = m, time.Now()
	}
	for h := host; h != ""; {
		if blockList[h] {
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

func sliceSeeds(list []string, k, size int) []string {
	lo := k * size
	if lo >= len(list) {
		return nil
	}
	hi := lo + size
	if hi > len(list) {
		hi = len(list)
	}
	return list[lo:hi]
}

func seedsText(s []string) string { return strings.Join(s, "\n") + "\n" }

// ---- tickets: "t1." + base64url(JSON) + "." + base64url(HMAC-SHA256 of the JSON)

func signTicket(t ticketData) string {
	b, _ := json.Marshal(t)
	m := hmac.New(sha256.New, hmacKey)
	m.Write(b)
	return "t1." + base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func parseTicket(s string) (ticketData, bool) {
	var t ticketData
	p := strings.Split(strings.TrimSpace(s), ".")
	if len(p) != 3 || p[0] != "t1" {
		return t, false
	}
	b, err1 := base64.RawURLEncoding.DecodeString(p[1])
	mac, err2 := base64.RawURLEncoding.DecodeString(p[2])
	if err1 != nil || err2 != nil {
		return t, false
	}
	m := hmac.New(sha256.New, hmacKey)
	m.Write(b)
	if subtle.ConstantTimeCompare(m.Sum(nil), mac) != 1 || json.Unmarshal(b, &t) != nil || t.V != 1 {
		return t, false
	}
	return t, true
}

// ---- who is asking

func contribAuth(r *http.Request) (*contributor, int, string) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(tok, "fsc_") {
		return nil, 401, "bad token"
	}
	h := sha256hex([]byte(tok))
	cMu.Lock()
	defer cMu.Unlock()
	for _, c := range contribs {
		if subtle.ConstantTimeCompare([]byte(c.TokenHash), []byte(h)) == 1 {
			if c.Status != "active" {
				return nil, 403, "this account is " + c.Status
			}
			c.LastSeen = time.Now().Unix()
			return c, 0, ""
		}
	}
	return nil, 401, "bad token"
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func verNum(v string) [3]int {
	var n [3]int
	for i, p := range strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3) {
		n[i], _ = strconv.Atoi(strings.TrimSpace(p))
	}
	return n
}

func verLess(a, b string) bool {
	x, y := verNum(a), verNum(b)
	for i := 0; i < 3; i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return false
}

func inList(l []string, v string) bool {
	for _, x := range l {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

// ---- the router

func contribRouter(w http.ResponseWriter, r *http.Request) {
	switch strings.TrimPrefix(r.URL.Path, "/api/contribute/") {
	case "policy":
		contribPolicy(w, r)
	case "stats":
		contribStats(w, r)
	case "ticket":
		contribTicket(w, r)
	case "submit":
		contribSubmit(w, r)
	case "complete":
		contribComplete(w, r)
	case "admin/create":
		contribAdminCreate(w, r)
	case "admin/revoke":
		contribAdminRevoke(w, r)
	case "admin/list":
		contribAdminList(w, r)
	default:
		http.NotFound(w, r)
	}
}

const contribPolicyText = `Flowsearch open crawling - the rules

* You run the official Flowsearch crawler (the fscontrib program with the sgcrawl crawler). You get a personal token from the operator.
* The crawler asks for work and gets a slice of sites. It may only crawl those sites; the server rejects pages from any other site.
* The crawler obeys robots.txt, waits between requests and identifies itself in its User-Agent. A page the server finds is forbidden by robots.txt counts against you.
* Your computer's IP address is what the crawled sites see. Do not run the crawler on a network where that is a problem.
* Only plain text is accepted: no HTML, no scripts, no personal data of private people. The server may drop pages that break these rules.
* Your first batches are held back until the server has fetched a sample of them again and the text matches. After that your pages are stored at once, and a
  sample is still checked. If samples do not match, your pages are dropped and the account can be suspended.
* There is a daily limit of pages for every account. The operator can revoke a token at any time.
* Pages are used for the Flowsearch search index. To have a site removed, contact the operator.
`

func contribPolicy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, contribPolicyText)
}

func contribStats(w http.ResponseWriter, r *http.Request) {
	cMu.Lock()
	defer cMu.Unlock()
	c := loadConfig()
	list := loadSeeds()
	slices := 0
	if len(list) > 0 {
		slices = (len(list) + c.SliceSize - 1) / c.SliceSize
	}
	done, open := 0, 0
	for _, a := range assigns {
		if a.State == "done" {
			done++
		} else if a.Expires > time.Now().Unix() {
			open++
		}
	}
	type row struct {
		Name  string  `json:"name"`
		Pages int64   `json:"pages"`
		Trust float64 `json:"trust"`
	}
	var board []row
	var total int64
	active := 0
	for _, x := range contribs {
		total += x.Pages
		if x.Status == "active" {
			active++
		}
		board = append(board, row{x.Name, x.Pages, float64(int(x.trust()*100)) / 100})
	}
	sort.Slice(board, func(i, j int) bool { return board[i].Pages > board[j].Pages })
	if len(board) > 20 {
		board = board[:20]
	}
	q, _ := filepath.Glob(cDir + "/quarantine/*.json")
	jsonOut(w, map[string]any{"contributors": active, "pages_accepted": total, "slices": slices, "slices_done": done, "slices_in_progress": open,
		"waiting_in_line": len(waiters), "batches_in_quarantine": len(q), "leaderboard": board})
}

// ---- admin (the main ingest token)

func contribAdminCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !authed(r) {
		jsonErr(w, 401, "POST with the main token")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 40 {
		jsonErr(w, 400, "give a name of 1 to 40 characters")
		return
	}
	tok := "fsc_" + base64.RawURLEncoding.EncodeToString(func() []byte { b := make([]byte, 32); rand.Read(b); return b }())
	c := &contributor{ID: randHex(6), Name: name, TokenHash: sha256hex([]byte(tok)), Created: time.Now().Unix(), Status: "active"}
	cMu.Lock()
	contribs[c.ID] = c
	saveContribs()
	cMu.Unlock()
	log.Printf("contrib: new contributor %s (%s)", c.ID, c.Name)
	jsonOut(w, map[string]string{"id": c.ID, "name": c.Name, "token": tok, "note": "this token is shown once; it is stored only as a hash"})
}

func contribAdminRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !authed(r) {
		jsonErr(w, 401, "POST with the main token")
		return
	}
	var req struct {
		ID     string `json:"id"`
		Status string `json:"status"` // revoked (default), suspended or active
	}
	json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	if req.Status == "" {
		req.Status = "revoked"
	}
	if req.Status != "revoked" && req.Status != "suspended" && req.Status != "active" {
		jsonErr(w, 400, "status must be revoked, suspended or active")
		return
	}
	cMu.Lock()
	defer cMu.Unlock()
	c := contribs[req.ID]
	if c == nil {
		jsonErr(w, 404, "no such contributor")
		return
	}
	c.Status = req.Status
	saveContribs()
	jsonOut(w, map[string]string{"id": c.ID, "status": c.Status})
}

func contribAdminList(w http.ResponseWriter, r *http.Request) {
	if !authed(r) {
		jsonErr(w, 401, "the main token is needed")
		return
	}
	cMu.Lock()
	defer cMu.Unlock()
	type row struct {
		ID, Name, Status string
		Pages            int64
		OK, Fail         int
		BatchesOK        int
		Trust            float64
		Trusted          bool
		Client           string
		LastSeen         int64
	}
	c := loadConfig()
	var l []row
	for _, x := range contribs {
		l = append(l, row{x.ID, x.Name, x.Status, x.Pages, x.OK, x.Fail, x.BatchesOK, x.trust(), x.trusted(c), x.Client, x.LastSeen})
	}
	sort.Slice(l, func(i, j int) bool { return l[i].Pages > l[j].Pages })
	jsonOut(w, l)
}

// ---- asking for work

func contribTicket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, 405, "POST only")
		return
	}
	c, code, msg := contribAuth(r)
	if c == nil {
		jsonErr(w, code, msg)
		return
	}
	var req struct {
		Client     string `json:"client_version"`
		ClientSHA  string `json:"client_sha256"`
		CrawlerSHA string `json:"crawler_sha256"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req)

	cMu.Lock()
	defer cMu.Unlock()
	conf := loadConfig()
	if conf.MinVersion != "" && (req.Client == "" || verLess(req.Client, conf.MinVersion)) {
		jsonErr(w, 426, "this version of the client is too old; the oldest allowed is "+conf.MinVersion+". Download the current release.")
		return
	}
	if len(conf.AllowedClients) > 0 && !inList(conf.AllowedClients, req.ClientSHA) {
		jsonErr(w, 403, "this build of the client is not an official release")
		return
	}
	if len(conf.AllowedCrawlers) > 0 && !inList(conf.AllowedCrawlers, req.CrawlerSHA) {
		jsonErr(w, 403, "this build of the crawler is not an official release")
		return
	}
	c.Client, c.ClientSHA, c.CrawlerSHA = req.Client, req.ClientSHA, req.CrawlerSHA
	now := time.Now().Unix()

	// the contributor still has a valid ticket: give the same one again
	for _, a := range assigns {
		if a.Contrib == c.ID && a.State == "open" && a.Expires > now {
			contribTicketReply(w, c, a, conf)
			return
		}
	}
	list := loadSeeds()
	if len(list) == 0 {
		jsonErr(w, 503, "there is no work yet: the operator has not added a seed list")
		return
	}
	// the waiting line: people who asked while there was no work, in the order they came
	kept := waiters[:0]
	for _, x := range waiters {
		if now-x.Seen < 180 && contribs[x.ID] != nil {
			kept = append(kept, x)
		}
	}
	waiters = kept
	pos := -1
	for i, x := range waiters {
		if x.ID == c.ID {
			pos = i
			waiters[i].Seen = now
		}
	}
	free := freeSlice(list, conf, now)
	if free < 0 || (len(waiters) > 0 && waiters[0].ID != c.ID) {
		if pos < 0 {
			waiters = append(waiters, waiter{c.ID, now})
			pos = len(waiters) - 1
		}
		w.WriteHeader(202)
		jsonOut(w, map[string]any{"wait": true, "position": pos + 1, "retry_after": 15,
			"message": "all work is taken or other contributors are ahead of you; you are number " + strconv.Itoa(pos+1) + " in line. Ask again in 15 seconds."})
		return
	}
	if pos >= 0 {
		waiters = append(waiters[:pos], waiters[pos+1:]...)
	}
	seeds := append([]string(nil), sliceSeeds(list, free, conf.SliceSize)...)
	a := &assignment{Slice: free, Contrib: c.ID, Issued: now, Expires: now + int64(conf.TicketHours)*3600, State: "open", Seeds: seeds}
	a.Ticket = randHex(8)
	assigns[free] = a
	saveAssigns()
	saveContribs()
	log.Printf("contrib: slice %d (%d sites) goes to %s", free, len(seeds), c.Name)
	contribTicketReply(w, c, a, conf)
}

// freeSlice: the lowest slice that nobody holds, whose ticket ran out, or that was finished long ago. -1 when there is none. Called with cMu held.
func freeSlice(list []string, conf contribConfig, now int64) int {
	n := (len(list) + conf.SliceSize - 1) / conf.SliceSize
	for k := 0; k < n; k++ {
		a := assigns[k]
		if a == nil {
			return k
		}
		if a.State == "open" && a.Expires <= now {
			return k
		}
		if a.State == "done" && conf.RecycleDays > 0 && now-a.DoneAt > int64(conf.RecycleDays)*86400 {
			return k
		}
	}
	return -1
}

func contribTicketReply(w http.ResponseWriter, c *contributor, a *assignment, conf contribConfig) {
	t := ticketData{V: 1, ID: a.Ticket, Contrib: c.ID, Slice: a.Slice, SeedsSHA: sha256hex([]byte(seedsText(a.Seeds))), Issued: a.Issued, Expires: a.Expires,
		MaxPages: conf.MaxPagesTicket, MaxPerSite: conf.MaxPagesPerSite, DelayMs: conf.DelayMs, Lang: conf.Lang}
	jsonOut(w, map[string]any{"ticket": signTicket(t), "slice": a.Slice, "expires": a.Expires, "seeds": seedsText(a.Seeds), "seeds_sha256": t.SeedsSHA,
		"max_pages": t.MaxPages, "max_pages_per_site": t.MaxPerSite, "delay_ms": t.DelayMs, "lang": t.Lang})
}

// ---- sending pages

func hostAllowed(host string, seeds []string) bool {
	for _, s := range seeds {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

func looksLikeHTML(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "<script") || strings.Contains(l, "<iframe") || strings.Count(l, "</") > 10 || strings.Contains(l, "javascript:")
}

func contribSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, 405, "POST only")
		return
	}
	c, code, msg := contribAuth(r)
	if c == nil {
		jsonErr(w, code, msg)
		return
	}
	t, ok := parseTicket(r.Header.Get("X-Flowsearch-Ticket"))
	if !ok {
		jsonErr(w, 403, "the ticket is missing or not valid")
		return
	}
	now := time.Now().Unix()
	if t.Contrib != c.ID {
		jsonErr(w, 403, "this ticket belongs to another contributor")
		return
	}
	if t.Expires < now {
		jsonErr(w, 403, "the ticket has expired; ask for new work")
		return
	}
	cMu.Lock()
	a := assigns[t.Slice]
	conf := loadConfig()
	var seeds []string
	if a != nil && a.Ticket == t.ID && a.Contrib == c.ID && a.State == "open" {
		seeds = a.Seeds
	}
	// at most 120 uploads a minute
	cut := now - 60
	kept := c.recent[:0]
	for _, x := range c.recent {
		if x > cut {
			kept = append(kept, x)
		}
	}
	c.recent = kept
	tooFast := len(c.recent) >= 120
	if !tooFast {
		c.recent = append(c.recent, now)
	}
	day := time.Now().UTC().Format("2006-01-02")
	if c.Day != day {
		c.Day, c.DayPages = day, 0
	}
	room := conf.DailyCap - c.DayPages
	sentSoFar := 0
	if a != nil {
		sentSoFar = a.Pages
	}
	cMu.Unlock()
	if seeds == nil {
		jsonErr(w, 403, "this ticket is no longer open")
		return
	}
	if tooFast {
		w.Header().Set("Retry-After", "30")
		jsonErr(w, 429, "too many uploads; wait a little")
		return
	}
	if room <= 0 || sentSoFar >= t.MaxPages {
		jsonErr(w, 429, "the limit of pages is reached (for today, or for this ticket)")
		return
	}

	lr := io.LimitReader(r.Body, 16<<20)
	var body []byte
	var err error
	if r.Header.Get("Content-Encoding") == "gzip" {
		var zr *gzip.Reader
		if zr, err = gzip.NewReader(lr); err == nil {
			body, err = io.ReadAll(io.LimitReader(zr, 64<<20))
		}
	} else {
		body, err = io.ReadAll(lr)
	}
	var pages []Page
	if err == nil {
		if t := strings.TrimSpace(string(body)); strings.HasPrefix(t, "{") { // one page
			var one Page
			if err = json.Unmarshal(body, &one); err == nil {
				pages = []Page{one}
			}
		} else {
			err = json.Unmarshal(body, &pages)
		}
	}
	if err != nil {
		jsonErr(w, 400, "the body must be a page or a JSON list of pages")
		return
	}
	if len(pages) > 2000 {
		jsonErr(w, 400, "at most 2000 pages in one upload")
		return
	}
	if len(pages) > room {
		pages = pages[:room]
	}
	if len(pages) > t.MaxPages-sentSoFar {
		pages = pages[:t.MaxPages-sentSoFar]
	}
	var good []Page
	rejected := map[string]int{}
	for _, p := range pages {
		u, ok := normalize(p.URL)
		pu, perr := url.Parse(u)
		switch {
		case !ok || perr != nil || len(u) > 2000 || pu.User != nil:
			rejected["bad address"]++
			continue
		case !hostAllowed(hostOfURL(u), seeds):
			rejected["not a site of this ticket"]++
			continue
		case blocked(hostOfURL(u)):
			rejected["blocked site"]++
			continue
		case strings.TrimSpace(p.Title+p.Text) == "":
			rejected["empty"]++
			continue
		case looksLikeHTML(p.Title) || looksLikeHTML(p.Text) || looksLikeHTML(p.Description):
			rejected["markup or script in the text"]++
			continue
		}
		good = append(good, Page{URL: u, Title: clip(p.Title, 300), Description: clip(p.Description, 500), Text: clip(p.Text, 5000), Source: "contrib", Received: now})
	}
	reply := map[string]any{"accepted": len(good), "rejected": rejected}
	if len(good) == 0 {
		jsonOut(w, reply)
		return
	}
	cMu.Lock()
	c.DayPages += len(good)
	c.Pages += int64(len(good))
	a.Pages += len(good)
	trusted := c.trusted(conf)
	saveContribs()
	saveAssigns()
	cMu.Unlock()
	if trusted {
		if err := contribStore(good, false); err != nil {
			w.Header().Set("Retry-After", "30")
			jsonErr(w, 503, "busy, try again later")
			return
		}
		go auditSample(c.ID, good)
		reply["state"] = "stored"
	} else {
		if err := quarantine(c.ID, t.ID, good); err != nil {
			jsonErr(w, 500, "could not save")
			return
		}
		reply["state"] = "held back until a sample has been checked"
	}
	jsonOut(w, reply)
}

// contribStore stores pages the way the ingest does. wait: a big batch waits for room in the queue (the verifier may); otherwise a full queue is an error.
func contribStore(pages []Page, wait bool) error {
	if len(pages) > 200 {
		if wait {
			ingestQ <- pages
			return nil
		}
		select {
		case ingestQ <- pages:
			return nil
		default:
			return fmt.Errorf("busy")
		}
	}
	_, _, _, _, err := processPages(pages)
	return err
}

type qBatch struct {
	Contrib string `json:"contributor"`
	Ticket  string `json:"ticket"`
	Made    int64  `json:"made"`
	Pages   []Page `json:"pages"`
}

func quarantine(cid, ticket string, pages []Page) error {
	b, err := json.Marshal(qBatch{cid, ticket, time.Now().Unix(), pages})
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%s/quarantine/%013d-%s-%s.json", cDir, time.Now().UnixMilli(), cid, randHex(3))
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}

func contribComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, 405, "POST only")
		return
	}
	c, code, msg := contribAuth(r)
	if c == nil {
		jsonErr(w, code, msg)
		return
	}
	t, ok := parseTicket(r.Header.Get("X-Flowsearch-Ticket"))
	if !ok || t.Contrib != c.ID {
		jsonErr(w, 403, "the ticket is missing or not valid")
		return
	}
	cMu.Lock()
	defer cMu.Unlock()
	a := assigns[t.Slice]
	if a == nil || a.Ticket != t.ID || a.Contrib != c.ID {
		jsonErr(w, 403, "this ticket is not open")
		return
	}
	if a.State != "done" {
		a.State, a.DoneAt = "done", time.Now().Unix()
		saveAssigns()
		log.Printf("contrib: slice %d finished by %s with %d pages", a.Slice, c.Name, a.Pages)
	}
	jsonOut(w, map[string]any{"done": true, "pages": a.Pages})
}

var _ = bytes.MinRead
