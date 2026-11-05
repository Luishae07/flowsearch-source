package main

// -discourse <file>: the same crawl for Discourse forums (discuss.python.org and others). A Discourse forum has a public JSON on every address:
// /latest.json lists the topics, /t/<id>.json has the posts of a topic, /categories.json the category names. A Browser Use browser opens them like
// it opens Lemmy's API. The robots.txt of the forum is checked first. Posts are saved to the same file and sent to the index like Lemmy posts,
// with community "c/<category>" and the platform "discourse".

import (
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"fmt"
)

var tagRe = regexp.MustCompile(`<[^>]*>`)

func plainText(cooked string) string {
	s := strings.NewReplacer("</p>", " ", "<br>", " ", "<br/>", " ", "</li>", " ", "</pre>", " ").Replace(cooked)
	s = tagRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(spaces.ReplaceAllString(html.UnescapeString(s), " "))
}

// robotsAllowsPath: does the host's robots.txt let a crawler ("*" or MySearchBot) fetch this path? An unreachable robots.txt counts as allowed.
func robotsAllowsPath(host, path string) bool {
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
			if applies && v != "" && strings.HasPrefix(path, v) && len(v) > bestLen {
				bestLen, allowed = len(v), k == "allow"
			}
		}
	}
	return allowed
}

type dPost struct {
	Username   string `json:"username"`
	Cooked     string `json:"cooked"`
	PostNumber int    `json:"post_number"`
	Score      float64 `json:"score"`
	Actions    []struct {
		ID    int `json:"id"`
		Count int `json:"count"`
	} `json:"actions_summary"`
}

func (p dPost) likes() int {
	for _, a := range p.Actions {
		if a.ID == 2 {
			return a.Count
		}
	}
	return 0
}

// discourseRun reads the newest topics of every host. emit is called for every finished post (it takes the lock itself).
func discourseRun(hosts []string, per, nbrowsers int, seen map[string]bool, mu *sync.Mutex, emit func(post)) {
	jobs := make(chan string, len(hosts))
	for _, h := range hosts {
		jobs <- h
	}
	close(jobs)
	var wg sync.WaitGroup
	for w := 0; w < nbrowsers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			br, closeB, err := newBrowser(time.Duration(w) * 2 * time.Second)
			for try := 0; err != nil && try < 5; try++ {
				log.Printf("browser %d: %v; trying again in 30 seconds", w, err)
				time.Sleep(30 * time.Second)
				br, closeB, err = newBrowser(0)
			}
			if err != nil {
				log.Printf("browser %d: %v", w, err)
				return
			}
			defer func() { closeB() }()
			getOK := func(u string, v any) error {
				err := br.get(u, v)
				for try := 0; err != nil && browserDead(err) && try < 4; try++ {
					log.Printf("browser %d is dead (%v): starting a new one", w, err)
					closeB()
					nb, nc, e2 := newBrowser(0)
					if e2 != nil {
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
				if !robotsAllowsPath(host, "/latest.json") || !robotsAllowsPath(host, "/t/") {
					log.Printf("%s: robots.txt asks crawlers to stay away: skipped", host)
					continue
				}
				cats := map[int]string{}
				var cl struct {
					CategoryList struct {
						Categories []struct {
							ID   int    `json:"id"`
							Name string `json:"name"`
						} `json:"categories"`
					} `json:"category_list"`
				}
				if getOK("https://"+host+"/categories.json", &cl) == nil {
					for _, c := range cl.CategoryList.Categories {
						cats[c.ID] = c.Name
					}
				}
				n := 0
				// per <= 0: no limit, the forum is read until its topics run out (or the time limits are reached)
				for page := 0; page < 100000 && (per <= 0 || n < per) && !timeUp() && !hostOver(hostStart); page++ {
					var list struct {
						TopicList struct {
							Topics []struct {
								ID         int    `json:"id"`
								Title      string `json:"title"`
								Slug       string `json:"slug"`
								PostsCount int    `json:"posts_count"`
								LikeCount  int    `json:"like_count"`
								CreatedAt  string `json:"created_at"`
								CategoryID int    `json:"category_id"`
							} `json:"topics"`
						} `json:"topic_list"`
					}
					if err := getOK(fmt.Sprintf("https://%s/latest.json?page=%d", host, page), &list); err != nil {
						log.Printf("%s: topic list: %v", host, err)
						break
					}
					if len(list.TopicList.Topics) == 0 {
						break
					}
					var unk map[string]bool // -remote: the ingest says which of these topics it does not have yet
					if remoteURL != "" {
						var us []string
						for _, t := range list.TopicList.Topics {
							us = append(us, fmt.Sprintf("https://%s/t/%s/%d", host, t.Slug, t.ID))
						}
						unk = remoteUnknown(us)
					}
					for _, t := range list.TopicList.Topics {
						if (per > 0 && n >= per) || timeUp() || hostOver(hostStart) {
							break
						}
						u := fmt.Sprintf("https://%s/t/%s/%d", host, t.Slug, t.ID)
						if unk != nil && !unk[u] {
							continue
						}
						mu.Lock()
						dup := seen[u]
						if !dup {
							seen[u] = true
						}
						mu.Unlock()
						if dup || strings.TrimSpace(t.Title) == "" {
							continue
						}
						var tj struct {
							PostStream struct {
								Posts []dPost `json:"posts"`
							} `json:"post_stream"`
						}
						body := ""
						var top []comment
						if getOK(fmt.Sprintf("https://%s/t/%d.json", host, t.ID), &tj) == nil && len(tj.PostStream.Posts) > 0 {
							body = clip(plainText(tj.PostStream.Posts[0].Cooked), 1000)
							rest := append([]dPost(nil), tj.PostStream.Posts[1:]...)
							sort.SliceStable(rest, func(i, j int) bool { return rest[i].likes() > rest[j].likes() })
							for i, p := range rest {
								if i >= 10 {
									break
								}
								top = append(top, comment{p.Username, clip(plainText(p.Cooked), 500), p.likes()})
							}
						}
						cat := cats[t.CategoryID]
						if cat == "" {
							cat = "general"
						}
						pt := post{U: u, Ap: u, T: clip(t.Title, 250), Body: body, Community: "c/" + strings.ToLower(strings.ReplaceAll(cat, " ", "-")), Name: cat,
							Instance: host, Score: t.LikeCount, Comments: max(t.PostsCount-1, 0), Published: t.CreatedAt, Top: top, Platform: "discourse"}
						emit(pt)
						n++
						time.Sleep(700 * time.Millisecond) // a forum's own limit for anonymous readers is gentle
					}
				}
				log.Printf("%s: %d topics", host, n)
			}
		}(w)
	}
	wg.Wait()
}
