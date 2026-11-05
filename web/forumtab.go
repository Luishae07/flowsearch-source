package main

// The Forums tab of the live page ("Control room"), in its own file: forumTabCSS, forumTabBody (put into the page where live2.go has <!--FORUMTAB-->)
// and forumTabJS. The script brings its own helpers and gives the page window.FZ.open() / window.FZ.close(), which live2.go calls when the tab is shown or left.
//
// The layout, top to bottom:
//   command bar    chips for the kind of forum (with counts), a search box, Pause
//   ticker         the titles of the newest posts, running past without stopping
//   tile board     the counter, the ring with the share of each platform, the crawl, a spotlight that turns through the hottest posts, a treemap of the
//                  programming languages (a click on a language filters everything)
//   live lanes     one lane for each kind of forum (Lemmy, Discourse, Flowforum, Programming); a chip makes one lane wide
//   cloud + sites  the biggest communities as a word cloud (a click searches for it), the biggest forum sites as bars
//   drawer         a click on any post opens it at the side with the text and the best comment

const forumTabCSS = `<style>
.fb{display:grid;gap:16px}
.fb-bar{display:flex;gap:10px;align-items:center;flex-wrap:wrap;position:sticky;top:74px;z-index:6;padding:10px 12px;border-radius:22px;border:1px solid rgba(255,255,255,.16);background:color-mix(in srgb,var(--card) 78%,transparent);backdrop-filter:blur(16px) saturate(1.5);-webkit-backdrop-filter:blur(16px) saturate(1.5);box-shadow:0 18px 40px -28px rgba(0,0,0,.9)}
.fb-chips{display:flex;gap:6px;flex-wrap:wrap}
.fb-chip{display:inline-flex;align-items:center;gap:7px;border:1px solid var(--line);background:none;color:var(--ink);border-radius:99px;padding:6px 13px;font:inherit;font-size:13.5px;cursor:pointer;transition:background .15s,transform .15s}
.fb-chip:hover{transform:translateY(-1px)}.fb-chip i{width:9px;height:9px;border-radius:50%}.fb-chip em{font-style:normal;opacity:.65;font-size:12px;font-variant-numeric:tabular-nums}
.fb-chip.on{background:var(--acc);color:var(--acc-ink);border-color:var(--acc);font-weight:700}.fb-chip.on em{opacity:.8}
.fb-bar input{flex:1;min-width:150px;border:1px solid var(--line);background:color-mix(in srgb,var(--bg) 40%,transparent);color:var(--ink);border-radius:99px;padding:8px 16px;font:inherit;font-size:14.5px}
.fb-bar input:focus{outline:none;border-color:var(--acc);box-shadow:0 0 0 4px var(--glow)}
.fb-btn{border:1px solid var(--line);background:none;color:var(--ink);border-radius:99px;padding:7px 15px;font:inherit;font-size:14px;cursor:pointer}.fb-btn:hover{border-color:var(--acc)}
.fb-ticker{display:flex;align-items:center;gap:0;overflow:hidden;border-radius:16px;border:1px solid rgba(255,255,255,.12);background:color-mix(in srgb,var(--card) 55%,transparent);height:42px}
.fb-tlabel{flex:none;display:flex;align-items:center;gap:8px;padding:0 16px;height:100%;font-size:12px;font-weight:800;letter-spacing:.14em;color:#06281a;background:#3ecf8e}.fb-tlabel i{width:8px;height:8px;border-radius:50%;background:#06281a;animation:fbp 1.3s infinite}
@keyframes fbp{50%{opacity:.25}}
.fb-track{display:flex;gap:34px;white-space:nowrap;padding-left:24px;animation:fbtick 110s linear infinite;will-change:transform}.fb-ticker:hover .fb-track{animation-play-state:paused}
@keyframes fbtick{to{transform:translateX(-50%)}}
.fb-track a{display:inline-flex;align-items:center;gap:9px;color:var(--ink);text-decoration:none;font-size:14px;cursor:pointer}.fb-track a:hover{color:var(--link)}.fb-track a i{width:8px;height:8px;border-radius:50%;flex:none}.fb-track a small{color:var(--dim);font-size:12px}
.fb-bento{display:grid;grid-template-columns:repeat(12,minmax(0,1fr));gap:16px}
.fb-a{grid-column:span 5}.fb-b{grid-column:span 3}.fb-c{grid-column:span 4}.fb-d{grid-column:span 7}.fb-e{grid-column:span 5}
@media(max-width:1000px){.fb-a,.fb-b,.fb-c,.fb-d,.fb-e{grid-column:1/-1}}
.fb-t{--g:rgba(255,255,255,.06);position:relative;overflow:hidden;border-radius:26px;padding:22px 24px;border:1px solid rgba(255,255,255,.13);background:radial-gradient(420px 220px at 0% 0%,var(--g),transparent 70%),color-mix(in srgb,var(--card) 66%,transparent);box-shadow:0 24px 60px -42px rgba(0,0,0,.95),0 1px 0 rgba(255,255,255,.1) inset}
.fb-t h4{margin:0 0 12px;font-size:11.5px;letter-spacing:.12em;text-transform:uppercase;color:var(--dim);font-weight:700;display:flex;align-items:center;gap:8px}
.fb-t small{color:var(--dim);font-size:12px;letter-spacing:.08em;text-transform:uppercase}
.fb-a{--g:rgba(0,168,107,.28);--ac:#00a86b}.fb-b{--g:rgba(123,92,255,.3);--ac:#7b5cff}.fb-c{--g:rgba(255,179,71,.22);--ac:#ffb347}.fb-d{--g:rgba(255,93,115,.2);--ac:#ff5d73}.fb-e{--g:rgba(45,127,249,.28);--ac:#2d7ff9}
.fb-t::before{content:"";position:absolute;left:26px;right:26px;top:0;height:3px;border-radius:0 0 4px 4px;background:var(--ac,transparent);opacity:.9}
.fb-tlabel.flash{background:#b8f5d6}.fb-tlabel{transition:background .5s}
.fb-sn{color:var(--dim);font-size:13px;line-height:1.45;margin:0 0 7px;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;overflow-wrap:anywhere}
.fb-qc{border-left:3px solid var(--c);padding:1px 0 1px 9px;margin:0 0 8px;font-size:12.5px;line-height:1.4;color:var(--dim);display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;overflow-wrap:anywhere}.fb-qc b{color:var(--ink);font-weight:600}
.fb-num{display:block;font-size:clamp(48px,7.5vw,88px);line-height:1.02;letter-spacing:-.05em;font-variant-numeric:tabular-nums;background:linear-gradient(135deg,var(--ink),color-mix(in srgb,var(--acc) 75%,var(--ink)));-webkit-background-clip:text;background-clip:text;color:transparent;margin:4px 0 2px}
.fb-a em{display:block;color:var(--dim);font-style:normal;font-size:13.5px;min-height:1.3em;margin-bottom:14px}
.fb-stats{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:10px}@media(max-width:560px){.fb-stats{grid-template-columns:repeat(2,1fr)}}
.fb-stats>div{border-radius:14px;padding:8px 12px;background:color-mix(in srgb,var(--bg) 42%,transparent);border:1px solid rgba(255,255,255,.1)}.fb-stats b{display:block;font-size:21px;font-variant-numeric:tabular-nums;letter-spacing:-.02em}.fb-stats span{font-size:11.5px;color:var(--dim)}
.fb-spark{display:flex;align-items:flex-end;gap:2px;height:56px;margin-top:16px}.fb-spark i{display:block;flex:1;min-height:3px;border-radius:3px 3px 0 0;background:linear-gradient(to top,#00a86b,#7ee8b5);opacity:.9;transition:height .5s cubic-bezier(.2,.8,.2,1)}
.fb-ringbox{position:relative;width:min(100%,200px);aspect-ratio:1;margin:0 auto 14px}.fb-ringbox svg{width:100%;height:100%;transform:rotate(-90deg)}.fb-ringbox circle{fill:none;stroke-width:13}
.fb-ringc{position:absolute;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center;pointer-events:none}.fb-ringc b{font-size:24px;letter-spacing:-.03em;font-variant-numeric:tabular-nums}
.fb-leg{display:grid;gap:8px}.fb-leg div{display:flex;align-items:center;gap:9px;font-size:13.5px}.fb-leg i{width:11px;height:11px;border-radius:4px;flex:none}.fb-leg b{margin-left:auto;font-variant-numeric:tabular-nums}.fb-leg span{color:var(--dim);min-width:36px;text-align:right;font-size:12.5px}
.fb-state{display:flex;align-items:center;gap:12px;margin-bottom:10px}.fb-dot{width:16px;height:16px;border-radius:50%;background:var(--dim);flex:none}.fb-state.on .fb-dot{background:#3ecf8e;animation:fbring 1.6s infinite}
@keyframes fbring{0%{box-shadow:0 0 0 0 rgba(62,207,142,.7)}100%{box-shadow:0 0 0 14px rgba(62,207,142,0)}}
.fb-state b{font-size:26px;letter-spacing:-.02em}.fb-sub{color:var(--dim);font-size:13.5px;line-height:1.5;margin:0 0 12px}.fb-sub a{color:var(--link)}
.fb-src{display:grid;gap:8px}.fb-src div{display:flex;align-items:center;gap:9px;font-size:13.5px;padding:7px 10px;border-radius:12px;background:color-mix(in srgb,var(--bg) 38%,transparent)}.fb-src i{width:8px;height:8px;border-radius:50%;background:var(--dim)}.fb-src .on i{background:#3ecf8e}.fb-src em{margin-left:auto;font-style:normal;color:var(--dim);font-size:12.5px}
.fb-d{display:flex;flex-direction:column;min-height:300px}
.fb-spot{flex:1;display:flex;flex-direction:column;gap:12px;animation:fbfade .55s ease both;cursor:pointer}
@keyframes fbfade{from{opacity:0;transform:translateY(8px)}to{opacity:1;transform:none}}
.fb-spot h3{margin:0;font-size:clamp(22px,2.7vw,32px);line-height:1.18;letter-spacing:-.02em;overflow-wrap:anywhere;display:-webkit-box;-webkit-line-clamp:4;-webkit-box-orient:vertical;overflow:hidden}
.fb-spot p{margin:0;color:var(--dim);font-size:15.5px;line-height:1.55;display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;overflow:hidden;overflow-wrap:anywhere}
.fb-quote{padding:12px 16px;border-radius:18px;background:color-mix(in srgb,var(--bg) 45%,transparent);border-left:4px solid var(--c);font-size:14.5px;line-height:1.5;color:var(--dim);overflow-wrap:anywhere;display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;overflow:hidden}.fb-quote b{color:var(--ink)}
.fb-dots{display:flex;gap:7px;margin-top:auto;padding-top:6px}.fb-dots button{width:26px;height:6px;border-radius:9px;border:0;background:var(--line);cursor:pointer;padding:0}.fb-dots button.on{background:var(--acc)}
.fb-meta{display:flex;gap:8px;align-items:center;flex-wrap:wrap;color:var(--dim);font-size:12.5px}.fb-meta b{color:var(--ink);font-size:13.5px}
.fb-tag{display:inline-block;border-radius:99px;padding:2px 10px;font-size:11px;font-weight:800;letter-spacing:.03em;color:#fff}
.fb-pills{display:flex;gap:8px;flex-wrap:wrap;align-items:center}.fb-pill{border:1px solid var(--line);border-radius:99px;padding:2px 11px;font-size:12.5px;color:var(--dim);font-variant-numeric:tabular-nums}.fb-pill.hot{border-color:#ff9f43;color:#ff9f43}
.fb-tree{position:relative;width:100%;aspect-ratio:1.6;border-radius:18px;overflow:hidden}
.fb-tree button{position:absolute;border:2px solid var(--card);border-radius:12px;color:#fff;font:inherit;cursor:pointer;display:flex;flex-direction:column;align-items:flex-start;justify-content:flex-end;padding:7px 9px;text-align:left;overflow:hidden;transition:filter .15s,transform .15s;line-height:1.1}
.fb-tree button:hover{filter:brightness(1.22);z-index:2}.fb-tree button b{font-weight:800;overflow:hidden;text-overflow:ellipsis;max-width:100%;white-space:nowrap}.fb-tree button span{opacity:.8;font-size:11.5px;font-variant-numeric:tabular-nums}.fb-tree button.on{outline:3px solid #fff;z-index:3}
.fb-lanes{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:16px;align-items:start}
@media(max-width:1180px){.fb-lanes{grid-template-columns:repeat(2,minmax(0,1fr))}}
@media(max-width:700px){.fb-lanes{display:flex;overflow-x:auto;scroll-snap-type:x mandatory;gap:12px;padding-bottom:6px}.fb-lane{flex:0 0 88%;scroll-snap-align:start}}
.fb-lanes.one{grid-template-columns:1fr}.fb-lanes.one .fb-list{grid-template-columns:repeat(auto-fill,minmax(300px,1fr))}
.fb-lane{border-radius:24px;padding:14px;border:1px solid rgba(255,255,255,.12);border-top:4px solid var(--c);background:color-mix(in srgb,var(--card) 58%,transparent)}
.fb-lh{display:flex;align-items:center;gap:9px;margin:2px 4px 12px}.fb-lh i{width:11px;height:11px;border-radius:50%;background:var(--c);box-shadow:0 0 12px var(--c)}.fb-lh b{font-size:16px}.fb-lh em{margin-left:auto;font-style:normal;color:var(--dim);font-size:12.5px;font-variant-numeric:tabular-nums}
.fb-list{display:grid;gap:10px}
.fb-card{--c:var(--acc);position:relative;padding:12px 13px;border-radius:16px;border:1px solid rgba(255,255,255,.1);background:color-mix(in srgb,var(--bg) 42%,transparent);cursor:pointer;transition:transform .16s,border-color .16s,box-shadow .16s}
.fb-card:hover{transform:translateY(-2px);border-color:color-mix(in srgb,var(--c) 65%,transparent);box-shadow:0 14px 30px -22px rgba(0,0,0,.9)}
.fb-card.new{animation:fbin .75s cubic-bezier(.2,.8,.2,1) both}
@keyframes fbin{0%{opacity:0;transform:translateY(-16px) scale(.97)}45%{box-shadow:0 0 0 5px color-mix(in srgb,var(--c) 40%,transparent)}100%{opacity:1;transform:none;box-shadow:none}}
.fb-ch{display:flex;align-items:center;gap:9px;margin-bottom:6px;min-width:0}.fb-ch>div{min-width:0;flex:1}.fb-ch b{display:block;font-size:12.5px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.fb-ch small{display:block;font-size:11.5px;letter-spacing:0;text-transform:none;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.fb-ch .fb-ago{flex:none;font-size:11.5px;color:var(--dim)}
.fb-av{flex:none;position:relative;width:30px;height:30px;border-radius:10px;display:flex;align-items:center;justify-content:center;font-weight:800;color:#fff;overflow:hidden;font-size:13px}.fb-av img{position:absolute;inset:0;width:100%;height:100%;object-fit:contain;background:#fff;border-radius:10px}
.fb-card h5{margin:0 0 7px;font-size:14.5px;line-height:1.35;font-weight:650;display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;overflow:hidden;overflow-wrap:anywhere}
.fb-card .fb-pills{gap:6px}.fb-card .fb-pill{padding:1px 9px;font-size:11.5px}
.fb-none{color:var(--dim);padding:26px 8px;text-align:center;font-size:14px}
.fb-two{display:grid;grid-template-columns:minmax(0,1.4fr) minmax(0,1fr);gap:16px}@media(max-width:1000px){.fb-two{grid-template-columns:1fr}}
.fb-cloud{display:flex;flex-wrap:wrap;gap:6px 14px;align-items:baseline;line-height:1.15}.fb-cloud button{border:0;background:none;font:inherit;font-weight:700;cursor:pointer;padding:2px 0;transition:transform .15s}.fb-cloud button:hover{transform:scale(1.08);text-decoration:underline}
.fb-sites{list-style:none;margin:0;padding:0;display:grid;gap:6px}.fb-sites li{position:relative;display:flex;align-items:center;gap:10px;padding:6px 10px;border-radius:12px;overflow:hidden;font-size:13.5px}.fb-sites li i.bar{position:absolute;left:0;top:0;bottom:0;background:color-mix(in srgb,var(--acc) 20%,transparent);transition:width .6s;z-index:0}.fb-sites li>*:not(.bar){position:relative;z-index:1}.fb-sites li span{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.fb-sites li em{margin-left:auto;font-style:normal;color:var(--dim);font-variant-numeric:tabular-nums}
.fb-veil{position:fixed;inset:0;background:rgba(0,0,0,.5);backdrop-filter:blur(3px);opacity:0;pointer-events:none;transition:opacity .25s;z-index:60}.fb-veil.on{opacity:1;pointer-events:auto}
.fb-drawer{position:fixed;top:0;right:0;bottom:0;width:min(540px,100vw);transform:translateX(105%);transition:transform .32s cubic-bezier(.2,.8,.2,1);z-index:61;overflow:auto;padding:26px 26px 40px;background:color-mix(in srgb,var(--bg2) 94%,transparent);border-left:1px solid rgba(255,255,255,.16);box-shadow:-30px 0 80px -30px rgba(0,0,0,.9);display:flex;flex-direction:column;gap:14px}.fb-drawer.on{transform:none}
.fb-drawer h2{margin:0;font-size:25px;line-height:1.25;letter-spacing:-.015em;overflow-wrap:anywhere}.fb-drawer p{margin:0;color:var(--dim);font-size:15.5px;line-height:1.6;overflow-wrap:anywhere}
.fb-x{align-self:flex-end;border:1px solid var(--line);background:none;color:var(--ink);width:38px;height:38px;border-radius:50%;font-size:18px;cursor:pointer}.fb-x:hover{border-color:var(--acc)}
.fb-go{display:inline-block;background:var(--acc);color:var(--acc-ink);font-weight:800;text-decoration:none;padding:11px 20px;border-radius:99px;margin-right:8px}.fb-more{border:1px solid var(--line);background:none;color:var(--ink);border-radius:99px;padding:10px 18px;font:inherit;cursor:pointer}.fb-more:hover{border-color:var(--acc)}
</style>`

const forumTabBody = `<div class=fb>
<div class=fb-bar>
<div class=fb-chips id=fbChips></div>
<input id=fbFind type=search placeholder="Search the stream   ( press / )" autocomplete=off>
<button type=button class=fb-btn id=fbPause>⏸ Pause</button>
</div>
<div class=fb-ticker><span class=fb-tlabel><i></i>LIVE</span><div class=fb-track id=fbTrack></div></div>
<div class=fb-bento>
<section class="fb-t fb-a">
<small>Forum posts saved</small>
<b class=fb-num id=fbTot>0</b>
<em id=fbSince>New posts appear below as they are saved</em>
<div class=fb-stats>
<div><b id=fbComm>0</b><span>communities</span></div>
<div><b id=fbInst>0</b><span>forum sites</span></div>
<div><b id=fbProg>0</b><span>programming</span></div>
<div><b id=fbRate>0</b><span>saved per minute</span></div>
</div>
<div class=fb-spark id=fbSpark></div>
</section>
<section class="fb-t fb-b">
<h4>Share of each forum kind</h4>
<div class=fb-ringbox><svg id=fbRing viewBox="0 0 120 120"></svg><div class=fb-ringc><b id=fbRingN>0</b><small>posts</small></div></div>
<div class=fb-leg id=fbLeg></div>
</section>
<section class="fb-t fb-c">
<h4>The crawl</h4>
<div id=fbCrawl></div>
</section>
<section class="fb-t fb-d">
<h4>Hot right now</h4>
<div class=fb-spot id=fbSpot></div>
<div class=fb-dots id=fbDots></div>
</section>
<section class="fb-t fb-e">
<h4>Programming languages <span style="margin-left:auto;text-transform:none;letter-spacing:0;font-weight:500">click one to filter</span></h4>
<div class=fb-tree id=fbTree></div>
</section>
</div>
<div class=fb-lanes id=fbLanes></div>
<div class=fb-two>
<section class=fb-t><h4>Biggest communities <span style="margin-left:auto;text-transform:none;letter-spacing:0;font-weight:500">click to search</span></h4><div class=fb-cloud id=fbCloud></div></section>
<section class=fb-t><h4>Biggest forum sites</h4><ul class=fb-sites id=fbSites></ul></section>
</div>
</div>
<div class=fb-veil id=fbVeil></div>
<aside class=fb-drawer id=fbDrawer></aside>`

const forumTabJS = `<script>
(function(){
const $=id=>document.getElementById(id);
const fmt=x=>Math.round(x||0).toLocaleString('en-US');
const mk=(tag,cls,text)=>{const e=document.createElement(tag);if(cls)e.className=cls;if(text!==undefined)e.textContent=text;return e};
const hue=s=>{let h=0;for(const c of s||'')h=(h*31+c.charCodeAt(0))>>>0;return h%360};
const ago=iso=>{const t=Date.parse(iso);if(!t)return'';const s=Math.max(1,Math.round((Date.now()-t)/1000));return s<60?s+' s':s<3600?Math.round(s/60)+' min':s<86400?Math.round(s/3600)+' h':Math.round(s/86400)+' d'};
const KEYS=['lemmy','discourse','flowforum','prog'];
const LANE={lemmy:['Lemmy','#00a86b'],discourse:['Discourse','#e0a100'],flowforum:['Flowforum','#7b5cff'],prog:['Programming','#2d7ff9']};
const kindOf=p=>p==='discourse'||p==='flowforum'?p:'lemmy';
const inLane=(k,p)=>k==='prog'?!!p.l:kindOf(p.p)===k;
let ws=null,last=null,paused=false,filter='all',lang='',base=null,seen=null,spot=0,spotKey='',tickKey='',treeKey='',cloudKey='',sitesKey='';
let spotItems=[],prevTotal=null;const samples=[],spark=[],nodes={lemmy:new Map(),discourse:new Map(),flowforum:new Map(),prog:new Map()};

function av(host){const a=mk('span','fb-av',(host||'?')[0].toUpperCase());a.style.background='hsl('+hue(host)+' 55% 42%)';const i=document.createElement('img');i.src='/flowcdn/fav/'+encodeURIComponent((host||'').replace(/^www\./,''))+'.jxl';i.alt='';i.loading='lazy';i.onerror=()=>i.remove();a.append(i);return a}
function tag(t,bg){const e=mk('span','fb-tag',t);e.style.background=bg;return e}
function pills(p){const w=mk('div','fb-pills');w.append(mk('span','fb-pill'+((p.s||0)>=100?' hot':''),'▲ '+fmt(p.s)));if(p.m)w.append(mk('span','fb-pill'+(p.m>=50?' hot':''),'💬 '+fmt(p.m)));return w}
function match(p){const q=$('fbFind').value.trim().toLowerCase();if(lang&&!(p.l&&p.g===lang))return false;return !q||(p.t+' '+p.c+' '+p.i+' '+(p.b||'')).toLowerCase().includes(q)}

// a treemap: the list is cut in two halves of about the same size, along the longer side, again and again
function tm(items,x,y,w,h,out){
  if(items.length===1){out.push({it:items[0],x:x,y:y,w:w,h:h});return}
  const tot=items.reduce((s,i)=>s+i.v,0);let acc=0,k=0;
  while(k<items.length-1&&(k===0||acc+items[k].v/2<tot/2)){acc+=items[k].v;k++}
  const a=items.slice(0,k),b=items.slice(k),f=acc/tot;
  if(w>=h){const wa=w*f;tm(a,x,y,wa,h,out);tm(b,x+wa,y,w-wa,h,out)}else{const ha=h*f;tm(a,x,y,w,ha,out);tm(b,x,y+ha,w,h-ha,out)}
}

// the drawer
function drawer(p){
  const dr=$('fbDrawer'),k=kindOf(p.p),col=LANE[k][1];dr.textContent='';
  const x=mk('button','fb-x','✕');x.type='button';x.onclick=closeDrawer;
  const meta=mk('div','fb-meta');meta.append(tag(LANE[k][0],col));if(p.l)meta.append(tag(p.g||'Programming','#2d7ff9'));meta.append(mk('b',null,p.c),mk('span',null,p.i),mk('span',null,ago(p.a)?ago(p.a)+' ago':(p.d||'')));
  dr.append(x,meta,mk('h2',null,p.t));
  if(p.b)dr.append(mk('p',null,p.b));
  if(p.tc){const q=mk('div','fb-quote');q.style.setProperty('--c',col);q.style.webkitLineClamp='unset';q.style.display='block';q.append(mk('b',null,(p.tc.a||'someone')+(p.tc.s?' (▲ '+fmt(p.tc.s)+')':'')+': '),document.createTextNode(p.tc.t));dr.append(q)}
  dr.append(pills(p));
  const row=mk('div'),go=mk('a','fb-go','Open the post ↗');go.href=p.u;go.target='_blank';go.rel='noopener noreferrer';
  const more=mk('button','fb-more','More from '+p.c);more.type='button';more.onclick=()=>{$('fbFind').value=p.c;closeDrawer();if(last)draw(last)};
  row.append(go,more);dr.append(row);
  dr.classList.add('on');$('fbVeil').classList.add('on');
}
function closeDrawer(){$('fbDrawer').classList.remove('on');$('fbVeil').classList.remove('on')}

// one card of a lane
function card(k,p){
  const col=LANE[k][1],c=mk('div','fb-card');c.style.setProperty('--c',col);if(seen&&!seen.has(p.u))c.classList.add('new');
  const ch=mk('div','fb-ch'),box=mk('div');box.append(mk('b',null,p.c),mk('small',null,p.i));
  const ag=mk('span','fb-ago',ago(p.a)||p.d||'');ag.dataset.a=p.a||'';
  ch.append(av(p.i),box,ag);c.append(ch,mk('h5',null,p.t));
  if(p.b)c.append(mk('div','fb-sn',p.b));
  if(p.tc){const q=mk('div','fb-qc');q.append(mk('b',null,(p.tc.a||'someone')+': '),document.createTextNode(p.tc.t));c.append(q)}
  const pl=pills(p);if(p.l&&k!=='prog')pl.prepend(tag(p.g||'Programming','#2d7ff9'));if(c.classList.contains('new'))pl.prepend(tag('NEW','#ff5d73'));c.append(pl);
  c.onclick=()=>drawer(p);return c;
}

function drawLanes(d){
  const box=$('fbLanes'),one=filter!=='all';box.classList.toggle('one',one);
  for(const k of KEYS){
    const lane=$('fbL_'+k);lane.hidden=one&&filter!==k;if(lane.hidden)continue;
    const list=lane.querySelector('.fb-list'),mp=nodes[k];
    const rows=(d.newest||[]).filter(p=>inLane(k,p)&&match(p)).slice(0,one?40:7),now=new Set();
    for(const e of list.querySelectorAll('.fb-none'))e.remove();
    for(const p of rows){now.add(p.u);let n=mp.get(p.u);if(!n){n=card(k,p);mp.set(p.u,n)}list.append(n)}
    for(const [u,n] of mp)if(!now.has(u)){n.remove();mp.delete(u)}
    if(!rows.length)list.append(mk('div','fb-none',k==='prog'&&lang?'No recent '+lang+' posts yet.':'Nothing here yet.'));
    const pl=d.platforms||{};lane.querySelector('em').textContent=fmt(k==='prog'?(d.languages||0):(pl[k]||0))+' posts';
  }
}

function drawTicker(d){
  const items=(d.newest||[]).filter(match).slice(0,26);const key=items.length?items[0].u+items.length:'';if(key===tickKey)return;tickKey=key;
  const tr=$('fbTrack');tr.textContent='';
  for(let r=0;r<2;r++)for(const p of items){const k=kindOf(p.p),a=mk('a');const dot=mk('i');dot.style.background=LANE[k][1];a.append(dot,document.createTextNode(p.t+' '),mk('small',null,p.c));a.onclick=()=>drawer(p);tr.append(a)}
}

function renderSpot(){
  const box=$('fbSpot'),dots=$('fbDots');
  if(!spotItems.length){box.textContent='';box.append(mk('div','fb-none','Waiting for posts…'));dots.textContent='';spotKey='';return}
  if(spot>=spotItems.length)spot=0;const p=spotItems[spot];
  dots.textContent='';spotItems.forEach((_,i)=>{const b=mk('button',i===spot?'on':null);b.type='button';b.onclick=()=>{spot=i;spotKey='';renderSpot()};dots.append(b)});
  if(spotKey===p.u)return;spotKey=p.u;
  const k=kindOf(p.p),col=LANE[k][1];box.textContent='';box.style.animation='none';void box.offsetWidth;box.style.animation='';
  const meta=mk('div','fb-meta');meta.append(tag(LANE[k][0],col));if(p.l)meta.append(tag(p.g||'Programming','#2d7ff9'));meta.append(mk('b',null,p.c),mk('span',null,p.i),mk('span',null,ago(p.a)?ago(p.a)+' ago':''));
  box.append(meta,mk('h3',null,p.t));
  if(p.b)box.append(mk('p',null,p.b));
  if(p.tc){const q=mk('div','fb-quote');q.style.setProperty('--c',col);q.append(mk('b',null,(p.tc.a||'someone')+': '),document.createTextNode(p.tc.t));box.append(q)}
  box.append(pills(p));box.onclick=()=>drawer(p);
}
function drawSpot(d){
  const c=(d.newest||[]).filter(match).slice().sort((a,b)=>((b.m||0)*2+(b.s||0))-((a.m||0)*2+(a.s||0))).slice(0,6);
  spotItems=c;renderSpot();
}

function drawTree(d){
  const L=Object.entries(d.langs||{}).map(([l,n])=>({l:l,v:Math.max(1,n)})).sort((a,b)=>b.v-a.v).slice(0,30);
  const key=JSON.stringify(L)+'|'+lang;if(key===treeKey)return;treeKey=key;
  const box=$('fbTree');box.textContent='';
  if(!L.length){box.append(mk('div','fb-none','No programming posts yet'));return}
  const out=[];tm(L,0,0,160,100,out);
  for(const r of out){
    const b=mk('button',lang===r.it.l?'on':null);b.type='button';b.title=r.it.l+': '+fmt(r.it.v)+' posts';
    b.style.cssText='left:'+(r.x/1.6)+'%;top:'+r.y+'%;width:'+(r.w/1.6)+'%;height:'+r.h+'%;background:hsl('+hue(r.it.l)+' 52% 36%);font-size:'+Math.max(10,Math.min(22,Math.sqrt(r.w*r.h)/3.3))+'px';
    const area=r.w*r.h; // a small block shows only its colour (the name is in the tooltip); a bigger one its name; a big one the number too
    if(area>=300)b.append(mk('b',null,r.it.l));
    if(area>=700)b.append(mk('span',null,fmt(r.it.v)));
    b.onclick=()=>{lang=lang===r.it.l?'':r.it.l;if(lang)filter='prog';if(last)draw(last)};
    box.append(b);
  }
}

function drawCloud(d){
  const T=(d.top||[]).slice(0,25);const key=JSON.stringify(T);if(key===cloudKey)return;cloudKey=key;
  const box=$('fbCloud');box.textContent='';if(!T.length){box.append(mk('div','fb-none','No communities yet'));return}
  const mx=Math.max(...T.map(t=>t.n));
  for(const t of T){const b=mk('button',null,t.c);b.type='button';b.title=fmt(t.n)+' posts';b.style.fontSize=(12.5+17*Math.sqrt(t.n/mx))+'px';b.style.color='hsl('+hue(t.c)+' 70% 62%)';b.onclick=()=>{$('fbFind').value=t.c.replace(/^c\//,'');if(last)draw(last);window.scrollTo({top:$('fbLanes').getBoundingClientRect().top+scrollY-90,behavior:'smooth'})};box.append(b)}
}
function drawSites(d){
  const S=(d.sites||[]).slice(0,10);const key=JSON.stringify(S);if(key===sitesKey)return;sitesKey=key;
  const ul=$('fbSites');ul.textContent='';if(!S.length){ul.append(mk('li','fb-none','No forum sites yet'));return}
  const mx=Math.max(...S.map(s=>s.n));
  for(const s of S){const li=mk('li'),bar=mk('i','bar');bar.style.width=(100*s.n/mx)+'%';li.append(bar,av(s.h),mk('span',null,s.h),mk('em',null,fmt(s.n)));ul.append(li)}
}

function drawCrawl(d){
  const src=d.sources||{},gh=src.gh||{},c=d.crawl||{},box=$('fbCrawl');box.textContent='';
  const on=gh.status==='in_progress'||gh.status==='queued'||gh.status==='waiting';
  const st=mk('div','fb-state'+(on?' on':''));st.append(mk('span','fb-dot'),mk('b',null,on?'Crawling now':(gh.status==='completed'?'Waiting for the next run':'No run yet')));box.append(st);
  let sub='GitHub did not answer';
  if(on)sub='Run started '+ago(gh.started)+' ago. Lemmy, Discourse and programming forums are read at the same time, on GitHub Actions.';
  else if(gh.status==='completed')sub='The last run '+(gh.conclusion==='success'?'finished':'ended ('+(gh.conclusion||'?')+')')+' '+ago(gh.updated)+' ago. The next one starts by itself.';
  else if(gh.status==='none')sub='No crawl run on GitHub yet.';
  const p=mk('p','fb-sub',sub+' ');if(gh.url){const a=mk('a',null,'See the run ↗');a.href=gh.url;a.target='_blank';a.rel='noopener noreferrer';p.append(a)}box.append(p);
  const list=mk('div','fb-src'),row=(name,on2,state)=>{const r=mk('div',on2?'on':null);r.append(mk('i'),mk('span',null,name),mk('em',null,state));list.append(r)};
  if(src.lemmy)row('Lemmy crawl on this server',true,'reading '+(c.site||'…'));
  if(src.discourse)row('Discourse crawl on this server',true,'running');
  row('Flowforum reader',!!src.flowforum,src.flowforum?(src.flowforum_last?'last change '+ago(new Date(src.flowforum_last*1000).toISOString())+' ago':'connected'):'offline');
  box.append(list);
}

function rate(total){
  const now=Date.now();samples.push([now,total]);while(samples.length>1&&now-samples[0][0]>60000)samples.shift();
  const a=samples[0],dt=(now-a[0])/60000;let r=dt>0.05?(total-a[1])/dt:0;if(r<0)r=0;$('fbRate').textContent=fmt(r);
  spark.push(Math.max(0,prevTotal===null?0:total-prevTotal));prevTotal=total;while(spark.length>60)spark.shift();
  const mx=Math.max(1,...spark),sp=$('fbSpark');sp.textContent='';for(const v of spark){const i=mk('i');i.style.height=Math.max(6,Math.round(100*v/mx))+'%';sp.append(i)}
}

function drawRing(d){
  const pl=d.platforms||{},tot=['lemmy','discourse','flowforum'].reduce((s,k)=>s+(pl[k]||0),0)||1,svg=$('fbRing'),NS='http://www.w3.org/2000/svg',R=46,C=2*Math.PI*R;let off=0;svg.textContent='';
  const ring=(col,len,o)=>{const c=document.createElementNS(NS,'circle');c.setAttribute('cx',60);c.setAttribute('cy',60);c.setAttribute('r',R);c.setAttribute('stroke',col);c.setAttribute('stroke-dasharray',len+' '+(C-len));c.setAttribute('stroke-dashoffset',-o);svg.append(c)};
  ring('rgba(128,128,128,.2)',C,0);
  const leg=$('fbLeg');leg.textContent='';
  for(const k of ['lemmy','discourse','flowforum']){const n=pl[k]||0,len=C*n/tot;if(n)ring(LANE[k][1],Math.max(0,len-1.5),off);off+=len;
    const r=mk('div'),sw=mk('i');sw.style.background=LANE[k][1];r.append(sw,mk('span',null,LANE[k][0]),mk('b',null,fmt(n)),mk('span',null,Math.round(100*n/tot)+'%'));r.children[1].style.cssText='color:var(--ink);text-align:left;min-width:0;font-size:13.5px';leg.append(r)}
}

function drawChips(d){
  const pl=d.platforms||{},box=$('fbChips');box.textContent='';
  const chip=(key,label,n,col)=>{const b=mk('button','fb-chip'+(filter===key?' on':''));b.type='button';if(col){const i=mk('i');i.style.background=col;b.append(i)}b.append(mk('span',null,label),mk('em',null,fmt(n)));b.onclick=()=>{filter=key;if(key!=='prog')lang='';if(last)draw(last)};box.append(b)};
  chip('all','All',d.total,null);
  for(const k of ['lemmy','discourse','flowforum'])chip(k,LANE[k][0],pl[k]||0,LANE[k][1]);
  chip('prog','Programming',d.languages||0,LANE.prog[1]);
  if(lang){const b=mk('button','fb-chip on');b.type='button';b.append(mk('span',null,lang+'  ✕'));b.onclick=()=>{lang='';if(last)draw(last)};box.append(b)}
}

// the big number rolls up to its new value
let rolled=0,rollTok=0;
function roll(el,to){
  const from=rolled||to,tok=++rollTok;rolled=to;
  if(from===to){el.textContent=fmt(to);return}
  const t0=performance.now(),step=t=>{if(tok!==rollTok)return;const k=Math.min(1,(t-t0)/700),e=1-Math.pow(1-k,3);el.textContent=fmt(from+(to-from)*e);if(k<1)requestAnimationFrame(step)};
  requestAnimationFrame(step);
}
let flashTotal=null;
function draw(d){
  last=d;
  roll($('fbTot'),d.total);$('fbRingN').textContent=fmt(d.total);
  if(flashTotal!==null&&d.total>flashTotal){const l=document.querySelector('.fb-tlabel');l.classList.add('flash');setTimeout(()=>l.classList.remove('flash'),600)}
  flashTotal=d.total;
  $('fbComm').textContent=d.loading?'…':fmt(d.communities);$('fbInst').textContent=d.loading?'…':fmt(d.instances);$('fbProg').textContent=d.loading?'…':fmt(d.languages||0);
  if(!d.loading)rate(d.total);
  if(base===null)base=d.total;const grown=d.total-base;$('fbSince').textContent=grown>0?'+'+fmt(grown)+' since this tab was opened':'New posts appear below as they are saved';
  drawChips(d);drawRing(d);drawCrawl(d);
  if(d.loading){$('fbSpot').textContent='';$('fbSpot').append(mk('div','fb-none','The forum index is loading. The board fills in a moment.'));return}
  drawTree(d);drawCloud(d);drawSites(d);
  if(!paused){drawTicker(d);drawSpot(d);drawLanes(d);seen=new Set((d.newest||[]).map(p=>p.u))}
}

function open(){
  if(ws)return;
  ws=new WebSocket((location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/ws/forums');
  ws.onmessage=e=>{try{draw(JSON.parse(e.data))}catch(x){}};
  ws.onclose=()=>{ws=null;if(!$('lxfo').hidden)setTimeout(open,1000)};
}
function close(){if(ws){ws.onclose=null;ws.close();ws=null}}
window.FZ={open:open,close:close};

// the lanes are made once
for(const k of KEYS){const l=mk('section','fb-lane');l.id='fbL_'+k;l.style.setProperty('--c',LANE[k][1]);const h=mk('div','fb-lh');h.append(mk('i'),mk('b',null,LANE[k][0]),mk('em',null,''));l.append(h,mk('div','fb-list'));$('fbLanes').append(l)}
$('fbFind').oninput=()=>{if(last){tickKey='';draw(last)}};
$('fbFind').onkeydown=e=>{if(e.key==='Escape'){e.target.value='';e.target.blur();if(last)draw(last)}};
$('fbPause').onclick=function(){paused=!paused;this.textContent=paused?'▶ Resume':'⏸ Pause';if(!paused&&last)draw(last)};
$('fbVeil').onclick=closeDrawer;
document.addEventListener('keydown',e=>{
  if(e.key==='Escape')closeDrawer();
  if(e.key==='/'&&!$('lxfo').hidden&&!/^(INPUT|TEXTAREA)$/.test((e.target||{}).tagName||'')){e.preventDefault();$('fbFind').focus()}
});
// the hot post turns every 9 seconds; the "x min" texts keep counting
setInterval(()=>{if(paused||spotItems.length<2||$('lxfo').hidden)return;spot=(spot+1)%spotItems.length;spotKey='';renderSpot()},9000);
setInterval(()=>{for(const e of document.querySelectorAll('.fb-ago')){if(e.dataset.a){const t=ago(e.dataset.a);if(t)e.textContent=t}}},15000);
})();
</script>`
