package main

// The Forums tab asks Jev what age of post a search wants: new posts (what people say now), old posts (a past year, history, nostalgia)
// or any age. The posts are then put in that order: newest first, oldest first, or by how well they match. Jev's answer is kept for a
// day per search; if Jev cannot answer in time, the order is by match.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	ageMu    sync.Mutex
	ageCache = map[string]ageVerdict{}
)

type ageVerdict struct {
	mode string
	at   time.Time
}

// jevForumAge returns "new", "old" or "any".
func jevForumAge(q string) string {
	k := strings.ToLower(strings.Join(strings.Fields(q), " "))
	ageMu.Lock()
	if v, ok := ageCache[k]; ok && time.Since(v.at) < 24*time.Hour {
		ageMu.Unlock()
		return v.mode
	}
	ageMu.Unlock()
	var saved string
	if fcGet("age|"+k, &saved) {
		return saved
	}
	key := jevKeyValue()
	if key == "" {
		return "any"
	}
	body, _ := json.Marshal(map[string]any{"model": jevModel, "state": "Search in a forum: " + clipRunes(q, 150), "questions": map[string]any{
		"age": map[string]any{
			"type":         "choice",
			"instructions": "The person searches discussion forums with this query. Which age of posts do they want?",
			"criteria": map[string]string{
				"new": "Recent posts: what people say now, current events, new releases, anything where an old post would be out of date",
				"old": "Old posts: a past year or event, history, nostalgia, how things used to be, old versions or the early days of something",
				"any": "The age does not matter: timeless questions, advice and discussions",
			},
		},
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", jevURL, bytes.NewReader(body))
	if err != nil {
		return "any"
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := jevClient.Do(req) // Jev through TypeSafe; clef only when TypeSafe fails
	if err != nil {
		return "any"
	}
	defer resp.Body.Close()
	var out struct {
		Answers map[string]struct {
			Choice string `json:"choice"`
		} `json:"answers"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil {
		return "any"
	}
	mode := out.Answers["age"].Choice
	if mode != "new" && mode != "old" {
		mode = "any"
	}
	ageMu.Lock()
	if len(ageCache) > 20000 {
		ageCache = map[string]ageVerdict{}
	}
	ageCache[k] = ageVerdict{mode, time.Now()}
	ageMu.Unlock()
	fcPut("age|"+k, mode)
	return mode
}
