package main

// Screenshots for search results: when a results page shows a result whose page has no screenshot yet, one of that very page
// (not just the site's front page) is taken in the background (Browser Use), so the next search has its preview. Pages wait in
// a line, two are photographed at a time, at most 1000 a day, and a page that was tried is not tried again for a week.

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const resultShotsPerDay = 1000

// a result's preview is a screenshot of the page the result opens, kept in pageShotDir under a short hash of its address
var pageShotDir = fsShots + "/pages"

type resultShotJob struct{ url, host, title, desc string }

var (
	resultShotMu    sync.Mutex
	resultShotTried = map[string]time.Time{}
	resultShotDay   string
	resultShotCount int
	resultShotLine  = make(chan resultShotJob, 300)
)

// pageShotKey is the file name (without .jxl) of the screenshot of one page: the address without its #fragment, hashed.
func pageShotKey(u string) string {
	if i := strings.Index(u, "#"); i >= 0 {
		u = u[:i]
	}
	h := sha1.Sum([]byte(u))
	return hex.EncodeToString(h[:8])
}

func pageShotFile(key string) string {
	if len(key) != 16 || strings.Trim(key, "0123456789abcdef") != "" {
		return ""
	}
	p := pageShotDir + "/" + key + ".jxl"
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

func init() {
	for i := 0; i < 2; i++ {
		go func() {
			for j := range resultShotLine {
				if !resultShotAllowed() {
					continue // the day's quota is used; the page is asked again after a week
				}
				if !surpriseSafe(j.host, j.title, j.url, j.desc) { // the same adult check as Surprise me: nothing unsuitable is photographed
					continue
				}
				start := time.Now()
				if err := takePageShot(j.url); err != nil {
					log.Printf("result screenshot of %s failed after %s: %v", j.url, time.Since(start).Round(time.Millisecond), err)
					continue
				}
				log.Printf("result screenshot of %s taken in %s", j.url, time.Since(start).Round(time.Millisecond))
			}
		}()
	}
}

// takePageShot opens the exact page in a browser (Browser Use) and keeps a JPEG XL screenshot of it.
func takePageShot(raw string) error {
	target := strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	img, err := buShot(target)
	if err != nil {
		return err
	}
	saved := false
	if bot, d := botCheckShot(img); bot { // a bot-check page is no preview: the Wayback Machine's saved copy of the page is photographed instead
		copyImg, cerr := savedCopyShot(raw)
		if cerr != nil {
			return fmt.Errorf("%w (distance %.3f) %v", errBotCheck, d, cerr)
		}
		img, saved = copyImg, true
	}
	var out, errb bytes.Buffer
	enc := exec.Command("cjxl", "-", "-", "-q", "90", "-e", "7")
	enc.Stdin, enc.Stdout, enc.Stderr = bytes.NewReader(img), &out, &errb
	if err := enc.Run(); err != nil || out.Len() == 0 {
		return fmt.Errorf("JPEG XL encoding failed: %v %s", err, strings.TrimSpace(errb.String()))
	}
	os.MkdirAll(pageShotDir, 0o755)
	key := pageShotKey(raw)
	if err := os.WriteFile(pageShotDir+"/"+key+".jxl", out.Bytes(), 0o644); err != nil {
		return err
	}
	markSavedCopy(pageShotDir+"/"+key+".wb", saved)
	// which address the file belongs to, for the /screenshots page
	if f, err := os.OpenFile(pageShotDir+"/index.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		line, _ := json.Marshal(map[string]any{"k": key, "u": raw, "at": time.Now().Unix()})
		f.Write(append(line, '\n'))
		f.Close()
	}
	return nil
}

// resultShotAllowed counts one screenshot against today's quota.
func resultShotAllowed() bool {
	resultShotMu.Lock()
	defer resultShotMu.Unlock()
	if today := time.Now().UTC().Format("2006-01-02"); today != resultShotDay {
		resultShotDay, resultShotCount = today, 0
	}
	if resultShotCount >= resultShotsPerDay {
		return false
	}
	resultShotCount++
	return true
}

// queueResultShot puts a result's page in line for a screenshot unless it was tried lately.
func queueResultShot(addr, host, title, desc string) {
	if !strings.HasPrefix(addr, "https://") && !strings.HasPrefix(addr, "http://") {
		return
	}
	key := pageShotKey(addr)
	resultShotMu.Lock()
	defer resultShotMu.Unlock()
	if t, ok := resultShotTried[key]; ok && time.Since(t) < 7*24*time.Hour {
		return
	}
	select {
	case resultShotLine <- resultShotJob{addr, host, title, desc}:
		resultShotTried[key] = time.Now()
		if len(resultShotTried) > 100000 {
			resultShotTried = map[string]time.Time{key: time.Now()}
		}
	default: // the line is full; the page comes up again on a later search
	}
}

// shotFile is the screenshot file of a site, from the webmaster/result folder (newer) or the old Action folder; "" if none.
func shotFile(host string) string {
	for _, d := range []string{fsShots + "", shotsDir} {
		p := d + "/" + host + ".jxl"
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
