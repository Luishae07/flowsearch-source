package main

// /screenshots: how many sites Flowsearch has a screenshot of, and a gallery of them (the look is in screenshotsui.go).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type shotEntry struct {
	Host string // the site, or for a page screenshot its address (shown as the caption)
	At   time.Time
	Key  string // set for a screenshot of one page: its file is pages/<Key>.jxl
	URL  string
}

var (
	shotListMu   sync.Mutex
	shotListAt   time.Time
	shotListData []shotEntry
)

// shotList lists the sites that have a screenshot (both folders, newest first); kept for 30 seconds.
func shotList() []shotEntry {
	shotListMu.Lock()
	defer shotListMu.Unlock()
	if time.Since(shotListAt) < 30*time.Second && shotListData != nil {
		return shotListData
	}
	seen := map[string]time.Time{}
	for _, d := range []string{fsShots + "", shotsDir} {
		ents, _ := os.ReadDir(d)
		for _, e := range ents {
			n := e.Name()
			if !strings.HasSuffix(n, ".jxl") || e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			h := strings.TrimSuffix(n, ".jxl")
			if t, ok := seen[h]; !ok || info.ModTime().After(t) {
				seen[h] = info.ModTime()
			}
		}
	}
	out := make([]shotEntry, 0, len(seen))
	for h, t := range seen {
		out = append(out, shotEntry{Host: h, At: t})
	}
	// screenshots of single pages: the index file says which address a file belongs to
	if f, err := os.Open(pageShotDir + "/index.jsonl"); err == nil {
		have := map[string]bool{}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var v struct {
				K  string `json:"k"`
				U  string `json:"u"`
				At int64  `json:"at"`
			}
			if json.Unmarshal(sc.Bytes(), &v) != nil || have[v.K] || pageShotFile(v.K) == "" {
				continue
			}
			have[v.K] = true
			out = append(out, shotEntry{Host: strings.TrimPrefix(strings.TrimPrefix(v.U, "https://"), "http://"), At: time.Unix(v.At, 0), Key: v.K, URL: v.U})
		}
		f.Close()
	}
	clean := out[:0:0]
	for _, e := range out {
		if !adultHost(hostOf(e.Host)) {
			clean = append(clean, e)
		}
	}
	out = clean
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	shotListData, shotListAt = out, time.Now()
	return out
}

// shotWindows are the "taken within..." filters of the screenshots page.
var shotWindows = []struct {
	Key, Label string
	D          time.Duration
}{{"1h", "Last hour", time.Hour}, {"5h", "Last 5 hours", 5 * time.Hour}, {"10h", "Last 10 hours", 10 * time.Hour}, {"month", "Last month", 30 * 24 * time.Hour}}

func screenshotsPage(w http.ResponseWriter, r *http.Request) {
	all := shotList()
	qv := r.URL.Query()
	if qv.Get("count") == "1" { // the page asks now and then whether there are new screenshots
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, `{"total":%d}`, len(all))
		return
	}
	q := strings.ToLower(strings.TrimSpace(qv.Get("q")))
	since := qv.Get("since")
	var sinceD time.Duration
	for _, sw := range shotWindows {
		if sw.Key == since {
			sinceD = sw.D
		}
	}
	if sinceD == 0 {
		since = ""
	}
	kind := qv.Get("kind") // "sites" = website front pages, "pages" = single pages, "" = everything
	if kind != "sites" && kind != "pages" {
		kind = ""
	}
	favsRaw := strings.TrimSpace(qv.Get("favs")) // the favourites of the visitor (kept in the browser), as a list of names
	if len(favsRaw) > 12000 {
		favsRaw = ""
	}
	favSet := map[string]bool{}
	for _, f := range strings.Split(favsRaw, ",") {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" && len(favSet) < 300 {
			favSet[f] = true
		}
	}
	sortBy := qv.Get("sort") // "old" = oldest first, "az" = by name, "" = newest first
	if sortBy != "old" && sortBy != "az" {
		sortBy = ""
	}
	list := make([]shotEntry, 0, len(all))
	counts := make([]int, len(shotWindows)) // how many of the pictures that match the search fall in each window
	for _, e := range all {
		if q != "" && !strings.Contains(strings.ToLower(e.Host), q) {
			continue
		}
		if (kind == "sites" && e.Key != "") || (kind == "pages" && e.Key == "") {
			continue
		}
		if len(favSet) > 0 && !favSet[strings.ToLower(e.Host)] {
			continue
		}
		age := time.Since(e.At)
		for i, sw := range shotWindows {
			if age < sw.D {
				counts[i]++
			}
		}
		if sinceD == 0 || age < sinceD {
			list = append(list, e)
		}
	}
	switch sortBy {
	case "old":
		sort.SliceStable(list, func(i, j int) bool { return list[i].At.Before(list[j].At) })
	case "az":
		sort.SliceStable(list, func(i, j int) bool { return strings.ToLower(list[i].Host) < strings.ToLower(list[j].Host) })
	}
	const per = 48
	pg, _ := strconv.Atoi(qv.Get("p"))
	last := max(1, (len(list)+per-1)/per)
	pg = min(max(pg, 1), last)
	from, to := (pg-1)*per, min(pg*per, len(list))
	if qv.Get("frag") == "1" { // "show more": only the next pictures, to be added under the ones already there
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, shotUIGrid(list[from:to], sortBy, qv.Get("lastday")))
		return
	}

	today, week, sites, pagesN := 0, 0, 0, 0
	daily := make([]int, 14) // screenshots taken on each of the last 14 days, today last
	for _, e := range all {
		if e.Key != "" {
			pagesN++
		} else {
			sites++
		}
		if time.Since(e.At) < 24*time.Hour {
			today++
		}
		if time.Since(e.At) < 7*24*time.Hour {
			week++
		}
		if d := int(time.Since(e.At).Hours() / 24); d >= 0 && d < 14 {
			daily[13-d]++
		}
	}
	resultShotMu.Lock()
	taken, waiting := resultShotCount, len(resultShotLine)
	resultShotMu.Unlock()

	body := `<main class="wrap">` + shotAppHTML(shotView{
		Q: strings.TrimSpace(qv.Get("q")), Since: since, Kind: kind, SortBy: sortBy, Favs: favsRaw,
		Total: len(all), Sites: sites, Pages: pagesN, Today: today, Week: week, Daily: daily, Counts: counts,
		NList: len(list), Page: pg, Last: last, Cards: list[from:to], FavMode: len(favSet) > 0,
		Taken: taken, Waiting: waiting, PerDay: resultShotsPerDay,
	}) + `</main>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, page("Screenshots - Flowsearch", "", shotUICSS+body))
}
