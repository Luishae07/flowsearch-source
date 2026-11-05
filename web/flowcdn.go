package main

// flowcdn on the Flowsearch server: the files a results page needs, handed out by us so the browser asks nobody else.
//   /flowcdn/vendor/leaflet/...   Leaflet (script, style, icons) for the map card, kept on this machine
//   /flowcdn/fav/<site>.jxl       a site's favicon as JPEG XL: fetched once from Google's favicon service (by this server, so the
//                                 visitor's browser never talks to Google), converted, kept in /media/data/flowcdn/fav/

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var flowcdnRoot = fsCDN

var (
	favHostRe    = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9-]{2,63}$`)
	favSlots     = make(chan struct{}, 6) // at most 6 favicons are fetched and converted at once
	favMissMu    sync.Mutex
	favMiss      = map[string]time.Time{} // sites that have no favicon: not asked again for a day
	favFetchHTTP = &http.Client{Timeout: 10 * time.Second}
)

func flowcdnFavicon(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/flowcdn/fav/"), ".jxl")
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	if !favHostRe.MatchString(host) || len(host) > 100 {
		http.NotFound(w, r)
		return
	}
	path := flowcdnRoot + "/fav/" + host + ".jxl"
	serve := func() {
		w.Header().Set("Content-Type", "image/jxl")
		w.Header().Set("Cache-Control", "public, max-age=2592000") // a month
		http.ServeFile(w, r, path)
	}
	if _, err := os.Stat(path); err == nil {
		serve()
		return
	}
	favMissMu.Lock()
	if t, ok := favMiss[host]; ok && time.Since(t) < 24*time.Hour {
		favMissMu.Unlock()
		http.NotFound(w, r)
		return
	}
	favMissMu.Unlock()
	miss := func() {
		favMissMu.Lock()
		if len(favMiss) > 50000 {
			favMiss = map[string]time.Time{}
		}
		favMiss[host] = time.Now()
		favMissMu.Unlock()
		go favBrowser(host, path) // Google had none: a real browser looks for the icon
		http.NotFound(w, r)
	}
	select {
	case favSlots <- struct{}{}:
		defer func() { <-favSlots }()
	case <-time.After(5 * time.Second):
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	// the favicon, from Google's favicon service; its "no icon" answer is not a 200
	req, _ := http.NewRequest("GET", "https://www.google.com/s2/favicons?domain="+url.QueryEscape(host)+"&sz=64", nil)
	req.Header.Set("User-Agent", "Flowsearch/1.0")
	resp, err := favFetchHTTP.Do(req)
	if err != nil {
		miss()
		return
	}
	defer resp.Body.Close()
	img, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != 200 || len(img) < 100 {
		miss()
		return
	}
	var out, errb bytes.Buffer
	enc := exec.Command("cjxl", "-", "-", "-d", "0", "-e", "7") // PNG in, lossless JPEG XL out
	enc.Stdin, enc.Stdout, enc.Stderr = bytes.NewReader(img), &out, &errb
	if enc.Run() != nil || out.Len() == 0 {
		miss()
		return
	}
	os.MkdirAll(flowcdnRoot+"/fav", 0o755)
	if os.WriteFile(path+".tmp", out.Bytes(), 0o644) != nil || os.Rename(path+".tmp", path) != nil {
		miss()
		return
	}
	serve()
}

// flowcdnVendor hands out the files under /media/data/flowcdn/vendor/ (Leaflet), cached by browsers for a year.
var flowcdnVendorFS = http.StripPrefix("/flowcdn/vendor/", http.FileServer(http.Dir(flowcdnRoot+"/vendor")))

func flowcdnVendor(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	flowcdnVendorFS.ServeHTTP(w, r)
}

// flowcdnTile hands out the map tiles of the map card from our own maps app (maptiles, port 8820, which keeps a disk cache of the tiles it has
// fetched): /flowcdn/tiles/{z}/{x}/{y}.png is the app's "osm-standard" tile. The dark look is done by CSS in the browser.
var tileHTTP = &http.Client{Timeout: 15 * time.Second}

func flowcdnTile(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/flowcdn/tiles/"), ".png"), "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || len(p) > 8 {
			http.NotFound(w, r)
			return
		}
		n[i] = v
	}
	z := n[0]
	if z > 19 || n[1] >= 1<<z || n[2] >= 1<<z { // a tile that does not exist at that zoom
		http.NotFound(w, r)
		return
	}
	resp, err := tileHTTP.Get("http://127.0.0.1:8820/tile/osm-standard/" + parts[0] + "/" + parts[1] + "/" + parts[2] + ".png")
	if err != nil {
		http.Error(w, "tile server not reachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		http.Error(w, "no tile", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=604800") // a week
	io.Copy(w, io.LimitReader(resp.Body, 2<<20))
}

// flowcdnYouTube hands out YouTube video thumbnails as JPEG XL: /flowcdn/yt/<video id>.jxl. The first request fetches the picture from
// YouTube (this server asks, not the visitor's browser), recompresses the JPEG without loss and keeps it in /media/data/flowcdn/yt/.
var ytIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func flowcdnYouTube(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/flowcdn/yt/"), ".jxl")
	if !ytIDRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	path := flowcdnRoot + "/yt/" + id + ".jxl"
	serve := func() {
		w.Header().Set("Content-Type", "image/jxl")
		w.Header().Set("Cache-Control", "public, max-age=2592000")
		http.ServeFile(w, r, path)
	}
	if _, err := os.Stat(path); err == nil {
		serve()
		return
	}
	favMissMu.Lock()
	if t, ok := favMiss["yt:"+id]; ok && time.Since(t) < 24*time.Hour {
		favMissMu.Unlock()
		http.NotFound(w, r)
		return
	}
	favMissMu.Unlock()
	miss := func() {
		favMissMu.Lock()
		favMiss["yt:"+id] = time.Now()
		favMissMu.Unlock()
		http.NotFound(w, r)
	}
	select {
	case favSlots <- struct{}{}:
		defer func() { <-favSlots }()
	case <-time.After(5 * time.Second):
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	req, _ := http.NewRequest("GET", "https://i.ytimg.com/vi/"+id+"/mqdefault.jpg", nil)
	req.Header.Set("User-Agent", "Flowsearch/1.0")
	resp, err := favFetchHTTP.Do(req)
	if err != nil {
		miss()
		return
	}
	defer resp.Body.Close()
	img, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || resp.StatusCode != 200 || len(img) < 500 {
		miss()
		return
	}
	var out, errb bytes.Buffer
	enc := exec.Command("cjxl", "-", "-", "-e", "7") // a JPEG goes in as a lossless JPEG XL recompression
	enc.Stdin, enc.Stdout, enc.Stderr = bytes.NewReader(img), &out, &errb
	if enc.Run() != nil || out.Len() == 0 {
		miss()
		return
	}
	os.MkdirAll(flowcdnRoot+"/yt", 0o755)
	if os.WriteFile(path+".tmp", out.Bytes(), 0o644) != nil || os.Rename(path+".tmp", path) != nil {
		miss()
		return
	}
	serve()
}

// amazonPicture turns an Amazon product picture address (https://m.media-amazon.com/images/I/<name>) into our own: /flowcdn/amz/<name>.jxl.
// Any other address is returned as it is.
func amazonPicture(src string) string {
	const prefix = "https://m.media-amazon.com/images/I/"
	if name, ok := strings.CutPrefix(src, prefix); ok && amzPicRe.MatchString(name) {
		return "/flowcdn/amz/" + name + ".jxl"
	}
	return src
}

var amzPicRe = regexp.MustCompile(`^[A-Za-z0-9._%+,-]{5,140}$`)

// flowcdnAmazon hands out Amazon product pictures as JPEG XL: /flowcdn/amz/<file name>.jxl. The first request fetches the picture from
// Amazon (this server asks, not the visitor's browser), recompresses it and keeps it in /media/data/flowcdn/amz/.
func flowcdnAmazon(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/flowcdn/amz/"), ".jxl")
	if !amzPicRe.MatchString(name) || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	path := flowcdnRoot + "/amz/" + name + ".jxl"
	serve := func() {
		w.Header().Set("Content-Type", "image/jxl")
		w.Header().Set("Cache-Control", "public, max-age=2592000")
		http.ServeFile(w, r, path)
	}
	if _, err := os.Stat(path); err == nil {
		serve()
		return
	}
	favMissMu.Lock()
	if t, ok := favMiss["amz:"+name]; ok && time.Since(t) < 24*time.Hour {
		favMissMu.Unlock()
		http.NotFound(w, r)
		return
	}
	favMissMu.Unlock()
	miss := func() {
		favMissMu.Lock()
		if len(favMiss) > 50000 {
			favMiss = map[string]time.Time{}
		}
		favMiss["amz:"+name] = time.Now()
		favMissMu.Unlock()
		http.NotFound(w, r)
	}
	select {
	case favSlots <- struct{}{}:
		defer func() { <-favSlots }()
	case <-time.After(5 * time.Second):
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	// Amazon gives WebP for a name with "FMwebp_" in it; without that part the same picture is a JPEG, which JPEG XL takes over without any loss
	var img []byte
	for _, n := range []string{strings.Replace(name, "FMwebp_", "", 1), name} {
		req, _ := http.NewRequest("GET", "https://m.media-amazon.com/images/I/"+n, nil)
		req.Header.Set("User-Agent", "Flowsearch/1.0")
		resp, err := favFetchHTTP.Do(req)
		if err != nil {
			log.Printf("flowcdn amz %s: fetch failed: %v", n, err)
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err == nil && resp.StatusCode == 200 && len(data) >= 500 {
			img = data
			break
		}
		log.Printf("flowcdn amz %s: Amazon answered %d, %d bytes", n, resp.StatusCode, len(data))
	}
	if img == nil {
		log.Printf("flowcdn amz %s: no picture", name)
		miss()
		return
	}
	encArgs := []string{"-", "-", "-e", "7"} // a JPEG is recompressed without loss (no quality setting)
	if len(img) > 12 && string(img[:4]) == "RIFF" && string(img[8:12]) == "WEBP" { // WebP: turn it into PNG first (the converter cannot read WebP)
		var png bytes.Buffer
		dec := exec.Command("convert", "webp:-", "png:-")
		dec.Stdin, dec.Stdout = bytes.NewReader(img), &png
		if dec.Run() != nil || png.Len() == 0 {
			miss()
			return
		}
		img = png.Bytes()
		encArgs = []string{"-", "-", "-q", "92", "-e", "7"} // from a PNG: high quality
	}
	var out, errb bytes.Buffer
	enc := exec.Command("cjxl", encArgs...)
	enc.Stdin, enc.Stdout, enc.Stderr = bytes.NewReader(img), &out, &errb
	if err := enc.Run(); err != nil || out.Len() == 0 {
		log.Printf("flowcdn amz %s: cjxl failed: %v %s", name, err, strings.TrimSpace(errb.String()))
		miss()
		return
	}
	os.MkdirAll(flowcdnRoot+"/amz", 0o755)
	if os.WriteFile(path+".tmp", out.Bytes(), 0o644) != nil || os.Rename(path+".tmp", path) != nil {
		miss()
		return
	}
	serve()
}
