package main

// jevchat: a chat page on 0.0.0.0:8870 where Jev writes its replies. Jev is a decision model, it cannot write sentences, so for every
// word of its reply it is asked one pick-one question: "which word comes next?" The choices come from a word list built from the forum
// text (/media/data/jevchat/vocab.json, made by cmd/jevchatbuild): the words that most often follow the previous word, the words of the
// conversation, the most common words, the marks . ? ! , and Send. A word that is on none of those lists is reached with "another word":
// then Jev first picks the first two letters, then the word among the commonest ones with those letters (all 150,000 words are reachable).
// Each word is streamed to the page as soon as Jev has chosen it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	jevURL    = "https://api.typesafe.ai/v1/systemone"
	jevModel  = "jev-latest"
	listen    = "0.0.0.0:8870"
	maxTokens = 40 // longest reply, in words and marks
)

var vocabFile = fsMedia + "/jevchat/vocab.json"

var sem = make(chan struct{}, 2) // two replies being written at the same time

func jevKey() string {
	b, _ := os.ReadFile(fsSecrets + "/typesafe_api_key")
	return strings.TrimSpace(string(b))
}

// ---- the vocabulary ----

// the words used when vocab.json does not exist yet
const baseWords = `i you he she it we they me my your his her its our their this that these those there here who what where when why how which
am is are was were be been being have has had do does did can could will would should may might must
say tell ask know think like love want need see look find make take give get go come help try use work play eat drink read write learn live feel mean call talk run walk sleep buy sell show let keep start stop wait remember forget believe understand answer
thing time day night year way world man woman friend people life place home water food money book game music movie city country name question idea problem fact number word computer phone dog cat car house school job family
good bad great nice happy sad big small new old best better hot cold fast slow easy hard right wrong true false real sure maybe very really just also too not no yes please sorry thanks hello hi well now today again always never often sometimes more most less much many some any all every each other another same only still even
a an the and or but if because so then than with without for from to in on at by of about into over after before up down out
one two three four five six seven eight nine ten hundred`

type prefix struct {
	p        string
	examples string
}

var (
	words    []string            // most common first
	follows  map[string][]string // context ("<s>", ",", a word) -> the tokens that most often follow it
	follows2 map[string][]string // two words ("<s> the", "of the") -> the tokens that most often follow them
	byPrefix map[string][]string // first two letters -> the commonest words that start with them
	prefixes []prefix            // the 200 most useful first-two-letters, with examples
	topWords []string            // the commonest words
)

func loadVocab() {
	var v struct {
		Words []string            `json:"words"`
		Next  map[string][]string `json:"next"`
		Next2 map[string][]string `json:"next2"`
	}
	if b, err := os.ReadFile(vocabFile); err == nil {
		json.Unmarshal(b, &v)
	}
	if len(v.Words) == 0 {
		fmt.Println("no", vocabFile, "yet: using the short built-in word list")
		v.Words = strings.Fields(baseWords)
	}
	follows, follows2 = v.Next, v.Next2
	good = map[string]bool{}
	for i, w := range v.Words {
		if realWord(w, i) {
			words = append(words, w)
			good[w] = true
		}
	}
	topWords = words[:min(120, len(words))]
	byPrefix = map[string][]string{}
	mass := map[string]float64{}
	for i, w := range words {
		if len(w) < 2 || w[1] == '\'' {
			continue
		}
		p := w[:2]
		if len(byPrefix[p]) < 150 {
			byPrefix[p] = append(byPrefix[p], w)
		}
		mass[p] += 1 / float64(i+20)
	}
	var ps []string
	for p := range byPrefix {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool { return mass[ps[i]] > mass[ps[j]] })
	if len(ps) > 200 {
		ps = ps[:200]
	}
	sort.Strings(ps)
	for _, p := range ps {
		prefixes = append(prefixes, prefix{p, strings.Join(byPrefix[p][:min(3, len(byPrefix[p]))], ", ")})
	}
	fmt.Println("vocabulary:", len(words), "words,", len(follows), "contexts,", len(follows2), "word pairs,", len(prefixes), "prefixes")
}

var wordRe = regexp.MustCompile(`[a-z]{2,20}`)

type turn struct {
	R string `json:"r"` // "me" or "jev"
	T string `json:"t"`
}

// the keys of the marks and of the two special choices; no word starts with #
var markKey = map[string]string{".": "#.", "?": "#?", "!": "#!", ",": "#,"}

const (
	keySend  = "#send"
	keyOther = "#other"
)

// reply is what has been written so far.
type reply struct {
	text    string
	toks    []string // the words and marks, lowercase
	nWords  int
	others  int  // how many words came through "another word"
	viaOth  bool // the word being added came through it
	prevOth bool // the last word added did
}

// pairContext: the last two tokens of the sentence being written ("<s> the", "of the"), or "" at the start of a sentence.
func (r *reply) pairContext() string {
	n := len(r.toks)
	if n == 0 {
		return ""
	}
	last := r.toks[n-1]
	if _, end := map[string]bool{".": true, "?": true, "!": true}[last]; end {
		return ""
	}
	if n >= 2 {
		if p := r.toks[n-2]; p != "." && p != "?" && p != "!" {
			return p + " " + last
		}
	}
	return "<s> " + last
}

func (r *reply) prev() string {
	if len(r.toks) == 0 {
		return ""
	}
	return r.toks[len(r.toks)-1]
}

// context is what decides which words are likely next: the start of a sentence, a comma, or the last word.
func (r *reply) context() string {
	p := r.prev()
	switch p {
	case "", ".", "?", "!":
		return "<s>"
	}
	return p
}

// recent: a word may not come back within two tokens, and the same pair of words may not be written twice (no "is the is the").
func (r *reply) blocked(w string) bool {
	n := len(r.toks)
	for i := max(0, n-2); i < n; i++ {
		if r.toks[i] == w {
			return true
		}
	}
	for i := 1; i < n; i++ {
		if r.toks[i] == w && r.toks[i-1] == r.prev() {
			return true
		}
	}
	return false
}

// choices builds the options for the next word or mark.
func choices(history []turn, r *reply) map[string]string {
	c := map[string]string{}
	add := func(w string) {
		if m, ok := markKey[w]; ok {
			if r.nWords > 0 && r.prev() != "," && markKey[r.prev()] == "" {
				c[m] = "the mark " + w
			}
			return
		}
		if good[w] && !r.blocked(w) {
			c[w] = w
		}
	}
	if pc := r.pairContext(); pc != "" {
		for _, w := range follows2[pc] {
			add(w)
		}
	}
	for _, w := range follows[r.context()] {
		add(w)
	}
	extra := 0
	for i := len(history) - 1; i >= 0 && extra < 25; i-- {
		for _, w := range wordRe.FindAllString(strings.ToLower(history[i].T), -1) {
			if _, ok := c[w]; !ok && extra < 25 && !r.blocked(w) {
				c[w] = w
				extra++
			}
		}
	}
	for _, w := range topWords {
		add(w)
	}
	for _, m := range []string{".", "?", "!", ","} {
		add(m)
	}
	if r.nWords >= 2 && r.prev() != "," {
		c[keySend] = "Send: the reply is complete"
	}
	if r.others < 2 && !r.prevOth {
		c[keyOther] = "Only if none of the words above fits: another word, not in this list"
	}
	return c
}

const examples = `Examples of how Jev replies (short, friendly, to the point):
User: hi
Jev: Hello! How are you today?
User: what is two plus two
Jev: It is four.
User: thanks
Jev: You are welcome!
`

func chatState(history []turn, text string) string {
	var st strings.Builder
	st.WriteString("A chat between a user and Jev. Jev is friendly, honest and brief: one or two short sentences that answer the user's last message.\n")
	st.WriteString(examples)
	st.WriteString("The real chat:\n")
	for _, t := range history {
		who := "User"
		if t.R == "jev" {
			who = "Jev"
		}
		fmt.Fprintf(&st, "%s: %s\n", who, t.T)
	}
	fmt.Fprintf(&st, "Jev (typing the reply now): %s", text)
	return st.String()
}

// pick asks Jev one pick-one question and returns the key it chose.
func pick(state, instr string, criteria map[string]string) (string, error) {
	body, _ := json.Marshal(map[string]any{"model": jevModel, "state": state, "questions": map[string]any{
		"q1": map[string]any{"type": "choice", "instructions": instr, "criteria": criteria},
	}})
	var last error
	for try := 0; try < 2; try++ {
		req, _ := http.NewRequest("POST", jevURL, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+jevKey())
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			last = err
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		var out struct {
			Answers map[string]struct {
				Choice string `json:"choice"`
			} `json:"answers"`
		}
		if resp.StatusCode != 200 || json.Unmarshal(raw, &out) != nil {
			last = fmt.Errorf("Jev answered %d: %.300s", resp.StatusCode, raw)
			continue
		}
		return out.Answers["q1"].Choice, nil
	}
	return "", last
}

const nextInstr = "Which word or mark comes next in Jev's reply? It must answer the user's last message and read like a sentence; choose Send when the reply is complete."

// nextToken returns the next word or mark of the reply ("" ends it).
func nextToken(history []turn, r *reply) (string, error) {
	state := chatState(history, r.text)
	k, err := pick(state, nextInstr, choices(history, r))
	if err != nil {
		return "", err
	}
	switch k {
	case keySend, "":
		return "", nil
	case keyOther:
		// first the first two letters, then the word
		pc := map[string]string{}
		for _, p := range prefixes {
			pc[p.p] = fmt.Sprintf("%s (%s)", p.p, p.examples)
		}
		p, err := pick(state, "The next word is a less common one. With which two letters does it start?", pc)
		if err != nil {
			return "", err
		}
		wc := map[string]string{}
		for _, w := range byPrefix[p] {
			if len(w) >= 3 && !r.blocked(w) {
				wc[w] = w
			}
		}
		if len(wc) == 0 {
			return "", nil
		}
		w, err := pick(state, "Which word comes next in Jev's reply?", wc)
		if err == nil {
			r.others++
			r.viaOth = true
		}
		return w, err
	}
	for m, mk := range markKey {
		if mk == k {
			return m, nil
		}
	}
	return k, nil
}

// ---- the keyboard mode: Jev types one key at a time ----

const maxKeys = 160

var keyDesc = map[string]string{} // key name -> what Jev is told
var keyChar = map[string]string{} // key name -> the character it types

func init() {
	for c := 'a'; c <= 'z'; c++ {
		keyDesc[string(c)], keyChar[string(c)] = "the letter "+string(c), string(c)
	}
	for c := '0'; c <= '9'; c++ {
		k := "d" + string(c)
		keyDesc[k], keyChar[k] = "the digit "+string(c), string(c)
	}
	sym := map[string]string{"excl": "!", "at": "@", "hash": "#", "dollar": "$", "percent": "%", "caret": "^", "amp": "&", "star": "*",
		"lparen": "(", "rparen": ")", "dot": ".", "dash": "-", "underscore": "_", "slash": "/", "colon": ":", "question": "?", "plus": "+",
		"comma": ",", "apostrophe": "'"}
	for k, c := range sym {
		keyDesc[k], keyChar[k] = "the symbol "+c, c
	}
	keyDesc["space"], keyChar["space"] = "a space", " "
	keyDesc["backspace"] = "Backspace: delete the last typed character"
	keyDesc["send"] = "Send: the reply is complete"
}

// add appends a word or mark: spacing, capital letters after . ? ! and "I".
func (r *reply) add(tok string) string {
	var out string
	r.prevOth, r.viaOth = r.viaOth, false // viaOth was set by nextToken if this word came through "another word"
	if _, ok := markKey[tok]; ok {
		out = tok
	} else {
		w := tok
		if w == "i" || strings.HasPrefix(w, "i'") {
			w = "I" + w[1:]
		}
		if r.text == "" || strings.HasSuffix(r.text, ".") || strings.HasSuffix(r.text, "?") || strings.HasSuffix(r.text, "!") {
			w = strings.ToUpper(w[:1]) + w[1:]
		}
		if r.text != "" {
			w = " " + w
		}
		out = w
		r.nWords++
	}
	r.text += out
	r.toks = append(r.toks, tok)
	return out
}

// ---- web ----

var (
	rateMu sync.Mutex
	rate   = map[string][]time.Time{}
)

func limited(ip string) bool {
	rateMu.Lock()
	defer rateMu.Unlock()
	now, keep := time.Now(), []time.Time{}
	for _, t := range rate[ip] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	keep = append(keep, now)
	rate[ip] = keep
	return len(keep) > 12 // a message is up to 40 requests to Jev
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// apiChat streams the reply: one JSON line per event, {"c":"hello"} text to append, {"done":true} the end.
func apiChat(w http.ResponseWriter, r *http.Request) {
	ip := r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip = r.RemoteAddr
	}
	fail := func(code int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if r.Method != "POST" {
		fail(405, "POST only")
		return
	}
	if limited(ip) {
		fail(429, "too many messages, wait a minute")
		return
	}
	var history []turn
	json.Unmarshal([]byte(r.FormValue("history")), &history)
	if len(history) > 6 {
		history = history[len(history)-6:]
	}
	for i := range history {
		history[i].T = clip(history[i].T, 300)
	}
	msg := strings.TrimSpace(r.FormValue("msg"))
	if msg == "" || len(msg) > 300 {
		fail(400, "write a message of up to 300 characters")
		return
	}
	history = append(history, turn{"me", msg})
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	default:
		fail(429, "Jev is busy writing for two other people, try again in a minute")
		return
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	enc := json.NewEncoder(w)
	emit := func(v any) {
		enc.Encode(v)
		if fl != nil {
			fl.Flush()
		}
	}
	if r.FormValue("mode") != "words" {
		if err := writeKeys(history, func() bool { return r.Context().Err() != nil }, emit); err != nil {
			emit(map[string]any{"error": "Jev did not answer: " + err.Error()})
			return
		}
		emit(map[string]any{"done": true})
		return
	}
	rep := &reply{}
	for i := 0; i < maxTokens; i++ {
		if r.Context().Err() != nil {
			return // the page was closed
		}
		tok, err := nextToken(history, rep)
		if err != nil {
			emit(map[string]any{"error": "Jev did not answer: " + err.Error()})
			return
		}
		if tok == "" {
			break
		}
		emit(map[string]any{"c": rep.add(tok)})
	}
	emit(map[string]any{"done": true})
}

func index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, pageHTML)
}

func main() {
	loadVocab()
	http.HandleFunc("/", index)
	http.HandleFunc("/api/chat", apiChat)
	fmt.Println("jevchat on", listen)
	if err := http.ListenAndServe(listen, nil); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

const pageHTML = `<!doctype html><html lang=en><head><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1"><title>Chat with Jev</title>
<style>
:root{--bg:#fff;--fg:#1b1f24;--dim:#667;--card:#f1f3f5;--line:#d9dce1;--acc:#2f6fed;--acc-ink:#fff}
@media(prefers-color-scheme:dark){:root{--bg:#14161a;--fg:#e8eaed;--dim:#9aa1ab;--card:#1d2026;--line:#2c3038;--acc:#6c9bff;--acc-ink:#0b1020}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,sans-serif;height:100dvh;display:flex;flex-direction:column}
header{padding:14px 16px;border-bottom:1px solid var(--line)}
header h1{margin:0;font-size:20px}header p{margin:2px 0 0;color:var(--dim);font-size:13.5px}
#chat{flex:1;overflow-y:auto;padding:16px;max-width:760px;width:100%;margin:0 auto}
.m{max-width:85%;margin:8px 0;padding:9px 13px;border-radius:16px;white-space:pre-wrap;word-wrap:break-word;min-height:1.5em}
.me{margin-left:auto;background:var(--acc);color:var(--acc-ink);border-bottom-right-radius:4px}
.jev{background:var(--card);border-bottom-left-radius:4px}
.typing::after{content:"";display:inline-block;width:2px;height:1em;background:var(--dim);margin-left:2px;vertical-align:text-bottom;animation:b 1s steps(1) infinite}
@keyframes b{50%{opacity:0}}
.err{color:#e5484d}
form{display:flex;gap:8px;padding:12px 16px;border-top:1px solid var(--line);max-width:760px;width:100%;margin:0 auto}
select{padding:0 10px;border:1px solid var(--line);border-radius:99px;background:var(--card);color:var(--fg);font:inherit}
input{flex:1;padding:11px 14px;border:1px solid var(--line);border-radius:99px;background:var(--card);color:var(--fg);font:inherit}
button{padding:0 20px;border:0;border-radius:99px;background:var(--acc);color:var(--acc-ink);font:inherit;font-weight:600;cursor:pointer}
button:disabled{opacity:.5}
</style></head><body>
<header><h1>Chat with Jev</h1><p>Jev cannot write sentences. It types its reply on a keyboard, one key at a time (or picks words from a list in Words mode).</p></header>
<div id=chat></div>
<form id=f><select id=mode title="How Jev writes"><option value=keys>Keyboard</option><option value=words>Words</option></select><input id=msg maxlength=300 placeholder="Say something to Jev" autocomplete=off autofocus><button id=go>Send</button></form>
<script>
var chat=document.getElementById('chat'),f=document.getElementById('f'),msg=document.getElementById('msg'),go=document.getElementById('go'),hist=[];
function bubble(cls,text){var d=document.createElement('div');d.className='m '+cls;d.textContent=text;chat.appendChild(d);chat.scrollTop=chat.scrollHeight;return d}
bubble('jev','Hi. I am Jev. I pick my words one at a time.');
f.addEventListener('submit',function(ev){
  ev.preventDefault();var t=msg.value.trim();if(!t)return;
  bubble('me',t);msg.value='';go.disabled=true;
  var w=bubble('jev typing',''),text='';
  function end(){w.className=w.className.replace(' typing','');go.disabled=false;msg.focus()}
  fetch('/api/chat',{method:'POST',body:new URLSearchParams({msg:t,history:JSON.stringify(hist),mode:document.getElementById('mode').value})}).then(function(r){
    var ct=r.headers.get('Content-Type')||'';
    if(ct.indexOf('ndjson')<0)return r.json().then(function(d){w.className='m jev err';w.textContent=d.error||'Failed.';go.disabled=false});
    var rd=r.body.getReader(),dec=new TextDecoder(),buf='';
    function line(l){
      if(!l)return;var d=JSON.parse(l);
      if(d.error){w.className='m jev err';w.textContent=d.error;return}
      if(d.c)text+=d.c;else if(d.b)text=text.slice(0,-1);
      w.textContent=text;chat.scrollTop=chat.scrollHeight;
    }
    function pump(){return rd.read().then(function(x){
      if(x.done){buf.split('\n').forEach(line);hist.push({r:'me',t:t},{r:'jev',t:text});hist=hist.slice(-6);end();return}
      buf+=dec.decode(x.value,{stream:true});var p=buf.split('\n');buf=p.pop();p.forEach(line);return pump();
    })}
    return pump();
  }).catch(function(){w.className='m jev err';w.textContent='The request failed.';go.disabled=false});
});
</script></body></html>`
