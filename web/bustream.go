package main

// "Try website": the webmaster page opens a WebSocket here; we rent a Browser Use Cloud Chrome, stream the picture
// as the browser draws it, and pass the clicks and keys on to the browser.

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

var buBusy = map[string]bool{}
var buMu sync.Mutex

var keyInfo = map[string][2]any{ // key -> {code, windowsVirtualKeyCode}
	"Enter": {"Enter", 13}, "Backspace": {"Backspace", 8}, "Tab": {"Tab", 9}, "Escape": {"Escape", 27}, "Delete": {"Delete", 46},
	"ArrowLeft": {"ArrowLeft", 37}, "ArrowUp": {"ArrowUp", 38}, "ArrowRight": {"ArrowRight", 39}, "ArrowDown": {"ArrowDown", 40},
	"Home": {"Home", 36}, "End": {"End", 35}, "PageUp": {"PageUp", 33}, "PageDown": {"PageDown", 34},
}

func wmBrowseWS(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	site, ok := normalizeSite(r.URL.Query().Get("site"))
	if user == "" || !ok || !hasSite(user, site) {
		http.Error(w, "forbidden", 403)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || key == "" {
		http.Error(w, "WebSocket only", 400)
		return
	}
	hj, ok2 := w.(http.Hijacker)
	if !ok2 {
		http.Error(w, "no hijack", 500)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Time{})
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	if brw.Flush() != nil {
		return
	}
	ws := &wsConn{c: conn, br: brw.Reader}
	say := func(o map[string]any) {
		b, _ := json.Marshal(o)
		ws.writeFrame(1, b)
	}

	buMu.Lock()
	if buBusy[user] {
		buMu.Unlock()
		say(map[string]any{"t": "status", "m": "You already have a session open - close that tab first."})
		return
	}
	buBusy[user] = true
	buMu.Unlock()
	defer func() { buMu.Lock(); delete(buBusy, user); buMu.Unlock() }()

	say(map[string]any{"t": "status", "m": "Starting a browser..."})
	id, cdpURL, err := buCreate()
	if err != nil {
		say(map[string]any{"t": "status", "m": "Could not start the browser (" + err.Error() + ")."})
		return
	}
	defer buStop(id)
	hard := time.AfterFunc(6*time.Minute, func() { conn.Close() }) // whatever happens, the session is over after 6 minutes
	defer hard.Stop()
	pageWS, err := buPageWS(cdpURL)
	if err != nil {
		say(map[string]any{"t": "status", "m": "Could not reach the browser (" + err.Error() + ")."})
		return
	}

	var c *cdpConn
	var mainFrame string
	var snapMu sync.Mutex
	snapping, again := false, false
	var snap func()
	snap = func() {
		snapMu.Lock()
		if snapping {
			again = true
			snapMu.Unlock()
			return
		}
		snapping = true
		snapMu.Unlock()
		if res, e := c.call("Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 70}); e == nil {
			var d struct {
				Data string `json:"data"`
			}
			json.Unmarshal(res, &d)
			if img, e2 := base64.StdEncoding.DecodeString(d.Data); e2 == nil {
				ws.writeFrame(2, img)
			}
		}
		snapMu.Lock()
		snapping = false
		redo := again
		again = false
		snapMu.Unlock()
		if redo {
			snap()
		}
	}
	c, err = newCDP(pageWS, func(method string, p json.RawMessage) {
		switch method {
		case "Page.loadEventFired":
			time.AfterFunc(400*time.Millisecond, snap)
		case "Page.screencastFrame": // the browser pushes a frame whenever the picture changes
			var d struct {
				Data      string `json:"data"`
				SessionID int    `json:"sessionId"`
			}
			if json.Unmarshal(p, &d) != nil {
				return
			}
			go func() {
				if img, e := base64.StdEncoding.DecodeString(d.Data); e == nil {
					ws.writeFrame(2, img)
				}
				c.call("Page.screencastFrameAck", map[string]any{"sessionId": d.SessionID})
			}()
		case "Page.frameNavigated":
			var d struct {
				Frame struct {
					ParentID string `json:"parentId"`
					URL      string `json:"url"`
				} `json:"frame"`
			}
			if json.Unmarshal(p, &d) == nil && d.Frame.ParentID == "" {
				say(map[string]any{"t": "url", "url": d.Frame.URL})
			}
		case "Fetch.requestPaused": // only pages of the webmaster's own site may be opened
			var d struct {
				RequestID string `json:"requestId"`
				FrameID   string `json:"frameId"`
				Request   struct {
					URL string `json:"url"`
				} `json:"request"`
			}
			if json.Unmarshal(p, &d) != nil {
				return
			}
			go func() {
				if d.FrameID == mainFrame && !hostAllowed(site, d.Request.URL) {
					c.call("Fetch.failRequest", map[string]any{"requestId": d.RequestID, "errorReason": "BlockedByClient"})
				} else {
					c.call("Fetch.continueRequest", map[string]any{"requestId": d.RequestID})
				}
			}()
		}
	})
	if err != nil {
		say(map[string]any{"t": "status", "m": "Could not connect to the browser (" + err.Error() + ")."})
		return
	}
	defer c.close()
	buSetup(c)
	if res, e := c.call("Page.getFrameTree", map[string]any{}); e == nil {
		var d struct {
			FrameTree struct {
				Frame struct {
					ID string `json:"id"`
				} `json:"frame"`
			} `json:"frameTree"`
		}
		json.Unmarshal(res, &d)
		mainFrame = d.FrameTree.Frame.ID
	}
	c.call("Fetch.enable", map[string]any{"patterns": []map[string]string{{"urlPattern": "*", "resourceType": "Document"}}})
	c.call("Page.startScreencast", map[string]any{"format": "jpeg", "quality": 65, "maxWidth": 1280, "maxHeight": 720, "everyNthFrame": 1})
	c.call("Page.navigate", map[string]any{"url": "https://" + site})
	say(map[string]any{"t": "status", "m": ""})

	type msg struct {
		T    string  `json:"t"`
		X    float64 `json:"x"`
		Y    float64 `json:"y"`
		DY   float64 `json:"dy"`
		K    string  `json:"k"`
		URL  string  `json:"url"`
	}
	in := make(chan msg, 64)
	go func() {
		defer close(in)
		for {
			raw, err := ws.readMsg()
			if err != nil {
				return
			}
			var m msg
			if json.Unmarshal(raw, &m) == nil {
				select {
				case in <- m:
				default:
				}
			}
		}
	}()
	mouse := func(typ string, m msg, extra map[string]any) {
		p := map[string]any{"type": typ, "x": m.X * 1280, "y": m.Y * 720, "button": "left", "clickCount": 1}
		for k, v := range extra {
			p[k] = v
		}
		if typ == "mouseMoved" {
			p["button"], p["clickCount"] = "none", 0
		}
		c.call("Input.dispatchMouseEvent", p)
	}
	last := time.Now()
	deadline := time.After(5 * time.Minute)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case m, open := <-in:
			if !open {
				return
			}
			if m.T != "move" {
				last = time.Now()
			}
			switch m.T {
			case "move":
				mouse("mouseMoved", m, nil)
			case "down":
				mouse("mouseMoved", m, nil)
				mouse("mousePressed", m, nil)
			case "up":
				mouse("mouseReleased", m, nil)
			case "wheel":
				mouse("mouseWheel", m, map[string]any{"deltaX": 0, "deltaY": m.DY})
			case "key":
				if info, named := keyInfo[m.K]; named {
					p := map[string]any{"key": m.K, "code": info[0], "windowsVirtualKeyCode": info[1]}
					if m.K == "Enter" {
						p["text"] = "\r"
					}
					p["type"] = "keyDown"
					c.call("Input.dispatchKeyEvent", p)
					p["type"] = "keyUp"
					delete(p, "text")
					c.call("Input.dispatchKeyEvent", p)
				} else if len([]rune(m.K)) == 1 {
					vk := int([]rune(strings.ToUpper(m.K))[0])
					p := map[string]any{"type": "keyDown", "key": m.K, "text": m.K, "unmodifiedText": m.K, "windowsVirtualKeyCode": vk}
					c.call("Input.dispatchKeyEvent", p)
					c.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": m.K, "windowsVirtualKeyCode": vk})
				}
			case "text": // pasted text
				if t := []rune(m.K); len(t) > 0 && len(t) <= 5000 {
					c.call("Input.insertText", map[string]any{"text": m.K})
				}
			case "selectall":
				c.call("Input.dispatchKeyEvent", map[string]any{"type": "rawKeyDown", "key": "a", "code": "KeyA", "windowsVirtualKeyCode": 65, "modifiers": 2, "commands": []string{"selectAll"}})
				c.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "a", "code": "KeyA", "windowsVirtualKeyCode": 65, "modifiers": 2})
			case "nav":
				if hostAllowed(site, m.URL) {
					c.call("Page.navigate", map[string]any{"url": m.URL})
				} else {
					say(map[string]any{"t": "status", "m": "Only " + site + " can be opened here."})
				}
			case "back":
				if res, e := c.call("Page.getNavigationHistory", map[string]any{}); e == nil {
					var h struct {
						CurrentIndex int `json:"currentIndex"`
						Entries      []struct {
							ID int `json:"id"`
						} `json:"entries"`
					}
					json.Unmarshal(res, &h)
					if h.CurrentIndex > 0 {
						c.call("Page.navigateToHistoryEntry", map[string]any{"entryId": h.Entries[h.CurrentIndex-1].ID})
					}
				}
			case "reload":
				c.call("Page.reload", map[string]any{})
			}
		case <-tick.C:
			if time.Since(last) > 90*time.Second {
				say(map[string]any{"t": "end"})
				return
			}
		case <-deadline:
			say(map[string]any{"t": "end"})
			return
		}
	}
}
