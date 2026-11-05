package main

// botcheck: a small service on 127.0.0.1:1919 that says whether a screenshot is a bot-check page (Cloudflare's "Just a moment" /
// "Verify you are human") by comparing it with reference pictures of such pages. Each picture is shrunk to 32 x 18 grey pixels and the
// average difference to the nearest reference is the distance; below -threshold means "it is a bot-check page".
//
//	POST /check            body = a screenshot (PNG, JPEG or JPEG XL)  ->  {"bot":true,"distance":0.021,"nearest":"..."}
//	POST /add?name=x       body = a picture of a bot-check page: kept as a reference
//	GET  /collect?site=h   opens the site in a browser (Browser Use), and if it shows a bot-check page keeps a picture of it as a reference
//	GET  /scan             compares every screenshot Flowsearch has stored with the references and lists the closest (nothing is changed)
//	GET  /                 how many references there are
// The references are the PNG files in /media/data/flowsearch-captcha-refs/.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var refDir = fsMedia + "/flowsearch-captcha-refs"

const gw, gh = 32, 18

type sig [gw * gh]float32

var (
	mu        sync.RWMutex
	refs      = map[string]sig{}
	threshold = flag.Float64("threshold", 0.007, "average grey difference (0..1) below which a picture counts as a bot-check page")
	siteRe    = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
)

// signature shrinks a picture to gw x gh grey pixels (the average brightness of each cell).
func signature(img image.Image) sig {
	b := img.Bounds()
	var sum, cnt [gw * gh]float64
	sx, sy := max(1, b.Dx()/192), max(1, b.Dy()/108)
	for y := b.Min.Y; y < b.Max.Y; y += sy {
		for x := b.Min.X; x < b.Max.X; x += sx {
			r, g, bl, _ := img.At(x, y).RGBA()
			i := (y-b.Min.Y)*gh/b.Dy()*gw + (x-b.Min.X)*gw/b.Dx()
			sum[i] += (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 65535
			cnt[i]++
		}
	}
	var s sig
	for i := range s {
		if cnt[i] > 0 {
			s[i] = float32(sum[i] / cnt[i])
		}
	}
	return s
}

func distance(a, b *sig) float64 {
	var d float64
	for i := range a {
		x := float64(a[i] - b[i])
		if x < 0 {
			x = -x
		}
		d += x
	}
	return d / float64(len(a))
}

func nearest(s *sig) (string, float64) {
	mu.RLock()
	defer mu.RUnlock()
	best, bd := "", 1.0
	for n, r := range refs {
		r := r
		if d := distance(s, &r); d < bd {
			best, bd = n, d
		}
	}
	return best, bd
}

// ppm reads the P6 picture djxl writes (no PNG encoding, which is the slow part).
func ppm(b []byte) (image.Image, error) {
	pos := 0
	tok := func() string {
		for pos < len(b) && (b[pos] == ' ' || b[pos] == '\n' || b[pos] == '\r' || b[pos] == '\t') {
			pos++
		}
		st := pos
		for pos < len(b) && b[pos] != ' ' && b[pos] != '\n' && b[pos] != '\r' && b[pos] != '\t' {
			pos++
		}
		return string(b[st:pos])
	}
	if tok() != "P6" {
		return nil, fmt.Errorf("not a PPM")
	}
	w, _ := strconv.Atoi(tok())
	h, _ := strconv.Atoi(tok())
	tok() // maxval
	pos++ // the one whitespace byte before the pixels
	if w <= 0 || h <= 0 || len(b)-pos < w*h*3 {
		return nil, fmt.Errorf("short PPM")
	}
	data := b[pos:]
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for p := 0; p < w*h; p++ {
		img.Pix[p*4], img.Pix[p*4+1], img.Pix[p*4+2], img.Pix[p*4+3] = data[p*3], data[p*3+1], data[p*3+2], 255
	}
	return img, nil
}

func decode(raw []byte) (image.Image, error) {
	if len(raw) > 12 && (bytes.HasPrefix(raw, []byte{0xFF, 0x0A}) || bytes.HasPrefix(raw, []byte("\x00\x00\x00\x0cJXL "))) { // JPEG XL: djxl writes it out as a PPM
		in, err := os.CreateTemp("/dev/shm", "bc*.jxl")
		if err != nil {
			return nil, err
		}
		defer os.Remove(in.Name())
		in.Write(raw)
		in.Close()
		out := in.Name() + ".ppm"
		defer os.Remove(out)
		if msg, err := exec.Command("djxl", in.Name(), out, "--quiet").CombinedOutput(); err != nil {
			return nil, fmt.Errorf("djxl: %v %.100s", err, msg)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			return nil, err
		}
		return ppm(b)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	return img, err
}

func loadRefs() {
	files, _ := filepath.Glob(refDir + "/*.png")
	m := map[string]sig{}
	for _, f := range files {
		if fh, err := os.Open(f); err == nil {
			if img, _, err := image.Decode(fh); err == nil {
				m[strings.TrimSuffix(filepath.Base(f), ".png")] = signature(img)
			}
			fh.Close()
		}
	}
	mu.Lock()
	refs = m
	mu.Unlock()
}

func saveRef(name string, img image.Image) error {
	os.MkdirAll(refDir, 0o755)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	if err := os.WriteFile(refDir+"/"+name+".png", buf.Bytes(), 0o644); err != nil {
		return err
	}
	loadRefs()
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func check(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 30<<20))
	img, err := decode(raw)
	if err != nil {
		http.Error(w, "not a picture: "+err.Error(), 400)
		return
	}
	s := signature(img)
	n, d := nearest(&s)
	writeJSON(w, map[string]any{"bot": n != "" && d < *threshold, "distance": d, "nearest": n})
}

func add(w http.ResponseWriter, r *http.Request) {
	name := regexp.MustCompile(`[^a-z0-9._-]`).ReplaceAllString(strings.ToLower(r.URL.Query().Get("name")), "")
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 30<<20))
	img, err := decode(raw)
	if name == "" || err != nil {
		http.Error(w, "need ?name= and a picture", 400)
		return
	}
	if err := saveRef(name, img); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	fmt.Fprintf(w, "saved; %d references\n", len(refs))
}

// collect opens a site the way Flowsearch does (1280 x 720) but does NOT wait for a bot check to clear: if the page is one, its picture is kept.
func collect(w http.ResponseWriter, r *http.Request) {
	site := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("site"))), "www.")
	if !siteRe.MatchString(site) {
		http.Error(w, "bad site", 400)
		return
	}
	var shot []byte
	var title string
	var isCheck bool
	err := func() error {
		id, cdpURL, err := buCreate()
		if err != nil {
			return err
		}
		defer func() { go buStop(id) }()
		wsURL, err := buPageWS(cdpURL)
		if err != nil {
			return err
		}
		loaded := make(chan struct{}, 1)
		c, err := newCDP(wsURL, func(m string, _ json.RawMessage) {
			if m == "Page.loadEventFired" {
				select {
				case loaded <- struct{}{}:
				default:
				}
			}
		})
		if err != nil {
			return err
		}
		defer c.close()
		buSetup(c)
		if _, err := c.call("Page.navigate", map[string]any{"url": "https://" + site}); err != nil {
			return err
		}
		select {
		case <-loaded:
		case <-time.After(22 * time.Second):
		}
		time.Sleep(2500 * time.Millisecond)
		isCheck = captchaSeen(c)
		if t, err := c.call("Runtime.evaluate", map[string]any{"returnByValue": true, "expression": "document.title"}); err == nil {
			var d struct{ Result struct{ Value string } }
			json.Unmarshal(t, &d)
			title = d.Result.Value
		}
		res, err := c.call("Page.captureScreenshot", map[string]any{"format": "png"})
		if err != nil {
			return err
		}
		var d struct{ Data string }
		json.Unmarshal(res, &d)
		shot, err = base64.StdEncoding.DecodeString(d.Data)
		return err
	}()
	if err != nil {
		fmt.Fprintf(w, "%s: failed: %v\n", site, err)
		return
	}
	if !isCheck {
		fmt.Fprintf(w, "%s: no bot check shown (title %q)\n", site, title)
		return
	}
	img, err := decode(shot)
	if err != nil {
		fmt.Fprintf(w, "%s: bad picture: %v\n", site, err)
		return
	}
	name := fmt.Sprintf("%s-%d", strings.ReplaceAll(site, ".", "_"), time.Now().Unix())
	if err := saveRef(name, img); err != nil {
		fmt.Fprintf(w, "%s: could not save: %v\n", site, err)
		return
	}
	fmt.Fprintf(w, "%s: BOT CHECK shown (title %q): saved as %s; %d references\n", site, title, name, len(refs))
}

// scan compares every stored screenshot with the references. Nothing is changed or deleted.
func scan(w http.ResponseWriter, r *http.Request) {
	type hit struct {
		File string  `json:"file"`
		D    float64 `json:"distance"`
		Ref  string  `json:"nearest"`
	}
	var files []string
	for _, d := range []string{fsShots + "", fsShots + "/pages"} {
		fs, _ := filepath.Glob(d + "/*.jxl")
		files = append(files, fs...)
	}
	var hits []hit
	var hmu sync.Mutex
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				raw, err := os.ReadFile(f)
				if err != nil {
					continue
				}
				img, err := decode(raw)
				if err != nil {
					continue
				}
				s := signature(img)
				n, dist := nearest(&s)
				hmu.Lock()
				hits = append(hits, hit{f, dist, n})
				hmu.Unlock()
			}
		}()
	}
	for _, f := range files {
		jobs <- f
	}
	close(jobs)
	wg.Wait()
	total := len(hits)
	sort.Slice(hits, func(i, j int) bool { return hits[i].D < hits[j].D })
	bands := map[string]int{}
	for _, h := range hits {
		for _, t := range []float64{0.02, 0.04, 0.06, 0.08, 0.10, 0.14} {
			if h.D < t {
				bands[fmt.Sprintf("under %.2f", t)]++
			}
		}
	}
	top := hits
	if len(top) > 60 {
		top = top[:60]
	}
	writeJSON(w, map[string]any{"screenshots": total, "references": len(refs), "threshold": *threshold, "counts": bands, "closest": top})
}

func main() {
	flag.Parse()
	loadRefs()
	mux := http.NewServeMux()
	mux.HandleFunc("/check", check)
	mux.HandleFunc("/add", add)
	mux.HandleFunc("/collect", collect)
	mux.HandleFunc("/scan", scan)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		fmt.Fprintf(w, "botcheck: %d references, threshold %.3f\n", len(refs), *threshold)
		mu.RUnlock()
	})
	srv := &http.Server{Addr: "127.0.0.1:1919", Handler: mux, ReadTimeout: 60 * time.Second, WriteTimeout: 10 * time.Minute}
	go func() {
		sc := make(chan os.Signal, 1)
		signal.Notify(sc, syscall.SIGTERM, syscall.SIGINT)
		<-sc
		buLiveMu.Lock()
		for id := range buLive {
			buStop(id)
		}
		buLiveMu.Unlock()
		os.Exit(0)
	}()
	fmt.Println("botcheck on", srv.Addr, "with", len(refs), "references")
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
