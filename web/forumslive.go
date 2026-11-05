package main

// The Lemmy tab of the Live page: the forum posts Flowsearch holds (total, per community, newest) and how far the Lemmy crawl is.

import (
	"bytes"
	"sync"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var lemmyCrawlLog = fsHome + "/lemmy-30k.log"

var (
	crawlSiteRe  = regexp.MustCompile(`^(\d\d:\d\d:\d\d) (\S+): (\d+) posts \((\d+) in all so far\)`)
	crawlStartRe = regexp.MustCompile(`(\d+) posts are already saved; (\d+) more to read`)
)

var (
	lcMu     sync.Mutex
	lcOffset int64
	lcLines  int
)

// savedPosts: how many posts the Lemmy file holds and the server of the newest one (the file only grows, so only the new part is read).
func savedPosts() (int, string) {
	lcMu.Lock()
	defer lcMu.Unlock()
	f, err := os.Open(lemmyPostsFile)
	if err != nil {
		return 0, ""
	}
	defer f.Close()
	st, _ := f.Stat()
	if st.Size() < lcOffset {
		lcOffset, lcLines = 0, 0
	}
	buf := make([]byte, 1<<20)
	f.Seek(lcOffset, 0)
	for {
		n, err := f.Read(buf)
		lcLines += bytes.Count(buf[:n], []byte{'\n'})
		lcOffset += int64(n)
		if err != nil || n == 0 {
			break
		}
	}
	inst := ""
	tail := make([]byte, min(int64(1<<17), st.Size()))
	f.ReadAt(tail, st.Size()-int64(len(tail)))
	ls := strings.Split(strings.TrimRight(string(tail), "\n"), "\n")
	for i := len(ls) - 1; i >= 1 && inst == ""; i-- { // the first piece of the tail may be cut
		var p struct {
			Instance string `json:"instance"`
		}
		if json.Unmarshal([]byte(ls[i]), &p) == nil {
			inst = p.Instance
		}
	}
	return lcLines, inst
}

var (
	flMu    sync.Mutex
	flAt    time.Time
	flHeavy map[string]any
)

// forumsLiveData: the numbers of the tab. The statistics over all posts are worked out at most every 3 seconds (for everyone together);
// the crawl line is read fresh each time.
func forumsLiveData() map[string]any {
	flMu.Lock()
	if flHeavy == nil || time.Since(flAt) > 3*time.Second {
		flHeavy, flAt = forumsLiveHeavy(), time.Now()
	}
	out := map[string]any{}
	for k, v := range flHeavy {
		out[k] = v
	}
	flMu.Unlock()
	out["crawl"] = forumsCrawl()
	out["sources"] = forumSources()
	out["newest"] = justSaved(14)
	return out
}

func apiForumsLive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(forumsLiveData())
}

// forumReady is true once the forum index (all Lemmy posts, about 335 MB of text) has been read in. That takes a while after every start, so it is
// started at startup in the background; until then the tab shows the post count from the file and "loading", instead of waiting for it.
var forumReady atomic.Bool

func forumsLiveHeavy() map[string]any {
	if !forumReady.Load() {
		go forumIndex()
		n, _ := savedPosts()
		return map[string]any{"total": n, "communities": 0, "instances": 0, "top": []any{}, "loading": true}
	}
	posts, communities, servers, top := forumIndex().stats()
	type cc struct {
		C string `json:"c"`
		N int    `json:"n"`
	}
	var comm []cc
	for _, t := range top {
		comm = append(comm, cc{t.C, t.N})
	}
	return map[string]any{"total": posts, "communities": communities, "instances": servers, "top": comm, "platforms": forumIndex().platformCounts(), "sites": forumIndex().topInstances(25), "languages": forumIndex().languagePosts(), "langs": forumIndex().languageCounts()}
}

// foSummary: the forum numbers that go into the /live stats message (and so into the MCP stats): posts per platform, language forum posts, sites, communities.
func foSummary() map[string]int {
	flMu.Lock()
	if flHeavy == nil || time.Since(flAt) > 3*time.Second {
		flHeavy, flAt = forumsLiveHeavy(), time.Now()
	}
	h := flHeavy
	flMu.Unlock()
	out := map[string]int{}
	for _, k := range []string{"total", "communities", "instances", "languages"} {
		if v, ok := h[k].(int); ok {
			out[k] = v
		}
	}
	if p, ok := h["platforms"].(map[string]int); ok {
		for k, v := range p {
			out[k] = v
		}
	}
	return out
}

// forumsCrawl: how far the Lemmy crawl is.
func forumsCrawl() map[string]any {
	crawl := map[string]any{"running": false}
	if st, err := os.Stat(lemmyCrawlLog); err == nil {
		b, _ := os.ReadFile(lemmyCrawlLog)
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		goal, saved := 0, 0
		site, read, siteN := "", 0, 0
		for _, l := range lines {
			if m := crawlStartRe.FindStringSubmatch(l); m != nil {
				saved, _ = strconv.Atoi(m[1])
				goal, _ = strconv.Atoi(m[2])
			}
			if m := crawlSiteRe.FindStringSubmatch(l); m != nil {
				site = m[2]
				siteN, _ = strconv.Atoi(m[3])
				read, _ = strconv.Atoi(m[4])
			}
		}
		done := len(lines) > 0 && strings.Contains(lines[len(lines)-1], "done")
		// the log only gets a line when a whole server is finished, so the live count is the growth of the file itself
		now, newest := savedPosts()
		if now-saved > read {
			read = now - saved
		}
		if newest != "" {
			site = newest
		}
		running := !done && time.Since(st.ModTime()) < 30*time.Minute && crawlRunning()
		crawl = map[string]any{"running": running, "site": site, "site_posts": siteN, "read": read, "goal": goal, "start_total": saved,
			"updated": st.ModTime().Unix()}
	}
	return crawl
}

// crawlProcs: which lemmycrawl processes are alive: the Lemmy crawl, the Discourse crawl, the Flowforum watcher (they are told apart by their flags).
func crawlProcs() (lemmy, discourse, flowforum bool) {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		if e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil || !strings.Contains(string(b), "lemmycrawl") {
			continue
		}
		s := string(b)
		switch {
		case strings.Contains(s, "-discourse"):
			discourse = true
		case strings.Contains(s, "-flowforum"):
			flowforum = true
		default:
			lemmy = true
		}
	}
	return
}

// crawlRunning: is the Lemmy crawl alive?
func crawlRunning() bool {
	l, _, _ := crawlProcs()
	return l
}

// forumSources: what is feeding the forums right now, for the Sources card of the Forums tab.
func forumSources() map[string]any {
	l, d, f := crawlProcs()
	last := int64(0)
	if st, err := os.Stat(fsData + "/flowforum-cursor.txt"); err == nil {
		last = st.ModTime().Unix()
	}
	return map[string]any{"lemmy": l, "discourse": d, "flowforum": f, "flowforum_last": last, "gh": forumCrawlGitHub()}
}

var (
	jsMu  sync.Mutex
	jsAt  time.Time
	jsOut []map[string]any
)

// justSaved: the posts saved last, newest first: up to n of EACH kind of forum (Lemmy, Discourse, Flowforum), so a filter in the live tab always has posts,
// even when one crawl has just saved thousands of posts of another kind. They are read from the end of the file (the last 24 MB) and kept for 5 seconds.
var (
	langMu   sync.Mutex
	langAt   time.Time
	langSet  map[string]bool
)

// isLangHost: is this forum one of the Python / programming language forums (forums-languages.txt)? The list is re-read every 10 minutes.
func isLangHost(h string) bool {
	langMu.Lock()
	defer langMu.Unlock()
	if langSet == nil || time.Since(langAt) > 10*time.Minute {
		langAt = time.Now()
		m := map[string]bool{}
		if b, err := os.ReadFile(fsHome + "/forums-languages.txt"); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
					m[l] = true
				}
			}
		}
		langSet = m
	}
	return langSet[strings.ToLower(h)]
}

// langName: which programming language a language forum is about (for the language filter of the Forums tab).
func langName(h string) string {
	switch strings.ToLower(h) {
	case "discuss.python.org", "discuss.scientific-python.org", "discourse.jupyter.org", "discourse.pymc.io", "discourse.matplotlib.org", "forum.djangoproject.com",
		"discuss.streamlit.io", "discuss.pytorch.org", "discuss.ray.io", "discuss.bokeh.org", "discourse.holoviz.org", "discourse.pangeo.io", "forums.fast.ai", "discuss.tvm.apache.org":
		return "Python"
	case "users.rust-lang.org", "internals.rust-lang.org":
		return "Rust"
	case "forum.golangbridge.org":
		return "Go"
	case "discourse.julialang.org":
		return "Julia"
	case "discourse.haskell.org":
		return "Haskell"
	case "discourse.elm-lang.org":
		return "Elm"
	case "elixirforum.com":
		return "Elixir"
	case "discourse.ocaml.org":
		return "OCaml"
	case "forum.crystal-lang.org":
		return "Crystal"
	case "discourse.llvm.org", "forum.juce.com", "discourse.cmake.org", "forum.openframeworks.cc", "forum.lvgl.io":
		return "C / C++"
	case "ziggit.dev":
		return "Zig"
	case "discuss.kotlinlang.org":
		return "Kotlin"
	case "forums.swift.org":
		return "Swift"
	case "users.scala-lang.org", "contributors.scala-lang.org":
		return "Scala"
	case "clojureverse.org":
		return "Clojure"
	case "erlangforums.com":
		return "Erlang"
	case "discuss.rubyonrails.org":
		return "Ruby"
	case "forum.posit.co", "discuss.ropensci.org", "discourse.mc-stan.org":
		return "R"
	case "racket.discourse.group":
		return "Racket"
	case "discourse.coq.inria.fr":
		return "Coq"
	case "forum.modular.com":
		return "Mojo"
	case "forum.vuejs.org", "discuss.emberjs.com":
		return "JavaScript"
	case "forum.rescript-lang.org", "discourse.purescript.org":
		return "ReScript / PureScript"
	case "discourse.nixos.org":
		return "Nix"
	case "community.neo4j.com", "discuss.dgraph.io":
		return "SQL & databases"
	}
	return "Other"
}

func justSaved(n int) []map[string]any {
	jsMu.Lock()
	defer jsMu.Unlock()
	if jsOut != nil && time.Since(jsAt) < 5*time.Second {
		return jsOut
	}
	jsAt = time.Now()
	f, err := os.Open(lemmyPostsFile)
	if err != nil {
		return jsOut
	}
	defer f.Close()
	st, _ := f.Stat()
	size := min(st.Size(), 24<<20)
	buf := make([]byte, size)
	if _, err := f.ReadAt(buf, st.Size()-size); err != nil {
		return jsOut
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	counts := map[string]int{}
	var out []map[string]any
	for i := len(lines) - 1; i >= 1; i-- { // the first line of the piece may be cut
		var p lemmyPost
		if json.Unmarshal([]byte(lines[i]), &p) != nil || p.U == "" || adultText(p.T) || adultText(p.Community) || adultHost(p.Instance) {
			continue
		}
		kind := p.Platform
		if kind == "" {
			kind = "lemmy"
		}
		lang := isLangHost(p.Instance)
		lg := ""
		if lang {
			lg = langName(p.Instance)
		}
		// up to n of each kind of forum, and up to n of each programming language (so each language filter has posts)
		if counts[kind] >= n && (!lang || counts["lg:"+lg] >= n) {
			if counts["lemmy"] >= n && counts["discourse"] >= n && counts["flowforum"] >= n && counts["languages"] >= n*14 {
				break
			}
			continue
		}
		counts[kind]++
		if lang {
			counts["languages"]++
			counts["lg:"+lg]++
		}
		date := ""
		if len(p.Published) >= 10 {
			date = p.Published[:10]
		}
		var tc map[string]any
		if len(p.Top) > 0 {
			tc = map[string]any{"a": p.Top[0].A, "t": clip(p.Top[0].T, 220), "s": p.Top[0].S}
		}
		out = append(out, map[string]any{"b": clip(strings.TrimSpace(p.Body), 260), "tc": tc, "t": clip(p.T, 90), "u": p.U, "c": p.Community, "i": p.Instance, "s": p.Score, "d": date, "m": p.Comments, "p": p.Platform, "a": p.Published, "l": lang, "g": lg})
	}
	jsOut = out
	return out
}
