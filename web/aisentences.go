package main

// Jev decides how long the AI answer box should be. It reads the query and the search results and answers one pick-one question:
// how many sentences a good answer needs (a simple fact: 1; a topic that needs explaining: 4 to 6). The AI is then told to write
// exactly that many. If Jev does not answer in 2 seconds, the AI keeps its usual "3 to 5 sentences".

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func jevSentences(q string, results []result) (int, error) {
	key := jevKeyValue()
	if key == "" || len(results) == 0 {
		return 0, fmt.Errorf("no key or no results")
	}
	var st strings.Builder
	st.WriteString("Query: " + clipRunes(q, 200) + "\n\nSearch results:\n")
	for i, r := range results {
		fmt.Fprintf(&st, "[%d] %s | %s\n", i+1, clipRunes(r.Title, 120), clipRunes(r.Snippet, 240))
	}
	body, _ := json.Marshal(map[string]any{"model": jevModel, "state": st.String(), "questions": map[string]any{
		"sentences": map[string]any{
			"type":         "choice",
			"instructions": "A search engine will write a short answer to the query above for a curious reader. How many sentences should that answer have?",
			"criteria": map[string]string{
				"1": "One sentence: a simple fact, a number, a date or a name",
				"2": "Two sentences: a short definition or a quick answer with one detail",
				"3": "Three sentences: a clear explanation of something small",
				"4": "Four sentences: a topic with a few parts to explain",
				"5": "Five sentences: a bigger topic, how something works or why it happens",
				"6": "Six sentences: a broad or complicated topic that needs context, causes and consequences",
			},
		},
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", jevURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := jevClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		Answers map[string]struct {
			Probabilities map[string]float64 `json:"probabilities"`
			Choice        string             `json:"choice"`
		} `json:"answers"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil {
		return 0, fmt.Errorf("jev: HTTP %d", resp.StatusCode)
	}
	a, ok := out.Answers["sentences"]
	if !ok {
		return 0, fmt.Errorf("jev: no answer")
	}
	// the expected number of sentences over all of Jev's probabilities, so a split opinion (2 or 4) gives 3
	sum, total := 0.0, 0.0
	for k, p := range a.Probabilities {
		var n int
		if _, err := fmt.Sscanf(k, "%d", &n); err == nil && n >= 1 && n <= 6 {
			sum += float64(n) * p
			total += p
		}
	}
	if total <= 0 {
		var n int
		if _, err := fmt.Sscanf(a.Choice, "%d", &n); err != nil || n < 1 || n > 6 {
			return 0, fmt.Errorf("jev: unreadable answer")
		}
		return n, nil
	}
	n := int(sum/total + 0.5)
	return min(max(n, 1), 6), nil
}
