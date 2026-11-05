package main

// -flowforum <address>: read the posts of our own forum (Flowforum, a Cloudflare Worker) from its public feed <address>/api/export.
// A plain request, no browser and no token: it is our own site. The feed has the posts in the shape of the Forums tab (platform "flowforum")
// with their best comments. A new post comes at once; a post that gets a new comment comes AGAIN with its new best comments (the Forums tab
// takes the newest line of a post). The cursor file flowforum-cursor.txt holds two numbers: the last post id and the last comment id read.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var flowforumCursor = fsData + "/flowforum-cursor.txt"

func flowforumRun(base string, emit func(post)) {
	base = strings.TrimRight(base, "/")
	if !strings.HasPrefix(base, "http") {
		base = "https://" + base
	}
	after, cafter := 0, 0
	if b, err := os.ReadFile(flowforumCursor); err == nil {
		f := strings.Fields(string(b))
		if len(f) > 0 {
			after, _ = strconv.Atoi(f[0])
		}
		if len(f) > 1 {
			cafter, _ = strconv.Atoi(f[1])
		}
	}
	client := &http.Client{Timeout: 30 * time.Second}
	newPosts, updated := 0, 0
	for { // no limit: ask again until the feed has nothing new
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/export?after=%d&cafter=%d", base, after, cafter), nil)
		req.Header.Set("User-Agent", "MySearchBot")
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("flowforum: %v", err)
			return
		}
		var feed struct {
			Last  int    `json:"last"`
			Lastc int    `json:"lastc"`
			Posts []post `json:"posts"`
		}
		err = json.NewDecoder(resp.Body).Decode(&feed)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			log.Printf("flowforum: answer %d, %v", resp.StatusCode, err)
			return
		}
		if len(feed.Posts) == 0 && feed.Lastc == cafter {
			break
		}
		for _, p := range feed.Posts {
			if strings.TrimSpace(p.T) == "" {
				continue
			}
			p.Platform = "flowforum"
			if p.ID() > after { // (see below)
				newPosts++
			} else {
				updated++
			}
			emit(p)
		}
		if feed.Last == after && feed.Lastc == cafter {
			break // nothing moved: do not ask the same thing again
		}
		after, cafter = feed.Last, feed.Lastc
		os.WriteFile(flowforumCursor, []byte(fmt.Sprintf("%d %d\n", after, cafter)), 0o644)
	}
	log.Printf("flowforum: %d new posts, %d posts with new comments, up to post %d and comment %d", newPosts, updated, after, cafter)
}

// flowforumWatch: ONE WebSocket to the forum (wss://<host>/ws?key=...). The forum sends the word "changed" when a post or comment is added; then
// pull() reads what is new. On connecting it also says "changed", so everything missed while this was off is read. A ping every 30 seconds keeps the
// connection open (the forum answers pong), and a lost connection is opened again after a few seconds.
func flowforumWatch(base string, pull func()) {
	host := strings.TrimPrefix(strings.TrimPrefix(strings.TrimRight(base, "/"), "https://"), "http://")
	wait := 5 * time.Second
	for {
		key, err := os.ReadFile(fsSecrets + "/flowforum_feed_key")
		if err != nil {
			log.Printf("flowforum: no key file: %v", err)
			time.Sleep(time.Minute)
			continue
		}
		ws, err := wsDial("wss://" + host + "/ws?key=" + strings.TrimSpace(string(key)))
		if err != nil {
			log.Printf("flowforum: cannot connect: %v; again in %s", err, wait)
			time.Sleep(wait)
			wait = min(wait*2, 2*time.Minute)
			continue
		}
		wait = 5 * time.Second
		log.Printf("flowforum: connected, waiting for changes")
		stop := make(chan struct{})
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					if ws.writeFrame(1, []byte("ping")) != nil {
						return
					}
				case <-stop:
					return
				}
			}
		}()
		for {
			ws.c.SetReadDeadline(time.Now().Add(100 * time.Second)) // a pong comes every 30 seconds; silence means the connection is dead
			msg, err := ws.readMsg()
			if err != nil {
				break
			}
			if string(msg) == "changed" {
				pull()
			}
		}
		close(stop)
		ws.c.Close()
		log.Printf("flowforum: connection lost, opening it again in %s", wait)
		time.Sleep(wait)
	}
}

// ID: the number at the end of the post's address (https://host/p/12).
func (p post) ID() int {
	i := strings.LastIndex(p.U, "/")
	n, _ := strconv.Atoi(p.U[i+1:])
	return n
}
