package main

// jevbrowse: a small web page on 0.0.0.0:8860 where you give a site and a task, and Jev browses for you in a Browser Use cloud browser.
// Jev is a decision model: it cannot write text, it picks from options. So every step the program reads the page (text and a numbered
// list of links/buttons), asks Jev "click / go back / done" and "which item", and does that in the browser. The result is the page Jev
// ended on (address, title, text and a screenshot) plus the log of its steps.

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	jevURL   = "https://api.typesafe.ai/v1/systemone"
	jevModel = "jev-latest"
	listen   = "0.0.0.0:8860"
)

var sem = make(chan struct{}, 2) // at most two browsers at the same time

type step struct {
	N      int     `json:"n"`
	URL    string  `json:"url"`
	Action string  `json:"action"`
	Label  string  `json:"label"`
	Conf   float64 `json:"conf"`
}

type job struct {
	mu     sync.Mutex
	Status string `json:"status"` // running | done | limit | error
	Error  string `json:"error,omitempty"`
	Steps  []step `json:"steps"`
	URL    string `json:"url,omitempty"`
	Title  string `json:"title,omitempty"`
	Text   string `json:"text,omitempty"`
	Shot   string `json:"shot,omitempty"`
	Now    string `json:"now,omitempty"` // what the program is doing right now
	live   []byte // latest picture of the browser
	subs   map[chan wsMsg]bool
	made   time.Time
}

var (
	jobs   = map[string]*job{}
	jobsMu sync.Mutex
)

func jevKey() string {
	b, _ := os.ReadFile(fsSecrets + "/typesafe_api_key")
	return strings.TrimSpace(string(b))
}

// ---- the page in the browser ----

type element struct {
	I int    `json:"i"`
	T string `json:"t"`
	H string `json:"h"`
	K string `json:"k"` // "t" = a text field to type into; empty = a link or button to click
	V string `json:"v"` // what a text field already contains
}

type pageInfo struct {
	URL   string    `json:"url"`
	Title string    `json:"title"`
	Text  string    `json:"text"`
	Els   []element `json:"els"`
}

const readJS = `(function(){
  var out=[],n=0;
  var fs=[].slice.call(document.querySelectorAll('input:not([type]),input[type=text],input[type=search],input[type=email],input[type=url],input[type=tel],input[type=number],textarea'));
  for(var k=0;k<fs.length&&n<8;k++){
    var f=fs[k],fr=f.getBoundingClientRect(),fc=getComputedStyle(f);
    if(fr.width<20||fr.height<10||fc.visibility==='hidden'||fc.display==='none'||f.disabled||f.readOnly)continue;
    var lb=f.labels&&f.labels[0]?f.labels[0].innerText:'';
    var ft=(lb||f.getAttribute('aria-label')||f.placeholder||f.name||f.title||f.type||'text').replace(/\s+/g,' ').trim().slice(0,60);
    f.setAttribute('data-jb',n);
    out.push({i:n,t:ft,h:'',k:'t',v:(f.value||'').slice(0,60)});n++;
  }
  var els=[].slice.call(document.querySelectorAll('a[href],button,[role=button],input[type=submit]'));
  for(var i=0;i<els.length&&n<40;i++){
    var e=els[i],r=e.getBoundingClientRect();
    if(r.width<4||r.height<4)continue;
    var cs=getComputedStyle(e);if(cs.visibility==='hidden'||cs.display==='none')continue;
    var t=(e.innerText||e.value||e.getAttribute('aria-label')||e.title||'').replace(/\s+/g,' ').trim().slice(0,70);
    if(!t)continue;
    e.setAttribute('data-jb',n);
    out.push({i:n,t:t,h:e.tagName==='A'?e.href:''});n++;
  }
  return JSON.stringify({url:location.href,title:document.title,text:(document.body?document.body.innerText:'').replace(/\s+/g,' ').slice(0,1000),els:out});
})()`

func eval(c *cdpConn, js string) (string, error) {
	raw, err := c.call("Runtime.evaluate", map[string]any{"expression": js, "returnByValue": true})
	if err != nil {
		return "", err
	}
	var r struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
	}
	json.Unmarshal(raw, &r)
	s, _ := r.Result.Value.(string)
	return s, nil
}

func waitLoad(c *cdpConn) {
	time.Sleep(700 * time.Millisecond)
	for i := 0; i < 40; i++ {
		if s, _ := eval(c, "document.readyState"); s == "complete" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
}

func readPage(c *cdpConn) (pageInfo, error) {
	var p pageInfo
	s, err := eval(c, readJS)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal([]byte(s), &p)
	return p, err
}

// ---- asking Jev ----

func short(h string) string {
	u, err := url.Parse(h)
	if err != nil || u.Host == "" {
		return ""
	}
	p := u.Path
	if len(p) > 40 {
		p = p[:40]
	}
	return u.Host + p
}

// ---- one job ----

// A watcher is one open WebSocket page: text messages carry the job as JSON, binary messages a JPEG frame of the browser.
type wsMsg struct {
	op byte
	p  []byte
}

// changed sends the job's state to everyone watching it.
func (j *job) changed() {
	j.mu.Lock()
	defer j.mu.Unlock()
	b, _ := json.Marshal(j)
	for ch := range j.subs {
		select {
		case ch <- wsMsg{1, b}:
		default:
		}
	}
}

// frame is called for every picture Chrome streams; slow watchers just skip frames.
func (j *job) frame(b []byte) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.live = b
	for ch := range j.subs {
		select {
		case ch <- wsMsg{2, b}:
		default:
		}
	}
}

func (j *job) setNow(s string) {
	j.mu.Lock()
	j.Now = s
	j.mu.Unlock()
	j.changed()
}

func (j *job) fail(msg string) {
	j.mu.Lock()
	j.Status, j.Error = "error", msg
	j.mu.Unlock()
	j.changed()
}

func runJob(j *job, start, task string, maxSteps int, texts []string) {
	defer func() { <-sem }()
	id, cdpURL, err := buCreate()
	if err != nil {
		j.fail("could not start a browser: " + err.Error())
		return
	}
	defer buStop(id)
	wsURL, err := buPageWS(cdpURL)
	if err != nil {
		j.fail("the browser has no tab: " + err.Error())
		return
	}
	var c *cdpConn
	c, err = newCDP(wsURL, func(method string, params json.RawMessage) {
		if method != "Page.screencastFrame" {
			return
		}
		var f struct {
			Data      string `json:"data"`
			SessionID int    `json:"sessionId"`
		}
		if json.Unmarshal(params, &f) != nil {
			return
		}
		go c.call("Page.screencastFrameAck", map[string]any{"sessionId": f.SessionID}) // Chrome sends the next frame after this
		if b, err := base64.StdEncoding.DecodeString(f.Data); err == nil {
			j.frame(b)
		}
	})
	if err != nil {
		j.fail("could not connect to the browser: " + err.Error())
		return
	}
	defer c.close()
	buSetup(c)
	c.call("Page.startScreencast", map[string]any{"format": "jpeg", "quality": 50, "maxWidth": 1280, "maxHeight": 720, "everyNthFrame": 1})
	j.setNow("Opening " + start)
	if _, err := c.call("Page.navigate", map[string]any{"url": start}); err != nil {
		j.fail("could not open the site: " + err.Error())
		return
	}
	waitLoad(c)

	visited := map[string]bool{}
	var hist []string
	status := "limit"
	for n := 1; n <= maxSteps; n++ {
		j.setNow("Reading the page")
		p, err := readPage(c)
		if err != nil {
			j.fail("could not read the page: " + err.Error())
			return
		}
		visited[p.URL] = true
		var els []element
		for _, e := range p.Els {
			if e.H == "" || !visited[e.H] {
				els = append(els, e)
			}
		}
		j.setNow(fmt.Sprintf("Asking Jev (%d items on the page)", len(els)))
		j.setNow("Looking at the page")
		pic := markedShot(c)
		j.setNow(fmt.Sprintf("Asking Jev (%d items on the page, with a picture)", len(els)))
		dec, err := askJev(task, p, els, hist, texts, pic)
		if err != nil {
			j.fail("Jev did not answer: " + err.Error())
			return
		}
		act, idx, conf := dec.Act, dec.Idx, dec.Conf
		label := ""
		if act == "click" || act == "type" || act == "write" {
			for _, e := range els {
				if e.I == idx && (e.K == "t") == (act != "click") {
					label = e.T
				}
			}
			if label == "" || (act == "type" && dec.Text == "") {
				act, label = "done", "" // Jev named an item that is not there
			} else if act == "type" {
				label = fmt.Sprintf("%q into %q", dec.Text, label)
			}
		}
		j.mu.Lock()
		j.Steps = append(j.Steps, step{n, p.URL, act, label, conf})
		j.mu.Unlock()
		j.changed()
		if act == "done" || act == "" {
			status = "done"
			break
		}
		if act == "write" {
			field := label
			j.setNow(fmt.Sprintf("Writing into %q", field))
			typed, entered := writeKeys(j, c, task, idx, field)
			label = fmt.Sprintf("%q into %q", typed, field)
			j.mu.Lock()
			j.Steps[len(j.Steps)-1].Label = label
			j.mu.Unlock()
			j.changed()
			hist = append(hist, "wrote "+label+" on "+p.Title)
			if entered {
				hist[len(hist)-1] += " and pressed Enter"
			}
		} else if act == "type" {
			j.setNow("Typing " + label)
			hist = append(hist, "typed "+label+" on "+p.Title)
			typeInto(c, idx, dec.Text, dec.Enter)
			if dec.Enter {
				hist[len(hist)-1] += " and pressed Enter"
			}
		} else if act == "back" {
			j.setNow("Going back")
			hist = append(hist, "went back from "+p.Title)
			eval(c, "history.back()")
		} else {
			j.setNow("Clicking \"" + label + "\"")
			hist = append(hist, "clicked '"+label+"' on "+p.Title)
			eval(c, fmt.Sprintf(`(function(){var e=document.querySelector('[data-jb="%d"]');if(e){e.target='_self';e.click()}})()`, idx))
		}
		waitLoad(c)
	}
	p, _ := readPage(c)
	shot := ""
	if raw, err := c.call("Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 55}); err == nil {
		var r struct {
			Data string `json:"data"`
		}
		json.Unmarshal(raw, &r)
		shot = "data:image/jpeg;base64," + r.Data
	}
	j.mu.Lock()
	j.Status, j.URL, j.Title, j.Text, j.Shot, j.Now = status, p.URL, p.Title, p.Text, shot, ""
	j.mu.Unlock()
	j.changed()
}

// ---- web ----

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

var schemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)

func apiRun(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	site := strings.TrimSpace(r.FormValue("site"))
	task := strings.TrimSpace(r.FormValue("task"))
	var texts []string
	for _, t := range strings.Split(r.FormValue("texts"), "\n") {
		if t = strings.TrimSpace(t); t != "" && len(texts) < 6 {
			texts = append(texts, clip(t, 200))
		}
	}
	steps, _ := strconv.Atoi(r.FormValue("steps"))
	if steps < 1 || steps > 12 {
		steps = 8
	}
	if site == "" || task == "" || len(site) > 300 || len(task) > 500 {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":"give a site and a task"}`)
		return
	}
	if !schemeRe.MatchString(site) {
		site = "https://" + site
	}
	if u, err := url.Parse(site); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":"the site must be an http or https address"}`)
		return
	}
	select {
	case sem <- struct{}{}:
	default:
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":"two browsers are busy right now, try again in a minute"}`)
		return
	}
	b := make([]byte, 8)
	rand.Read(b)
	id := hex.EncodeToString(b)
	j := &job{Status: "running", made: time.Now(), subs: map[chan wsMsg]bool{}}
	jobsMu.Lock()
	for k, o := range jobs {
		if time.Since(o.made) > time.Hour {
			delete(jobs, k)
		}
	}
	jobs[id] = j
	jobsMu.Unlock()
	go func() {
		done := make(chan struct{})
		go func() { runJob(j, site, task, steps, texts); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Minute):
			j.fail("took longer than 5 minutes")
		}
	}()
	fmt.Fprintf(w, `{"id":%q}`, id)
}

func apiJob(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	jobsMu.Lock()
	j := jobs[r.URL.Query().Get("id")]
	jobsMu.Unlock()
	if j == nil {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":"no such job"}`)
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	json.NewEncoder(w).Encode(j)
}

// wsJob is the WebSocket a results page opens: it gets the job's state as text messages and the browser's picture as binary ones.
func wsJob(w http.ResponseWriter, r *http.Request) {
	jobsMu.Lock()
	j := jobs[r.URL.Query().Get("id")]
	jobsMu.Unlock()
	key := r.Header.Get("Sec-WebSocket-Key")
	hj, ok := w.(http.Hijacker)
	if j == nil || key == "" || !ok || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "not a WebSocket request for a job", 400)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
	ws := &wsConn{c: conn, br: brw.Reader}
	ch := make(chan wsMsg, 8)
	j.mu.Lock()
	j.subs[ch] = true
	first, _ := json.Marshal(j)
	last := j.live
	j.mu.Unlock()
	defer func() {
		j.mu.Lock()
		delete(j.subs, ch)
		j.mu.Unlock()
	}()
	ws.writeFrame(1, first)
	if len(last) > 0 {
		ws.writeFrame(2, last)
	}
	gone := make(chan struct{})
	go func() { // reads pings and the close from the page
		for {
			if _, err := ws.readMsg(); err != nil {
				close(gone)
				return
			}
		}
	}()
	for {
		select {
		case m := <-ch:
			if ws.writeFrame(m.op, m.p) != nil {
				return
			}
		case <-gone:
			return
		}
	}
}

func index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, strings.ReplaceAll(pageHTML, "{{model}}", html.EscapeString(jevModel)))
}

func main() {
	http.HandleFunc("/", index)
	http.HandleFunc("/api/run", apiRun)
	http.HandleFunc("/api/job", apiJob)
	http.HandleFunc("/ws/job", wsJob)
	fmt.Println("jevbrowse on", listen)
	if err := http.ListenAndServe(listen, nil); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

const pageHTML = `<!doctype html><html lang=en><head><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1"><title>Jev browses</title>
<style>
:root{--bg:#fff;--fg:#1b1f24;--dim:#667;--card:#f4f5f7;--line:#d9dce1;--acc:#2f6fed;--acc-ink:#fff}
@media(prefers-color-scheme:dark){:root{--bg:#14161a;--fg:#e8eaed;--dim:#9aa1ab;--card:#1d2026;--line:#2c3038;--acc:#6c9bff;--acc-ink:#0b1020}}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,sans-serif}
main{max-width:760px;margin:0 auto;padding:28px 16px 60px}
h1{margin:0 0 4px;font-size:26px}.sub{color:var(--dim);margin:0 0 20px}
label{display:block;font-weight:600;margin:14px 0 4px}
input,textarea{width:100%;box-sizing:border-box;padding:10px 12px;border:1px solid var(--line);border-radius:10px;background:var(--card);color:var(--fg);font:inherit}
button{margin-top:16px;padding:10px 22px;border:0;border-radius:10px;background:var(--acc);color:var(--acc-ink);font:inherit;font-weight:600;cursor:pointer}
button:disabled{opacity:.5}
#log{margin-top:26px}.st{padding:8px 12px;border:1px solid var(--line);border-radius:10px;background:var(--card);margin:6px 0;font-size:14.5px}
.st small{color:var(--dim);display:block;word-break:break-all}
.res{margin-top:16px;padding:14px;border:1px solid var(--line);border-radius:12px}
.res img{width:100%;border-radius:8px;margin-top:10px;border:1px solid var(--line)}
.err{color:#e5484d}
</style></head><body><main>
<h1>Jev browses</h1><p class=sub>Give a site and a task. Jev ({{model}}) reads each page and decides what to click, in a real cloud browser. It picks from options: it clicks, goes back, or types, either one of the texts you allow or letter by letter on a keyboard it is given (a-z, 0-9, symbols).</p>
<form id=f>
<label for=site>Site</label><input id=site name=site placeholder="example.com" required>
<label for=task>What should Jev do?</label><textarea id=task name=task rows=3 placeholder="Find the pricing page, or: search for cats" required></textarea>
<label for=texts>Texts Jev may type (one per line, optional)</label><textarea id=texts name=texts rows=2 placeholder="cheap flights to Berlin"></textarea>
<p class=sub style="margin:4px 0 0;font-size:13.5px">Jev cannot write text itself. It can only type one of these lines into a text field. They are sent to Jev and typed into the site, so never put a password here.</p>
<label for=steps>Most steps</label><input id=steps name=steps type=number min=1 max=12 value=8>
<button id=go>Start</button>
</form>
<div id=livebox hidden><p class=sub id=now></p><img id=live alt="Live view of the browser" style="width:100%;border-radius:10px;border:1px solid var(--line)"></div>
<div id=log></div>
<script>
var f=document.getElementById('f'),log=document.getElementById('log'),go=document.getElementById('go');
function el(t,c,x){var e=document.createElement(t);if(c)e.className=c;if(x!=null)e.textContent=x;return e}
f.addEventListener('submit',function(ev){
  ev.preventDefault();go.disabled=true;log.textContent='';log.appendChild(el('p','sub','Starting a browser...'));
  fetch('/api/run',{method:'POST',body:new URLSearchParams(new FormData(f))}).then(function(r){return r.json()}).then(function(d){
    if(d.error){log.textContent='';log.appendChild(el('p','err',d.error));go.disabled=false;return}
    watch(d.id);
  }).catch(function(){log.textContent='';log.appendChild(el('p','err','The request failed.'));go.disabled=false});
});
var box=document.getElementById('livebox'),liveImg=document.getElementById('live'),nowEl=document.getElementById('now'),sock=null;
// draws the job; returns true while it is still running
function render(j){
  nowEl.textContent=j.now||'';
  log.textContent='';
  (j.steps||[]).forEach(function(s){
    var d=el('div','st',s.n+'. '+(s.action==='click'?'clicked "'+s.label+'"':s.action==='type'?'typed '+s.label:s.action==='write'?'wrote '+s.label:s.action==='back'?'went back':'decided it is done')+' ('+Math.round(s.conf*100)+'%)');
    d.appendChild(el('small','',s.url));log.appendChild(d);
  });
  if(j.status==='running'){log.appendChild(el('p','sub','Working...'));return true}
  go.disabled=false;box.hidden=true;
  if(j.status==='error'){log.appendChild(el('p','err',j.error));return false}
  var r=el('div','res');
  r.appendChild(el('b','',(j.status==='done'?'Jev says it is done on: ':'Step limit reached on: ')+(j.title||j.url)));
  var a=el('a','',j.url);a.href=j.url;a.target='_blank';a.rel='noopener';a.style.display='block';a.style.wordBreak='break-all';r.appendChild(a);
  r.appendChild(el('p','sub',j.text||''));
  if(j.shot){var im=el('img');im.src=j.shot;im.alt='Screenshot of the last page';r.appendChild(im)}
  log.appendChild(r);
  return false;
}
// the live connection: text messages are the job, binary messages are pictures of the browser
function watch(id){
  box.hidden=false;nowEl.textContent='';liveImg.removeAttribute('src');
  var finished=false;
  sock=new WebSocket((location.protocol==='https:'?'wss://':'ws://')+location.host+'/ws/job?id='+id);
  sock.onmessage=function(ev){
    if(typeof ev.data==='string'){if(!render(JSON.parse(ev.data))){finished=true;sock.close()}return}
    var u=URL.createObjectURL(ev.data);liveImg.onload=function(){URL.revokeObjectURL(u)};liveImg.src=u;
  };
  sock.onclose=function(){if(!finished)poll(id)};
}
// fallback when the WebSocket cannot be used
function poll(id){
  fetch('/api/job?id='+id).then(function(r){return r.json()}).then(function(j){
    if(render(j))setTimeout(function(){poll(id)},1200);
  }).catch(function(){setTimeout(function(){poll(id)},2000)});
}
</script></main></body></html>`
