// lemmycrawl reads about 1000 Lemmy posts with Browser Use: real browsers open Lemmy's own public API addresses (/api/v3/post/list and
// /api/v3/comment/list) and read the JSON on the page: title, text, community, score, number of comments and the top 10 comments of each.
// Because a browser does the fetching, a Cloudflare or other captcha page is waited out (Browser Use solves it, we wait 3 seconds at a time).
// It checks each instance's robots.txt first (a plain request) and skips an instance that asks crawlers to stay away. Every post is saved to
// lemmy-posts.jsonl (for the comments dropdown of the Forums tab) and sent to the index, 500 posts at a time in one request.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

var out = fsData + "/lemmy-posts.jsonl"

var instances = []string{"lemmy.world", "lemmy.ml", "sh.itjust.works", "lemmy.ca", "programming.dev", "lemmy.zip", "beehaw.org", "slrpnk.net",
	"lemm.ee", "feddit.de", "lemmy.blahaj.zone", "discuss.tchncs.de", "infosec.pub", "midwest.social", "lemmy.dbzer0.com", "aussie.zone"}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

var spaces = regexp.MustCompile(`\s+`)
var captchaRe = regexp.MustCompile(`(?i)just a moment|attention required|verify you are human|verifying you are human|checking your browser|are you a robot|security check|captcha`)

// robotsAllows: does the instance's robots.txt let a crawler ("*" or MySearchBot) fetch /api/v3/ ? An unreachable robots.txt counts as allowed.
func robotsAllows(host string) bool {
	req, _ := http.NewRequest("GET", "https://"+host+"/robots.txt", nil)
	req.Header.Set("User-Agent", "MySearchBot")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil || resp.StatusCode != 200 {
		return true
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 500_000))
	applies, allowed, bestLen := false, true, -1
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch k {
		case "user-agent":
			applies = v == "*" || strings.EqualFold(v, "MySearchBot")
		case "disallow", "allow":
			if applies && v != "" && strings.HasPrefix("/api/v3/post/list", v) && len(v) > bestLen {
				bestLen, allowed = len(v), k == "allow"
			}
		}
	}
	return allowed
}

// browser is one Browser Use browser that reads JSON pages.
type browser struct {
	c     *cdpConn
	plain bool // -plain: no browser, normal HTTP requests
}

func (b *browser) js(expr string, out any) error {
	r, err := b.c.call("Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true})
	if err != nil {
		return err
	}
	var d struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	json.Unmarshal(r, &d)
	return json.Unmarshal(d.Result.Value, out)
}

// get opens an API address in the browser and decodes the JSON text of the page. A captcha page is waited out, 3 seconds at a time.
func (b *browser) get(u string, v any) error {
	if b.plain {
		return plainOrFirefox(u, v)
	}
	if _, err := b.c.call("Page.navigate", map[string]any{"url": u}); err != nil {
		return err
	}
	var text string
	for i := 0; i < 30; i++ { // up to about 30 seconds: the page, or a challenge that Browser Use solves
		time.Sleep(600 * time.Millisecond)
		var cur string
		if err := b.js(`(document.body ? document.body.innerText : '').slice(0, 6000000)`, &cur); err != nil {
			continue
		}
		t := strings.TrimSpace(cur)
		if strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
			text = t
			break
		}
		if captchaRe.MatchString(t[:min(len(t), 400)]) {
			log.Printf("captcha page for %s: waiting 3 seconds", u)
			time.Sleep(3 * time.Second)
		}
		text = t
	}
	if !strings.HasPrefix(text, "{") {
		return fmt.Errorf("no JSON on the page (%q)", clip(text, 60))
	}
	return json.Unmarshal([]byte(text), v)
}

// browserDead: the error says the connection to the browser is gone (not that a page was bad).
func browserDead(err error) bool {
	if plainMode { // there is no browser to be dead: a timeout is a slow server, and plainGet has already tried again
		return false
	}
	s := err.Error()
	for _, k := range []string{"broken pipe", "connection reset", "use of closed", "EOF", "closed", "timeout", "i/o"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

var (
	closersMu sync.Mutex
	closers   []func()
)

func newBrowser(delay time.Duration) (*browser, func(), error) {
	if plainMode { // no browser to start
		return &browser{plain: true}, func() {}, nil
	}
	time.Sleep(delay)
	var cdpURL string
	var stop func()
	var err error
	if chromeMode { // a Chrome started on this machine (a GitHub Actions runner)
		if stop, cdpURL, err = chromeStart(); err != nil {
			return nil, nil, err
		}
	} else {
		var id string
		if id, cdpURL, err = buCreate(); err != nil {
			return nil, nil, err
		}
		os.MkdirAll(fsRun + "/flowsearch-browsers", 0o755)
		os.WriteFile(fsRun + "/flowsearch-browsers/"+id, nil, 0o644)
		stop = func() {
			os.Remove(fsRun + "/flowsearch-browsers/" + id)
			buStop(id)
		}
	}
	wsURL, err := buPageWS(cdpURL)
	if err != nil {
		stop()
		return nil, nil, err
	}
	c, err := newCDP(wsURL, func(string, json.RawMessage) {})
	if err != nil {
		stop()
		return nil, nil, err
	}
	buSetup(c)
	c.call("Network.setBlockedURLs", map[string]any{"urls": []string{"*.jpg*", "*.png*", "*.gif*", "*.webp*", "*.svg*", "*.woff*", "*.mp4*"}})
	closeAll := func() { c.close(); stop() }
	closersMu.Lock()
	closers = append(closers, closeAll)
	closersMu.Unlock()
	return &browser{c: c}, closeAll, nil
}

type post struct {
	U         string    `json:"u"`
	Ap        string    `json:"ap"`
	T         string    `json:"t"`
	Body      string    `json:"body"`
	Community string    `json:"community"`
	Name      string    `json:"name"`
	Instance  string    `json:"instance"`
	Score     int       `json:"score"`
	Comments  int       `json:"comments"`
	Published string    `json:"published"`
	Top       []comment `json:"top,omitempty"`
	Platform  string    `json:"platform,omitempty"` // "" = Lemmy, "discourse"
}
type comment struct {
	A string `json:"a"`
	T string `json:"t"`
	S int    `json:"s"`
}

func main() {
	want := flag.Int("n", 1000, "how many posts in all")
	perInstance := flag.Int("per", 80, "posts from each instance")
	browsers := flag.Int("browsers", 4, "how many Browser Use browsers read at the same time")
	find := flag.String("find", "", "look for communities with this name on lemmy.world (through a browser) and print them, then stop")
	community := flag.String("community", "", "crawl only this community, e.g. openstreetmap@lemmy.world, newest posts first, as many as -n")
	instance := flag.String("instance", "lemmy.world", "with -community: the instance whose API is opened")
	serversFile := flag.String("servers", "", "a file with one Lemmy server per line, biggest first: crawl these instead of the built-in list")
	newest := flag.Bool("new", false, "read the NEWEST posts of every server instead of the best of all time; -n is then how many new posts to add in this run")
	discourse := flag.String("discourse", "", "a file with one Discourse forum per line (discuss.python.org ...): crawl these (newest topics, -per from each) instead of Lemmy")
	flowforum := flag.String("flowforum", "", "the address of our own forum (Flowforum): read its new posts from /api/export instead of crawling Lemmy")
	watch := flag.Bool("watch", false, "with -flowforum: stay running and read the forum whenever it has a new post or comment")
	minutes := flag.Int("minutes", 0, "stop by itself after this many minutes and send what is waiting (0: no limit)")
	hostMinutes := flag.Int("hostminutes", 0, "no forum keeps a worker longer than this many minutes (0: no limit)")
	ffFlag := flag.Bool("firefox", false, "with -plain: an address that answers 403 is read with Firefox (geckodriver) instead")
	plainFlag := flag.Bool("plain", false, "no browser at all: fetch the JSON addresses with normal HTTP requests (-browsers is then the number of workers)")
	chromeFlag := flag.Bool("chrome", false, "use a Chrome started on this machine (a GitHub Actions runner) instead of a Browser Use browser")
	remote := flag.String("remote", "", "the address of the Flowsearch ingest: send the posts there (and ask it which are new) instead of writing lemmy-posts.jsonl")
	flag.Parse()
	chromeMode = *chromeFlag
	plainMode = *plainFlag
	ffMode = *ffFlag
	if *minutes > 0 {
		deadline = time.Now().Add(time.Duration(*minutes) * time.Minute)
	}
	hostBudget = time.Duration(*hostMinutes) * time.Minute
	remoteURL = strings.TrimRight(*remote, "/")
	log.SetFlags(log.Ltime)
	sortBy := "TopAll"
	if *newest {
		sortBy = "New"
	}
	if *serversFile != "" {
		b, err := os.ReadFile(*serversFile)
		if err != nil {
			log.Fatalf("servers file: %v", err)
		}
		instances = nil
		for _, l := range strings.Fields(string(b)) {
			instances = append(instances, strings.ToLower(l))
		}
		log.Printf("%d servers from %s", len(instances), *serversFile)
	}
	origWant := *want              // the number asked for; the posts already saved are subtracted only for the all-instances crawl
	sig := make(chan os.Signal, 1) // stopped with kill: close the browsers, so they do not keep running and billing
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		closersMu.Lock()
		for _, c := range closers {
			c()
		}
		closersMu.Unlock()
		os.Exit(1)
	}()
	if *find != "" {
		br, closeB, err := newBrowser(0)
		if err != nil {
			log.Fatal(err)
		}
		defer closeB()
		var res struct {
			Communities []struct {
				Community struct {
					Name    string `json:"name"`
					Title   string `json:"title"`
					ActorID string `json:"actor_id"`
				} `json:"community"`
				Counts struct {
					Subscribers int `json:"subscribers"`
					Posts       int `json:"posts"`
				} `json:"counts"`
			} `json:"communities"`
		}
		if err := br.get("https://"+*instance+"/api/v3/search?q="+*find+"&type_=Communities&limit=15&sort=TopAll", &res); err != nil {
			log.Fatal(err)
		}
		for _, c := range res.Communities {
			fmt.Printf("%-28s %-40s %6d subscribers %6d posts  %s\n", c.Community.Name, clip(c.Community.Title, 40), c.Counts.Subscribers, c.Counts.Posts, c.Community.ActorID)
		}
		return
	}
	var mu sync.Mutex
	var posts []post
	seen := map[string]bool{}
	// posts saved by an earlier run are not read again, and count toward the total
	if ef, err := os.Open(out); err == nil {
		sc := bufio.NewScanner(ef)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		have := 0
		for sc.Scan() {
			var p struct {
				Ap string `json:"ap"`
			}
			if json.Unmarshal(sc.Bytes(), &p) == nil && p.Ap != "" {
				seen[p.Ap] = true
				have++
			}
		}
		ef.Close()
		if !*newest { // -new: the posts already saved do not count, -n is the number of new ones
			*want -= have
		}
		log.Printf("%d posts are already saved; %d more to read", have, max(*want, 0))
	}
	// every finished post is written to the file at once and joins a batch for the index; a full batch of 500 goes out in ONE request
	f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil && remoteURL == "" { // with -remote the posts are not written to this file
		log.Fatal(err)
	}
	if f != nil {
		defer f.Close()
	}
	token, _ := os.ReadFile(fsSecrets + "/mysearch_token")
	var batch []map[string]string
	flush := func() {
		remoteFlush() // -remote: the posts that are waiting go to the ingest
		if len(batch) == 0 {
			return
		}
		log.Printf("sending a batch of %d posts to the index in one request", len(batch))
		body, _ := json.Marshal(batch)
		if req, e := http.NewRequest("POST", "http://127.0.0.1:8811/api/ingest", bytes.NewReader(body)); e == nil {
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
			req.Header.Set("Content-Type", "application/json")
			if resp, e := http.DefaultClient.Do(req); e == nil {
				resp.Body.Close()
			}
		}
		batch = nil
	}
	emit := func(p post) { // called with mu held
		if remoteURL != "" { // -remote: a batch of 1000 posts goes to the ingest in one request
			rbatch = append(rbatch, p)
			if len(rbatch) >= 1000 {
				remoteFlush()
			}
			return
		}
		line, _ := json.Marshal(p)
		f.Write(append(line, '\n'))
		var cs []string
		for _, c := range p.Top {
			cs = append(cs, c.T)
		}
		text := p.T + ". " + p.Community + " on " + p.Instance + ". " + p.Body + " " + strings.Join(cs, " ")
		source := "lemmy"
		if p.Platform != "" {
			source = p.Platform
		}
		batch = append(batch, map[string]string{"url": p.U, "title": p.T, "description": p.Community + " - " + p.Name, "text": clip(text, 2000), "source": source})
		if len(batch) >= 500 {
			flush()
		}
	}
	if *flowforum != "" {
		pull := func() {
			flowforumRun(*flowforum, func(p post) {
				mu.Lock()
				emit(p)
				mu.Unlock()
			})
			mu.Lock()
			flush()
			mu.Unlock()
		}
		if !*watch {
			pull()
			return
		}
		flowforumWatch(*flowforum, pull) // one WebSocket: the forum says "changed", then it is read
	}
	if *discourse != "" {
		b, err := os.ReadFile(*discourse)
		if err != nil {
			log.Fatalf("discourse file: %v", err)
		}
		var hosts []string
		for _, l := range strings.Fields(string(b)) {
			hosts = append(hosts, strings.ToLower(l))
		}
		log.Printf("%d Discourse forums from %s", len(hosts), *discourse)
		discourseRun(hosts, *perInstance, *browsers, seen, &mu, func(p post) {
			mu.Lock()
			posts = append(posts, p)
			emit(p)
			mu.Unlock()
		})
		mu.Lock()
		flush()
		mu.Unlock()
		log.Printf("%d topics in all", len(posts))
		return
	}
	if *community != "" { // one community, newest posts first, up to -n of them; batches of 500 to the index
		br, closeB, err := newBrowser(0)
		if err != nil {
			log.Fatal(err)
		}
		defer closeB()
		n := 0
		for page := 1; n < origWant; page++ {
			var list struct {
				Posts []struct {
					Post struct {
						ID        int    `json:"id"`
						Name      string `json:"name"`
						Body      string `json:"body"`
						ApID      string `json:"ap_id"`
						Published string `json:"published"`
					} `json:"post"`
					Community struct {
						Name  string `json:"name"`
						Title string `json:"title"`
					} `json:"community"`
					Counts struct {
						Score    int `json:"score"`
						Comments int `json:"comments"`
					} `json:"counts"`
				} `json:"posts"`
			}
			if err := br.get(fmt.Sprintf("https://%s/api/v3/post/list?community_name=%s&sort=New&limit=50&page=%d", *instance, *community, page), &list); err != nil {
				log.Printf("page %d: %v", page, err)
				break
			}
			if len(list.Posts) == 0 {
				break
			}
			for _, p := range list.Posts {
				if n >= origWant {
					break
				}
				if seen[p.Post.ApID] || strings.TrimSpace(p.Post.Name) == "" {
					continue
				}
				seen[p.Post.ApID] = true
				pt := post{U: fmt.Sprintf("https://%s/post/%d", *instance, p.Post.ID), Ap: p.Post.ApID, T: clip(p.Post.Name, 250), Body: clip(spaces.ReplaceAllString(p.Post.Body, " "), 1000),
					Community: "c/" + p.Community.Name, Name: p.Community.Title, Instance: *instance, Score: p.Counts.Score, Comments: p.Counts.Comments, Published: p.Post.Published}
				if p.Counts.Comments > 0 {
					var cl struct {
						Comments []struct {
							Comment struct {
								Content string `json:"content"`
							} `json:"comment"`
							Creator struct {
								Name string `json:"name"`
							} `json:"creator"`
							Counts struct {
								Score int `json:"score"`
							} `json:"counts"`
						} `json:"comments"`
					}
					if br.get(fmt.Sprintf("https://%s/api/v3/comment/list?post_id=%d&sort=Top&limit=10&max_depth=1&type_=All", *instance, p.Post.ID), &cl) == nil {
						for _, c := range cl.Comments {
							pt.Top = append(pt.Top, comment{c.Creator.Name, clip(spaces.ReplaceAllString(c.Comment.Content, " "), 500), c.Counts.Score})
						}
					}
				}
				mu.Lock()
				posts = append(posts, pt)
				emit(pt)
				mu.Unlock()
				n++
			}
			log.Printf("%d posts so far", n)
		}
		mu.Lock()
		flush()
		mu.Unlock()
		log.Printf("done: %d posts of c/%s", n, *community)
		return
	}
	jobs := make(chan string, len(instances))
	for _, h := range instances {
		jobs <- h
	}
	close(jobs)
	var wg sync.WaitGroup
	for w := 0; w < *browsers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			br, closeB, err := newBrowser(time.Duration(w) * 2 * time.Second) // start one after another
			for try := 0; err != nil && try < 5; try++ {                      // the service may be busy: try again
				log.Printf("browser %d: %v; trying again in 30 seconds", w, err)
				time.Sleep(30 * time.Second)
				br, closeB, err = newBrowser(0)
			}
			if err != nil {
				log.Printf("browser %d: %v", w, err)
				return
			}
			defer func() { closeB() }() // closeB changes when the browser is replaced
			// a Browser Use browser lives only about an hour, then its connection breaks ("broken pipe"): the dead one is closed, a new one is
			// started within a minute or two, and the page that failed is read again
			getOK := func(u string, v any) error {
				err := br.get(u, v)
				for try := 0; err != nil && browserDead(err) && try < 4; try++ {
					log.Printf("browser %d is dead (%v): starting a new one", w, err)
					closeB()
					nb, nc, e2 := newBrowser(0)
					if e2 != nil {
						log.Printf("browser %d: new browser failed: %v; trying again in 30 seconds", w, e2)
						time.Sleep(30 * time.Second)
						continue
					}
					br, closeB = nb, nc
					err = br.get(u, v)
				}
				return err
			}
			for host := range jobs {
				if timeUp() {
					continue
				}
				hostStart := time.Now()
				if !robotsAllows(host) {
					log.Printf("%s: robots.txt asks crawlers to stay away: skipped", host)
					continue
				}
				n := 0
				// -per 0: no limit, the server is read until its posts run out (or the time limits are reached)
				for page := 1; page < 100000 && (*perInstance <= 0 || (page <= (*perInstance+39)/40 && n < *perInstance)) && !timeUp() && !hostOver(hostStart); page++ {
					var list struct {
						Posts []struct {
							Post struct {
								ID        int    `json:"id"`
								Name      string `json:"name"`
								Body      string `json:"body"`
								ApID      string `json:"ap_id"`
								Published string `json:"published"`
							} `json:"post"`
							Community struct {
								Name  string `json:"name"`
								Title string `json:"title"`
							} `json:"community"`
							Counts struct {
								Score    int `json:"score"`
								Comments int `json:"comments"`
							} `json:"counts"`
						} `json:"posts"`
					}
					if err := getOK(fmt.Sprintf("https://%s/api/v3/post/list?type_=Local&sort=%s&limit=40&page=%d", host, sortBy, page), &list); err != nil {
						log.Printf("%s: post list: %v", host, err)
						break
					}
					if len(list.Posts) == 0 {
						break // the last page was the one before: a server answers "400" for pages beyond its last, so do not ask for them
					}
					var unk map[string]bool // -remote: the ingest says which of these posts it does not have yet
					if remoteURL != "" {
						var aps []string
						for _, p := range list.Posts {
							aps = append(aps, p.Post.ApID)
						}
						unk = remoteUnknown(aps)
						if sortBy == "New" && len(unk) == 0 {
							break // newest first: when a whole page is known already, all older pages are known too
						}
					}
					for _, p := range list.Posts {
						if (*perInstance > 0 && n >= *perInstance) || timeUp() || hostOver(hostStart) {
							break
						}
						if unk != nil && !unk[p.Post.ApID] {
							continue
						}
						mu.Lock()
						dup := seen[p.Post.ApID] || (*want > 0 && len(posts) >= *want)
						if !dup {
							seen[p.Post.ApID] = true
						}
						mu.Unlock()
						if dup || strings.TrimSpace(p.Post.Name) == "" {
							continue
						}
						pt := post{U: fmt.Sprintf("https://%s/post/%d", host, p.Post.ID), Ap: p.Post.ApID, T: clip(p.Post.Name, 250), Body: clip(spaces.ReplaceAllString(p.Post.Body, " "), 1000),
							Community: "c/" + p.Community.Name, Name: p.Community.Title, Instance: host, Score: p.Counts.Score, Comments: p.Counts.Comments, Published: p.Post.Published}
						if p.Counts.Comments > 0 {
							var cl struct {
								Comments []struct {
									Comment struct {
										Content string `json:"content"`
									} `json:"comment"`
									Creator struct {
										Name string `json:"name"`
									} `json:"creator"`
									Counts struct {
										Score int `json:"score"`
									} `json:"counts"`
								} `json:"comments"`
							}
							if getOK(fmt.Sprintf("https://%s/api/v3/comment/list?post_id=%d&sort=Top&limit=10&max_depth=1&type_=All", host, p.Post.ID), &cl) == nil {
								for _, c := range cl.Comments {
									pt.Top = append(pt.Top, comment{c.Creator.Name, clip(spaces.ReplaceAllString(c.Comment.Content, " "), 500), c.Counts.Score})
								}
							}
						}
						mu.Lock()
						posts = append(posts, pt)
						emit(pt)
						mu.Unlock()
						n++
					}
				}
				log.Printf("%s: %d posts (%d in all so far)", host, n, func() int { mu.Lock(); defer mu.Unlock(); return len(posts) }())
			}
		}(w)
	}
	wg.Wait()
	log.Printf("%d posts in all", len(posts))
	mu.Lock()
	flush() // what is left of the last batch
	mu.Unlock()
}
