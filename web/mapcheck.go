package main

// The map card on a results page may only appear for a search that is about a place. Jev decides with one yes/no question; its answer
// is kept for a day per search. If Jev cannot answer, no map is shown (better no map than a map of a random place).

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
	placeMu    sync.Mutex
	placeCache = map[string]placeVerdict{}
)

type placeVerdict struct {
	yes bool
	at  time.Time
}

func jevIsPlace(q string) bool {
	k := strings.ToLower(strings.Join(strings.Fields(q), " "))
	placeMu.Lock()
	if v, ok := placeCache[k]; ok && time.Since(v.at) < 24*time.Hour {
		placeMu.Unlock()
		return v.yes
	}
	placeMu.Unlock()
	key := jevKeyValue()
	if key == "" {
		return false
	}
	body, _ := json.Marshal(map[string]any{"model": jevModel, "state": "Search query: " + clipRunes(q, 120), "questions": map[string]any{
		"place": map[string]any{
			"type":         "noul",
			"instructions": "Is this search mainly about a place on the map: a city, town, country, region, street address, landmark, building or other named location that someone would want to see on a map?",
			"criteria": map[string]string{
				"true":  "The search names a place, or asks for a place or the way to one",
				"false": "It is about a product, a person, a program, a website, a question, a topic or anything else, even if a word in it is also the name of a town",
			},
		},
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
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
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil || out.Answers["place"].Noul == nil {
		return false
	}
	yes := *out.Answers["place"].Noul >= 0.75
	placeMu.Lock()
	if len(placeCache) > 20000 {
		placeCache = map[string]placeVerdict{}
	}
	placeCache[k] = placeVerdict{yes, time.Now()}
	placeMu.Unlock()
	return yes
}
