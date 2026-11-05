// rbxcrawl crawls Roblox game pages in a real browser (Browser Use), because the pages are built by JavaScript.
//
//	rbxcrawl -collect -queue games.txt          reads Roblox's English game sitemaps (sitemap-games.xml -> sitemap-games-1..10.xml) into a file of game addresses
//	rbxcrawl -queue games.txt -shard 0 -shards 4   opens the games one after another and reads title, creator, players, visits, favorites, genre, description
//
// Every game is saved at once to roblox-games.jsonl (for the Roblox tab) and, 500 at a time, sent to the index in ONE request per batch.
// It is a separate program, so restarting the web server does not stop it.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"hash/crc32"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var gamesFile = fsData + "/roblox-games.jsonl"

// flowcdn: our own picture store. A game's picture from Roblox's CDN is downloaded once and kept as JPEG XL (a JPEG is recompressed
// without any loss) in flowcdnDir under a short hash of its original address; the Flowsearch server hands the files out as /flowcdn/roblox/<hash>.jxl.
var flowcdnDir = fsCDN + "/roblox"

func flowcdnKey(src string) string {
	h := sha1.Sum([]byte(src))
	return hex.EncodeToString(h[:8])
}

func flowcdnSave(src string) {
	if !strings.HasPrefix(src, "https://") {
		return
	}
	path := flowcdnDir + "/" + flowcdnKey(src) + ".jxl"
	if _, err := os.Stat(path); err == nil {
		return
	}
	req, _ := http.NewRequest("GET", src, nil)
	req.Header.Set("User-Agent", "MySearchBot")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	img, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || resp.StatusCode != 200 || len(img) < 500 {
		return
	}
	var out, errb bytes.Buffer
	enc := exec.Command("cjxl", "-", "-", "-e", "7")
	enc.Stdin, enc.Stdout, enc.Stderr = bytes.NewReader(img), &out, &errb
	if enc.Run() != nil || out.Len() == 0 {
		log.Printf("flowcdn: could not convert %s: %s", src, strings.TrimSpace(errb.String()))
		return
	}
	os.MkdirAll(flowcdnDir, 0o755)
	if os.WriteFile(path+".tmp", out.Bytes(), 0o644) == nil {
		os.Rename(path+".tmp", path)
	}
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func js(c cdpCaller, expr string, out any) error {
	r, err := c.call("Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true})
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

var locRe = regexp.MustCompile(`<loc>([^<]+)</loc>`)

func fetchText(u string) (string, error) {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "MySearchBot")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	return string(b), err
}

// collect writes the addresses of the English games from Roblox's sitemaps.
func collect(queue string) {
	idx, err := fetchText("https://www.roblox.com/sitemap-games.xml")
	if err != nil {
		log.Fatal(err)
	}
	out, err := os.Create(queue)
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	total := 0
	for _, child := range locRe.FindAllStringSubmatch(idx, -1) {
		body, err := fetchText(child[1])
		if err != nil {
			log.Printf("%s: %v", child[1], err)
			continue
		}
		n := 0
		for _, m := range locRe.FindAllStringSubmatch(body, -1) {
			if strings.Contains(m[1], "/games/") {
				out.WriteString(m[1] + "\n")
				n++
			}
		}
		total += n
		log.Printf("%s: %d games", child[1], n)
		time.Sleep(500 * time.Millisecond)
	}
	log.Printf("done: %d game addresses in %s", total, queue)
}

type game struct {
	U, T, D, Creator, Genre, Active, Visits, Favorites, Img string
}

var (
	statRe = func(label string) *regexp.Regexp {
		return regexp.MustCompile(`(?m)^` + label + `\s*\n\s*([0-9][0-9.,]*\s?[KMB]?\+?)`)
	}
	activeRe, visitsRe, favRe = statRe("Active"), statRe("Visits"), statRe("Favorites")
	creatorRe                 = regexp.MustCompile(`(?m)^By\s+@?([^\n]+)`)
	genreRe                   = regexp.MustCompile(`(?m)^Genre\s*\n\s*([^\n]+)`)
	flushBatch                = func() {}
)

func main() {
	queue := flag.String("queue", "rbx-games.txt", "file of game addresses")
	collectOnly := flag.Bool("collect", false, "write the file of game addresses from Roblox's sitemaps and stop")
	shard := flag.Int("shard", 0, "this crawler's number (0 up)")
	shards := flag.Int("shards", 1, "how many crawlers run at once")
	n := flag.Int("n", 1000000, "how many games to save")
	batchSize := flag.Int("batch", 500, "games per request to the index")
	pause := flag.Duration("pause", 100*time.Millisecond, "pause between games")
	firefoxFlag := flag.Bool("firefox", false, "use a Firefox started on this machine (a GitHub Actions runner) instead of a Browser Use browser")
	chromeFlag := flag.Bool("chrome", false, "use a Chrome started on this machine (a GitHub Actions runner) instead of a Browser Use browser")
	remote := flag.String("remote", "", "send the games to the Flowsearch ingest at this address instead of writing the files here (needs MYSEARCH_TOKEN)")
	minutes := flag.Int("minutes", 0, "stop by itself after this many minutes (0: no limit)")
	flag.Parse()
	log.SetFlags(log.Ltime)
	remoteURL = strings.TrimRight(*remote, "/")
	if *minutes > 0 {
		rbxDeadline = time.Now().Add(time.Duration(*minutes) * time.Minute)
	}
	if *collectOnly {
		collect(*queue)
		return
	}

	// the games already saved are skipped
	have := map[string]bool{}
	if f, err := os.Open(gamesFile); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var v struct {
				U string `json:"u"`
			}
			if json.Unmarshal(sc.Bytes(), &v) == nil {
				have[v.U] = true
			}
		}
		f.Close()
	}
	var todo []string
	if f, err := os.Open(*queue); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			u := strings.TrimSpace(sc.Text())
			if u != "" && !have[u] && (*shards <= 1 || int(crc32.ChecksumIEEE([]byte(u))%uint32(*shards)) == *shard) {
				todo = append(todo, u)
			}
		}
		f.Close()
	} else {
		log.Fatal(err)
	}
	if remoteURL != "" { // the ingest says which of these games it does not have yet
		before := len(todo)
		todo = remoteUnknown(todo)
		log.Printf("the ingest has %d of the %d games already; %d are new", before-len(todo), before, len(todo))
		if *shards <= 1 && len(todo) > 1 { // in a different order every time, so a run that is stopped early does not always see the same games
			rand.Shuffle(len(todo), func(i, j int) { todo[i], todo[j] = todo[j], todo[i] })
		}
	}
	log.Printf("%d games for this crawler", len(todo))

	time.Sleep(time.Duration(*shard) * 2 * time.Second) // start one after another, so Browser Use is not asked for many browsers at once
	var c cdpCaller
	var stopBrowser func()
	if *firefoxFlag { // a Firefox started on this machine, driven over WebDriver
		firefoxMode = true
		ffc, err := newFFConn("en-US,en", func(string, json.RawMessage) {})
		if err != nil {
			log.Fatal(err)
		}
		c, stopBrowser = ffc, ffc.stop
	} else if *chromeFlag { // a Chrome started on this machine, driven over the DevTools
		chromeMode = true
		stop, cdpURL, err := chromeStart()
		if err != nil {
			log.Fatal(err)
		}
		stopBrowser = stop
		wsURL, err := buPageWS(cdpURL)
		if err != nil {
			stop()
			log.Fatal(err)
		}
		cc, err := newCDP(wsURL, func(string, json.RawMessage) {})
		if err != nil {
			stop()
			log.Fatal(err)
		}
		c = cc
	} else {
		id, cdpURL, err := buCreate()
		if err != nil {
			log.Fatal(err)
		}
		os.MkdirAll(fsRun + "/flowsearch-browsers", 0o755)
		os.WriteFile(fsRun + "/flowsearch-browsers/"+id, nil, 0o644)
		stopBrowser = func() {
			buStop(id)
			os.Remove(fsRun + "/flowsearch-browsers/" + id)
		}
		wsURL, err := buPageWS(cdpURL)
		if err != nil {
			log.Fatal(err)
		}
		cc, err := newCDP(wsURL, func(string, json.RawMessage) {})
		if err != nil {
			log.Fatal(err)
		}
		c = cc
	}
	defer stopBrowser()
	defer c.close()
	sig := make(chan os.Signal, 1) // stopped with kill: send what is waiting, close the browser
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		flushBatch()
		stopBrowser()
		os.Exit(1)
	}()
	buSetup(c)
	c.call("Network.setBlockedURLs", map[string]any{"urls": []string{"*.jpg*", "*.jpeg*", "*.png*", "*.gif*", "*.webp*", "*.svg*", "*.ico*", "*.woff*", "*.ttf*", "*.mp4*", "*.webm*", "*.m3u8*"}})

	var listF *os.File
	if remoteURL == "" {
		var err error
		listF, err = os.OpenFile(gamesFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatal(err)
		}
		defer listF.Close()
	}
	token, _ := os.ReadFile(fsSecrets + "/mysearch_token")
	var batch []map[string]string
	flushBatch = func() {
		if remoteURL != "" {
			remoteFlush()
			if len(batch) > 0 {
				batch = nil
			}
			return
		}
		if len(batch) == 0 {
			return
		}
		log.Printf("sending a batch of %d games to the index in one request", len(batch))
		body, _ := json.Marshal(batch)
		if req, e := http.NewRequest("POST", "http://127.0.0.1:8811/api/ingest", bytes.NewReader(body)); e == nil {
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
			req.Header.Set("Content-Type", "application/json")
			if resp, e := http.DefaultClient.Do(req); e == nil {
				resp.Body.Close()
			} else {
				log.Printf("sending the batch failed: %v", e)
			}
		}
		batch = nil
	}
	defer flushBatch()

	saved, skipped, streak := 0, 0, 0
	for _, u := range todo {
		if saved >= *n || (!rbxDeadline.IsZero() && time.Now().After(rbxDeadline)) {
			break
		}
		if _, e := c.call("Page.navigate", map[string]any{"url": u}); e != nil {
			continue
		}
		// read the page as soon as the game's title and its numbers are there
		var raw string
		deadline := time.Now().Add(14 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(250 * time.Millisecond)
			js(c, `JSON.stringify({url: location.href, h1: ((document.querySelector('h1')||{}).innerText||'').trim(), len: (document.body.innerText||'').length, has: /Active\s*\n\s*[0-9]/.test(document.body.innerText||'') && /Visits\s*\n\s*[0-9]/.test(document.body.innerText||'')})`, &raw)
			var st struct {
				URL string
				H1  string
				Len int
				Has bool
			}
			json.Unmarshal([]byte(raw), &st)
			if strings.Contains(st.URL, "/games/") && st.H1 != "" && st.Has {
				break
			}
		}
		var page string
		js(c, `JSON.stringify({title: ((document.querySelector('h1')||{}).innerText||'').trim(), desc: (document.querySelector('meta[property="og:description"]')||document.querySelector('meta[name="description"]')||{}).content||'', img: (document.querySelector('meta[property="og:image"]')||{}).content||'', text: (document.body.innerText||'').slice(0, 4000), page: document.title+' | '+location.href})`, &page)
		var p struct{ Title, Desc, Img, Text, Page string }
		json.Unmarshal([]byte(page), &p)
		if p.Title == "" {
			skipped++
			streak++
			log.Printf("%s: no game on the page; page is %q", u, clip(p.Page, 100))
			if streak >= 15 {
				log.Printf("%d pages in a row without a game; pausing 5 minutes", streak)
				time.Sleep(5 * time.Minute)
				streak = 0
			}
			continue
		}
		streak = 0
		first := func(re *regexp.Regexp) string {
			if m := re.FindStringSubmatch(p.Text); m != nil {
				return strings.TrimSpace(m[1])
			}
			return ""
		}
		g := game{U: u, T: clip(p.Title, 150), D: clip(strings.Join(strings.Fields(p.Desc), " "), 400), Creator: clip(first(creatorRe), 60), Genre: first(genreRe),
			Active: first(activeRe), Visits: first(visitsRe), Favorites: first(favRe), Img: p.Img}
		if remoteURL != "" { // the games go to the ingest, which saves them, indexes their text and keeps their pictures
			rGames = append(rGames, map[string]string{"u": g.U, "t": g.T, "d": g.D, "creator": g.Creator, "genre": g.Genre, "active": g.Active, "visits": g.Visits, "favorites": g.Favorites, "img": g.Img})
			if len(rGames) >= 100 {
				remoteFlush()
			}
			saved++
			log.Printf("saved %d: %s | %s | %s playing | %s visits", saved, clip(g.T, 40), g.Creator, g.Active, g.Visits)
			time.Sleep(*pause)
			continue
		}
		line, _ := json.Marshal(map[string]string{"u": g.U, "t": g.T, "d": g.D, "creator": g.Creator, "genre": g.Genre, "active": g.Active, "visits": g.Visits, "favorites": g.Favorites, "img": g.Img})
		listF.Write(append(line, '\n'))
		flowcdnSave(g.Img)
		facts := strings.TrimSpace(strings.Join([]string{"by " + g.Creator, g.Genre, g.Active + " playing", g.Visits + " visits", g.Favorites + " favorites"}, ", "))
		batch = append(batch, map[string]string{"url": u, "title": g.T, "description": clip(facts+". "+g.D, 300), "text": clip(g.T+". Roblox game. "+facts+". "+g.D, 2000), "source": "roblox"})
		if len(batch) >= *batchSize {
			flushBatch()
		}
		saved++
		log.Printf("saved %d: %s | %s | %s playing | %s visits", saved, clip(g.T, 40), g.Creator, g.Active, g.Visits)
		time.Sleep(*pause)
	}
	log.Printf("done: %d saved, %d skipped", saved, skipped)
}
