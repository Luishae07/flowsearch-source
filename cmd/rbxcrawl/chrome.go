package main

// -chrome: the browser is a real Chrome started on this machine (a GitHub Actions runner has Google Chrome): headless, with the DevTools on a local port.
// The page reading is the same as with a Browser Use browser (cdp.go); only the start and the stop of the browser are here.

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

var chromeMode bool

func chromeBin() string {
	for _, n := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// chromeStart starts a headless Chrome and returns how to stop it and the address of its DevTools (http://127.0.0.1:port).
func chromeStart() (func(), string, error) {
	bin := chromeBin()
	if bin == "" {
		return nil, "", fmt.Errorf("no Chrome found on this machine")
	}
	port := 9302
	dir, err := os.MkdirTemp("", "chrome-")
	if err != nil {
		return nil, "", err
	}
	var errb bytes.Buffer
	cmd := exec.Command(bin, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--no-first-run", "--no-default-browser-check",
		"--disable-extensions", "--remote-debugging-address=127.0.0.1", fmt.Sprintf("--remote-debugging-port=%d", port), "--user-data-dir="+dir,
		"--window-size=1280,720", "--lang=en-US", "about:blank")
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return nil, "", err
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	stop := func() {
		cmd.Process.Kill()
		<-exited
		os.RemoveAll(dir)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	for i := 0; i < 300; i++ { // ask every 0.1 seconds, for 30 seconds at most
		select {
		case <-exited:
			os.RemoveAll(dir)
			return nil, "", fmt.Errorf("Chrome stopped at once: %s", clip(errb.String(), 300))
		default:
		}
		if resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(base + "/json/version"); err == nil {
			resp.Body.Close()
			return stop, base, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	return nil, "", fmt.Errorf("Chrome did not open its DevTools port within 30 seconds")
}
