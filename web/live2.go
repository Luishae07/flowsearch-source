package main

// /live, the new design (replaces livePage). One page, no tabs: everything Flowsearch knows about itself.
//   * the numbers come over the WebSocket /ws/stats, plus /api/stats (characters, segments), /api/crawlrun (the GitHub crawl) and /api/youtube
//   * the page moves ONLY when a batch of 10,000 new pages has been added; every batch is written into the batch list and the batch charts
//   * every website in a list keeps its favicon: /flowcdn/fav/<host>.jxl on top of a coloured initial

import (
	"fmt"
	"net/http"
	"strings"
)

const live2CSS = `<style>
.wrap:has(.lx){max-width:1380px}
body:has(.lx)::before{content:"";position:fixed;inset:0;z-index:-1;pointer-events:none;background:
 radial-gradient(900px 500px at 8% -8%,color-mix(in srgb,var(--aero1) 42%,transparent),transparent 70%),
 radial-gradient(800px 520px at 96% 6%,color-mix(in srgb,var(--acc) 22%,transparent),transparent 70%),
 radial-gradient(700px 600px at 50% 110%,color-mix(in srgb,#7c5cff 20%,transparent),transparent 70%);animation:lxaur 22s ease-in-out infinite alternate}
@keyframes lxaur{from{transform:translate3d(0,0,0) scale(1)}to{transform:translate3d(-2%,2%,0) scale(1.06)}}
body:has(.lx)::after{content:"";position:fixed;inset:0;z-index:-1;pointer-events:none;opacity:.35;background-image:linear-gradient(rgba(255,255,255,.04) 1px,transparent 1px),linear-gradient(90deg,rgba(255,255,255,.04) 1px,transparent 1px);background-size:44px 44px;mask-image:radial-gradient(ellipse at 50% 0,#000 0,transparent 70%)}
.lx{display:grid;gap:18px}
.lx-head{display:flex;align-items:flex-end;justify-content:space-between;gap:14px;flex-wrap:wrap;padding-top:6px}
.lx-head h1{margin:0;font-size:46px;line-height:1;letter-spacing:-.04em;background:linear-gradient(100deg,var(--ink),color-mix(in srgb,var(--acc) 80%,var(--ink)) 60%,var(--link));-webkit-background-clip:text;background-clip:text;color:transparent}
.lx-head p{margin:6px 0 0;color:var(--dim);font-size:15px}
.lx-pill{display:inline-flex;align-items:center;gap:9px;border-radius:99px;padding:7px 16px;font-size:14px;border:1px solid rgba(255,255,255,.22);background:color-mix(in srgb,var(--card) 55%,transparent);backdrop-filter:blur(14px) saturate(1.6);box-shadow:0 8px 24px -14px rgba(0,0,0,.7)}
.lx-pill i{width:9px;height:9px;border-radius:50%;background:#3ecf8e;animation:lxp 1.6s infinite}
.lx-pill[data-s=reconnecting] i,.lx-pill[data-s=connecting] i{background:var(--acc);animation:none}
.lx-pill[data-s=paused] i{background:var(--dim);animation:none}
@keyframes lxp{0%{box-shadow:0 0 0 0 rgba(62,207,142,.6)}70%{box-shadow:0 0 0 10px rgba(62,207,142,0)}100%{box-shadow:0 0 0 0 rgba(62,207,142,0)}}
.lx-card{position:relative;border-radius:28px;border:1px solid rgba(255,255,255,.17);background:linear-gradient(180deg,rgba(255,255,255,.07),rgba(255,255,255,.02)),color-mix(in srgb,var(--card) 62%,transparent);backdrop-filter:blur(18px) saturate(1.6);-webkit-backdrop-filter:blur(18px) saturate(1.6);box-shadow:0 24px 54px -34px rgba(0,0,0,.85),0 1px 0 rgba(255,255,255,.14) inset;padding:22px 24px;min-width:0;transition:transform .25s ease,border-color .25s ease}
.lx-card:hover{transform:translateY(-2px);border-color:rgba(255,255,255,.3)}
.lx-card h3{margin:0 0 16px;font-size:15px;letter-spacing:.02em;display:flex;align-items:center;gap:9px;flex-wrap:wrap}.lx-card h3 small{color:var(--dim);font-weight:400}
.lx-ico{width:26px;height:26px;border-radius:9px;display:inline-flex;align-items:center;justify-content:center;font-size:14px;background:color-mix(in srgb,var(--acc) 22%,transparent)}
.lx-hero{display:grid;grid-template-columns:minmax(0,1.45fr) minmax(0,1fr);gap:18px}
.lx-big{overflow:hidden;background:linear-gradient(135deg,color-mix(in srgb,var(--aero1) 40%,transparent),color-mix(in srgb,var(--acc) 16%,transparent)),var(--card)}
.lx-big::after{content:"";position:absolute;right:-60px;top:-60px;width:260px;height:260px;border-radius:50%;background:radial-gradient(circle,color-mix(in srgb,var(--acc) 38%,transparent),transparent 68%);filter:blur(8px);opacity:.7;pointer-events:none}
.lx-big small{color:var(--dim);letter-spacing:.14em;text-transform:uppercase;font-size:12px}
.lx-num{font-size:clamp(48px,9vw,110px);line-height:1;font-weight:800;letter-spacing:-.05em;font-variant-numeric:tabular-nums;margin:10px 0 8px;background:linear-gradient(180deg,#ffd28a,var(--acc));-webkit-background-clip:text;background-clip:text;color:transparent;filter:drop-shadow(0 6px 22px color-mix(in srgb,var(--acc) 38%,transparent))}
.lx-sub{color:var(--dim);font-size:16px}.lx-sub b{color:var(--ink);font-variant-numeric:tabular-nums}
.lx-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(176px,1fr));gap:12px}
.lx-k{position:relative;border-radius:20px;border:1px solid rgba(255,255,255,.14);background:linear-gradient(180deg,rgba(255,255,255,.06),rgba(255,255,255,.015)),color-mix(in srgb,var(--card) 52%,transparent);padding:15px 17px;min-width:0;overflow:hidden}
.lx-k::before{content:"";position:absolute;left:0;top:14px;bottom:14px;width:3px;border-radius:0 3px 3px 0;background:var(--acc);opacity:.85}
.lx-k small{display:block;color:var(--dim);font-size:11.5px;letter-spacing:.08em;text-transform:uppercase}
.lx-k b{display:block;font-size:27px;font-variant-numeric:tabular-nums;letter-spacing:-.025em;margin-top:4px;overflow:hidden;text-overflow:ellipsis}
.lx-k em{display:block;font-style:normal;color:var(--dim);font-size:12.5px;margin-top:2px}
.lx-two{display:grid;grid-template-columns:repeat(auto-fit,minmax(400px,1fr));gap:18px}
.lx-three{display:grid;grid-template-columns:repeat(auto-fit,minmax(310px,1fr));gap:18px}
.lx-bars{display:flex;align-items:flex-end;gap:7px;height:160px}
.lx-bar{flex:1;display:flex;flex-direction:column;justify-content:flex-end;align-items:center;height:100%;min-width:0}
.lx-bar i{display:block;width:100%;border-radius:9px 9px 3px 3px;min-height:3px;background:linear-gradient(180deg,var(--acc),color-mix(in srgb,var(--acc) 42%,transparent));transition:height .5s cubic-bezier(.2,.8,.2,1);box-shadow:0 0 18px -6px var(--acc)}
.lx-bar.now i{opacity:.65;background-image:repeating-linear-gradient(135deg,rgba(255,255,255,.28) 0 6px,transparent 6px 12px),linear-gradient(180deg,var(--acc),var(--acc))}
.lx-bar span{font-size:10.5px;color:var(--dim);margin-top:6px;white-space:nowrap}.lx-bar b{font-size:10.5px;color:var(--dim);font-weight:400;margin-bottom:4px;white-space:nowrap;font-variant-numeric:tabular-nums}
.lx-bars.cool .lx-bar i{background:linear-gradient(180deg,var(--link),color-mix(in srgb,var(--aero1) 55%,transparent));box-shadow:0 0 18px -6px var(--link)}
ul.lx-l{list-style:none;margin:0;padding:0;counter-reset:r}
.lx-l li{position:relative;display:flex;align-items:center;gap:11px;padding:9px 12px;margin:5px 0;border-radius:15px;background:color-mix(in srgb,var(--line) 36%,transparent);overflow:hidden;min-width:0;transition:background .2s}
.lx-l li:hover{background:color-mix(in srgb,var(--line) 62%,transparent)}
.lx-l li.none{color:var(--dim);justify-content:center}
.lx-l .fill{position:absolute;left:0;top:0;bottom:0;background:linear-gradient(90deg,color-mix(in srgb,var(--acc) 26%,transparent),color-mix(in srgb,var(--acc) 6%,transparent));border-right:2px solid var(--acc);transition:width .5s ease}
.lx-l .l,.lx-l .r{position:relative}.lx-l .l{flex:1;min-width:0;display:flex;align-items:center;gap:10px;overflow:hidden;white-space:nowrap;text-overflow:ellipsis}.lx-l .r{flex:none;color:var(--dim);font-variant-numeric:tabular-nums}
.lx-rank{position:relative;flex:none;width:22px;text-align:center;color:var(--dim);font-size:12px;font-variant-numeric:tabular-nums}
.lx-feed li{background:none;border-radius:0;border-bottom:1px solid color-mix(in srgb,var(--line) 70%,transparent);margin:0;padding:10px 4px}
.lx-feed li:hover{background:color-mix(in srgb,var(--line) 30%,transparent)}
.lx-feed li.new{animation:lxin .55s ease both}
.pl{display:inline-block;border-radius:99px;padding:1px 9px;font-size:11.5px;font-weight:700;color:#fff;letter-spacing:.02em}.pl-lemmy{background:#00a86b}.pl-discourse{background:#e0a100}.pl-flowforum{background:#7b5cff}
.lx-feed .pl{flex:none;margin-right:8px}
@keyframes lxin{from{opacity:0;transform:translateY(-10px);background:color-mix(in srgb,var(--acc) 24%,transparent)}to{opacity:1;transform:none}}
.lx-feed a{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;text-decoration:none;color:var(--link);font-weight:500}.lx-feed a:hover{text-decoration:underline}
.lx-feed .h{flex:none;color:var(--dim);font-size:12.5px;max-width:38%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.lx-av{position:relative;flex:none;width:26px;height:26px;border-radius:8px;display:inline-flex;align-items:center;justify-content:center;font-size:13px;font-weight:700;color:#fff;overflow:hidden;box-shadow:0 4px 10px -5px rgba(0,0,0,.6)}
.lx-av img{position:absolute;inset:0;width:100%;height:100%;object-fit:contain;background:#fff;border-radius:8px}
.lx-tag{display:inline-block;border-radius:99px;padding:3px 11px;font-size:12px;border:1px solid var(--line);position:relative}
.lx-tag.ok{border-color:#3ecf8e;color:#3ecf8e;background:rgba(62,207,142,.1)}.lx-tag.bad{border-color:#ff6b6b;color:#ff6b6b;background:rgba(255,107,107,.1)}.lx-tag.run{border-color:var(--acc);color:var(--acc);background:color-mix(in srgb,var(--acc) 12%,transparent)}
.lx-feed{max-height:600px;overflow:hidden}
.lx-card.shim .lx-num,.lx-card.shim .lx-k b{animation:lxsh 1.6s ease-in-out infinite}
@keyframes lxsh{50%{opacity:.55}}
.lx-tabs{display:flex;flex-wrap:wrap;gap:6px;padding:6px;border-radius:99px;border:1px solid rgba(255,255,255,.2);background:color-mix(in srgb,var(--card) 52%,transparent);backdrop-filter:blur(16px) saturate(1.6);width:fit-content;max-width:100%;box-shadow:0 12px 30px -18px rgba(0,0,0,.7)}
.lx-tabs button{border:0;background:transparent;color:var(--dim);font:inherit;font-size:15px;padding:9px 18px;border-radius:99px;cursor:pointer;transition:background .2s,color .2s,transform .15s}
.lx-tabs button:hover{color:var(--ink);transform:translateY(-1px)}
.lx-tabs button.on{background:var(--acc);color:var(--acc-ink);font-weight:700;box-shadow:0 8px 20px -10px rgba(0,0,0,.7)}
.lx-pbar{height:9px;border-radius:99px;background:rgba(128,128,128,.25);margin-top:10px;overflow:hidden}.lx-pbar i{display:block;height:100%;width:0;background:linear-gradient(90deg,var(--acc),var(--link));transition:width .5s ease}
[hidden]{display:none!important}
.lx-foot{text-align:center;color:var(--dim);font-size:13px;padding:8px 0 4px}
@media(max-width:900px){.lx-hero{grid-template-columns:1fr}.lx-two{grid-template-columns:1fr}.lx-head h1{font-size:36px}}
@media(prefers-reduced-motion:reduce){*{animation:none!important;transition:none!important}}
</style>`

const live2Body = `<div class=lx>
<div class=lx-head><div><h1>Live</h1><p>Everything Flowsearch knows. The page moves each time a new batch of pages has been stored.</p></div><span id=lxstate class=lx-pill data-s=connecting><i></i><span>Connecting…</span></span></div>

<nav class=lx-tabs aria-label="What to watch">
<button id=lxt-all class=on>🌐 Overview</button><button id=lxt-fo>💬 Forums</button><button id=lxt-yt>▶ YouTube</button><button id=lxt-de>🇩🇪 Deutsch</button><button id=lxt-am>🛒 Amazon</button><button id=lxt-rb>🎮 Games</button>
</nav>

<div id=lxall class=lx>
<section class=lx-hero>
<div class="lx-card lx-big shim" id=lxbig>
<small>Pages Flowsearch knows</small>
<div class=lx-num id=lxpages>0</div>
<div class=lx-sub><b id=lxsites>0</b> websites · <b id=lxterms>0</b> different words</div>
</div>
<div class=lx-card>
<h3><span class=lx-ico>📦</span>Batches <small id=lxrunnote></small></h3>
<div class=lx-grid>
<div class=lx-k><small>Batches seen</small><b id=lxnb2>0</b><em>since this page was opened</em></div>
<div class=lx-k><small>Last batch</small><b id=lxlbp>–</b><em id=lxlbs>pages</em></div>
<div class=lx-k><small>Stored in</small><b id=lxlbw>–</b><em>for the last batch</em></div>
<div class=lx-k><small id=lxrunn>This run</small><b id=lxrun>0</b><em>pages added by the crawl</em></div>
</div>
</div>
</section>

<section class=lx-grid>
<div class=lx-k><small>Text read</small><b id=lxchars>0</b><em>characters in the index</em></div>
<div class=lx-k><small>Index files</small><b id=lxseg>0</b><em>segments on disk</em></div>
<div class=lx-k><small>Pages per site</small><b id=lxavg>0</b><em>on average</em></div>
<div class=lx-k><small>Words per page</small><b id=lxwpp>0</b><em>different words, on average</em></div>
<div class=lx-k><small>Deutsche Seiten</small><b id=lxde>0</b><em id=lxdesub></em></div>
<div class=lx-k><small>YouTube videos</small><b id=lxyt>0</b><em>Flowsearch knows about</em></div>
</section>

<section class=lx-three>
<div class=lx-card><h3><span class=lx-ico>📊</span>Pages per batch <small>every batch seen on this page</small></h3><div class=lx-bars id=lxbh></div></div>
<div class=lx-card><h3><span class=lx-ico>⏱</span>Minutes between batches <small>how fast they come</small></h3><div class="lx-bars cool" id=lxbt></div></div>
<div class=lx-card><h3><span class=lx-ico>📅</span>Pages per day <small>last 8 days</small></h3><div class=lx-bars id=lxh3></div></div>
</section>

<section class=lx-two>
<div class=lx-card><h3><span class=lx-ico>🆕</span>Newest pages <small>from the latest batch</small></h3><ul class="lx-l lx-feed" id=lxrecent></ul></div>
<div style="display:grid;gap:18px;align-content:start">
<div class=lx-card><h3><span class=lx-ico>📥</span>Batches that arrived <small><span id=lxnb>0</span> in all · the page moves when a new one is stored</small></h3><ul class="lx-l lx-feed" id=lxbatches><li class=none>Waiting for the next 10,000 pages…</li></ul></div>
<div class=lx-card><h3><span class=lx-ico>🏆</span>Biggest websites</h3><ul class=lx-l id=lxtop></ul></div>
<div class=lx-card><h3><span class=lx-ico>🔁</span>Collection rounds</h3><ul class=lx-l id=lxruns></ul></div>
</div>
</section>

<section class=lx-two>
<div class=lx-card>
<h3><span class=lx-ico>🕷</span>The GitHub crawl <small>Rust crawler, follows links to other websites, 10,000 pages per request</small></h3>
<div class=lx-grid style="margin-bottom:12px">
<div class=lx-k><small>State</small><b id=lxcrstate>…</b><em id=lxcrsub></em></div>
<div class=lx-k><small>Next batch</small><b id=lxcrnext>0</b><em id=lxcrsites></em></div>
<div class=lx-k><small>Seed list</small><b id=lxcrseeds>0</b><em id=lxcrdisc></em></div>
</div>
<ul class=lx-l id=lxcrruns></ul>
</div>
<div class=lx-card>
<h3><span class=lx-ico>🇩🇪</span>German pages <small>.de .at .ch .li</small></h3>
<div class=lx-grid style="margin-bottom:12px">
<div class=lx-k><small>Pages</small><b id=lxdp>0</b></div>
<div class=lx-k><small>Websites</small><b id=lxds>0</b></div>
</div>
<ul class=lx-l id=lxdtop></ul>
</div>
</section>
</div>

<div id=lxfo class=lx hidden>
<!--FORUMTAB-->
</div>

<div id=lxytv hidden class=lx>
<section class=lx-grid>
<div class=lx-k><small>YouTube videos</small><b id=lxyttot>0</b><em>Flowsearch knows about</em></div>
<div class=lx-k><small>Random video</small><b style="font-size:1rem"><a class=lx-tag href="/surprise?site=youtube.com&kind=video">Surprise me</a></b></div>
</section>
<div class=lx-card><h3><span class=lx-ico>▶</span>12 random videos <small>reload for new ones</small></h3><ul class="lx-l lx-feed" id=lxytlist></ul></div>
</div>

<div id=lxdev hidden class=lx>
<section class=lx-grid>
<div class=lx-k><small>Deutsche Seiten</small><b id=lxdepages>0</b><em>.de .at .ch .li</em></div>
<div class=lx-k><small>Websites</small><b id=lxdesites>0</b></div>
</section>
<section class=lx-two>
<div class=lx-card><h3><span class=lx-ico>🏆</span>Größte Websites</h3><ul class=lx-l id=lxdetop></ul></div>
<div class=lx-card><h3><span class=lx-ico>🆕</span>Neueste Seiten <small>aus dem letzten Batch</small></h3><ul class="lx-l lx-feed" id=lxderecent></ul></div>
</section>
</div>

<div id=lxam hidden class=lx>
<section class=lx-grid>
<div class=lx-k><small>Products</small><b id=lxamtot>0</b><em>Amazon pages in the index</em></div>
<div class=lx-k><small>amazon.com</small><b id=lxamcom>0</b></div>
<div class=lx-k><small>amazon.de</small><b id=lxamde>0</b></div>
<div class=lx-k><small>With a price</small><b id=lxampriced>0</b></div>
</section>
<div class=lx-card><h3><span class=lx-ico>🛒</span>Newest products</h3><ul class="lx-l lx-feed" id=lxamlist></ul></div>
</div>

<div id=lxrb hidden class=lx>
<section class=lx-grid>
<div class=lx-k><small>Roblox games read</small><b id=lxrbgames>0</b><em>with creator, genre, players and visits</em></div>
<div class=lx-k><small>Players right now</small><b id=lxrbplayers>0</b><em>in these games</em></div>
</section>
<section class=lx-two>
<div class=lx-card><h3><span class=lx-ico>🔥</span>Most players right now</h3><ul class="lx-l lx-feed" id=lxrbtop></ul></div>
<div class=lx-card><h3><span class=lx-ico>🆕</span>Newest games</h3><ul class="lx-l lx-feed" id=lxrbrecent></ul></div>
</section>
</div>
</div>`

const live2JS = `<script>
(function(){
const $=id=>document.getElementById(id);
const fmt=x=>Math.round(x).toLocaleString('en-US');
const big=n=>n>=1e12?(n/1e12).toFixed(2)+' T':n>=1e9?(n/1e9).toFixed(2)+' G':n>=1e6?(n/1e6).toFixed(1)+' M':n>=1e3?(n/1e3).toFixed(1)+' k':String(n);
const set=(id,v)=>{const e=$(id);if(e&&e.textContent!==v)e.textContent=v};
const hm=t=>new Date(t).toLocaleTimeString('en-GB',{hour:'2-digit',minute:'2-digit'});
const hue=h=>{let x=0;for(const c of h)x=(x*31+c.charCodeAt(0))>>>0;return x%360};
// a website's icon: its own favicon from our server on top of a coloured initial (the initial shows if the icon cannot load)
function av(host){
  host=(host||'').replace(/^www\./,'');
  const s=document.createElement('span');s.className='lx-av';s.style.background='linear-gradient(135deg,hsl('+hue(host)+' 60% 50%),hsl('+((hue(host)+40)%360)+' 55% 38%))';s.textContent=(host[0]||'?').toUpperCase();
  const i=document.createElement('img');i.src='/flowcdn/fav/'+encodeURIComponent(host)+'.jxl';i.alt='';i.loading='lazy';i.referrerPolicy='no-referrer';i.onerror=()=>i.remove();
  s.append(i);return s;
}
// a number that glides to its new value
const shown={};
function glide(id,to){
  const e=$(id);if(!e)return;const from=shown[id]===undefined?to:shown[id];shown[id]=to;
  if(from===to||Math.abs(to-from)>1e8){e.textContent=fmt(to);return}
  const t0=performance.now(),d=900;
  (function step(t){const k=Math.min(1,(t-t0)/d),ease=1-Math.pow(1-k,3);e.textContent=fmt(from+(to-from)*ease);if(k<1&&shown[id]===to)requestAnimationFrame(step)})(t0);
}
function bars(el,vals,labels){
  if(!el)return;
  if(el.children.length!==vals.length){el.textContent='';vals.forEach(()=>{const c=document.createElement('div');c.className='lx-bar';const b=document.createElement('b'),i=document.createElement('i'),s=document.createElement('span');c.append(b,i,s);el.append(c)})}
  const mx=Math.max(1,...vals);
  vals.forEach((n,k)=>{const c=el.children[k];c.classList.toggle('now',k===vals.length-1);c.children[0].textContent=fmt(n);c.children[1].style.height=Math.max(2,Math.round(100*n/mx))+'%';c.children[2].textContent=labels[k]});
}
function list(ul,rows,mk){ul.textContent='';if(!rows.length){const li=document.createElement('li');li.className='none';li.textContent='Nothing yet';ul.append(li);return}for(const r of rows)ul.append(mk(r))}
function toplist(ul,tops){
  const mx=tops.length?Math.max(1,tops[0].pages):1;let n=0;
  list(ul,tops,s=>{const li=document.createElement('li'),f=document.createElement('i');f.className='fill';f.style.width=(100*s.pages/mx)+'%';
    const k=document.createElement('span');k.className='lx-rank';k.textContent=++n;
    const l=document.createElement('span');l.className='l';l.append(av(s.host),document.createTextNode(s.host.replace(/^www\./,'')));
    const r=document.createElement('span');r.className='r';r.textContent=fmt(s.pages);li.append(f,k,l,r);return li});
}
const seen=new Set();
let S={},pend=null,ws=null;
function status(s){const el=$('lxstate');el.dataset.s=s;el.lastChild.textContent=s==='live'?'Live':s==='paused'?'Paused while the tab is hidden':s==='connecting'?'Connecting…':'Reconnecting…'}
function connect(){
  if(document.hidden)return;
  ws=new WebSocket((location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/ws/stats');
  ws.onopen=()=>status('live');
  ws.onclose=()=>{if(!document.hidden){status('reconnecting');setTimeout(connect,1000)}};
  ws.onmessage=e=>{const m=JSON.parse(e.data);pend=Object.assign(pend||{},m)};
}
function draw(d){
  Object.assign(S,d);
  if(d.p!==undefined){
    $('lxbig').classList.remove('shim');
    glide('lxpages',d.p);set('lxsites',fmt(d.s));set('lxterms',fmt(d.w));
    set('lxrun',fmt(d.run));set('lxrunn',d.ra?'This run':'Last run');
    set('lxrunnote',d.ra?'crawler running':'crawler idle');
    set('lxavg',d.s?(d.p/d.s).toFixed(1):'0');set('lxwpp',d.p?Math.round(d.w/d.p*10)/10+'':'0');
  }
  if(d.dhist){const day=864e5,base=Math.floor(d.t/day)*day,n=d.dhist.length;bars($('lxh3'),d.dhist,d.dhist.map((_,i)=>new Date(base-(n-1-i)*day).toLocaleDateString(undefined,{month:'short',day:'numeric'})))}
  if(d.runs){
    const mx=Math.max(1,...d.runs.map(r=>r.n)),day=ms=>new Date(ms).toLocaleDateString('en-GB',{weekday:'short'});
    list($('lxruns'),d.runs.slice().reverse(),r=>{const li=document.createElement('li'),f=document.createElement('i');f.className='fill';f.style.width=(100*r.n/mx)+'%';
      const l=document.createElement('span');l.className='l';l.textContent=day(r.s)+' '+hm(r.s)+' to '+hm(r.e)+(r.a?'  ● running':'');
      const rr=document.createElement('span');rr.className='r';rr.textContent=fmt(r.n);li.append(f,l,rr);return li});
  }
  if(d.top)toplist($('lxtop'),d.top);
  if(d.recent){
    const ul=$('lxrecent');ul.textContent='';
    for(const p of d.recent.slice().reverse()){
      const li=document.createElement('li');if(!seen.has(p.url)){li.className='new';seen.add(p.url)}
      const a=document.createElement('a');a.href=p.url;a.target='_blank';a.rel='noopener noreferrer';a.textContent=p.title||p.url;
      const h=document.createElement('span');h.className='h';h.textContent=p.host.replace(/^www\./,'');
      li.append(av(p.host),a,h);ul.append(li);
    }
    if(seen.size>500)seen.clear();
  }
  if(d.fo&&d.fo.languages!==undefined)set('fbProg',fmt(d.fo.languages));
  if(d.de){
    glide('lxdp',d.de.p);set('lxds',fmt(d.de.s));
    set('lxde',fmt(d.de.p));set('lxdesub',fmt(d.de.s)+' Websites');
    set('lxdepages',fmt(d.de.p));set('lxdesites',fmt(d.de.s));
    if(d.de.top){toplist($('lxdtop'),d.de.top);toplist($('lxdetop'),d.de.top)}
    if(d.de.recent){const ul=$('lxderecent');ul.textContent='';for(const p of d.de.recent.slice().reverse()){const li=document.createElement('li'),a=document.createElement('a');a.href=p.url;a.target='_blank';a.rel='noopener noreferrer';a.textContent=p.title||p.url;const h=document.createElement('span');h.className='h';h.textContent=p.host.replace(/^www\./,'');li.append(av(p.host),a,h);ul.append(li)}
      if(!d.de.recent.length){const li=document.createElement('li');li.className='none';li.textContent='Noch keine deutschen Seiten';ul.append(li)}}
  }
}
// the batches come from the file batches.json (written by the ingest after every stored batch; /api/batches). The page moves ONLY when that
// file shows a new batch: then everything is drawn at once (counters, charts, lists). The WebSocket only keeps the latest numbers ready.
let acc={},firstDrawn=false,knownCount=null;
function gate(m){
  Object.assign(acc,m);
  if(!firstDrawn&&acc.p!==undefined){draw(acc);firstDrawn=true}
}
function renderBatches(d){
  const bs=d.batches||[],n=bs.length;
  set('lxnb',fmt(d.count||0));set('lxnb2',fmt(d.count||0));
  if(n){const last=bs[n-1];set('lxlbp',fmt(last.pages));set('lxlbs','pages · '+fmt(last.added)+' new, '+fmt(last.duplicates)+' already known');set('lxlbw',(last.ms/1000).toFixed(0)+' s')}
  const chart=bs.slice(-16),lab=chart.map(b=>'#'+b.seq);
  bars($('lxbh'),chart.map(b=>b.pages),lab);
  bars($('lxbt'),chart.map((b,i)=>i?Math.round((b.at-chart[i-1].at)/600)/100:0),lab);
  const ul=$('lxbatches');
  list(ul,bs.slice(-12).reverse(),b=>{const li=document.createElement('li'),l=document.createElement('span'),r=document.createElement('span');
    l.className='l';l.textContent='#'+b.seq+'  +'+fmt(b.added)+' new pages'+(b.duplicates?' · '+fmt(b.duplicates)+' already known':'')+' · stored in '+Math.round(b.ms/1000)+' s';
    r.className='r';r.textContent=new Date(b.at).toLocaleString('en-GB',{day:'2-digit',month:'short',hour:'2-digit',minute:'2-digit'});li.append(l,r);return li});
  if(!n){ul.textContent='';const li=document.createElement('li');li.className='none';li.textContent='No batch stored yet';ul.append(li)}
}
function pollBatches(){
  fetch('/api/batches?last=60').then(r=>r.json()).then(d=>{
    renderBatches(d);
    if(knownCount!==null&&d.count>knownCount&&acc.p!==undefined)draw(acc); // a new batch was stored: the page moves
    knownCount=d.count;
  }).catch(()=>{});
}
function loop(){if(pend){const m=pend;pend=null;gate(m)}requestAnimationFrame(loop)}
document.addEventListener('visibilitychange',()=>{if(document.hidden){if(ws){ws.onclose=null;ws.close()}status('paused')}else{status('connecting');connect()}});

// slower numbers (every 20 seconds): the size of the index, the GitHub crawl, YouTube
function slow(){
  fetch('/api/stats').then(r=>r.json()).then(d=>{set('lxchars',big(d.characters||0));set('lxseg',fmt(d.segments||0))}).catch(()=>{});
  fetch('/api/youtube').then(r=>r.json()).then(d=>set('lxyt',fmt(d.count||0))).catch(()=>{});
  fetch('/api/crawlrun').then(r=>r.json()).then(d=>{
    const runs=d.runs||[],cur=runs[0],ago=t=>{if(!t)return '';const s=Math.max(0,(Date.now()-new Date(t))/1000);return s<90?Math.round(s)+' s ago':s<5400?Math.round(s/60)+' min ago':s<172800?Math.round(s/3600)+' h ago':Math.round(s/86400)+' days ago'};
    const word=r=>r.status==='completed'?(r.conclusion==='success'?'finished':r.conclusion==='cancelled'?'cancelled':'failed'):r.status==='in_progress'?'running':'waiting';
    set('lxcrstate',cur?word(cur):'no run yet');
    set('lxcrsub',cur?(cur.status==='completed'?'ended ':'started ')+ago(cur.status==='completed'?cur.updated:cur.started):'');
    set('lxcrnext',fmt(d.next_batch||0));set('lxcrsites',"site number "+fmt((d.next_batch||0)*(d.batch_size||500))+" on");
    set('lxcrseeds',fmt(d.seeds||0));set('lxcrdisc',fmt(d.discovered||0)+' found by the seed-list bot');
    list($('lxcrruns'),runs,r=>{const li=document.createElement('li'),l=document.createElement('a'),rr=document.createElement('span'),t=document.createElement('span');
      l.className='l';l.href=r.url;l.target='_blank';l.rel='noopener noreferrer';l.textContent='run '+r.id;l.style.color='var(--link)';l.style.textDecoration='none';
      const w=word(r);t.className='lx-tag '+(w==='finished'?'ok':w==='failed'?'bad':w==='running'?'run':'');t.textContent=w;rr.className='r';rr.textContent=ago(r.started);li.append(l,t,rr);return li});
  }).catch(()=>{});
}
slow();setInterval(slow,20000);
pollBatches();setInterval(pollBatches,4000);

// ---- the tabs: Forums, YouTube, Deutsch, Amazon, Games. Each one loads its own data only while it is open ----
const TABS=[['lxt-all','lxall'],['lxt-fo','lxfo'],['lxt-yt','lxytv'],['lxt-de','lxdev'],['lxt-am','lxam'],['lxt-rb','lxrb']];
let fws=null,amT=null,rbT=null,fPrev=null,fSeen=null;
const nf=x=>(x||0).toLocaleString('en-GB');
function linkRow(a,small){const li=document.createElement('li'),k=document.createElement('a');k.href=a.u;k.target='_blank';k.rel='noopener noreferrer';k.textContent=a.t;if(small){const s=document.createElement('span');s.className='h';s.textContent=small;li.append(k,s)}else li.append(k);return li}
function fill(ul,rows,mk,none){ul.textContent='';if(!rows.length){const li=document.createElement('li');li.className='none';li.textContent=none;ul.append(li);return}for(const r of rows)ul.append(mk(r))}
// the Forums tab is in forumtab.go: its script gives the page window.FZ
function foOpen(){if(window.FZ)window.FZ.open()}
function foClose(){if(window.FZ)window.FZ.close()}
function ytLoad(){fetch('/api/youtube').then(r=>r.json()).then(d=>{set('lxyttot',nf(d.count));fill($('lxytlist'),d.sample||[],v=>linkRow(v),'No videos yet')}).catch(()=>{})}
function amLoad(){fetch('/api/amazon').then(r=>r.json()).then(d=>{set('lxamtot',nf(d.total));set('lxamcom',nf(d.com));set('lxamde',nf(d.de));set('lxampriced',nf(d.priced));
  fill($('lxamlist'),d.recent||[],v=>linkRow({u:v.u,t:(v.price?v.price+'  ':'')+v.t},v.site),'No products yet')}).catch(()=>{})}
function rbLoad(){fetch('/api/roblox').then(r=>r.json()).then(d=>{set('lxrbgames',nf(d.games));set('lxrbplayers',nf(d.players));
  const row=v=>linkRow(v,(v.creator?'by '+v.creator+' · ':'')+(v.active?v.active+' playing':'')+(v.visits?' · '+v.visits+' visits':''));
  fill($('lxrbtop'),d.top||[],row,'No games yet');fill($('lxrbrecent'),d.recent||[],row,'No games yet')}).catch(()=>{})}
function openTab(id){
  for(const [b,v] of TABS){$(b).classList.toggle('on',b===id);$(v).hidden=TABS.find(t=>t[0]===id)[1]!==v}
  if(id==='lxt-fo')foOpen();else foClose();
  clearInterval(amT);clearInterval(rbT);
  if(id==='lxt-yt')ytLoad();
  if(id==='lxt-am'){amLoad();amT=setInterval(amLoad,10000)}
  if(id==='lxt-rb'){rbLoad();rbT=setInterval(rbLoad,10000)}
  try{localStorage.setItem('lxtab',id)}catch(e){}
}
for(const [b] of TABS)$(b).onclick=()=>openTab(b);
try{const t=localStorage.getItem('lxtab');if(t&&TABS.some(x=>x[0]===t)&&t!=='lxt-all')openTab(t)}catch(e){}
loop();connect();
})();
</script>`

func livePage2(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	// the Forums tab has its own file (forumtab.go): its markup goes where <!--FORUMTAB--> is, its script comes before the page script
	body := strings.Replace(live2Body, "<!--FORUMTAB-->", forumTabBody, 1)
	fmt.Fprint(w, page("Live - Flowsearch", "", live2CSS+forumTabCSS+body+forumTabJS+live2JS))
}
