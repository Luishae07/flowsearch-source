package main

// For each post of the Forums page, Jev picks the comment that is the best answer to the search, or none. One request for the whole page: a
// pick-one question per post, the options being its comments. The page shows that comment under the post.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	bestMu    sync.Mutex
	bestCache = map[string]int{}
)

// jevBestComments returns, for each post address, the index of its best comment (-1: none, or Jev could not say).
func jevBestComments(q string, posts []lemmyPost) map[string]int {
	res := map[string]int{}
	key := jevKeyValue()
	if key == "" || len(posts) == 0 {
		return res
	}
	var todo []lemmyPost
	bestMu.Lock()
	for _, p := range posts {
		var saved int
		if v, ok := bestCache[strings.ToLower(q)+"\x00"+p.U]; ok {
			res[p.U] = v
		} else if fcGet("bestf|"+strings.ToLower(q)+"|"+p.U, &saved) { // "bestf": the forced picks; the older "best|" ones could say none
			res[p.U] = saved
		} else {
			todo = append(todo, p)
		}
	}
	bestMu.Unlock()
	if len(todo) == 0 {
		return res
	}
	var st strings.Builder
	st.WriteString("Someone searched a forum for: " + clipRunes(q, 150) + "\n\nThe posts and their comments:\n")
	qs := map[string]any{}
	for i, p := range todo {
		fmt.Fprintf(&st, "\n[Post %d] %s\n", i+1, clipRunes(p.T, 140))
		crit := map[string]string{} // no "none" option: Jev must pick the best comment of every post
		for j, c := range p.Top {
			if j >= 10 {
				break
			}
			fmt.Fprintf(&st, "  comment %d: %s\n", j+1, clipRunes(c.T, 200))
			crit["c"+strconv.Itoa(j+1)] = "Comment " + strconv.Itoa(j+1) + " of this post: " + clipRunes(c.T, 120)
		}
		qs["p"+strconv.Itoa(i+1)] = map[string]any{
			"type":         "choice",
			"instructions": fmt.Sprintf("Which comment of [Post %d] is the best answer to the search, or the most useful for the person who searched?", i+1),
			"criteria":     crit,
		}
	}
	body, _ := json.Marshal(map[string]any{"model": jevModel, "state": st.String(), "questions": qs})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", jevURL, bytes.NewReader(body))
	if err != nil {
		return res
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := jevClient.Do(req) // the best comment is Jev's job (TypeSafe; clef only if TypeSafe fails); the rest of the Forums tab runs on clef
	if err != nil {
		log.Printf("forums: best comments: %v", err)
		return res
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("forums: best comments: Jev answered HTTP %d", resp.StatusCode)
	}
	var out struct {
		Answers map[string]struct {
			Choice string `json:"choice"`
		} `json:"answers"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil {
		return res
	}
	bestMu.Lock()
	defer bestMu.Unlock()
	if len(bestCache) > 20000 {
		bestCache = map[string]int{}
	}
	for i, p := range todo {
		idx := -1
		if c := out.Answers["p"+strconv.Itoa(i+1)].Choice; strings.HasPrefix(c, "c") {
			if n, err := strconv.Atoi(c[1:]); err == nil && n >= 1 && n <= len(p.Top) {
				idx = n - 1
			}
		}
		res[p.U] = idx
		bestCache[strings.ToLower(q)+"\x00"+p.U] = idx
		if idx >= 0 {
			fcPut("bestf|"+strings.ToLower(q)+"|"+p.U, idx)
		}
	}
	return res
}

var forumsDebug bool

// jevJudgeForums lets Jev look at the 25 best posts of a search and say which ones really are about it: the posts that Jev finds good come
// first, and ones that Jev finds beside the point are dropped. If Jev does not answer in time, the order stays as it is.
func jevJudgeForums(q string, hits []forumHit) []forumHit {
	n := min(len(hits), 30)
	if n < 2 {
		return hits
	}
	items := make([]rrItem, n)
	for i := 0; i < n; i++ {
		p := hits[i].p
		text := p.Body
		if len(p.Top) > 0 {
			text += " | comment: " + p.Top[0].T
		}
		items[i] = rrItem{p.T, p.U, text}
	}
	var good, keep []float64
	jk := "judge|" + strings.ToLower(q)
	for _, it := range items {
		jk += "|" + it.URL
	}
	var saved struct{ G, K []float64 }
	if fcGet(jk, &saved) && len(saved.G) == n && len(saved.K) == n {
		good, keep = saved.G, saved.K
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		var err error
		good, keep, _, err = jevScoresWith(jevClient, ctx, q, items)
		if err != nil {
			return hits
		}
		fcPut(jk, struct{ G, K []float64 }{good, keep})
	}
	if forumsDebug {
		for i := 0; i < n && i < 10; i++ {
			log.Printf("forums jev #%d good=%.2f keep=%.2f | %s", i+1, good[i], keep[i], clipRunes(hits[i].p.T, 60))
		}
	}
	top := hits[0].score
	if top <= 0 {
		top = 1
	}
	out := make([]forumHit, 0, len(hits))
	for i := 0; i < n; i++ {
		if keep[i] < 0.2 && good[i] < 0.25 { // Jev: nothing to do with the search
			continue
		}
		h := hits[i]
		h.score = 0.35*h.score/top + 0.65*good[i] // the match score and Jev's judgement together
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	for _, h := range hits[n:] { // the rest has no Jev judgement: on the same 0 to 1 scale, below the judged ones
		h.score = 0.35 * h.score / top
		out = append(out, h)
	}
	return out
}
