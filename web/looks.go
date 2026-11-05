package main

// /looks: search the screenshots by how a page looks. embed_shots.py turns every screenshot into 1024 numbers with
// voyage-multimodal-3.5 (through OpenRouter); a typed description or a dropped picture is turned into numbers the same way,
// and the screenshots whose numbers are closest come first.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

var shotVecFile = fsData + "/shot-vectors.jsonl"

type shotVec struct {
	ID, U, Shot, Cap string
	V                []float32
}

var (
	svMu    sync.Mutex
	svMtime time.Time
	svSize  int64
	svAll   []shotVec
)

func shotVectors() []shotVec {
	svMu.Lock()
	defer svMu.Unlock()
	st, err := os.Stat(shotVecFile)
	if err != nil {
		return svAll
	}
	if st.ModTime().Equal(svMtime) && st.Size() == svSize {
		return svAll
	}
	f, err := os.Open(shotVecFile)
	if err != nil {
		return svAll
	}
	defer f.Close()
	var all []shotVec
	dec := json.NewDecoder(f)
	for dec.More() {
		var j struct {
			ID, U, Shot, Cap string
			V                []float32
		}
		if dec.Decode(&j) != nil {
			break
		}
		var n float64
		for _, x := range j.V {
			n += float64(x) * float64(x)
		}
		if n == 0 {
			continue
		}
		n = math.Sqrt(n)
		for i := range j.V {
			j.V[i] /= float32(n)
		}
		all = append(all, shotVec{j.ID, j.U, j.Shot, j.Cap, j.V})
	}
	svAll, svMtime, svSize = all, st.ModTime(), st.Size()
	return svAll
}

var (
	cfOnce sync.Once
	cfTok  string
	cfAcct string
)

// cfRun calls a Workers AI model and returns its "result".
func cfRun(model string, body any) (json.RawMessage, error) { return cfRunCtx(nil, model, body) }

func cfRunCtx(parent *http.Request, model string, body any) (json.RawMessage, error) {
	cfOnce.Do(func() {
		b, _ := os.ReadFile(fsSecrets + "/cloudflare_api_token")
		cfTok = strings.TrimSpace(string(b))
		req, _ := http.NewRequest("GET", "https://api.cloudflare.com/client/v4/accounts", nil)
		req.Header.Set("Authorization", "Bearer "+cfTok)
		if resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req); err == nil {
			defer resp.Body.Close()
			var o struct {
				Result []struct{ ID string }
			}
			if json.NewDecoder(resp.Body).Decode(&o) == nil && len(o.Result) > 0 {
				cfAcct = o.Result[0].ID
			}
		}
	})
	if cfAcct == "" {
		return nil, fmt.Errorf("no account")
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", "https://api.cloudflare.com/client/v4/accounts/"+cfAcct+"/ai/run/"+model, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+cfTok)
	req.Header.Set("Content-Type", "application/json")
	if parent != nil {
		req = req.WithContext(parent.Context())
	}
	resp, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var o struct {
		Result  json.RawMessage
		Success bool
	}
	if resp.StatusCode != 200 || json.Unmarshal(raw, &o) != nil || !o.Success {
		log.Printf("looks: workers ai %s HTTP %d: %s", model, resp.StatusCode, clip(string(raw), 200))
		return nil, fmt.Errorf("workers ai: HTTP %d", resp.StatusCode)
	}
	return o.Result, nil
}

// embedText: the numbers of a sentence (bge-m3).
func embedText(text string) ([]float32, error) {
	raw, err := cfRun("@cf/baai/bge-m3", map[string]any{"text": []string{text}})
	if err != nil {
		return nil, err
	}
	var o struct{ Data [][]float32 }
	if json.Unmarshal(raw, &o) != nil || len(o.Data) == 0 {
		return nil, fmt.Errorf("bge-m3: no numbers")
	}
	return o.Data[0], nil
}

// embedPicture: a vision model says in a sentence what the picture shows, then the sentence is turned into numbers.
func embedPicture(img []byte) (string, []float32, error) {
	px := make([]int, len(img))
	for i, c := range img {
		px[i] = int(c)
	}
	raw, err := cfRun("@cf/llava-hf/llava-1.5-7b-hf", map[string]any{"image": px, "max_tokens": 90,
		"prompt": "Describe this web page screenshot in one sentence: its layout, main colors, and what kind of site it is."})
	if err != nil {
		return "", nil, err
	}
	var o struct{ Description string }
	if json.Unmarshal(raw, &o) != nil || strings.TrimSpace(o.Description) == "" {
		return "", nil, fmt.Errorf("llava: no description")
	}
	v, err := embedText(strings.TrimSpace(o.Description))
	return strings.TrimSpace(o.Description), v, err
}

type lookHit struct {
	U     string  `json:"u"`
	Cap   string  `json:"cap"`
	Shot  string  `json:"shot"`
	Score float64 `json:"score"`
}

func apiLooks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if limited(r) {
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":"too many searches"}`)
		return
	}
	var q []float32
	var err error
	seen := ""
	if r.Method == "POST" { // a picture
		b, _ := io.ReadAll(io.LimitReader(r.Body, 3<<20))
		ct := http.DetectContentType(b)
		if len(b) == 0 || !strings.HasPrefix(ct, "image/") {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"send a picture"}`)
			return
		}
		seen, q, err = embedPicture(b)
	} else {
		text := strings.TrimSpace(r.URL.Query().Get("q"))
		if text == "" || len(text) > 300 {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"type a description"}`)
			return
		}
		q, err = embedText(text)
	}
	if err != nil || len(q) == 0 {
		w.WriteHeader(502)
		fmt.Fprint(w, `{"error":"the embedding service did not answer"}`)
		return
	}
	var n float64
	for _, x := range q {
		n += float64(x) * float64(x)
	}
	n = math.Sqrt(n)
	var hits []lookHit
	for _, s := range shotVectors() {
		if len(s.V) != len(q) {
			continue
		}
		var d float64
		for i, x := range s.V {
			d += float64(x) * float64(q[i])
		}
		if adultHost(hostOf(s.U)) || adultText(s.Cap) {
			continue
		}
		hits = append(hits, lookHit{s.U, s.Cap, s.Shot, d / n})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > 18 {
		hits = hits[:18]
	}
	json.NewEncoder(w).Encode(map[string]any{"hits": hits, "total": len(shotVectors()), "seen": seen})
}

func looksPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body := `<style>
.lk{max-width:980px}
.lk .row{display:flex;gap:10px;flex-wrap:wrap}.lk .row .fld{flex:1;min-width:220px}
.lk .drop{margin-top:14px;border:2px dashed rgba(255,255,255,.28);border-radius:20px;padding:24px;text-align:center;color:var(--dim);cursor:pointer;transition:border-color .15s,background .15s}
.lk .drop:hover{border-color:var(--acc)}
.lk .drop.on{border-color:var(--acc);background:var(--glow);color:var(--ink)}
.lk .g{display:grid;grid-template-columns:repeat(auto-fill,minmax(210px,1fr));gap:14px;margin-top:18px}
.lk .c{display:block;border-radius:20px;overflow:hidden;text-decoration:none;color:var(--ink);border:1px solid rgba(255,255,255,.16);background:var(--card);box-shadow:0 14px 34px -26px rgba(0,0,0,.8);transition:transform .18s,border-color .18s}
.lk .c:hover{transform:translateY(-3px);border-color:var(--acc)}
.lk .c img{width:100%;aspect-ratio:16/10;object-fit:cover;display:block;background:#8883}
.lk .c div{padding:10px 13px;font-size:13.5px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.lk .c small{color:var(--dim)}
.lk #msg{color:var(--dim);margin:16px 2px 0}
</style><main class="wrap lk">` + pageHead("Search by looks", "Describe how a website looks, or drop in a picture. Flowsearch shows the screenshots of sites that look the most like it.") + `
<section class=gc><form id=f class=row><input type=text id=q class=fld placeholder="a dark dashboard with green buttons" autocomplete=off aria-label="Describe a website"><button class=btn type=submit>Search</button></form>
<div class=drop id=drop>Drop a picture here, or click to choose one<input type=file id=file accept="image/*" hidden></div></section>
<p id=msg></p><div class=g id=g></div></main>
<script>(function(){
var g=document.getElementById('g'),msg=document.getElementById('msg');
function show(d){g.innerHTML='';if(d.error){msg.textContent=d.error;return}
 msg.textContent=(d.seen?'Flowsearch sees: '+d.seen+' · ':'')+'Closest of '+d.total+' screenshots';
 d.hits.forEach(function(h){var a=document.createElement('a');a.className='c';a.href=h.u;a.rel='noopener noreferrer';
  var i=document.createElement('img');i.src=h.shot;i.loading='lazy';i.alt='';a.append(i);
  var t=document.createElement('div');a.title=h.cap||'';t.textContent=h.u.replace(/^https?:\/\/(www\.)?/,'');var s=document.createElement('small');s.textContent=' '+Math.round(h.score*100)+'%';t.append(s);a.append(t);g.append(a)})}
function go(p){msg.textContent='Looking…';p.then(function(r){return r.json()}).then(show).catch(function(){msg.textContent='That did not work'})}
document.getElementById('f').addEventListener('submit',function(e){e.preventDefault();var q=document.getElementById('q').value.trim();if(q)go(fetch('/api/looks?q='+encodeURIComponent(q)))});
function pic(file){if(!file||!/^image\//.test(file.type))return;var im=new Image();im.onload=function(){var s=Math.min(1,800/im.width),c=document.createElement('canvas');c.width=im.width*s;c.height=im.height*s;c.getContext('2d').drawImage(im,0,0,c.width,c.height);c.toBlob(function(b){go(fetch('/api/looks',{method:'POST',body:b}))},'image/jpeg',.7)};im.src=URL.createObjectURL(file)}
var d=document.getElementById('drop'),fi=document.getElementById('file');
d.addEventListener('click',function(){fi.click()});fi.addEventListener('change',function(){pic(fi.files[0])});
['dragenter','dragover'].forEach(function(n){d.addEventListener(n,function(e){e.preventDefault();d.classList.add('on')})});
['dragleave','drop'].forEach(function(n){d.addEventListener(n,function(e){e.preventDefault();d.classList.remove('on')})});
d.addEventListener('drop',function(e){pic(e.dataTransfer.files[0])});
})();</script>`
	fmt.Fprint(w, page("Search by looks - Flowsearch", "", html.EscapeString("")+body))
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	return strings.SplitN(u, "/", 2)[0]
}
