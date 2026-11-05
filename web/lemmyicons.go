package main

// The icons of the Forums tab grow with the searches. When a search shows a post, the pictures that belong to it are fetched once,
// slowly, in the background: the logo of its instance, the icon of its community and the profile pictures of the commenters that
// are shown. Each one is read from the instance's own API (robots.txt first), made into a small JPEG XL file on our flowcdn and
// remembered in lemmy-icons.json. The next search shows it; what is not known yet shows the plain person icon / site icon.

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	lemmyIconsFile = fsData + "/lemmy-icons.json"
	lemmyIconsDir  = fsCDN + "/lemmy"
)

// icFetch: true = a search that shows a post asks that instance, once and slowly, for its pictures (the browser never does: it only loads our flowcdn copy).
const icFetch = true

type iconWant struct{ kind, inst, name, postID string } // kind: "i" instance, "c" community, "u" user

type iconEntry struct {
	File string `json:"f"` // "" when the instance has no picture for it
	At   int64  `json:"at"`
}

var (
	icMu     sync.Mutex
	icData   map[string]iconEntry
	icQueued = map[string]bool{}
	icQueue  = make(chan iconWant, 500)
	icOnce   sync.Once
	icRobots = map[string]*rbFile{}
	icClient = &http.Client{Timeout: 15 * time.Second}
	postID   = regexp.MustCompile(`/post/(\d+)`)
)

func icKey(w iconWant) string { return w.kind + "|" + w.inst + "|" + strings.ToLower(w.name) }

func icLoad() {
	if icData != nil {
		return
	}
	icData = map[string]iconEntry{}
	if b, err := os.ReadFile(lemmyIconsFile); err == nil {
		json.Unmarshal(b, &icData)
	}
}

// lemmyIcon returns the address of the picture, or "" while it is not known (it is asked for in the background).
func lemmyIcon(kind, inst, name, postURL string) string {
	w := iconWant{kind, strings.ToLower(inst), name, ""}
	if m := postID.FindStringSubmatch(postURL); m != nil {
		w.postID = m[1]
	}
	k := icKey(w)
	icMu.Lock()
	icLoad()
	e, ok := icData[k]
	if icFetch && !ok && !icQueued[k] && len(icQueue) < cap(icQueue) {
		icQueued[k] = true
		icOnce.Do(func() { go icWorker() })
		icQueue <- w
	}
	icMu.Unlock()
	if ok && e.File != "" {
		return "/flowcdn/lemmy/" + e.File
	}
	return ""
}

func icSave(k, file string) {
	icMu.Lock()
	defer icMu.Unlock()
	icLoad()
	icData[k] = iconEntry{file, time.Now().Unix()}
	delete(icQueued, k)
	b, _ := json.Marshal(icData)
	os.WriteFile(lemmyIconsFile+".tmp", b, 0644)
	os.Rename(lemmyIconsFile+".tmp", lemmyIconsFile)
}

func icWorker() {
	for w := range icQueue {
		file := ""
		if pic := icFind(w); pic != "" {
			file = icStore(pic)
		}
		icSave(icKey(w), file)
		time.Sleep(1200 * time.Millisecond) // slow: one request about every second, for all instances together
	}
}

func icAllowed(inst, path string) bool {
	icMu.Lock()
	f, ok := icRobots[inst]
	icMu.Unlock()
	if !ok {
		var body string
		if resp, err := icClient.Get("https://" + inst + "/robots.txt"); err == nil {
			if resp.StatusCode == 200 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<18))
				body = string(b)
			}
			resp.Body.Close()
		}
		f = parseRB(body)
		icMu.Lock()
		icRobots[inst] = f
		icMu.Unlock()
	}
	ok2, _, _ := f.check(path)
	return ok2
}

func icGet(inst, path string, v any) bool {
	if !icAllowed(inst, path) {
		return false
	}
	req, _ := http.NewRequest("GET", "https://"+inst+path, nil)
	req.Header.Set("User-Agent", "MySearchBot")
	resp, err := icClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v) == nil
}

// icFind asks the instance's API for the address of the picture.
func icFind(w iconWant) string {
	switch w.kind {
	case "i":
		var o struct {
			SiteView struct{ Site struct{ Icon string } `json:"site"` } `json:"site_view"`
		}
		if icGet(w.inst, "/api/v3/site", &o) {
			return o.SiteView.Site.Icon
		}
	case "c":
		if w.postID == "" {
			return ""
		}
		var o struct {
			PostView struct{ Community struct{ Icon string } `json:"community"` } `json:"post_view"`
		}
		if icGet(w.inst, "/api/v3/post?id="+w.postID, &o) {
			return o.PostView.Community.Icon
		}
	case "u":
		var o struct {
			PersonView struct{ Person struct{ Avatar string } `json:"person"` } `json:"person_view"`
		}
		if icGet(w.inst, "/api/v3/user?username="+url.QueryEscape(w.name), &o) {
			return o.PersonView.Person.Avatar
		}
	}
	return ""
}

// icStore downloads a picture and keeps it as a small JPEG XL file on flowcdn; returns the file name.
func icStore(pic string) string {
	if !strings.HasPrefix(pic, "https://") {
		return ""
	}
	h := sha1.Sum([]byte(pic))
	name := hex.EncodeToString(h[:8]) + ".jxl"
	dst := lemmyIconsDir + "/" + name
	if _, err := os.Stat(dst); err == nil {
		return name
	}
	req, _ := http.NewRequest("GET", pic, nil)
	req.Header.Set("User-Agent", "MySearchBot")
	resp, err := icClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 3<<20))
	if resp.StatusCode != 200 || len(raw) == 0 || !strings.HasPrefix(http.DetectContentType(raw), "image/") {
		return ""
	}
	os.MkdirAll(lemmyIconsDir, 0755)
	conv := exec.Command("convert", "-[0]", "-resize", "64x64^", "-gravity", "center", "-extent", "64x64", "png:-")
	conv.Stdin = strings.NewReader(string(raw))
	png, err := conv.Output()
	if err != nil || len(png) == 0 {
		return ""
	}
	enc := exec.Command("cjxl", "-", dst, "-d", "0", "-e", "7")
	enc.Stdin = strings.NewReader(string(png))
	if out, err := enc.CombinedOutput(); err != nil {
		log.Printf("lemmy icon: cjxl failed: %v %s", err, strings.TrimSpace(string(out)))
		os.Remove(dst)
		return ""
	}
	return name
}

var lemmyIconsFS = http.StripPrefix("/flowcdn/lemmy/", http.FileServer(http.Dir(lemmyIconsDir)))

func flowcdnLemmy(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, ".jxl") || strings.Contains(r.URL.Path[len("/flowcdn/lemmy/"):], "/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jxl")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	lemmyIconsFS.ServeHTTP(w, r)
}

func iconImg(src string, size int, cls string) string {
	return fmt.Sprintf(`<img class="%s" src="%s" width=%d height=%d alt="" loading=lazy>`, cls, src, size, size)
}
