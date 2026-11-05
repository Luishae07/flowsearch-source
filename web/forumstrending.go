package main

// "Trending in Forums" on the home page: the hottest forum posts of the last days, from the Forums index (score, comment count, date).
// The crawl mostly saves the best posts of each server, so there may be few from the last week: the window widens (a week, a month,
// three months, a year, everything) until six topics are found. The list is worked out at most every 5 minutes.

import (
	"encoding/json"
	"bytes"
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type trendItem struct{ Title, Community string }

var (
	trMu   sync.Mutex
	trAt   time.Time
	trData []trendItem
)

func (x *fIndex) trending(n int) []trendItem {
	x.mu.RLock()
	defer x.mu.RUnlock()
	f, err := os.Open(lemmyPostsFile)
	if err != nil {
		return nil
	}
	defer f.Close()
	now := time.Now().Unix()
	for _, days := range []int64{7, 30, 90, 365, 36500} {
		type cand struct {
			id  int
			hot float64
		}
		var cs []cand
		for id := range x.offs {
			if !x.alive[id] || x.pub[id] < now-days*86400 {
				continue
			}
			cs = append(cs, cand{id, float64(x.score[id]) + 2*float64(x.comments[id])})
		}
		sort.Slice(cs, func(i, j int) bool { return cs[i].hot > cs[j].hot })
		var out []trendItem
		seen := map[string]bool{}
		for _, c := range cs {
			if len(out) >= n || c.hot < 20 && days < 36500 { // a quiet post is not a trend (unless nothing else is left)
				break
			}
			buf := make([]byte, x.lens[c.id])
			if _, err := f.ReadAt(buf, x.offs[c.id]); err != nil {
				continue
			}
			var p lemmyPost
			if json.Unmarshal(buf, &p) != nil || strings.TrimSpace(p.T) == "" {
				continue
			}
			k := strings.ToLower(strings.Join(strings.Fields(p.T), " "))
			if seen[k] { // the same post cross-posted to several servers
				continue
			}
			seen[k] = true
			out = append(out, trendItem{clip(p.T, 70), p.Community})
		}
		if len(out) >= n || days == 36500 {
			return out
		}
	}
	return nil
}

// techOnly asks Jev, for each of the hottest posts, whether it is about technology, and keeps the first n that are.
func techOnly(items []trendItem, n int) []trendItem {
	if len(items) == 0 || jevKeyValue() == "" {
		return nil
	}
	var st strings.Builder
	st.WriteString("Titles of forum posts:\n")
	qs := map[string]any{}
	for i, it := range items {
		fmt.Fprintf(&st, "[%d] %s (%s)\n", i+1, clipRunes(it.Title, 100), it.Community)
		qs[fmt.Sprintf("t%d", i+1)] = map[string]any{
			"type":         "noul",
			"instructions": fmt.Sprintf("Is post [%d] about technology: computers, software, programming, the internet, hardware, gadgets, security, open source or science?", i+1),
			"criteria": map[string]string{
				"true":  "A technology topic",
				"false": "Politics, news, sport, entertainment or anything else that is not about technology",
			},
		}
	}
	body, _ := json.Marshal(map[string]any{"model": jevModel, "state": st.String(), "questions": qs})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", jevURL, bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+jevKeyValue())
	req.Header.Set("Content-Type", "application/json")
	resp, err := jevClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var out struct {
		Answers map[string]struct {
			Noul *float64 `json:"noul"`
		} `json:"answers"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil {
		return nil
	}
	var keep []trendItem
	for i, it := range items {
		if a := out.Answers[fmt.Sprintf("t%d", i+1)]; a.Noul != nil && *a.Noul >= 0.6 && len(keep) < n {
			keep = append(keep, it)
		}
	}
	return keep
}

// forumsTrendingHTML: the "Trending in forums" block of the home page (empty while the Forums index is still being built).
func forumsTrendingHTML() string {
	if forumIdx == nil { // the index is still starting up: no block yet, and the home page does not wait for it
		return ""
	}
	trMu.Lock()
	if time.Since(trAt) > 5*time.Minute || len(trData) == 0 && time.Since(trAt) > 20*time.Second {
		trData, trAt = techOnly(forumIdx.trending(30), 6), time.Now()
	}
	items := trData
	trMu.Unlock()
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=try><h4>Trending in forums</h4><div class=chips>`)
	for _, it := range items {
		b.WriteString(`<a class=chip href="/search?tab=forums&q=` + url.QueryEscape(it.Title) + `" title="` + html.EscapeString(it.Community) + `">` + html.EscapeString(it.Title) + `</a>`)
	}
	b.WriteString(`</div></div>`)
	return b.String()
}
