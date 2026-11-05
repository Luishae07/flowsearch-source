package main

// Roblox games from GitHub Actions. The Roblox crawl (rbxcrawl, in Firefox on a GitHub runner) cannot write the games file itself, so it sends the games
// here, to the same public address as the page ingest:
//   POST /api/roblox/known  {"urls":[game addresses]}                     -> {"new":[the ones this server does not have yet]}
//   POST /api/roblox/games  {"games":[{u,t,d,creator,genre,active,visits,favorites,img}]}  (gzip allowed)  -> {"added":n}
// Both need the same Bearer token as /api/ingest. A game is saved to roblox-games.jsonl (the Roblox tab), its text goes to the search index through the
// forum queue (forumPageQ), and its picture is fetched here and kept as JPEG XL in /media/data/flowcdn/roblox (what the crawler used to do itself).

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	rbxGamesFile  = fsData + "/roblox-games.jsonl"
	rbxFlowcdnDir = fsCDN + "/roblox"
)

var (
	rbxMu     sync.Mutex
	rbxSet    = map[uint64]struct{}{}
	rbxLoaded bool
	rbxImgQ   = make(chan string, 5000)
	rbxImgOne sync.Once
)

func rbxLoad() {
	if rbxLoaded {
		return
	}
	if f, err := os.Open(rbxGamesFile); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var v struct {
				U string `json:"u"`
			}
			if json.Unmarshal(sc.Bytes(), &v) == nil && v.U != "" {
				rbxSet[fkHash(v.U)] = struct{}{}
			}
		}
		f.Close()
	}
	rbxLoaded = true
}

func rbxImgSave(src string) {
	if !strings.HasPrefix(src, "https://") {
		return
	}
	sum := sha1.Sum([]byte(src))
	path := rbxFlowcdnDir + "/" + hex.EncodeToString(sum[:8]) + ".jxl"
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
	os.MkdirAll(rbxFlowcdnDir, 0o755)
	if os.WriteFile(path+".tmp", out.Bytes(), 0o644) == nil {
		os.Rename(path+".tmp", path)
	}
}

// rbxImgWorker converts the pictures one after another in the background (cjxl is the slow part).
func rbxImgWorker() {
	for src := range rbxImgQ {
		rbxImgSave(src)
	}
}

func robloxKnown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !authed(r) {
		http.Error(w, `{"error":"POST with the token"}`, 401)
		return
	}
	body, err := forumBody(r)
	var req struct {
		URLs []string `json:"urls"`
	}
	if err != nil || json.Unmarshal(body, &req) != nil || len(req.URLs) > 50000 {
		http.Error(w, `{"error":"bad request"}`, 400)
		return
	}
	rbxMu.Lock()
	rbxLoad()
	unknown := []string{}
	for _, u := range req.URLs {
		if _, ok := rbxSet[fkHash(u)]; !ok {
			unknown = append(unknown, u)
		}
	}
	rbxMu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{"new": unknown})
}

func robloxGames(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !authed(r) {
		http.Error(w, `{"error":"POST with the token"}`, 401)
		return
	}
	body, err := forumBody(r)
	var req struct {
		Games []map[string]string `json:"games"`
	}
	if err != nil || json.Unmarshal(body, &req) != nil || len(req.Games) > 5000 {
		http.Error(w, `{"error":"bad request"}`, 400)
		return
	}
	rbxImgOne.Do(func() { go rbxImgWorker() })
	rbxMu.Lock()
	defer rbxMu.Unlock()
	rbxLoad()
	var data bytes.Buffer
	var pages []Page
	for _, g := range req.Games {
		u := g["u"]
		if !strings.HasPrefix(u, "https://www.roblox.com/games/") || strings.TrimSpace(g["t"]) == "" {
			continue
		}
		if _, ok := rbxSet[fkHash(u)]; ok {
			continue
		}
		rbxSet[fkHash(u)] = struct{}{}
		line, _ := json.Marshal(map[string]string{"u": u, "t": g["t"], "d": g["d"], "creator": g["creator"], "genre": g["genre"], "active": g["active"], "visits": g["visits"], "favorites": g["favorites"], "img": g["img"]})
		data.Write(line)
		data.WriteByte('\n')
		facts := strings.TrimSpace(strings.Join([]string{"by " + g["creator"], g["genre"], g["active"] + " playing", g["visits"] + " visits", g["favorites"] + " favorites"}, ", "))
		pages = append(pages, Page{URL: u, Title: clip(g["t"], 150), Description: clip(facts+". "+g["d"], 300), Text: clip(g["t"]+". Roblox game. "+facts+". "+g["d"], 2000), Source: "roblox"})
		select {
		case rbxImgQ <- g["img"]:
		default:
		}
	}
	if data.Len() > 0 {
		f, err := os.OpenFile(rbxGamesFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			http.Error(w, `{"error":"could not save"}`, 500)
			return
		}
		_, err = f.Write(data.Bytes())
		f.Close()
		if err != nil {
			http.Error(w, `{"error":"could not save"}`, 500)
			return
		}
	}
	if len(pages) > 0 {
		select {
		case forumPageQ <- pages:
		default:
		}
	}
	json.NewEncoder(w).Encode(map[string]int{"added": len(pages), "waiting": len(forumPageQ)})
}
