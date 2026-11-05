package main

// "Try website" tab: an interactive, slow-motion remote browser. One GitHub Actions job runs real
// Playwright+Firefox against the site, looping for a few minutes: screenshot -> upload -> poll us for the
// next click/nav command -> act on it -> repeat. We cache the last frame seen for each URL so clicking
// something already visited this session shows instantly while a fresh capture happens in the background.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type browseCmd struct {
	Type string  `json:"type"` // "nav", "click", "scroll", "drag", "key", "stop", "none"
	URL  string  `json:"url,omitempty"`
	X    float64 `json:"x,omitempty"` // 0..1, fraction of viewport
	Y    float64 `json:"y,omitempty"`
	X2   float64 `json:"x2,omitempty"` // drag: end position (X,Y is the start)
	Y2   float64 `json:"y2,omitempty"`
	DY   float64 `json:"dy,omitempty"`   // scroll: pixels, +down/-up
	Text string  `json:"text,omitempty"` // key: text to type, or a key name like "Enter"/"Backspace"
}

type browseSession struct {
	mu        sync.Mutex
	Owner     string
	Site      string
	CurURL    string
	Frame     []byte
	Cache     map[string][]byte
	CacheHits []string // order, to cap cache size
	Pending   *browseCmd
	Started   time.Time
	LastFrame time.Time
	Done      bool
}

var (
	browseMu  sync.Mutex
	browseSet = map[string]*browseSession{}
)

func newBrowseSession(owner, site string) string {
	b := make([]byte, 12)
	rand.Read(b)
	id := hex.EncodeToString(b)
	browseMu.Lock()
	browseSet[id] = &browseSession{Owner: owner, Site: site, Cache: map[string][]byte{}, Started: time.Now()}
	browseMu.Unlock()
	return id
}

func getBrowseSession(id string) *browseSession {
	browseMu.Lock()
	defer browseMu.Unlock()
	return browseSet[id]
}

// browseToken authenticates GitHub Actions calling us back (poll/frame), reusing the same shared
// secret the crawler ingest already uses, since it's already a GH repo secret.
func browseToken() string {
	b, _ := os.ReadFile(fsSecrets + "/mysearch_token")
	return strings.TrimSpace(string(b))
}

// wmBrowseStart begins a session for the site the webmaster is currently viewing, and kicks off the
// GitHub Actions browse workflow.
func wmBrowseStart(w http.ResponseWriter, r *http.Request) {
	back := func(site, m string) {
		http.Redirect(w, r, "/webmaster?view=browse&site="+url.QueryEscape(site)+"&msg="+url.QueryEscape(m), http.StatusSeeOther)
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/webmaster", http.StatusSeeOther)
		return
	}
	user := currentUser(r)
	if user == "" || !csrfOK(r, user) {
		back(r.FormValue("site"), "Please log in first.")
		return
	}
	site, ok := normalizeSite(r.FormValue("site"))
	if !ok || !hasSite(user, site) {
		back(r.FormValue("site"), "Add the site first.")
		return
	}
	id := newBrowseSession(user, site)
	go ghCmd("workflow", "run", "browse.yml", "--repo", flowRepo,
		"-f", "site="+site, "-f", "session="+id).Run()
	http.Redirect(w, r, "/webmaster?view=browse&site="+url.QueryEscape(site)+"&session="+id, http.StatusSeeOther)
}

func wmBrowseStop(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == "" || !csrfOK(r, user) {
		http.Error(w, "forbidden", 403)
		return
	}
	s := getBrowseSession(r.FormValue("session"))
	if s == nil || s.Owner != user {
		http.Error(w, "no such session", 404)
		return
	}
	s.mu.Lock()
	s.Pending = &browseCmd{Type: "stop"}
	s.Done = true
	s.mu.Unlock()
	http.Redirect(w, r, "/webmaster?view=browse&site="+url.QueryEscape(s.Site), http.StatusSeeOther)
}

func wmBrowseNav(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == "" || !csrfOK(r, user) {
		http.Error(w, "forbidden", 403)
		return
	}
	s := getBrowseSession(r.FormValue("session"))
	if s == nil || s.Owner != user {
		http.Error(w, "no such session", 404)
		return
	}
	target := r.FormValue("url")
	s.mu.Lock()
	s.Pending = &browseCmd{Type: "nav", URL: target}
	if cached, ok := s.Cache[target]; ok {
		s.Frame = cached
		s.CurURL = target
	}
	s.mu.Unlock()
	w.WriteHeader(204)
}

func wmBrowseClick(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == "" || !csrfOK(r, user) {
		http.Error(w, "forbidden", 403)
		return
	}
	s := getBrowseSession(r.FormValue("session"))
	if s == nil || s.Owner != user {
		http.Error(w, "no such session", 404)
		return
	}
	x, _ := strconv.ParseFloat(r.FormValue("x"), 64)
	y, _ := strconv.ParseFloat(r.FormValue("y"), 64)
	s.mu.Lock()
	s.Pending = &browseCmd{Type: "click", X: x, Y: y}
	s.mu.Unlock()
	w.WriteHeader(204)
}

func wmBrowseScroll(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == "" || !csrfOK(r, user) {
		http.Error(w, "forbidden", 403)
		return
	}
	s := getBrowseSession(r.FormValue("session"))
	if s == nil || s.Owner != user {
		http.Error(w, "no such session", 404)
		return
	}
	dy, _ := strconv.ParseFloat(r.FormValue("dy"), 64)
	s.mu.Lock()
	s.Pending = &browseCmd{Type: "scroll", DY: dy}
	s.mu.Unlock()
	w.WriteHeader(204)
}

func wmBrowseDrag(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == "" || !csrfOK(r, user) {
		http.Error(w, "forbidden", 403)
		return
	}
	s := getBrowseSession(r.FormValue("session"))
	if s == nil || s.Owner != user {
		http.Error(w, "no such session", 404)
		return
	}
	x, _ := strconv.ParseFloat(r.FormValue("x"), 64)
	y, _ := strconv.ParseFloat(r.FormValue("y"), 64)
	x2, _ := strconv.ParseFloat(r.FormValue("x2"), 64)
	y2, _ := strconv.ParseFloat(r.FormValue("y2"), 64)
	s.mu.Lock()
	s.Pending = &browseCmd{Type: "drag", X: x, Y: y, X2: x2, Y2: y2}
	s.mu.Unlock()
	w.WriteHeader(204)
}

func wmBrowseKey(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == "" || !csrfOK(r, user) {
		http.Error(w, "forbidden", 403)
		return
	}
	s := getBrowseSession(r.FormValue("session"))
	if s == nil || s.Owner != user {
		http.Error(w, "no such session", 404)
		return
	}
	s.mu.Lock()
	s.Pending = &browseCmd{Type: "key", Text: r.FormValue("text")}
	s.mu.Unlock()
	w.WriteHeader(204)
}

// wmBrowsePoll is called by the GitHub Actions job to fetch (and clear) the next pending command.
func wmBrowsePoll(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+browseToken() {
		http.Error(w, "forbidden", 403)
		return
	}
	s := getBrowseSession(r.URL.Query().Get("session"))
	if s == nil {
		json.NewEncoder(w).Encode(browseCmd{Type: "stop"})
		return
	}
	s.mu.Lock()
	cmd := s.Pending
	s.Pending = nil
	done := s.Done
	s.mu.Unlock()
	if done {
		json.NewEncoder(w).Encode(browseCmd{Type: "stop"})
		return
	}
	if cmd == nil {
		cmd = &browseCmd{Type: "none"}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cmd)
}

// wmBrowseFrame is called by the GitHub Actions job after every screenshot.
func wmBrowseFrame(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+browseToken() {
		http.Error(w, "forbidden", 403)
		return
	}
	s := getBrowseSession(r.URL.Query().Get("session"))
	if s == nil {
		http.Error(w, "no such session", 404)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 6<<20))
	if err != nil {
		http.Error(w, "bad body", 400)
		return
	}
	curURL := r.Header.Get("X-URL")
	s.mu.Lock()
	s.Frame = data
	s.CurURL = curURL
	s.LastFrame = time.Now()
	if curURL != "" {
		if _, ok := s.Cache[curURL]; !ok {
			s.CacheHits = append(s.CacheHits, curURL)
			if len(s.CacheHits) > 25 {
				delete(s.Cache, s.CacheHits[0])
				s.CacheHits = s.CacheHits[1:]
			}
		}
		s.Cache[curURL] = data
	}
	s.mu.Unlock()
	w.WriteHeader(204)
}

// wmBrowseView serves the current frame as an image for the webmaster page's <img> tag to poll.
func wmBrowseView(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	s := getBrowseSession(r.URL.Query().Get("session"))
	if s == nil || user == "" || s.Owner != user {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="800"><rect width="100%" height="100%" fill="#11151a"/></svg>`))
		return
	}
	s.mu.Lock()
	frame := s.Frame
	s.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	if len(frame) == 0 {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="800"><rect width="100%" height="100%" fill="#11151a"/><text x="50%" y="50%" fill="#8b97a2" font-family="sans-serif" font-size="20" text-anchor="middle">Starting the browser...</text></svg>`))
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(frame)
}

// wmBrowseStatus reports the session's current URL and whether it's finished, for the URL bar / idle timeout.
func wmBrowseStatus(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	s := getBrowseSession(r.URL.Query().Get("session"))
	if s == nil || user == "" || s.Owner != user {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"done":true}`))
		return
	}
	s.mu.Lock()
	curURL, done, lastFrame := s.CurURL, s.Done, s.LastFrame
	s.mu.Unlock()
	if !lastFrame.IsZero() && time.Since(lastFrame) > 2*time.Minute {
		done = true
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"url": curURL, "done": done})
}

// wmAIExplore: a real browser opens the site and turns it into text, then the AI writes a short summary,
// filed as a notification. No CI job, so it takes seconds.
func wmAIExplore(w http.ResponseWriter, r *http.Request) {
	back := func(site, m string) {
		http.Redirect(w, r, "/webmaster?view=live&site="+url.QueryEscape(site)+"&msg="+url.QueryEscape(m), http.StatusSeeOther)
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/webmaster", http.StatusSeeOther)
		return
	}
	user := currentUser(r)
	if user == "" || !csrfOK(r, user) {
		back(r.FormValue("site"), "Please log in first.")
		return
	}
	site, ok := normalizeSite(r.FormValue("site"))
	if !ok || !hasSite(user, site) {
		back(r.FormValue("site"), "Add the site first.")
		return
	}
	go func() {
		sum, err := aiExploreSite(site)
		if err != nil {
			sum = "could not explore it (" + err.Error() + ")"
		}
		addNotification(user, site, "AI explored your site: "+sum)
	}()
	back(site, "AI is exploring "+site+" now - the result shows up as a notification in about half a minute.")
}

// wmAIExploreReport receives the AI's summary from the GitHub Actions job and files it as a
// notification, same auth as the browse endpoints.
func wmAIExploreReport(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+browseToken() {
		http.Error(w, "forbidden", 403)
		return
	}
	user := r.URL.Query().Get("user")
	site := r.URL.Query().Get("site")
	if user == "" || site == "" {
		http.Error(w, "missing user/site", 400)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	if err != nil {
		http.Error(w, "bad body", 400)
		return
	}
	summary := strings.TrimSpace(string(body))
	if summary == "" {
		summary = "(the AI didn't return anything usable)"
	}
	addNotification(user, site, "AI explored your site: "+summary)
	w.WriteHeader(204)
}

func registerBrowse() {
	http.HandleFunc("/webmaster/browse/start", wmBrowseStart)
	http.HandleFunc("/webmaster/browse/stop", wmBrowseStop)
	http.HandleFunc("/webmaster/browse/nav", wmBrowseNav)
	http.HandleFunc("/webmaster/browse/click", wmBrowseClick)
	http.HandleFunc("/webmaster/browse/scroll", wmBrowseScroll)
	http.HandleFunc("/webmaster/browse/drag", wmBrowseDrag)
	http.HandleFunc("/webmaster/browse/key", wmBrowseKey)
	http.HandleFunc("/webmaster/browse/poll", wmBrowsePoll)
	http.HandleFunc("/webmaster/browse/frame", wmBrowseFrame)
	http.HandleFunc("/webmaster/browse/view", wmBrowseView)
	http.HandleFunc("/webmaster/browse/status", wmBrowseStatus)
	http.HandleFunc("/webmaster/ai-explore", wmAIExplore)
	http.HandleFunc("/webmaster/ai-explore/report", wmAIExploreReport)
}


// aiExploreSite: a real browser opens the site and reads its text, then DeepSeek (the same model as the search
// answer box) writes a short summary for the owner.
func aiExploreSite(site string) (string, error) {
	text, err := buText(site)
	if err != nil {
		return "", err
	}
	if len(text) > 12000 {
		text = text[:12000]
	}
	if strings.TrimSpace(text) == "" {
		return "the page was empty when opened in a real browser - a crawler would see nothing either.", nil
	}
	kb, err := os.ReadFile(deepseekKeyFile)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{
		"model":       "deepseek-flash",
		"max_tokens":  400,
		"temperature": 0.2,
		"thinking":    map[string]string{"type": "disabled"},
		"messages": []map[string]string{
			{"role": "system", "content": "You look at a website for its owner who wants it found in a search engine. Answer in plain English, 4 to 6 short sentences: what the site is, its main sections or pages, and the biggest thing a search engine might struggle with (thin text, text hidden behind scripts, cookie walls, missing titles). No lists, no markdown."},
			{"role": "user", "content": "Site: " + site + "\n\nWhat a real browser shows, as text:\n" + text},
		},
	})
	req, _ := http.NewRequest("POST", "https://api.deepseek.com/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(kb)))
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil || len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("the AI returned nothing")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}
