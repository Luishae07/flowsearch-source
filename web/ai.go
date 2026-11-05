package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// AI summary: the best results of a search are given to DeepSeek, which writes a short answer from them.
// Only the query and the result titles, addresses and snippets leave this box.

var deepseekKeyFile = fsSecrets + "/deepseek_api_key"

var (
	aiMu     sync.Mutex
	aiHits   = map[string][]time.Time{}
	aiDay    string
	aiToday  int
	aiStream = &http.Client{Timeout: 19 * time.Second}
)

const aiVer = "v9|" // changes when the instructions change, so old saved answers are not used

const (
	aiDailyCap = 3000
)

var citeRe = regexp.MustCompile(`\s*\[\d+\]`)

func aiAllowed(ip string) bool {
	aiMu.Lock()
	defer aiMu.Unlock()
	now := time.Now()
	cut := now.Add(-time.Minute)
	h := aiHits[ip][:0]
	for _, t := range aiHits[ip] {
		if t.After(cut) {
			h = append(h, t)
		}
	}
	if len(h) >= 6 {
		aiHits[ip] = h
		return false
	}
	aiHits[ip] = append(h, now)
	if len(aiHits) > 5000 {
		aiHits = map[string][]time.Time{}
	}
	return true
}

func aiCount() bool {
	aiMu.Lock()
	defer aiMu.Unlock()
	if d := time.Now().UTC().Format("2006-01-02"); d != aiDay {
		aiDay, aiToday = d, 0
	}
	if aiToday >= aiDailyCap {
		return false
	}
	aiToday++
	return true
}

type aiSrc struct {
	N     int    `json:"n"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// apiAI streams the answer as JSON lines (application/x-ndjson):
//
//	{"sources":[{"n":1,"title":"...","url":"..."}]}
//	{"delta":"words "} ... one line per piece of text, as the model writes it
//	{"done":true}
//
// or the single line {"found":false}.
func apiAI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fl, _ := w.(http.Flusher)
	line := func(v any) {
		b, _ := json.Marshal(v)
		w.Write(append(b, '\n'))
		if fl != nil {
			fl.Flush()
		}
	}
	none := func() { line(map[string]bool{"found": false}) }
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 150 || strings.Contains(strings.ToLower(q), "site:") || adultQuery(q) {
		none()
		return
	}
	de := isDE(r)
	key := aiVer + strings.ToLower(strings.Join(strings.Fields(q), " "))
	if de {
		key = "de|" + key
	}
	if saved, ok := aiGet(key); ok {
		var c struct {
			Text    string  `json:"text"`
			Sources []aiSrc `json:"sources"`
		}
		if json.Unmarshal(saved, &c) == nil && c.Text != "" {
			line(map[string]any{"sources": c.Sources})
			// a saved answer is streamed too, word by word
			for _, wd := range strings.SplitAfter(c.Text, " ") {
				line(map[string]string{"delta": wd})
				select {
				case <-r.Context().Done():
					return
				case <-time.After(30 * time.Millisecond):
				}
			}
			line(map[string]bool{"done": true})
			return
		}
	}
	// the cache warm-up (a script on this box, with the secret token) is not rate limited
	warm := warmAuth(r)
	if !warm && (!aiAllowed(clientIP(r)) || !aiCount()) {
		none()
		return
	}
	kb, err := os.ReadFile(deepseekKeyFile)
	if err != nil {
		none()
		return
	}
	a := run(q, 1, 6, de)
	if len(a.Results) == 0 {
		none()
		return
	}
	var ctx strings.Builder
	var srcs []aiSrc
	for i, x := range a.Results {
		ctx.WriteString(fmt.Sprintf("[%d] %s\n%s\n%s\n\n", i+1, x.Title, x.URL, clip(x.Snippet, 600)))
		srcs = append(srcs, aiSrc{i + 1, firstNonEmpty(x.Title, x.URL), x.URL})
	}
	// Jev reads the query and the results and says how many sentences the answer needs
	length, maxTok := "3 to 5 sentences", 450
	jevN, jerr := jevSentences(q, a.Results)
	if jerr == nil {
		length, maxTok = fmt.Sprintf("exactly %d sentence", jevN), 90+jevN*80
		if jevN != 1 {
			length += "s"
		}
	}
	lang := "English"
	if de {
		lang = "German"
	}
	sysMsg := "You write the answer box of a search engine. Explain what the query is about the way a good teacher would: clear, concrete, easy to understand, " + length + ". Write in " + lang + ". Base the answer mostly on what the search results below say, and fill the gaps with your own general knowledge so that it really explains the topic. If the two disagree, trust the results. Never mention Google Business (also not Google Business Profile or Google My Business), even if the results do. Never write phrases like 'the results mention', 'the search results include' or 'the results also mention': state the facts directly, as if you know them. Never talk about the sources at all: no 'one blog post', 'an article', 'a website', 'a page', 'the book says' or similar. Plain text, except code: put code examples in fenced blocks (three backticks, then the code, then three backticks) and short code words in single backticks. No other markdown, no lists, no citations, no square brackets." + " You also have one tool, fetch_page, which fetches a live web page by its address. Use it only if the search results below do not cover the question, on one of their addresses, at most three times, and never mention it."

	userMsg := "Query: " + q + "\n\nSearch results:\n\n" + ctx.String()
	toolbox := &aiToolbox{de: de, key: strings.TrimSpace(string(kb))}
	final, msgs := aiToolPhase(strings.TrimSpace(string(kb)), sysMsg, userMsg, maxTok, toolbox)
	if final != "" { // the model answered (after searching or reading, if it wanted to): stream it word by word
		line(map[string]any{"sources": srcs})
		if jerr == nil {
			line(map[string]any{"jev_sentences": jevN})
		}
		for _, wd := range strings.SplitAfter(final, " ") {
			line(map[string]string{"delta": wd})
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		line(map[string]bool{"done": true})
		if text := strings.TrimSpace(citeRe.ReplaceAllString(final, "")); text != "" {
			out, _ := json.Marshal(map[string]any{"found": true, "text": text, "sources": srcs})
			aiPut(key, out)
		}
		return
	}
	body, _ := json.Marshal(map[string]any{
		"model":       "deepseek-flash",
		"max_tokens":  maxTok,
		"temperature": 0.2,
		"stream":      true,
		"thinking":    map[string]string{"type": "disabled"},
		"messages":    msgs,
	})
	req, _ := http.NewRequest("POST", "https://api.deepseek.com/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(kb)))
	resp, err := aiStream.Do(req)
	if err != nil || resp.StatusCode != 200 {
		none()
		return
	}
	defer resp.Body.Close()
	line(map[string]any{"sources": srcs})
	if jerr == nil {
		line(map[string]any{"jev_sentences": jevN}) // what Jev decided
	}
	var full strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var ch struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &ch) != nil || len(ch.Choices) == 0 || ch.Choices[0].Delta.Content == "" {
			continue
		}
		full.WriteString(ch.Choices[0].Delta.Content)
		line(map[string]string{"delta": ch.Choices[0].Delta.Content})
	}
	line(map[string]bool{"done": true})
	text := strings.TrimSpace(citeRe.ReplaceAllString(full.String(), ""))
	if text == "" {
		return
	}
	out, _ := json.Marshal(map[string]any{"found": true, "text": text, "sources": srcs})
	aiPut(key, out)
}

// warmAuth: the request carries the box's secret token (the cache warm-up script), so it is not rate limited.
func warmAuth(r *http.Request) bool {
	tb, err := os.ReadFile(fsSecrets + "/mysearch_token")
	return err == nil && len(tb) > 20 && strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == strings.TrimSpace(string(tb))
}
