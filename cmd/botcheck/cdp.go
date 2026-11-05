package main

// Browser Use Cloud: a real Chrome we rent by the second. buCreate starts one, the page's DevTools (CDP) WebSocket
// is driven by the small own WebSocket + CDP client below, and buStop ends it (and the billing).

import (
	"bufio"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const buAPI = "https://api.browser-use.com/api/v4/browsers"

func buKey() string {
	b, _ := os.ReadFile(fsSecrets + "/browseruse_key")
	return strings.TrimSpace(string(b))
}

func buCall(method, u string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, u, rd)
	req.Header.Set("X-Browser-Use-API-Key", buKey())
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Browser Use answered %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func buCreate() (id, cdpURL string, err error) {
	var r struct {
		ID     string `json:"id"`
		CdpURL string `json:"cdpUrl"`
	}
	if err = buCall("POST", buAPI, map[string]any{"proxyCountryCode": nil}, &r); err != nil {
		return
	}
	buLiveMu.Lock()
	buLive[r.ID] = true
	buLiveMu.Unlock()
	os.MkdirAll(fsRun + "/flowsearch-browsers", 0o755)
	os.WriteFile(fsRun + "/flowsearch-browsers/"+r.ID, nil, 0o644) // the web server's sweeper leaves a browser with a marker alone
	return r.ID, r.CdpURL, nil
}

var buLive = map[string]bool{} // browsers a session of ours is using right now
var buLiveMu sync.Mutex

// buStop ends a browser for good: it asks again until the service answers.
func buStop(id string) {
	for i := 0; i < 5; i++ {
		if buCall("PATCH", buAPI+"/"+id, map[string]string{"action": "stop"}, nil) == nil {
			break
		}
		time.Sleep(time.Duration(i+1) * time.Second)
	}
	buLiveMu.Lock()
	delete(buLive, id)
	buLiveMu.Unlock()
	os.Remove(fsRun + "/flowsearch-browsers/" + id)
}

// buSweep stops every browser of the account that no session of ours is using (left over after a crash or restart).
func buSweep() {
	var r struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if buCall("GET", buAPI+"?pageSize=100", nil, &r) != nil {
		return
	}
	for _, b := range r.Items {
		buLiveMu.Lock()
		mine := buLive[b.ID]
		buLiveMu.Unlock()
		if _, err := os.Stat(fsRun + "/flowsearch-browsers/" + b.ID); err == nil {
			mine = true // a separate program (the Amazon crawl) is using this one and has left its name here
		}
		if b.Status != "stopped" && !mine {
			buStop(b.ID)
		}
	}
}

// buPageWS finds the DevTools WebSocket of the browser's first tab.
func buPageWS(cdpURL string) (string, error) {
	var last error
	for i := 0; i < 40; i++ {
		resp, err := http.Get(strings.TrimRight(cdpURL, "/") + "/json/list")
		if err == nil {
			var tabs []struct {
				Type string `json:"type"`
				WS   string `json:"webSocketDebuggerUrl"`
			}
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if json.Unmarshal(data, &tabs) == nil {
				for _, t := range tabs {
					if t.Type == "page" && t.WS != "" {
						return t.WS, nil
					}
				}
			}
			last = errors.New("the browser has no tab")
		} else {
			last = err
		}
		time.Sleep(150 * time.Millisecond)
	}
	return "", last
}

// ---- WebSocket (client side masks its frames, server side does not) ----

type wsConn struct {
	c      net.Conn
	br     *bufio.Reader
	client bool
	wmu    sync.Mutex
}

func wsDial(raw string) (*wsConn, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":443"
	}
	c, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", host, &tls.Config{ServerName: u.Hostname()})
	if err != nil {
		return nil, err
	}
	kb := make([]byte, 16)
	rand.Read(kb)
	fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		u.RequestURI(), u.Host, base64.StdEncoding.EncodeToString(kb))
	br := bufio.NewReaderSize(c, 1<<20)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		c.Close()
		return nil, errors.New("the browser did not accept the connection")
	}
	return &wsConn{c: c, br: br, client: true}, nil
}

func (w *wsConn) writeFrame(op byte, p []byte) error {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	h := []byte{0x80 | op}
	m := byte(0)
	if w.client {
		m = 0x80
	}
	switch n := len(p); {
	case n < 126:
		h = append(h, m|byte(n))
	case n < 65536:
		h = append(h, m|126, byte(n>>8), byte(n))
	default:
		h = append(h, m|127, 0, 0, 0, 0, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	if w.client {
		mk := make([]byte, 4)
		rand.Read(mk)
		h = append(h, mk...)
		q := make([]byte, len(p))
		for i := range p {
			q[i] = p[i] ^ mk[i%4]
		}
		p = q
	}
	w.c.SetWriteDeadline(time.Now().Add(20 * time.Second))
	_, err := w.c.Write(append(h, p...))
	return err
}

// readMsg returns the next whole text/binary message (answers pings, ends on close).
func (w *wsConn) readMsg() ([]byte, error) {
	var msg []byte
	for {
		var h [2]byte
		if _, err := io.ReadFull(w.br, h[:]); err != nil {
			return nil, err
		}
		fin, op, masked, n := h[0]&0x80 != 0, h[0]&0xf, h[1]&0x80 != 0, uint64(h[1]&0x7f)
		if n == 126 {
			var b [2]byte
			if _, err := io.ReadFull(w.br, b[:]); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		} else if n == 127 {
			var b [8]byte
			if _, err := io.ReadFull(w.br, b[:]); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		if n > 64<<20 {
			return nil, errors.New("frame too big")
		}
		var mk [4]byte
		if masked {
			if _, err := io.ReadFull(w.br, mk[:]); err != nil {
				return nil, err
			}
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(w.br, p); err != nil {
			return nil, err
		}
		if masked {
			for i := range p {
				p[i] ^= mk[i%4]
			}
		}
		switch op {
		case 8:
			return nil, io.EOF
		case 9:
			w.writeFrame(10, p)
			continue
		case 10:
			continue
		}
		msg = append(msg, p...)
		if fin {
			return msg, nil
		}
	}
}

// ---- Chrome DevTools Protocol over that WebSocket ----

type cdpReply struct {
	result json.RawMessage
	err    string
}

type cdpConn struct {
	ws      *wsConn
	mu      sync.Mutex
	next    int
	pend    map[int]chan cdpReply
	onEvent func(method string, params json.RawMessage)
}

func newCDP(wsURL string, onEvent func(string, json.RawMessage)) (*cdpConn, error) {
	ws, err := wsDial(wsURL)
	if err != nil {
		return nil, err
	}
	c := &cdpConn{ws: ws, pend: map[int]chan cdpReply{}, onEvent: onEvent}
	go c.loop()
	return c, nil
}

func (c *cdpConn) loop() {
	for {
		m, err := c.ws.readMsg()
		if err != nil {
			c.mu.Lock()
			for id, ch := range c.pend {
				ch <- cdpReply{err: "the browser connection closed"}
				delete(c.pend, id)
			}
			c.mu.Unlock()
			return
		}
		var x struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(m, &x) != nil {
			continue
		}
		if x.ID != 0 {
			c.mu.Lock()
			ch := c.pend[x.ID]
			delete(c.pend, x.ID)
			c.mu.Unlock()
			if ch != nil {
				r := cdpReply{result: x.Result}
				if x.Error != nil {
					r.err = x.Error.Message
				}
				ch <- r
			}
		} else if c.onEvent != nil {
			c.onEvent(x.Method, x.Params)
		}
	}
}

func (c *cdpConn) call(method string, params any) (json.RawMessage, error) {
	ch := make(chan cdpReply, 1)
	c.mu.Lock()
	c.next++
	id := c.next
	c.pend[id] = ch
	c.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := c.ws.writeFrame(1, b); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.err != "" {
			return nil, errors.New(r.err)
		}
		return r.result, nil
	case <-time.After(30 * time.Second):
		return nil, errors.New("the browser did not answer")
	}
}

func (c *cdpConn) close() { c.ws.c.Close() }

// buSetup puts the tab in a 1280x720 English desktop browser.
func buSetup(c *cdpConn) {
	c.call("Page.enable", map[string]any{})
	c.call("Network.enable", map[string]any{})
	c.call("Network.setExtraHTTPHeaders", map[string]any{"headers": map[string]string{"Accept-Language": "en-US,en;q=0.9"}})
	c.call("Emulation.setLocaleOverride", map[string]any{"locale": "en-US"})
	c.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1280, "height": 720, "deviceScaleFactor": 1, "mobile": false})
}

func hostAllowed(site, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == site || strings.HasSuffix(h, "."+site)
}

// buOnce opens https://site in a fresh browser, runs fn on the loaded page, and stops the browser.
func buOnce(site string, fn func(c *cdpConn) error) error { return buRun(site, false, fn) }

// buRun is buOnce; fast skips the phone-size and language set-up and the wait after the load, for a plain text file.
// The browser is stopped in the background (it asks until the service answers), so the caller does not wait for that.
func buRun(site string, fast bool, fn func(c *cdpConn) error) error {
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
	if fast {
		c.call("Page.enable", map[string]any{})
	} else {
		buSetup(c)
	}
	if _, err := c.call("Page.navigate", map[string]any{"url": "https://" + site}); err != nil {
		return err
	}
	select {
	case <-loaded:
	case <-time.After(22 * time.Second):
	}
	if !fast {
		time.Sleep(1500 * time.Millisecond)
		waitForCaptcha(c) // a challenge page: Browser Use solves it by itself, so wait 3 seconds at a time until the real page is there
	}
	return fn(c)
}

// captchaRe: what a Cloudflare or other challenge / captcha page says in its title or at the top of its text.
var captchaRe = regexp.MustCompile(`(?i)just a moment|attention required|verify you are human|verifying you are human|checking your browser|checking if the site connection is secure|are you a robot|robot check|security check|performing security verification|enable javascript and cookies|ddos protection|captcha`)
var captchaURLRe = regexp.MustCompile(`(?i)/cdn-cgi/|captcha|/checkpoint|challenge`)

// captchaSeen says whether the page that is open is a challenge or captcha page.
func captchaSeen(c *cdpConn) bool {
	r, err := c.call("Runtime.evaluate", map[string]any{"returnByValue": true, "expression": `JSON.stringify({t: document.title, u: location.href, b: ((document.body||{}).innerText||'').slice(0, 400)})`})
	if err != nil {
		return false
	}
	var d struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	json.Unmarshal(r, &d)
	var st struct{ T, U, B string }
	if json.Unmarshal([]byte(d.Result.Value), &st) != nil {
		return false
	}
	return captchaRe.MatchString(st.T+" "+st.B) || captchaURLRe.MatchString(st.U)
}

// waitForCaptcha: if the page is a challenge, wait 3 seconds and look again, up to 10 times (30 seconds), so that the screenshot is
// taken of the real page and not of the captcha. Returns true if a challenge was seen.
func waitForCaptcha(c *cdpConn) bool {
	if !captchaSeen(c) {
		return false
	}
	for i := 0; i < 10; i++ {
		time.Sleep(3 * time.Second)
		if !captchaSeen(c) {
			time.Sleep(1500 * time.Millisecond) // the real page is arriving: let it draw
			return true
		}
	}
	return true
}

func buShot(site string) (img []byte, err error) {
	err = buOnce(site, func(c *cdpConn) error {
		r, e := c.call("Page.captureScreenshot", map[string]any{"format": "png"})
		if e != nil {
			return e
		}
		var d struct {
			Data string `json:"data"`
		}
		json.Unmarshal(r, &d)
		img, e = base64.StdEncoding.DecodeString(d.Data)
		return e
	})
	return
}

func buText(site string) (text string, err error) {
	err = buOnce(site, func(c *cdpConn) error {
		r, e := c.call("Runtime.evaluate", map[string]any{"expression": "document.title + '\\n\\n' + document.body.innerText", "returnByValue": true})
		if e != nil {
			return e
		}
		var d struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		json.Unmarshal(r, &d)
		text = d.Result.Value
		return nil
	})
	return
}
