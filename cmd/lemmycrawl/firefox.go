package main

// -firefox (with -plain): a forum that answers the plain request with 403 is read with a real Firefox instead. Firefox is driven over WebDriver with
// geckodriver (a GitHub Actions runner has both; the folder is in the environment variable GECKOWEBDRIVER). At most 3 Firefox windows are open at once.
// A forum that said 403 once goes to Firefox for all its later requests.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var errBlocked = errors.New("HTTP 403")

var (
	ffMode    bool
	ffBlocked sync.Map // forum host -> true
	ffPool    = make(chan *ffSession, 3)
	ffMade    int32
	ffPort    int32 = 9500
)

type ffSession struct {
	base, id string
	stop     func()
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
	resp, err := (&http.Client{Timeout: 70 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("geckodriver answered %d %s", resp.StatusCode, clip(string(data), 120))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func ffNew() (*ffSession, error) {
	bin := geckoBin()
	if bin == "" {
		return nil, fmt.Errorf("no geckodriver found on this machine")
	}
	port := int(atomic.AddInt32(&ffPort, 1))
	cmd := exec.Command(bin, "--port", fmt.Sprint(port), "--log", "fatal")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	kill := func() { cmd.Process.Kill(); cmd.Wait() }
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	ready := false
	for i := 0; i < 100 && !ready; i++ { // geckodriver answers within a second or two
		if wd("GET", base+"/status", nil, nil) == nil {
			ready = true
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !ready {
		kill()
		return nil, fmt.Errorf("geckodriver did not start")
	}
	caps := map[string]any{"capabilities": map[string]any{"alwaysMatch": map[string]any{
		"browserName": "firefox",
		"moz:firefoxOptions": map[string]any{
			"args":  []string{"-headless"},
			"prefs": map[string]any{"devtools.jsonview.enabled": false}, // a JSON address shows as plain text, not in the JSON viewer
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
	return &ffSession{base: base, id: id, stop: func() { wd("DELETE", base+"/session/"+id, nil, nil); kill() }}, nil
}

// get opens a JSON address in Firefox and decodes the text of the page; a challenge page is waited out, 3 seconds at a time.
func (s *ffSession) get(u string, v any) error {
	if err := wd("POST", s.base+"/session/"+s.id+"/url", map[string]string{"url": u}, nil); err != nil {
		return err
	}
	text := ""
	for i := 0; i < 40; i++ {
		time.Sleep(700 * time.Millisecond)
		var r struct {
			Value string `json:"value"`
		}
		if err := wd("POST", s.base+"/session/"+s.id+"/execute/sync",
			map[string]any{"script": "return document.body ? document.body.innerText.slice(0, 6000000) : ''", "args": []any{}}, &r); err != nil {
			continue
		}
		t := strings.TrimSpace(r.Value)
		if (strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}")) || (strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]")) {
			return json.Unmarshal([]byte(t), v)
		}
		if captchaRe.MatchString(t[:min(len(t), 400)]) {
			time.Sleep(3 * time.Second)
		}
		text = t
	}
	return fmt.Errorf("no JSON on the page (%q)", clip(text, 60))
}

// ffFetch reads one address with Firefox (a window from the pool, or a new one while fewer than 3 are open).
func ffFetch(u string, v any) error {
	var s *ffSession
	select {
	case s = <-ffPool:
	default:
		if atomic.AddInt32(&ffMade, 1) <= 3 {
			ns, err := ffNew()
			if err != nil {
				atomic.AddInt32(&ffMade, -1)
				return err
			}
			s = ns
		} else {
			atomic.AddInt32(&ffMade, -1)
			s = <-ffPool
		}
	}
	err := s.get(u, v)
	if err != nil && (strings.Contains(err.Error(), "geckodriver") || strings.Contains(err.Error(), "connection")) {
		s.stop() // the window is gone: it is not put back
		atomic.AddInt32(&ffMade, -1)
		return err
	}
	ffPool <- s
	return err
}

// plainOrFirefox: a plain request; if the forum says 403, Firefox is used for it from then on.
func plainOrFirefox(u string, v any) error {
	if !ffMode {
		return plainGet(u, v)
	}
	host := ""
	if p, err := url.Parse(u); err == nil {
		host = p.Host
	}
	if _, blocked := ffBlocked.Load(host); !blocked {
		err := plainGet(u, v)
		if !errors.Is(err, errBlocked) {
			return err
		}
		ffBlocked.Store(host, true)
		log.Printf("%s answered the plain request with 403: Firefox is used for it from now on", host)
	}
	return ffFetch(u, v)
}
