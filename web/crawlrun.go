package main

// The "Crawl" tab of /live: what the GitHub Actions crawl (the Rust crawler, workflow flowcrawl.yml in Luishae07/flowsearch) is doing.
// The last runs come from GitHub's public API (no key; cached for a minute), the batch counter and the number of discovered sites
// come from the files the seed-list bot and the dispatcher keep on this box.

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type crRunInfo struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Started    string `json:"started"`
	Updated    string `json:"updated"`
	URL        string `json:"url"`
}

var (
	crMu   sync.Mutex
	crAt   time.Time
	crData []byte
)

func crawlRunsFromGitHub() []crRunInfo {
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/Luishae07/flowsearch/actions/workflows/flowcrawl.yml/runs?per_page=5", nil)
	req.Header.Set("User-Agent", "flowsearch-live")
	req.Header.Set("Accept", "application/vnd.github+json")
	if tk, err := os.ReadFile(fsSecrets + "/github_token.txt"); err == nil { // with the token GitHub allows 5000 calls an hour instead of 60
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tk)))
	}
	c := http.Client{Timeout: 10 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var j struct {
		Runs []struct {
			ID           int64  `json:"id"`
			Status       string `json:"status"`
			Conclusion   string `json:"conclusion"`
			RunStartedAt string `json:"run_started_at"`
			UpdatedAt    string `json:"updated_at"`
			HTMLURL      string `json:"html_url"`
		} `json:"workflow_runs"`
	}
	if json.NewDecoder(resp.Body).Decode(&j) != nil {
		return nil
	}
	out := []crRunInfo{}
	for _, r := range j.Runs {
		out = append(out, crRunInfo{r.ID, r.Status, r.Conclusion, r.RunStartedAt, r.UpdatedAt, r.HTMLURL})
	}
	return out
}

func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		n++
	}
	return n
}

func apiCrawlRun(w http.ResponseWriter, r *http.Request) {
	crMu.Lock()
	defer crMu.Unlock()
	if crData == nil || time.Since(crAt) > time.Minute {
		var st struct {
			Next  int `json:"next_batch"`
			Fails int `json:"fails"`
		}
		if b, err := os.ReadFile(fsData + "/actions-crawl-state.json"); err == nil {
			json.Unmarshal(b, &st)
		}
		disc := countLines(fsData + "/seeds-discovered.txt")
		tranco := countLines(fsHome + "/seeds/seeds-tranco.txt")
		out, _ := json.Marshal(map[string]any{
			"runs": crawlRunsFromGitHub(), "next_batch": st.Next, "batch_size": 500, "discovered": disc, "tranco": tranco,
			"seeds": tranco + disc, "fails": st.Fails,
		})
		crData, crAt = out, time.Now()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(crData)
}
