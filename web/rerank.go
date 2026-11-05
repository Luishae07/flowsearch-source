package main

// Re-ranking with Jev (typesafe/jev-1.13 through OpenRouter's Decisions API). Jev does not write text: it answers typed
// questions about a state. Here the state is the search and its top results, and every result gets one yes/no question
// ("is this a good answer?"); the returned probabilities decide the order. One request per search, a few hundred tokens.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const jevURL = "https://api.typesafe.ai/v1/systemone" // TypeSafe's own API (not OpenRouter)
const jevModel = "jev-latest"

// jevWindow is how many results Jev looks at: a post that was cross-posted to dozens of servers fills more than ten places.
const jevWindow = 30

type rrItem struct {
	Title, URL, Text string
}

var (
	jevKeyOnce sync.Once
	jevKey     string
	jevClient  = &http.Client{Timeout: 6 * time.Second, Transport: jevTransport{}}
	jevMu      sync.Mutex
	jevCache   = map[string]jevHit{}
)

type jevHit struct {
	at              time.Time
	good, keep, dup []float64
}

func jevKeyValue() string {
	jevKeyOnce.Do(func() {
		b, _ := os.ReadFile(fsSecrets + "/typesafe_api_key")
		jevKey = strings.TrimSpace(string(b))
	})
	return jevKey
}

func clipRunes(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// jevScores returns, for every item, three probabilities (0..1): good = it is a good answer to the query, keep = the
// person who typed the query would care about it at all (low means nonsense for this query), dup = it repeats an
// earlier result (the same post on another site, a repost, a copy; always 0 for the first). An error means "keep the
// normal order".
func jevScores(ctx context.Context, query string, items []rrItem) (good, keep, dup []float64, err error) {
	return jevScoresWith(jevClient, ctx, query, items)
}

func jevScoresWith(client *http.Client, ctx context.Context, query string, items []rrItem) (good, keep, dup []float64, err error) {
	key := jevKeyValue()
	if key == "" || len(items) < 2 {
		return nil, nil, nil, fmt.Errorf("no key or nothing to order")
	}
	if len(items) > jevWindow {
		items = items[:jevWindow]
	}
	var st strings.Builder
	st.WriteString("Search query: " + clipRunes(query, 200) + "\n\nResults:\n")
	ck := query
	for i, it := range items {
		fmt.Fprintf(&st, "[%d] %s | %s | %s\n", i+1, clipRunes(it.Title, 100), clipRunes(it.URL, 80), clipRunes(it.Text, 150))
		ck += "\n" + it.URL
	}
	jevMu.Lock()
	if h, ok := jevCache[ck]; ok && time.Since(h.at) < 10*time.Minute {
		jevMu.Unlock()
		return h.good, h.keep, h.dup, nil
	}
	jevMu.Unlock()

	qs := map[string]any{}
	// short wording: the time of a request grows with its size
	yn := func(instr, t, f string) map[string]any {
		return map[string]any{"type": "noul", "instructions": instr, "criteria": map[string]string{"true": t, "false": f}}
	}
	for i := range items {
		n := i + 1
		qs[fmt.Sprintf("r%d", n)] = yn(fmt.Sprintf("Is [%d] a good answer to the query?", n), "wanted", "off topic or weak")
		qs[fmt.Sprintf("k%d", n)] = yn(fmt.Sprintf("Would the searcher care about [%d]?", n), "related", "nonsense or spam")
		qs[fmt.Sprintf("a%d", n)] = yn(fmt.Sprintf("Is [%d] adult content?", n), "adult or sexual", "ordinary (news, health, education are fine)")
		if i > 0 {
			qs[fmt.Sprintf("d%d", n)] = yn(fmt.Sprintf("Is [%d] a copy of an earlier result?", n), "same page or story", "different")
		}
	}
	body, _ := json.Marshal(map[string]any{"model": jevModel, "state": st.String(), "questions": qs})
	req, err := http.NewRequestWithContext(ctx, "POST", jevURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, nil, nil, fmt.Errorf("jev: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Answers map[string]struct {
			Noul *float64 `json:"noul"`
		} `json:"answers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, nil, nil, err
	}
	for i, it := range items {
		if a, ok := out.Answers[fmt.Sprintf("a%d", i+1)]; ok && a.Noul != nil {
			adultRecord(it.URL, *a.Noul)
		}
	}
	good, keep, dup = make([]float64, len(items)), make([]float64, len(items)), make([]float64, len(items))
	for i := range items {
		a, ok := out.Answers[fmt.Sprintf("r%d", i+1)]
		b, ok2 := out.Answers[fmt.Sprintf("k%d", i+1)]
		if !ok || !ok2 || a.Noul == nil || b.Noul == nil {
			return nil, nil, nil, fmt.Errorf("jev: answer %d missing", i+1)
		}
		good[i], keep[i] = *a.Noul, *b.Noul
		if i > 0 {
			d, ok3 := out.Answers[fmt.Sprintf("d%d", i+1)]
			if !ok3 || d.Noul == nil {
				return nil, nil, nil, fmt.Errorf("jev: answer %d missing", i+1)
			}
			dup[i] = *d.Noul
		}
	}
	jevMu.Lock()
	if len(jevCache) > 500 {
		jevCache = map[string]jevHit{}
	}
	jevCache[ck] = jevHit{time.Now(), good, keep, dup}
	jevMu.Unlock()
	return good, keep, dup, nil
}
