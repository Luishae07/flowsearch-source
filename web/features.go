package main

// New pages of Flowsearch (not searches):
//   /more      all the pages of Flowsearch in one place
//   /tools     the instant answers as a toolbox: type a calculation, a unit, a hash ... and get the answer
//   /quiz      "Guess the website": a screenshot, four names
//   /wall      a wall of screenshots that moves by itself
//   /compare   two screenshots on top of each other with a slider (or side by side)
//   /site/X    what Flowsearch knows about one website: its screenshot, how many pages, the pages, the links

import (
	"encoding/base64"
	"fmt"
	"html"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const featCSS = `<style>
.ft{max-width:1100px;margin:0 auto}
.ft h1{margin:0 0 6px;font-size:clamp(30px,4vw,44px);letter-spacing:-.03em}
.ft .lead{color:var(--dim);font-size:17px;margin:0 0 24px;max-width:680px}
.ft-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(230px,1fr));gap:14px}
.ft-card{display:block;border:1px solid var(--line);background:var(--card);border-radius:22px;padding:20px 22px;color:var(--ink);text-decoration:none;transition:transform .2s,border-color .2s,box-shadow .2s}
.ft-card:hover{transform:translateY(-3px);border-color:var(--acc);box-shadow:0 18px 40px -26px rgba(0,0,0,.8)}
.ft-card .ic{font-size:30px;display:block;margin:0 0 8px}.ft-card b{display:block;font-size:18px;margin:0 0 4px}.ft-card span{color:var(--dim);font-size:14.5px}
.ft-box{border:1px solid var(--line);background:var(--card);border-radius:22px;padding:20px 22px;margin:0 0 18px}
.ft form{display:flex;gap:10px;flex-wrap:wrap;margin:0 0 16px}
.ft input.fld{flex:1;min-width:220px;border-radius:14px;padding:13px 16px;font-size:17px}
.ft .chips{display:flex;gap:8px;flex-wrap:wrap}
.ft .chips a{border:1px solid var(--line);background:var(--card);border-radius:99px;padding:6px 14px;color:var(--dim);text-decoration:none;font-size:14px}.ft .chips a:hover{border-color:var(--acc);color:var(--ink)}
.ft h2{font-size:15px;letter-spacing:.08em;text-transform:uppercase;color:var(--dim);margin:26px 0 10px;font-weight:600}
.qz-img{width:100%;aspect-ratio:16/10;object-fit:cover;object-position:top;border-radius:18px;border:1px solid var(--line);background:#8883;display:block}
.qz-opts{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:10px;margin:16px 0 8px}
.qz-opts button{border:1px solid var(--line);background:var(--card);color:var(--ink);font:inherit;font-size:17px;padding:14px 16px;border-radius:14px;cursor:pointer;transition:all .15s}
.qz-opts button:hover:not(:disabled){border-color:var(--acc)}
.qz-opts button.ok{background:#1d8a52;border-color:#1d8a52;color:#fff}.qz-opts button.no{background:#b3322f;border-color:#b3322f;color:#fff}
.qz-bar{display:flex;gap:18px;flex-wrap:wrap;align-items:center;color:var(--dim);margin:0 0 14px}.qz-bar b{color:var(--ink);font-size:20px}
.wall{position:fixed;inset:0;display:grid;grid-template-columns:repeat(var(--cols,5),1fr);gap:12px;padding:0 12px;overflow:hidden;background:#05070b}
.wall .col{display:flex;flex-direction:column;gap:12px;animation:wallup var(--t,80s) linear infinite}
.wall .col:nth-child(even){animation-direction:reverse}
.wall:hover .col{animation-play-state:paused}
@keyframes wallup{from{transform:translateY(0)}to{transform:translateY(-50%)}}
.wall a{display:block;border-radius:14px;overflow:hidden;aspect-ratio:16/10;position:relative;background:#14171e;transition:transform .25s,box-shadow .25s;flex:none}
.wall a:hover{transform:scale(1.04);z-index:2;box-shadow:0 20px 50px -10px #000}
.wall img{width:100%;height:100%;object-fit:cover;object-position:top;display:block}
.wall a span{position:absolute;left:0;right:0;bottom:0;padding:22px 10px 8px;font:600 13px system-ui,sans-serif;color:#fff;background:linear-gradient(transparent,rgba(0,0,0,.8));opacity:0;transition:opacity .2s}.wall a:hover span{opacity:1}
.wall-top{position:fixed;left:14px;top:14px;z-index:5;display:flex;gap:8px}.wall-top a{background:rgba(0,0,0,.6);border:1px solid rgba(255,255,255,.25);color:#fff;border-radius:99px;padding:8px 16px;text-decoration:none;font:14px system-ui,sans-serif;backdrop-filter:blur(8px)}
.cmp{position:relative;border-radius:18px;overflow:hidden;border:1px solid var(--line);background:#000;aspect-ratio:16/10;user-select:none}
.cmp img{position:absolute;inset:0;width:100%;height:100%;object-fit:cover;object-position:top}
.cmp .b{clip-path:inset(0 0 0 var(--x,50%))}
.cmp .hd{position:absolute;top:0;bottom:0;left:var(--x,50%);width:3px;margin-left:-1.5px;background:#fff;box-shadow:0 0 12px rgba(0,0,0,.6);pointer-events:none}
.cmp .hd::after{content:"⇆";position:absolute;top:50%;left:50%;transform:translate(-50%,-50%);width:38px;height:38px;border-radius:50%;background:#fff;color:#111;display:grid;place-items:center;font-size:18px}
.cmp input[type=range]{position:absolute;inset:0;width:100%;height:100%;opacity:0;cursor:ew-resize}
.cmp .lab{position:absolute;top:12px;padding:4px 12px;border-radius:99px;background:rgba(0,0,0,.6);color:#fff;font-size:13px;pointer-events:none}.cmp .lab.l{left:12px}.cmp .lab.r{right:12px}
.cmp.side{display:grid;grid-template-columns:1fr 1fr;gap:6px;background:none;border:0;aspect-ratio:auto}.cmp.side .b{clip-path:none}.cmp.side img{position:static;aspect-ratio:16/10;border-radius:14px;border:1px solid var(--line)}.cmp.side .hd,.cmp.side input,.cmp.side .lab{display:none}
.sp-top{display:grid;grid-template-columns:minmax(0,1.3fr) minmax(0,1fr);gap:22px;align-items:start}
.sp-top img{width:100%;border-radius:18px;border:1px solid var(--line);aspect-ratio:16/10;object-fit:cover;object-position:top;background:#8883;display:block}
.sp-stat{display:grid;grid-template-columns:1fr 1fr;gap:10px;margin:0 0 14px}.sp-stat div{border:1px solid var(--line);border-radius:16px;padding:12px 16px;background:var(--card)}.sp-stat b{display:block;font-size:26px;font-variant-numeric:tabular-nums}.sp-stat small{color:var(--dim)}
.sp-links{display:flex;gap:8px;flex-wrap:wrap}.sp-links a{border:1px solid var(--line);background:var(--card);border-radius:12px;padding:9px 16px;color:var(--ink);text-decoration:none}.sp-links a.go{background:var(--acc);color:var(--acc-ink);border-color:var(--acc);font-weight:700}.sp-links a:hover{border-color:var(--acc)}
.sp-pages{list-style:none;margin:0;padding:0}.sp-pages li{padding:10px 4px;border-bottom:1px solid var(--line)}.sp-pages a{color:var(--link);text-decoration:none;font-weight:500}.sp-pages small{display:block;color:var(--dim);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
@media(max-width:800px){.sp-top{grid-template-columns:1fr}}
</style>`

func featPage(title, body string) string {
	return page(title+" - Flowsearch", "", featCSS+`<div class=ft>`+body+`</div>`)
}

// ---------- /more ----------

func morePage(w http.ResponseWriter, r *http.Request) {
	card := func(href, icon, name, desc string) string {
		return fmt.Sprintf(`<a class=ft-card href="%s"><span class=ic>%s</span><b>%s</b><span>%s</span></a>`, href, icon, name, desc)
	}
	body := `<h1>Everything Flowsearch can do</h1><p class=lead>Searching is only the start. These are the other places.</p><div class=ft-grid>` +
		card("/screenshots", "🖼", "Screenshots", "A gallery of websites as Flowsearch saw them: filter, save favourites, play a slideshow.") +
		card("/wall", "🧱", "Screenshot wall", "A wall of websites that scrolls by itself. Good on a big screen.") +
		card("/quiz", "🎯", "Guess the website", "A screenshot and four names. How many in a row?") +
		card("/compare", "⇆", "Compare", "Two websites on top of each other, with a slider.") +
		card("/tools", "🧰", "Tools", "Calculator, units, time in a city, hashes, dice, timer and more.") +
		card("/live", "📡", "Live", "What Flowsearch is reading right now, and how big it is getting.") +
		card("/chat", "💬", "AI mode", "Ask a question and get an answer with sources.") +
		card("/surprise?pick=page", "🎲", "Surprise me", "A random page from the whole index.") +
		card("/minecraft", "⛏", "Minecraft", "Videos and results about Minecraft.") +
		card("/settings", "⚙", "Settings", "How Flowsearch looks and works.") + `</div>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, featPage("More", body))
}

// ---------- /tools ----------

var toolExamples = [][2]string{
	{"Calculator", "12*(3+4)"}, {"Percent", "15% of 80"}, {"Length", "5 km in miles"}, {"Temperature", "100 f to c"}, {"Speed", "60 mph in km/h"}, {"Data", "3 gb to mb"},
	{"Time", "time in tokyo"}, {"Time", "new york time"}, {"Dice", "2d6"}, {"Coin", "flip a coin"}, {"Random", "random number 1 to 50"}, {"Colour", "#ff8800"},
	{"Password", "password 20"}, {"UUID", "uuid"}, {"Date", "today"}, {"Countdown", "days until christmas"}, {"Hash", "sha256 hello"}, {"Base64", "base64 hello"},
	{"Binary", "255 to binary"}, {"Roman", "2024 in roman"}, {"Primes", "prime factors 360"}, {"Fibonacci", "fibonacci 50"}, {"Average", "average 3 5 8"}, {"Divisors", "gcd 12 18"},
	{"Morse", "morse sos"}, {"Tip", "20% tip on 85"}, {"BMI", "bmi 70kg 180cm"}, {"Timer", "timer 5 minutes"}, {"Stopwatch", "stopwatch"},
}

func toolsPage(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var b strings.Builder
	b.WriteString(`<h1>Tools</h1><p class=lead>Type a calculation, a unit, a city, a colour, something to hash ... and the answer appears. Nothing is sent to other sites.</p>`)
	fmt.Fprintf(&b, `<form method=get action=/tools><input class=fld name=q value="%s" placeholder="for example: 5 km in miles" autofocus aria-label="What to work out"><button class=btn type=submit>Work it out</button></form>`, html.EscapeString(q))
	if q != "" {
		if c := instantCard(q); c != "" {
			b.WriteString(c)
		} else {
			b.WriteString(`<div class=ft-box>No tool answers that. Try one of the examples below, or <a href="/search?q=` + url.QueryEscape(q) + `">search for it</a>.</div>`)
		}
	}
	b.WriteString(`<h2>Examples</h2><div class=chips>`)
	for _, e := range toolExamples {
		fmt.Fprintf(&b, `<a href="/tools?q=%s" title="%s">%s</a>`, url.QueryEscape(e[1]), html.EscapeString(e[0]), html.EscapeString(e[1]))
	}
	b.WriteString(`</div>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, featPage("Tools", b.String()))
}

// ---------- screenshot helpers ----------

// siteShots: the front-page screenshots (not single pages)
func siteShots() []shotEntry {
	all := shotList()
	out := make([]shotEntry, 0, len(all))
	for _, e := range all {
		if e.Key == "" {
			out = append(out, e)
		}
	}
	return out
}

var hostArgRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,78}[a-z0-9])?$`)

// ---------- /quiz ----------

func quizPage(w http.ResponseWriter, r *http.Request) {
	s := siteShots()
	if len(s) < 8 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, featPage("Guess the website", `<h1>Guess the website</h1><p class=lead>There are not enough screenshots yet.</p>`))
		return
	}
	perm := rand.Perm(len(s))[:4]
	right := s[perm[0]]
	opts := make([]string, 4)
	for i, p := range perm {
		opts[i] = s[p].Host
	}
	rand.Shuffle(4, func(i, j int) { opts[i], opts[j] = opts[j], opts[i] })
	var b strings.Builder
	b.WriteString(`<h1>Guess the website</h1><p class=lead>Which website is this? Pick one of the four.</p>`)
	b.WriteString(`<div class=qz-bar><span>Score <b id=qs>0</b></span><span>Streak <b id=qk>0</b></span><span>Best streak <b id=qb>0</b></span></div>`)
	fmt.Fprintf(&b, `<div style="max-width:760px"><img class=qz-img src="%s" alt="A screenshot of a website" id=qi><div class=qz-opts>`, shotImg(right))
	for _, o := range opts {
		fmt.Fprintf(&b, `<button type=button data-h="%s">%s</button>`, html.EscapeString(o), html.EscapeString(o))
	}
	fmt.Fprintf(&b, `</div><p id=qm class=lead style="min-height:28px;margin:0"></p><p><a class=btn href="/quiz" id=qn style="display:none">Next screenshot →</a> <a class=btn href="/search?q=site:%s" id=qv style="display:none">Search this site</a></p></div>`, url.QueryEscape(right.Host))
	ans := base64.StdEncoding.EncodeToString([]byte(right.Host))
	b.WriteString(`<script>(function(){const a=atob('` + ans + `'),bs=[...document.querySelectorAll('.qz-opts button')];let sc=0,sk=0,bk=0;try{sc=+localStorage.getItem('qzsc')||0;sk=+localStorage.getItem('qzsk')||0;bk=+localStorage.getItem('qzbk')||0}catch(e){}
function paint(){qs.textContent=sc;qk.textContent=sk;qb.textContent=bk}paint();
bs.forEach(b=>b.onclick=()=>{bs.forEach(x=>x.disabled=true);const ok=b.dataset.h===a;b.classList.add(ok?'ok':'no');if(!ok)bs.forEach(x=>{if(x.dataset.h===a)x.classList.add('ok')});
 if(ok){sc++;sk++;bk=Math.max(bk,sk);qm.textContent='Correct!'}else{sk=0;qm.textContent='It was '+a+'.'}
 try{localStorage.setItem('qzsc',sc);localStorage.setItem('qzsk',sk);localStorage.setItem('qzbk',bk)}catch(e){}paint();qn.style.display='';qv.style.display='';
 document.addEventListener('keydown',e=>{if(e.key==='Enter')location.href='/quiz'},{once:true})});
})();</script>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, featPage("Guess the website", b.String()))
}

// ---------- /wall ----------

func wallPage(w http.ResponseWriter, r *http.Request) {
	s := siteShots()
	if len(s) == 0 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, featPage("Wall", `<h1>Wall</h1><p class=lead>There are no screenshots yet.</p>`))
		return
	}
	cols := 5
	n := min(len(s), 100)
	perm := rand.Perm(len(s))[:n]
	var b strings.Builder
	fmt.Fprintf(&b, `<div class=wall-top><a href=/screenshots>← Screenshots</a><a href=/more>More</a></div><div class=wall style="--cols:%d">`, cols)
	for c := 0; c < cols; c++ {
		var items strings.Builder
		for i := c; i < n; i += cols {
			e := s[perm[i]]
			h := html.EscapeString(e.Host)
			fmt.Fprintf(&items, `<a href="/site/%s"><img src="%s" alt="" loading=lazy><span>%s</span></a>`, url.PathEscape(e.Host), shotImg(e), h)
		}
		it := items.String()
		fmt.Fprintf(&b, `<div class=col style="--t:%ds">%s%s</div>`, 70+c*17, it, it) // the list twice: the move up by half is a seamless loop
	}
	b.WriteString(`</div>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, shell("Wall - Flowsearch", featCSS+b.String()))
}

// ---------- /compare ----------

func comparePage(w http.ResponseWriter, r *http.Request) {
	a := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("a")))
	bb := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("b")))
	var b strings.Builder
	b.WriteString(`<h1>Compare</h1><p class=lead>Two websites on top of each other. Drag the divider, or show them side by side.</p>`)
	s := siteShots()
	b.WriteString(`<datalist id=hs>`)
	for i, e := range s {
		if i >= 400 {
			break
		}
		b.WriteString(`<option value="` + html.EscapeString(e.Host) + `">`)
	}
	b.WriteString(`</datalist>`)
	fmt.Fprintf(&b, `<form method=get action=/compare><input class=fld name=a list=hs value="%s" placeholder="first website, e.g. wikipedia.org" aria-label="First website"><input class=fld name=b list=hs value="%s" placeholder="second website" aria-label="Second website"><button class=btn type=submit>Compare</button></form>`, html.EscapeString(a), html.EscapeString(bb))
	if hostArgRe.MatchString(a) && hostArgRe.MatchString(bb) {
		ia, ib := "/shot/"+url.PathEscape(a)+".jxl", "/shot/"+url.PathEscape(bb)+".jxl"
		fmt.Fprintf(&b, `<div class=cmp id=cm><img class=a src="%s" alt=""><img class=b src="%s" alt=""><div class=hd></div><span class="lab l">%s</span><span class="lab r">%s</span><input type=range min=0 max=100 value=50 id=cr aria-label="Slider"></div>`, ia, ib, html.EscapeString(a), html.EscapeString(bb))
		b.WriteString(`<p><button class=btn type=button id=cs>Side by side</button> <a class=btn href="/compare?a=` + url.QueryEscape(bb) + `&b=` + url.QueryEscape(a) + `">Swap</a></p>`)
		b.WriteString(`<script>(function(){const c=document.getElementById('cm'),r=document.getElementById('cr');r.oninput=()=>c.style.setProperty('--x',r.value+'%');document.getElementById('cs').onclick=function(){c.classList.toggle('side');this.textContent=c.classList.contains('side')?'Slider':'Side by side'}})();</script>`)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, featPage("Compare", b.String()))
}

// ---------- /site/<host> ----------

func sitePage(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/site/"), "www."))
	if !hostArgRe.MatchString(host) {
		http.Redirect(w, r, "/more", http.StatusFound)
		return
	}
	pages := ix.HostPagesAny(host)
	if pages == 0 {
		_, pages = ix.HostsWithSuffix(host)
	}
	var shotAt string
	for _, e := range siteShots() {
		if e.Host == host {
			shotAt = shotAgo(e.At)
			break
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<h1>%s</h1><p class=lead>What Flowsearch knows about this website.</p><div class=sp-top><div>`, html.EscapeString(host))
	if shotAt != "" {
		fmt.Fprintf(&b, `<img src="/shot/%s.jxl" alt="Screenshot of %s" onerror="this.remove()"><p style="color:var(--dim);font-size:13px;margin:6px 2px 0">Screenshot taken %s</p>`, url.PathEscape(host), html.EscapeString(host), shotAt)
	} else {
		b.WriteString(`<div class=ft-box>There is no screenshot of this website yet. A screenshot is taken when one of its pages shows up in a search.</div>`)
	}
	fmt.Fprintf(&b, `<p style="margin:12px 0 0"><button class=btn type=button id=rt data-h="%s">⟳ Take a new screenshot</button> <span id=rtm style="color:var(--dim);font-size:14px"></span></p>`, html.EscapeString(host))
	b.WriteString(`<script>(function(){const bt=document.getElementById('rt'),m=document.getElementById('rtm'),h=bt.dataset.h;
function pic(){let im=document.querySelector('.sp-top img');if(!im){im=document.createElement('img');bt.closest('div').prepend(im)}im.src='/shot/'+encodeURIComponent(h)+'.jxl?t='+Date.now();const n=bt.closest('div').querySelector('.ft-box');if(n)n.remove()}
function poll(n){fetch('/api/retake?site='+encodeURIComponent(h)).then(r=>r.json()).then(d=>{
  if(d.state==='done'){m.textContent='Done: the new screenshot is shown.';bt.disabled=false;pic()}
  else if(d.state==='failed'){m.textContent=d.message;bt.disabled=false}
  else if(n>0){m.textContent='Taking the screenshot … a few seconds';setTimeout(()=>poll(n-1),2000)}
  else{m.textContent='It takes longer than usual. Reload this page in a minute.';bt.disabled=false}}).catch(()=>{bt.disabled=false})}
bt.onclick=()=>{bt.disabled=true;m.textContent='Asking for a new screenshot …';fetch('/api/retake?site='+encodeURIComponent(h),{method:'POST'}).then(r=>r.json()).then(d=>{
  if(d.state==='done'){m.textContent=d.message;bt.disabled=false;pic()}
  else if(d.state==='running')poll(45);
  else{m.textContent=d.message;bt.disabled=false}}).catch(()=>{m.textContent='Could not ask.';bt.disabled=false})}})();</script>`)
	b.WriteString(`</div><div>`)
	fmt.Fprintf(&b, `<div class=sp-stat><div><b>%s</b><small>pages in the index</small></div><div><b>%s</b><small>screenshot</small></div></div>`, commas(pages), map[bool]string{true: "yes", false: "no"}[shotAt != ""])
	fmt.Fprintf(&b, `<div class=sp-links><a class=go href="/search?q=site:%s">🔎 Search this site</a><a href="https://%s" target=_blank rel="noopener noreferrer">↗ Open it</a><a href="https://web.archive.org/web/*/%s" target=_blank rel="noopener noreferrer">🕰 Old versions</a><a href="/compare?a=%s">⇆ Compare</a></div></div></div>`, url.QueryEscape(host), html.EscapeString(host), html.EscapeString(host), url.QueryEscape(host))
	ids, _ := ix.HostDocPage(host, 0, 12)
	if len(ids) > 0 {
		b.WriteString(`<h2>Some of its pages</h2><ul class=sp-pages>`)
		n := 0
		for _, id := range ids {
			d, ok := ix.Doc(id)
			if !ok {
				continue
			}
			rec := load(d)
			if rec.URL == "" || adultPage(d.Host, rec.Title, rec.Description) {
				continue
			}
			t := rec.Title
			if strings.TrimSpace(t) == "" {
				t = rec.URL
			}
			fmt.Fprintf(&b, `<li><a href="%s" rel="noopener noreferrer">%s</a><small>%s</small></li>`, html.EscapeString(rec.URL), html.EscapeString(clip(t, 90)), html.EscapeString(clip(rec.URL, 100)))
			n++
		}
		b.WriteString(`</ul>`)
		if n == 0 {
			b.WriteString(`<p class=lead>Nothing to show yet.</p>`)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, featPage(host, b.String()))
}
