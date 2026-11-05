package main

// -firefox: the browser is a Firefox started on this machine (a GitHub Actions runner has Firefox and geckodriver; the folder is in GECKOWEBDRIVER).
// Firefox does not speak the Chrome DevTools protocol any more, so it is driven over WebDriver. The crawler only needs five DevTools commands, and
// ffConn answers them with their WebDriver counterparts:
//   Page.navigate        open the address (WebDriver waits until the page has loaded; then the "page loaded" event is raised)
//   Runtime.evaluate     run a piece of JavaScript in the page and give back its value
//   Network.setCookie    remembered, and set as soon as a page of that site is open (WebDriver can only set a cookie for the open site)
//   the rest             (blocking pictures, headers, locale) is done by Firefox settings when it starts, or ignored

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

var firefoxMode bool

// cdpCaller is what the crawler needs from a browser: the Chrome DevTools connection (cdpConn) or Firefox (ffConn).
type cdpCaller interface {
	call(method string, params any) (json.RawMessage, error)
	close()
}

type ffConn struct {
	base, id string
	stop     func()
	onEvent  func(string, json.RawMessage)
	cookies  []map[string]any
}

func geckoBin() string {
	var c []string
	if d := os.Getenv("GECKOWEBDRIVER"); d != "" {
		c = append(c, strings.TrimRight(d, "/")+"/geckodriver")
	}
	c = append(c, "/usr/local/share/gecko_driver/geckodriver")
	for _, p := range c {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("geckodriver"); err == nil {
		return p
	}
	return ""
}

func wd(method, u string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, u, rd)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("geckodriver answered %d %s", resp.StatusCode, clip(string(data), 160))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func newFFConn(lang string, onEvent func(string, json.RawMessage)) (*ffConn, error) {
	bin := geckoBin()
	if bin == "" {
		return nil, fmt.Errorf("no geckodriver found on this machine")
	}
	port := 9500
	cmd := exec.Command(bin, "--port", fmt.Sprint(port), "--log", "fatal")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	kill := func() { cmd.Process.Kill(); cmd.Wait() }
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	ready := false
	for i := 0; i < 150 && !ready; i++ { // geckodriver answers within a second or two
		if wd("GET", base+"/status", nil, nil) == nil {
			ready = true
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !ready {
		kill()
		return nil, fmt.Errorf("geckodriver did not start within 15 seconds")
	}
	caps := map[string]any{"capabilities": map[string]any{"alwaysMatch": map[string]any{
		"browserName": "firefox",
		"moz:firefoxOptions": map[string]any{
			"args": []string{"-headless", "-width", "1280", "-height", "800"},
			"prefs": map[string]any{
				"intl.accept_languages":      lang,
				"permissions.default.image":  2, // the price is in the page's HTML: no pictures, so a page is ready much sooner
				"media.autoplay.default":     5,
				"browser.tabs.warnOnClose":   false,
				"datareporting.policy.dataSubmissionEnabled": false,
			},
		}}}}
	var r struct {
		Value struct {
			SessionID string `json:"sessionId"`
		} `json:"value"`
	}
	if err := wd("POST", base+"/session", caps, &r); err != nil || r.Value.SessionID == "" {
		kill()
		return nil, fmt.Errorf("Firefox did not start: %v", err)
	}
	id := r.Value.SessionID
	wd("POST", base+"/session/"+id+"/timeouts", map[string]int{"pageLoad": 30000, "script": 30000}, nil)
	return &ffConn{base: base, id: id, onEvent: onEvent, stop: func() { wd("DELETE", base+"/session/"+id, nil, nil); kill() }}, nil
}

func (f *ffConn) close() {} // the Firefox is stopped by stopBrowser

// applyCookies sets the remembered cookies that belong to the site of the page that is open now.
func (f *ffConn) applyCookies(pageURL string) {
	u, err := url.Parse(pageURL)
	if err != nil {
		return
	}
	rest := f.cookies[:0]
	for _, ck := range f.cookies {
		dom, _ := ck["domain"].(string)
		if strings.HasSuffix(u.Hostname(), strings.TrimPrefix(dom, ".")) {
			wd("POST", f.base+"/session/"+f.id+"/cookie", map[string]any{"cookie": map[string]any{
				"name": ck["name"], "value": ck["value"], "path": "/", "secure": true}}, nil)
		} else {
			rest = append(rest, ck)
		}
	}
	f.cookies = rest
}

func (f *ffConn) call(method string, params any) (json.RawMessage, error) {
	p, _ := params.(map[string]any)
	switch method {
	case "Page.navigate":
		u, _ := p["url"].(string)
		if err := wd("POST", f.base+"/session/"+f.id+"/url", map[string]string{"url": u}, nil); err != nil {
			return nil, err
		}
		f.applyCookies(u)
		f.onEvent("Page.loadEventFired", nil)
		return json.RawMessage(`{}`), nil
	case "Runtime.evaluate":
		expr, _ := p["expression"].(string)
		var r struct {
			Value json.RawMessage `json:"value"`
		}
		if err := wd("POST", f.base+"/session/"+f.id+"/execute/sync", map[string]any{"script": "return eval(arguments[0]);", "args": []any{expr}}, &r); err != nil {
			return nil, err
		}
		out, _ := json.Marshal(map[string]any{"result": map[string]any{"value": r.Value}})
		return out, nil
	case "Network.setCookie":
		f.cookies = append(f.cookies, p)
		return json.RawMessage(`{}`), nil
	}
	return json.RawMessage(`{}`), nil // Page.enable, Network.enable, headers, blocked addresses, locale: not needed here
}
