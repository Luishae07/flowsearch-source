package main

import (
	"bufio"
	"encoding/json"
	"html"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type newsEntry struct {
	Date   string `json:"date"`
	Text   string `json:"text"`
	Detail string `json:"detail,omitempty"`
}

var newsPath = fsData + "/news.jsonl"

func readNews() []newsEntry {
	f, err := os.Open(newsPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []newsEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e newsEntry
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func appendNews(date, text string) error {
	f, err := os.OpenFile(newsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(newsEntry{Date: date, Text: text})
	_, err = f.Write(append(b, '\n'))
	return err
}

func newsPage(w http.ResponseWriter, r *http.Request) {
	entries := readNews()
	if idStr := r.URL.Query().Get("id"); idStr != "" {
		id, _ := strconv.Atoi(idStr)
		if id >= 0 && id < len(entries) {
			e := entries[id]
			detail := e.Detail
			if detail == "" {
				detail = e.Text
			}
			body := `<main class="wrap" style="max-width:760px"><p><a class="btn ghost" href=/news>&larr; All news</a></p>` +
				pageHead(html.EscapeString(e.Text), `<span class=pill2>`+html.EscapeString(e.Date)+`</span>`) +
				`<section class=gc><p style="font-size:17px;line-height:1.65">` + html.EscapeString(detail) + `</p></section></main>`
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(page(e.Text+" - Flowsearch", "", body)))
			return
		}
	}
	var b strings.Builder
	b.WriteString(`<style>.nl{display:flex;flex-direction:column;gap:12px;position:relative;padding-left:22px}.nl::before{content:"";position:absolute;left:5px;top:8px;bottom:8px;width:2px;background:linear-gradient(var(--acc),transparent);opacity:.5}.nl a.gc{padding:14px 20px;position:relative}.nl a.gc::before{content:"";position:absolute;left:-23px;top:22px;width:10px;height:10px;border-radius:50%;background:var(--acc);box-shadow:0 0 0 4px var(--glow)}.nl time{display:block;color:var(--acc);font-size:13px;letter-spacing:.04em;margin-bottom:3px}</style><main class="wrap" style="max-width:780px">`)
	b.WriteString(pageHead("What's new", "Everything that changed in Flowsearch, newest first."))
	b.WriteString(`<div class=nl>`)
	for i, e := range entries {
		b.WriteString(`<a class=gc href="/news?id=` + strconv.Itoa(i) + `"><time>` + html.EscapeString(e.Date) + `</time>` + html.EscapeString(e.Text) + `</a>`)
	}
	b.WriteString(`</div></main>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(page("News - Flowsearch", "", b.String())))
}

func apiNews(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(readNews())
}

func seedNewsIfEmpty() {
	if _, err := os.Stat(newsPath); err == nil {
		return
	}
	seed := []newsEntry{
		{Date: "2026-09-24", Text: "Started Flowsearch: own crawler, own JSONL storage, own index, no third-party libs."},
		{Date: "2026-09-25", Text: "Rewrote crawler from Python to Go: 55 pages/s to 300-850 pages/s."},
		{Date: "2026-09-26", Text: "Added Common Crawl seed list (50,000 sites), unlimited pages per site."},
		{Date: "2026-09-27", Text: "Added GitHub Codespace crawler calling back into the ingest API."},
		{Date: "2026-09-27", Text: "Built sgpack: own compression format for stored pages, cut storage to ~1.3 KB/page."},
		{Date: "2026-09-28", Text: "English-only filtering (IsEnglish) to keep the index relevant."},
		{Date: "2026-09-28", Text: "Live WebSocket stats page: pages/sec, per-minute and per-hour charts, per-run stats."},
		{Date: "2026-09-29", Text: "Tried link-based ranking (PageRank-style) and extra heuristics, reverted - results got worse."},
		{Date: "2026-09-29", Text: "Redesigned the API docs page."},
		{Date: "2026-09-30", Text: "Added knowledge cards powered by Wikeutral, shown on the right of results."},
		{Date: "2026-09-30", Text: "Added favicons via Google's favicon service."},
		{Date: "2026-09-30", Text: "Added webmaster tools: accounts, site submission, ownership verification."},
		{Date: "2026-09-30", Text: "Added a separate crawler just for webmaster-submitted sites."},
		{Date: "2026-10-01", Text: "Reached ~2.1M English pages indexed, ~2.7 GB total storage."},
	}
	for _, e := range seed {
		appendNews(e.Date, e.Text)
	}
}
