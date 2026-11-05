package main

// Surprise me must not send anyone to an adult site. A page is checked in two steps: the blocklist search already uses, and
// a Jev yes/no question ("is this adult content?") on its title, address and description. The verdict is kept per site for
// a day. If Jev does not answer, the page is not used (the button just draws another one).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

type safeVerdict struct {
	ok bool
	at time.Time
}

var (
	safeMu    sync.Mutex
	safeCache = map[string]safeVerdict{}
)

func surpriseSafe(host, title, addr, desc string) bool {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	if host == "" || isBlocked(host) || adultPage(host, title, desc) {
		return false
	}
	cacheKey := host
	if strings.HasSuffix(host, "youtube.com") || strings.HasSuffix(host, "amazon.de") || strings.HasSuffix(host, "amazon.com") {
		cacheKey = addr // a site of many unrelated videos: every video is judged on its own
	}
	safeMu.Lock()
	if v, ok := safeCache[cacheKey]; ok && time.Since(v.at) < 24*time.Hour {
		safeMu.Unlock()
		return v.ok
	}
	safeMu.Unlock()
	key := jevKeyValue()
	if key == "" {
		return false
	}
	state := fmt.Sprintf("Site: %s\nAddress: %s\nTitle: %s\nDescription: %s", host, clipRunes(addr, 200), clipRunes(title, 160), clipRunes(desc, 300))
	body, _ := json.Marshal(map[string]any{"model": jevModel, "state": state, "questions": map[string]any{
		"adult": map[string]any{
			"type":         "noul",
			"instructions": "Is this page adult content: pornography, sexual content, escort or dating-for-sex services, or a site only meant for adults?",
			"criteria": map[string]string{
				"true":  "Adult or sexual content, or not suitable for a general audience of all ages",
				"false": "Ordinary content, safe for everyone",
			},
		},
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", jevURL, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := jevClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out struct {
		Answers map[string]struct {
			Noul *float64 `json:"noul"`
		} `json:"answers"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil || out.Answers["adult"].Noul == nil {
		return false
	}
	ok := *out.Answers["adult"].Noul < 0.2 // anything doubtful is skipped
	safeMu.Lock()
	if len(safeCache) > 20000 {
		safeCache = map[string]safeVerdict{}
	}
	safeCache[cacheKey] = safeVerdict{ok, time.Now()}
	safeMu.Unlock()
	return ok
}

var surpriseSiteRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// surpriseSite sends the visitor to a random stored page of the site they typed (/surprise?site=wikipedia.org). Only
// pages Flowsearch holds are used, never the typed address itself, and every page is checked like any Surprise me page.
func surpriseSite(w http.ResponseWriter, r *http.Request, site, kind string) {
	w.Header().Set("Cache-Control", "no-store")
	site = strings.ToLower(strings.TrimSpace(site))
	site = strings.TrimPrefix(strings.TrimPrefix(site, "https://"), "http://")
	site = strings.TrimPrefix(strings.SplitN(site, "/", 2)[0], "www.")
	if !surpriseSiteRe.MatchString(site) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	_, total := ix.HostDocPage(site, 0, 1)
	if total == 0 {
		http.Redirect(w, r, "/search?q=site:"+site, http.StatusFound)
		return
	}
	switch kind {
	case "video":
		surpriseFromList(w, r, youtubeVideos(), "www.youtube.com", "/search?q=site:youtube.com")
		return
	case "mace":
		surpriseFromList(w, r, mcAsList(true), "www.youtube.com", "/minecraft")
		return
	case "minecraft":
		surpriseFromList(w, r, mcAsList(false), "www.youtube.com", "/minecraft")
		return
	case "music":
		surpriseFromList(w, r, readList(youtubeMusicFile), "www.youtube.com", "/search?q=site:youtube.com")
		return
	case "products":
		surpriseFromList(w, r, readList(amazonDeFile), "www.amazon.de", "/search?q=site:amazon.de")
		return
	case "connector":
		surpriseFromList(w, r, connectorPages(), "claude.com", "/search?q=site:claude.com/marketplace/connectors")
		return
	}
	deadline := time.Now().Add(8 * time.Second)
	off := 0
	if total > 200 {
		off = rand.Intn(total - 200)
	}
	ids, _ := ix.HostDocPage(site, off, 200)
	rand.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	for _, id := range ids {
		if time.Now().After(deadline) {
			break
		}
		d, ok := ix.Doc(id)
		if !ok {
			continue
		}
		rec := load(d)
		if rec.URL == "" || !strings.HasPrefix(rec.URL, "http") || strings.TrimSpace(rec.Title) == "" {
			continue
		}
		if !surpriseSafe(d.Host, rec.Title, rec.URL, rec.Description) {
			continue
		}
		http.Redirect(w, r, withReroll(rec.URL, r), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/search?q=site:"+site, http.StatusFound)
}

// Lists of pages for the buttons that a search cannot list (its site filter works on the best matches for the words, and other
// sites crowd these pages out). One JSON line each: u address, t title, d description. A list is read again when its file changes.
var (
	youtubeVideosFile = fsData + "/youtube-videos.jsonl"
	youtubeMusicFile  = fsData + "/youtube-music.jsonl"
	amazonDeFile      = fsData + "/amazon-de-products.jsonl"
)

type listItem struct{ U, T, D string }

var (
	listMu    sync.Mutex
	listMtime = map[string]time.Time{}
	listData  = map[string][]listItem{}
)

func readList(path string) []listItem {
	listMu.Lock()
	defer listMu.Unlock()
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if t, ok := listMtime[path]; ok && t.Equal(st.ModTime()) {
		return listData[path]
	}
	f, err := os.Open(path)
	if err != nil {
		return listData[path]
	}
	defer f.Close()
	var out []listItem
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var v listItem
		if json.Unmarshal(sc.Bytes(), &v) == nil && strings.HasPrefix(v.U, "https://") && !adultText(v.T) && !adultText(v.D) {
			out = append(out, v)
		}
	}
	listMtime[path], listData[path] = st.ModTime(), out
	return out
}

func youtubeVideos() []listItem {
	var out []listItem
	for _, v := range readList(youtubeVideosFile) {
		if strings.HasPrefix(v.U, "https://www.youtube.com/watch?v=") {
			out = append(out, v)
		}
	}
	return out
}

// surpriseFromList sends the visitor to a random page of a list, after the adult check; the host is what the check is told.
func surpriseFromList(w http.ResponseWriter, r *http.Request, items []listItem, host, fallback string) {
	items = append([]listItem(nil), items...) // a copy: the list is shared
	rand.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	deadline := time.Now().Add(8 * time.Second)
	for _, v := range items {
		if time.Now().After(deadline) {
			break
		}
		if surpriseSafe(host, v.T, v.U, v.D) {
			http.Redirect(w, r, withReroll(v.U, r), http.StatusFound)
			return
		}
	}
	http.Redirect(w, r, fallback, http.StatusFound)
}

const connectorPrefix = "https://claude.com/marketplace/connectors"

var (
	connMu   sync.Mutex
	connList []listItem
	connAt   time.Time
)

// connectorPages: the pages of Claude's connector marketplace (the MCP servers it lists) that Flowsearch has crawled. The pages of the
// host claude.com are looked through once per 30 minutes (at most 20 seconds) and the ones under /marketplace/connectors/ are kept.
func connectorPages() []listItem {
	connMu.Lock()
	defer connMu.Unlock()
	if !connAt.IsZero() && time.Since(connAt) < 30*time.Minute {
		return connList
	}
	deadline := time.Now().Add(20 * time.Second)
	var out []listItem
	_, total := ix.HostDocPage("claude.com", 0, 1)
	for off := 0; off < total && time.Now().Before(deadline); off += 500 {
		ids, _ := ix.HostDocPage("claude.com", off, 500)
		for _, id := range ids {
			d, ok := ix.Doc(id)
			if !ok {
				continue
			}
			rec := load(d)
			if u := strings.TrimRight(rec.URL, "/"); strings.HasPrefix(u, connectorPrefix+"/") && strings.TrimSpace(rec.Title) != "" {
				out = append(out, listItem{rec.URL, rec.Title, rec.Description})
			}
		}
	}
	connList, connAt = out, time.Now()
	return out
}

// apiYoutube: how many YouTube videos Flowsearch holds and 12 of them at random, for the Live page's YouTube tab.
func apiYoutube(w http.ResponseWriter, r *http.Request) {
	vids := append([]listItem(nil), youtubeVideos()...)
	rand.Shuffle(len(vids), func(i, j int) { vids[i], vids[j] = vids[j], vids[i] })
	type item struct {
		U string `json:"u"`
		T string `json:"t"`
	}
	sample := []item{}
	for _, v := range vids {
		if len(sample) == 12 {
			break
		}
		sample = append(sample, item{v.U, clip(v.T, 110)})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{"count": len(vids), "sample": sample})
}

// surpriseGo (only the main Surprise me button uses it) tells the visitor where Surprise me is sending them (title, site and full address) and then goes there after 4
// seconds; they can go at once, stop, or ask for another one.
func surpriseGo(w http.ResponseWriter, r *http.Request, target, title string) {
	w.Header().Set("Cache-Control", "no-store")
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	js, _ := json.Marshal(withReroll(target, r))
	again := r.URL.RequestURI()
	body := `<main class="wrap" style="max-width:720px;margin:0 auto;padding:48px 16px;text-align:center">` +
		`<h1>Surprise! 🎲</h1><p>Flowsearch is sending you to</p>` +
		`<p style="font-size:1.4rem;line-height:1.3"><a href="` + html.EscapeString(target) + `" rel="noopener noreferrer"><b>` + html.EscapeString(clip(strings.TrimSpace(title), 160)) + `</b></a></p>` +
		`<p style="opacity:.8">on <b>` + html.EscapeString(strings.TrimPrefix(u.Host, "www.")) + `</b></p>` +
		`<p style="opacity:.6;word-break:break-all;font-size:.9rem">` + html.EscapeString(clip(target, 200)) + `</p>` +
		`<p id=cd style="margin:22px 0">Going in 4…</p>` +
		`<p><a class=chip href="` + html.EscapeString(target) + `" rel="noopener noreferrer">Go there now</a> <a class=chip href="` + html.EscapeString(again) + `">Another one</a> <a class=chip href=# id=stop>Stop</a></p></main>` +
		`<script>let n=4,t=setInterval(()=>{n--;document.getElementById('cd').textContent=n>0?'Going in '+n+'…':'Going…';if(n<=0){clearInterval(t);location.href=` + string(js) + `}},1000);` +
		`document.getElementById('stop').onclick=e=>{e.preventDefault();clearInterval(t);document.getElementById('cd').textContent='Stopped. Pick a button below.'}</script>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, shell("Surprise! - Flowsearch", body))
}
