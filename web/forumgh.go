package main

// The forum crawl runs on GitHub Actions (workflow forumcrawl.yml in Luishae07/flowsearch), so the Sources card of the Forums tab asks GitHub
// about its latest run (cached for 20 seconds; with the token GitHub allows 5000 calls an hour).

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	fgMu   sync.Mutex
	fgAt   time.Time
	fgData = map[string]any{"status": "unknown"}
)

func forumCrawlGitHub() map[string]any {
	fgMu.Lock()
	defer fgMu.Unlock()
	if time.Since(fgAt) < 20*time.Second {
		return fgData
	}
	fgAt = time.Now() // also after a failure: GitHub is not asked again for 20 seconds
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/Luishae07/flowsearch/actions/workflows/forumcrawl.yml/runs?per_page=1", nil)
	req.Header.Set("User-Agent", "flowsearch-live")
	req.Header.Set("Accept", "application/vnd.github+json")
	if tk, err := os.ReadFile(fsSecrets + "/github_token.txt"); err == nil {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tk)))
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fgData
	}
	defer resp.Body.Close()
	var j struct {
		Runs []struct {
			Status       string `json:"status"`
			Conclusion   string `json:"conclusion"`
			RunStartedAt string `json:"run_started_at"`
			UpdatedAt    string `json:"updated_at"`
			HTMLURL      string `json:"html_url"`
		} `json:"workflow_runs"`
	}
	if json.NewDecoder(resp.Body).Decode(&j) != nil {
		return fgData
	}
	if len(j.Runs) == 0 {
		fgData = map[string]any{"status": "none"}
		return fgData
	}
	r := j.Runs[0]
	fgData = map[string]any{"status": r.Status, "conclusion": r.Conclusion, "started": r.RunStartedAt, "updated": r.UpdatedAt, "url": r.HTMLURL}
	return fgData
}
