package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Cache warm-up: one DeepSeek request explains 100 topics at once (instead of one request per topic).
// POST /api/ai-warm with the secret token and a body of one word or phrase per line.

var (
	warmBusy atomic.Bool
	warmHTTP = &http.Client{Timeout: 240 * time.Second}
)

func apiAIWarm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !warmAuth(r) {
		http.Error(w, "no", 403)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	var words []string
	for _, l := range strings.Split(string(body), "\n") {
		if l = strings.ToLower(strings.Join(strings.Fields(l), " ")); l != "" && len(l) <= 100 && !strings.Contains(l, "||") {
			words = append(words, l)
		}
	}
	if !warmBusy.CompareAndSwap(false, true) {
		http.Error(w, "a warm-up is already running", 409)
		return
	}
	go warmRun(words)
	fmt.Fprintf(w, `{"started":%d}`, len(words))
}

func warmRun(words []string) {
	defer warmBusy.Store(false)
	var todo []string
	for _, wd := range words {
		if _, ok := aiGet(aiVer + wd); !ok {
			todo = append(todo, wd)
		}
	}
	log.Printf("ai warm-up: %d topics, %d not saved yet", len(words), len(todo))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	var saved atomic.Int64
	for i := 0; i < len(todo); i += 100 {
		batch := todo[i:min(i+100, len(todo))]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			n := warmBatch(batch)
			log.Printf("ai warm-up: batch of %d saved %d (total %d)", len(batch), n, saved.Add(int64(n)))
		}()
	}
	wg.Wait()
	log.Printf("ai warm-up finished: %d saved", saved.Load())
}

func warmBatch(batch []string) int {
	kb, err := os.ReadFile(deepseekKeyFile)
	if err != nil {
		return 0
	}
	srcs := map[string][]aiSrc{}
	var ctx strings.Builder
	for _, wd := range batch {
		a := run(wd, 1, 6, false)
		if len(a.Results) == 0 {
			continue
		}
		ctx.WriteString("### " + wd + "\n")
		for i, x := range a.Results {
			srcs[wd] = append(srcs[wd], aiSrc{i + 1, firstNonEmpty(x.Title, x.URL), x.URL})
			if i < 4 {
				ctx.WriteString(firstNonEmpty(x.Title, x.URL) + ": " + clip(x.Snippet, 350) + "\n")
			}
		}
		ctx.WriteString("\n")
	}
	if len(srcs) == 0 {
		return 0
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":       "deepseek-flash",
		"max_tokens":  8000,
		"temperature": 0.2,
		"thinking":    map[string]string{"type": "disabled"},
		"messages": []map[string]string{
			{"role": "system", "content": "You write the answer boxes of a search engine, many at once. For every topic below, explain what it is the way a good teacher would: clear, concrete, easy to understand, 2 to 3 sentences, in English. Base each answer mostly on the search result text under its topic and fill gaps with your own general knowledge. Never mention Google Business (also not Google Business Profile or Google My Business). Never talk about the sources ('the results', 'an article', 'a website', 'the book says'). Plain text, except short code words in single backticks. No markdown, no lists, no square brackets. Output exactly one line per topic: the topic exactly as written, then ' || ', then the explanation, all on one line. Nothing else."},
			{"role": "user", "content": ctx.String()},
		},
	})
	req, _ := http.NewRequest("POST", "https://api.deepseek.com/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(kb)))
	resp, err := warmHTTP.Do(req)
	if err != nil {
		log.Printf("ai warm-up: %v", err)
		return 0
	}
	defer resp.Body.Close()
	var dr struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&dr) != nil || len(dr.Choices) == 0 {
		return 0
	}
	n := 0
	for _, l := range strings.Split(dr.Choices[0].Message.Content, "\n") {
		wd, text, ok := strings.Cut(l, " || ")
		wd = strings.ToLower(strings.TrimSpace(strings.Trim(wd, "#* ")))
		text = strings.TrimSpace(citeRe.ReplaceAllString(text, ""))
		s, known := srcs[wd]
		if !ok || !known || len(text) < 20 {
			continue
		}
		out, _ := json.Marshal(map[string]any{"found": true, "text": text, "sources": s})
		aiPut(aiVer+wd, out)
		n++
	}
	return n
}
