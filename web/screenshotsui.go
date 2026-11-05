package main

// /screenshots: a photo-library style app. A sidebar with the search, the library (everything, front pages, single pages, favourites), the time and the
// order; a toolbar with a size slider; a gallery grouped by day with sticky day headings, a day scrubber on the right edge, and endless scrolling;
// a viewer with the picture on one side and its details and actions on the other (zoom, slideshow, copy link, save, search the site).

import (
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"
)

const shotUICSS = `<style>
.wrap:has(.ap){max-width:1640px}
.ap{display:grid;grid-template-columns:268px minmax(0,1fr);gap:26px;align-items:start}
.ap-side{position:sticky;top:84px;max-height:calc(100vh - 100px);overflow:auto;padding:18px 16px;border-radius:26px;border:1px solid var(--line);background:var(--card);scrollbar-width:thin}
.ap-side h2{margin:2px 6px 14px;font-size:21px;letter-spacing:-.02em}
.ap-search{position:relative;margin:0 0 16px}
.ap-search input{width:100%;box-sizing:border-box;border-radius:14px;padding:11px 14px 11px 38px;border:1px solid var(--line);background:color-mix(in srgb,var(--bg) 60%,transparent);color:var(--ink);font:inherit;font-size:15px}
.ap-search input:focus{outline:none;border-color:var(--acc);box-shadow:0 0 0 3px var(--glow)}
.ap-search::before{content:"⌕";position:absolute;left:14px;top:50%;transform:translateY(-52%);color:var(--dim);font-size:18px}
.ap-h{margin:18px 8px 6px;color:var(--dim);font-size:11.5px;letter-spacing:.1em;text-transform:uppercase}
.ap-item{display:flex;align-items:center;gap:10px;padding:9px 12px;border-radius:12px;color:var(--ink);text-decoration:none;font-size:15px;cursor:pointer;border:0;background:none;width:100%;font-family:inherit;text-align:left;transition:background .15s}
.ap-item:hover{background:color-mix(in srgb,var(--line) 55%,transparent)}
.ap-item.on{background:color-mix(in srgb,var(--acc) 22%,transparent);font-weight:700}
.ap-item i{font-style:normal;width:22px;text-align:center;opacity:.85}
.ap-item small{margin-left:auto;color:var(--dim);font-variant-numeric:tabular-nums;font-size:12.5px}
.ap-days{display:flex;align-items:flex-end;gap:3px;height:46px;margin:4px 8px 2px}
.ap-days i{flex:1;border-radius:3px 3px 1px 1px;background:linear-gradient(180deg,var(--acc),color-mix(in srgb,var(--acc) 35%,transparent));min-height:3px}
.ap-days i:last-child{outline:1px solid var(--acc)}
.ap-stat{display:grid;grid-template-columns:1fr 1fr;gap:8px;margin:10px 4px 2px}
.ap-stat div{background:color-mix(in srgb,var(--line) 35%,transparent);border-radius:12px;padding:8px 11px}.ap-stat b{display:block;font-size:19px;font-variant-numeric:tabular-nums}.ap-stat small{color:var(--dim);font-size:11.5px}
.ap-main{min-width:0}
.ap-bar{display:flex;align-items:center;gap:14px;flex-wrap:wrap;justify-content:space-between;margin:0 0 14px}
.ap-bar h1{margin:0;font-size:clamp(26px,3vw,34px);letter-spacing:-.03em}
.ap-bar .cnt{color:var(--dim);font-size:14px;margin-left:10px;font-weight:400}
.ap-tools{display:flex;gap:10px;align-items:center;flex-wrap:wrap}
.ap-tools button{border:1px solid var(--line);background:var(--card);color:var(--ink);font:inherit;font-size:14px;padding:8px 14px;border-radius:12px;cursor:pointer}
.ap-tools button:hover{border-color:var(--acc)}
.ap-zoom{display:flex;align-items:center;gap:8px;color:var(--dim);font-size:13px}.ap-zoom input{width:120px;accent-color:var(--acc)}
.ap-menu{display:none}
.ap-new{display:none;align-items:center;justify-content:center;gap:12px;margin:0 0 14px;padding:11px 18px;border-radius:14px;background:color-mix(in srgb,var(--acc) 18%,var(--card));border:1px solid var(--acc);font-size:15px}
.ap-new.on{display:flex}.ap-new a{color:var(--acc-ink);background:var(--acc);padding:5px 16px;border-radius:99px;text-decoration:none;font-weight:700}
.sg-grid{--min:250px;display:grid;grid-template-columns:repeat(auto-fill,minmax(var(--min),1fr));gap:10px}
.sg-day{grid-column:1/-1;position:sticky;top:70px;z-index:3;align-self:start;justify-self:start;margin:12px 0 2px;padding:6px 16px;border-radius:99px;font-size:14px;font-weight:700;background:color-mix(in srgb,var(--bg) 78%,transparent);border:1px solid var(--line);backdrop-filter:blur(10px)}
.sg-card{position:relative;display:block;aspect-ratio:16/10;border-radius:16px;overflow:hidden;background:linear-gradient(100deg,color-mix(in srgb,var(--line) 70%,transparent) 30%,color-mix(in srgb,var(--line) 25%,transparent) 50%,color-mix(in srgb,var(--line) 70%,transparent) 70%);background-size:300% 100%;animation:sgshim 1.4s linear infinite;color:#fff;text-decoration:none;transition:transform .2s ease,box-shadow .25s;content-visibility:auto;contain-intrinsic-size:250px 156px}
@keyframes sgshim{from{background-position:100% 0}to{background-position:0 0}}
.sg-card.loaded{animation:none}
.sg-card:hover,.sg-card:focus-visible{transform:scale(1.025);z-index:2;box-shadow:0 20px 44px -18px var(--tint,rgba(0,0,0,.8));outline:2px solid var(--tint,var(--acc));outline-offset:0}
.sg-card img.pic{position:absolute;inset:0;width:100%;height:100%;object-fit:cover;object-position:top;opacity:0;transition:opacity .4s ease,transform .6s ease}
.sg-card img.pic.ok{opacity:1}
.sg-card:hover img.pic{transform:scale(1.05)}
.sg-card .ov{position:absolute;left:0;right:0;bottom:0;padding:30px 12px 10px;display:flex;align-items:center;gap:8px;background:linear-gradient(180deg,transparent,rgba(0,0,0,.78));opacity:0;transform:translateY(6px);transition:opacity .2s,transform .2s;min-width:0}
.sg-card:hover .ov,.sg-card:focus-visible .ov{opacity:1;transform:none}
.sg-card .ov b{font-size:14px;font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0;text-shadow:0 1px 6px #000}
.sg-card .ov small{margin-left:auto;font-size:12px;opacity:.8;white-space:nowrap;flex:none}
@media(hover:none){.sg-card .ov{opacity:1;transform:none}}
.sg-card .new{position:absolute;left:9px;top:9px;z-index:1;background:var(--acc);color:var(--acc-ink);font-size:10.5px;font-weight:800;letter-spacing:.06em;border-radius:99px;padding:3px 9px;text-transform:uppercase}
.sg-heart{position:absolute;right:9px;top:9px;z-index:2;width:34px;height:34px;border-radius:50%;border:1px solid rgba(255,255,255,.3);background:rgba(0,0,0,.5);color:#fff;font-size:17px;line-height:1;cursor:pointer;backdrop-filter:blur(6px);opacity:0;transform:scale(.85);transition:opacity .2s,transform .2s,background .2s}
.sg-card:hover .sg-heart,.sg-card:focus-within .sg-heart,.sg-heart.on{opacity:1;transform:none}
.sg-heart.on{background:#e5365b;border-color:#e5365b}
@media(hover:none){.sg-heart{opacity:1;transform:none}}
.sg-fav{position:relative;flex:none;width:20px;height:20px;border-radius:6px;display:inline-flex;align-items:center;justify-content:center;font-size:11px;font-weight:700;color:#fff;overflow:hidden}
.sg-fav img{position:absolute;inset:0;width:100%;height:100%;object-fit:contain;background:#fff;border-radius:6px}
.sg-more{display:flex;justify-content:center;margin:26px 0 4px}.sg-more[hidden]{display:none}.sg-more button{border:1px solid var(--line);background:var(--card);color:var(--ink);font:inherit;padding:10px 22px;border-radius:99px;cursor:pointer}
.sg-pg{display:flex;gap:12px;align-items:center;justify-content:center;margin:30px 0 6px;color:var(--dim);flex-wrap:wrap}.sg-js .sg-pg{display:none}
.sg-empty{color:var(--dim);padding:40px 6px;text-align:center}
.sg-note{color:var(--dim);font-size:13.5px;margin:24px 0 0}
.ap-scrub{position:fixed;right:10px;top:50%;transform:translateY(-50%);z-index:20;display:flex;flex-direction:column;gap:3px;max-height:70vh;overflow:hidden;padding:8px 6px;border-radius:99px;background:color-mix(in srgb,var(--card) 80%,transparent);border:1px solid var(--line);backdrop-filter:blur(10px)}
.ap-scrub button{position:relative;border:0;background:none;padding:5px 7px;cursor:pointer}
.ap-scrub button i{display:block;width:8px;height:8px;border-radius:50%;background:var(--dim);opacity:.55;transition:all .2s}
.ap-scrub button.on i{background:var(--acc);opacity:1;transform:scale(1.5)}
.ap-scrub button span{position:absolute;right:26px;top:50%;transform:translateY(-50%);white-space:nowrap;background:var(--card);border:1px solid var(--line);border-radius:8px;padding:3px 10px;font-size:12.5px;color:var(--ink);opacity:0;pointer-events:none;transition:opacity .15s}
.ap-scrub button:hover span{opacity:1}
.sg-view{position:fixed;inset:0;z-index:200;background:rgba(7,9,13,.96);display:none;grid-template-columns:minmax(0,1fr) 330px;transition:background .5s}
.sg-view.on{display:grid}
.sg-view .pane{position:relative;display:flex;align-items:center;justify-content:center;padding:26px 70px;min-width:0;min-height:0}
.sg-view .stage{max-width:100%;max-height:calc(100vh - 52px);overflow:auto;border-radius:12px;box-shadow:0 30px 80px -20px #000;background:#000}
.sg-view img{display:block;max-width:100%;max-height:calc(100vh - 52px);object-fit:contain;cursor:zoom-in;margin:auto}
.sg-view.zoom .stage{cursor:zoom-out}.sg-view.zoom img{max-width:none;max-height:none;width:min(1700px,210%);cursor:zoom-out}
.sg-view .info{background:var(--card);color:var(--ink);padding:26px 22px;display:flex;flex-direction:column;gap:14px;overflow:auto;border-left:1px solid var(--line)}
.sg-view .info .who{display:flex;align-items:center;gap:11px;min-width:0}.sg-view .info .who b{font-size:19px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.sg-view .info small{color:var(--dim);display:block}
.sg-view .info .addr{word-break:break-all;font-size:13px;color:var(--dim)}
.sg-view .acts{display:grid;gap:8px}
.sg-view .acts a,.sg-view .acts button{display:flex;align-items:center;justify-content:center;gap:8px;padding:11px 14px;border-radius:12px;border:1px solid var(--line);background:color-mix(in srgb,var(--bg) 50%,transparent);color:var(--ink);text-decoration:none;font:inherit;font-size:15px;cursor:pointer}
.sg-view .acts a.go{background:var(--acc);color:var(--acc-ink);border-color:var(--acc);font-weight:700}
.sg-view .acts a:hover,.sg-view .acts button:hover{border-color:var(--acc)}
.sg-view .keys{margin-top:auto;color:var(--dim);font-size:12.5px;line-height:1.8}.sg-view .keys kbd{background:color-mix(in srgb,var(--line) 70%,transparent);border:1px solid var(--line);border-radius:5px;padding:0 6px;font:inherit;font-size:12px}
.sg-view button.r{position:absolute;background:rgba(255,255,255,.1);border:1px solid rgba(255,255,255,.22);color:#fff;border-radius:50%;width:46px;height:46px;font-size:20px;cursor:pointer}
.sg-view button.r:hover{background:var(--acc);color:var(--acc-ink)}
.sg-view .x{top:16px;right:16px;z-index:3}.sg-view .p{left:14px;top:50%}.sg-view .n{right:14px;top:50%}
.sg-view .k{position:absolute;top:22px;left:26px;color:rgba(255,255,255,.6);font-size:13px}
.sg-view .bar{position:absolute;left:0;top:0;height:3px;background:var(--acc);width:0}.sg-view .bar.go{animation:sgbar 4s linear forwards}@keyframes sgbar{to{width:100%}}
.sg-help{position:fixed;inset:0;z-index:300;background:rgba(8,10,14,.8);display:grid;place-items:center;backdrop-filter:blur(4px)}.sg-help[hidden]{display:none}
.sg-help div{background:var(--card);color:var(--ink);border:1px solid var(--line);border-radius:20px;padding:22px 28px;min-width:300px;max-width:92vw;box-shadow:0 30px 80px -20px #000}
.sg-help h3{margin:0 0 12px}.sg-help p{margin:7px 0;display:flex;gap:14px;justify-content:space-between}.sg-help kbd{background:color-mix(in srgb,var(--line) 70%,transparent);border:1px solid var(--line);border-radius:6px;padding:1px 8px;font:inherit;font-size:13px}
@media(max-width:1100px){.ap-scrub{display:none}}
@media(max-width:900px){
  .ap{grid-template-columns:1fr}
  .ap-menu{display:inline-flex}
  .ap-side{position:fixed;left:0;top:0;bottom:0;z-index:150;width:min(310px,88vw);max-height:none;border-radius:0 26px 26px 0;transform:translateX(-105%);transition:transform .25s ease}
  .ap-side.open{transform:none;box-shadow:0 0 0 100vmax rgba(0,0,0,.55)}
  .sg-view{grid-template-columns:1fr;grid-template-rows:minmax(0,1fr) auto}
  .sg-view .pane{padding:60px 10px 10px}.sg-view .info{border-left:0;border-top:1px solid var(--line);max-height:42vh;padding:16px}
  .sg-view .p,.sg-view .n{top:auto;bottom:calc(42vh + 12px)}.sg-view .keys{display:none}
}
@media(prefers-reduced-motion:reduce){*{animation:none!important;transition:none!important}}
</style>`

const shotUIJS = `<div class=sg-view id=sgv aria-hidden=true role=dialog aria-label="Picture viewer"><div class=bar id=sgbar></div><div class=pane><span class=k id=sgk></span><button class="r p" id=sgp aria-label="Previous">‹</button><div class=stage id=sgs><img id=sgi alt=""></div><button class="r n" id=sgn aria-label="Next">›</button></div>
<aside class=info><button class="r x" id=sgx aria-label="Close">✕</button><div class=who><span id=sgfv></span><b id=sgt></b></div><small id=sgu></small><div class=addr id=sga></div>
<div class=acts><a class=go id=sgh href="#">🔎 Search this site</a><a id=sgo href="#" target=_blank rel="noopener noreferrer">↗ Open the page</a><button id=sgrt type=button>⟳ New screenshot</button><a id=sgi2 href="#">ⓘ About this website</a><a id=sgcm href="#">⇆ Compare with another</a><button id=sgf type=button>♡ Save</button><button id=sgl type=button>▶ Slideshow</button><button id=sgz type=button>⤢ Zoom</button><button id=sgc type=button>⧉ Copy link</button></div>
<div class=keys><kbd>←</kbd> <kbd>→</kbd> step · <kbd>Z</kbd> zoom · <kbd>Space</kbd> slideshow · <kbd>S</kbd> save · <kbd>Esc</kbd> close</div></aside></div>
<div class=sg-help id=sghelp hidden><div><h3>Keys</h3><p><span>Search</span><kbd>/</kbd></p><p><span>A random picture</span><kbd>R</kbd></p><p><span>Next, previous</span><kbd>→</kbd> <kbd>←</kbd></p><p><span>Save to favourites</span><kbd>S</kbd></p><p><span>Zoom in or out</span><kbd>Z</kbd></p><p><span>Slideshow</span><kbd>Space</kbd></p><p><span>Close</span><kbd>Esc</kbd></p><p><span>This list</span><kbd>?</kbd></p></div></div>
<script>
(function(){
const app=document.querySelector('.ap'),grid=document.querySelector('.sg-grid'),v=document.getElementById('sgv'),im=document.getElementById('sgi');let i=0,timer=null;
app.classList.add('sg-js');
const cards=()=>[...grid.querySelectorAll('.sg-card')];
// every picture gives its card a glow in its own main colour (the picture squeezed down to one pixel)
function tint(p){try{const c=document.createElement('canvas');c.width=c.height=1;const g=c.getContext('2d');g.drawImage(p,0,0,1,1);const d=g.getImageData(0,0,1,1).data,card=p.closest('.sg-card');if(card)card.style.setProperty('--tint','rgb('+d[0]+','+d[1]+','+d[2]+')')}catch(e){}}
function fade(root){root.querySelectorAll('img.pic:not(.seen)').forEach(p=>{p.classList.add('seen');const ok=()=>{p.classList.add('ok');const c=p.closest('.sg-card');if(c)c.classList.add('loaded');tint(p)};if(p.complete&&p.naturalWidth)ok();else{p.addEventListener('load',ok);p.addEventListener('error',()=>{const c=p.closest('.sg-card');if(c)c.style.animation='none'})}})}
fade(document);
// favourites: kept in this browser. The Favourites item shows only those pictures; the server picks them out of the whole list, so all of them are found
let favs=[];try{favs=JSON.parse(localStorage.getItem('sgfav')||'[]')}catch(e){}
const fb=document.getElementById('sgfavs'),inFav=!!grid.dataset.favs;
function paintFavBtn(){if(!fb)return;fb.classList.toggle('on',inFav);const s=fb.querySelector('small');if(s)s.textContent=favs.length}
function paintHearts(root){root.querySelectorAll('.sg-heart').forEach(b=>{const on=favs.includes(b.dataset.h);b.classList.toggle('on',on);b.textContent=on?'♥':'♡'})}
function viewHeart(){const c=cards()[i],b=document.getElementById('sgf');if(c&&b)b.textContent=favs.includes(c.dataset.h)?'♥ Saved':'♡ Save'}
function toggleFav(h){const k=favs.indexOf(h);if(k>=0)favs.splice(k,1);else favs.unshift(h);try{localStorage.setItem('sgfav',JSON.stringify(favs.slice(0,300)))}catch(e){}paintFavBtn();paintHearts(grid);viewHeart()}
paintFavBtn();paintHearts(grid);
if(fb)fb.onclick=()=>{const u=new URL(location.href);u.searchParams.delete('p');u.hash='';
  if(inFav){u.searchParams.delete('favs');location.href=u.toString()}
  else if(favs.length){u.searchParams.set('favs',favs.slice(0,120).join(','));location.href=u.toString()}
  else{const s=fb.querySelector('small');if(s){s.textContent='click a ♡';setTimeout(paintFavBtn,2200)}}};
// the viewer
function show(k){const cs=cards();if(!cs.length)return;i=(k+cs.length)%cs.length;const c=cs[i];v.classList.remove('zoom');im.src=c.dataset.img;
  document.getElementById('sgt').textContent=c.dataset.title;document.getElementById('sgu').textContent=c.dataset.ago;document.getElementById('sga').textContent=c.dataset.open;
  const f=c.querySelector('.sg-fav');document.getElementById('sgfv').innerHTML=f?f.outerHTML:'';
  document.getElementById('sgh').href=c.dataset.href;document.getElementById('sgo').href=c.dataset.open;
  {const hst=(c.dataset.h||'').replace(/^https?:\/\//,'').replace(/\/.*$/,'').replace(/^www\./,'');const ab=document.getElementById('sgi2'),cp=document.getElementById('sgcm');ab.href='/site/'+encodeURIComponent(hst);cp.href='/compare?a='+encodeURIComponent(hst);ab.style.display=cp.style.display=hst?'':'none';const rb2=document.getElementById('sgrt');if(rb2)rb2.style.display=(hst&&!/\//.test(c.dataset.h||''))?'':'none'}document.getElementById('sgk').textContent=(i+1)+' / '+cs.length;
  v.classList.add('on');v.setAttribute('aria-hidden','false');viewHeart();
  const tn=c.style.getPropertyValue('--tint'); // the viewer takes on the colour of the picture
  v.style.background=tn?'radial-gradient(ellipse at 35% 40%,'+tn.replace('rgb(','rgba(').replace(')',',.45)')+',rgba(7,9,13,.97) 72%)':'';
  try{history.replaceState(null,'','#v='+encodeURIComponent(c.dataset.title))}catch(e){}
  for(const d of [1,-1]){const n=cs[(i+d+cs.length)%cs.length];if(n){const pre=new Image();pre.src=n.dataset.img}}
  if(i>=cs.length-3)more();
}
function hide(){stop();v.classList.remove('on','zoom');v.setAttribute('aria-hidden','true');im.removeAttribute('src');try{history.replaceState(null,'',location.pathname+location.search)}catch(e){}}
grid.addEventListener('click',e=>{const hb=e.target.closest('.sg-heart');if(hb){e.preventDefault();e.stopPropagation();toggleFav(hb.dataset.h);return}const c=e.target.closest('.sg-card');if(!c||e.ctrlKey||e.metaKey||e.shiftKey||e.button)return;e.preventDefault();show(cards().indexOf(c))});
document.getElementById('sgx').onclick=hide;document.getElementById('sgp').onclick=()=>show(i-1);document.getElementById('sgn').onclick=()=>show(i+1);
document.getElementById('sgs').addEventListener('click',()=>v.classList.toggle('zoom'));document.getElementById('sgz').onclick=()=>v.classList.toggle('zoom');
document.getElementById('sgf').onclick=()=>{const c=cards()[i];if(c)toggleFav(c.dataset.h)};
function copy(btn,text,label){(navigator.clipboard?navigator.clipboard.writeText(text):Promise.reject()).then(()=>{btn.textContent='✓ Copied'},()=>{btn.textContent='Not allowed'});setTimeout(()=>{btn.textContent=label},1600)}
document.getElementById('sgc').onclick=function(){const o=cards()[i].dataset.open;copy(this,o.startsWith('http')?o:location.origin+o,'⧉ Copy link')};
// a new screenshot of the website in view (front pages only): asked for, then waited for
const rt=document.getElementById('sgrt');
if(rt)rt.onclick=function(){const c=cards()[i];if(!c)return;const h=c.dataset.h,b=this,old='⟳ New screenshot';b.disabled=true;b.textContent='Asking …';
  const done=()=>{const u='/shot/'+encodeURIComponent(h)+'.jxl?t='+Date.now();im.src=u;const p=c.querySelector('img.pic');if(p)p.src=u;b.textContent='✓ New screenshot';b.disabled=false;setTimeout(()=>{b.textContent=old},2500)};
  const fail=m=>{b.textContent=m;b.disabled=false;setTimeout(()=>{b.textContent=old},3500)};
  const poll=n=>fetch('/api/retake?site='+encodeURIComponent(h)).then(r=>r.json()).then(d=>{if(d.state==='done')done();else if(d.state==='failed')fail(d.message.slice(0,60));else if(n>0){b.textContent='Taking it … '+(46-n)*2+' s';setTimeout(()=>poll(n-1),2000)}else fail('Takes too long')}).catch(()=>fail('Could not ask'));
  fetch('/api/retake?site='+encodeURIComponent(h),{method:'POST'}).then(r=>r.json()).then(d=>{if(d.state==='done')done();else if(d.state==='running')poll(45);else fail(d.message.slice(0,60))}).catch(()=>fail('Could not ask'))};
const sb=document.getElementById('sgl'),bar=document.getElementById('sgbar');
function stop(){clearInterval(timer);timer=null;sb.textContent='▶ Slideshow';bar.classList.remove('go')}
function play(){sb.textContent='❚❚ Pause';const tick=()=>{bar.classList.remove('go');void bar.offsetWidth;bar.classList.add('go')};tick();timer=setInterval(()=>{show(i+1);tick()},4000)}
sb.onclick=()=>timer?stop():play();
const helpEl=document.getElementById('sghelp');helpEl.addEventListener('click',()=>{helpEl.hidden=true});
function random(){const n=cards().length;if(n)show(Math.floor(Math.random()*n))}
const rb=document.getElementById('sgrand');if(rb)rb.onclick=random;
document.addEventListener('keydown',e=>{
  const free=!/INPUT|TEXTAREA/.test((document.activeElement||{}).tagName);
  if(e.key==='?'&&free){helpEl.hidden=!helpEl.hidden;return}
  if(e.key==='Escape'&&!helpEl.hidden){helpEl.hidden=true;return}
  if(!v.classList.contains('on')){
    if(e.key==='/'&&free){const f=document.querySelector('.ap-search input');if(f){e.preventDefault();f.focus();f.select()}}
    else if((e.key==='r'||e.key==='R')&&free&&!e.ctrlKey&&!e.metaKey){random()}
    return}
  if(e.key==='Escape')hide();else if(e.key==='ArrowLeft')show(i-1);else if(e.key==='ArrowRight')show(i+1);else if(e.key==='z'||e.key==='Z')v.classList.toggle('zoom');else if(e.key==='s'||e.key==='S'){const c=cards()[i];if(c)toggleFav(c.dataset.h)}else if(e.key===' '){e.preventDefault();timer?stop():play()}});
let sx=null;v.addEventListener('touchstart',e=>{sx=e.touches[0].clientX},{passive:true});v.addEventListener('touchend',e=>{if(sx===null||v.classList.contains('zoom'))return;const dx=e.changedTouches[0].clientX-sx;if(Math.abs(dx)>60)show(i+(dx<0?1:-1));sx=null},{passive:true});
// more pictures as the page is scrolled (or with the button): only the next ones are fetched and added under the ones already there
let page=+grid.dataset.page,pages=+grid.dataset.pages,busy=false;const mb=document.getElementById('sgmb'),mw=document.getElementById('sgmore');
function more(){
  if(busy||page>=pages)return;busy=true;if(mb)mb.textContent='Loading…';
  const days=grid.querySelectorAll('.sg-day'),ld=days.length?days[days.length-1].textContent:'';
  fetch('/screenshots?'+grid.dataset.qs+'&p='+(page+1)+'&frag=1&lastday='+encodeURIComponent(ld)).then(r=>r.text()).then(h=>{grid.insertAdjacentHTML('beforeend',h);page++;fade(grid);paintHearts(grid);scrub();busy=false;if(mb)mb.textContent='Show more';if(page>=pages&&mw)mw.hidden=true;else watch()}).catch(()=>{busy=false;if(mb)mb.textContent='Try again'});
}
if(mw&&page<pages){mw.hidden=false;if(mb)mb.onclick=more}
let io=null;function watch(){if(!('IntersectionObserver' in window)||!mw)return;if(io)io.disconnect();io=new IntersectionObserver(es=>{if(es[0].isIntersecting)more()},{rootMargin:'700px'});io.observe(mw)}
watch();
// the size of the pictures: a slider (remembered)
const zs=document.getElementById('sgzoom');
function size(px){grid.style.setProperty('--min',px+'px');try{localStorage.setItem('sgmin',px)}catch(e){}}
if(zs){try{const s=+localStorage.getItem('sgmin');if(s>=140&&s<=480){zs.value=s;size(s)}}catch(e){}zs.oninput=()=>size(zs.value)}
// the days along the right edge: one dot for each day, the day in view lights up, a click goes there
const sc=document.getElementById('sgscrub');let so=null;
function scrub(){
  if(!sc)return;const ds=[...grid.querySelectorAll('.sg-day')];sc.textContent='';
  ds.forEach(d=>{const b=document.createElement('button'),t=document.createElement('span');b.type='button';b.append(document.createElement('i'),t);t.textContent=d.textContent;b.setAttribute('aria-label',d.textContent);b.onclick=()=>d.scrollIntoView({behavior:'smooth',block:'start'});sc.append(b);d._b=b});
  sc.hidden=ds.length<2;
  if(so)so.disconnect();if(!('IntersectionObserver' in window))return;
  so=new IntersectionObserver(es=>{for(const e of es)if(e.isIntersecting){sc.querySelectorAll('.on').forEach(x=>x.classList.remove('on'));if(e.target._b)e.target._b.classList.add('on')}},{rootMargin:'-80px 0px -75% 0px'});
  ds.forEach(d=>so.observe(d));
}
scrub();
// the menu on a phone
const mn=document.getElementById('sgmenu'),sd=document.querySelector('.ap-side');
if(mn&&sd){mn.onclick=()=>sd.classList.toggle('open');document.addEventListener('click',e=>{if(sd.classList.contains('open')&&!sd.contains(e.target)&&e.target!==mn)sd.classList.remove('open')})}
// a link to this view
const lv=document.getElementById('sgview');if(lv)lv.onclick=function(){copy(this,location.origin+location.pathname+location.search,'⧉ Copy link to this view')};
// a link to one picture: it opens when the page loads
(function(){const m=/#v=(.+)$/.exec(location.hash);if(!m)return;const t=decodeURIComponent(m[1]),k=cards().findIndex(c=>c.dataset.title===t);if(k>=0)show(k)})();
// new screenshots: the page asks now and then
const total=+grid.dataset.total,nb=document.getElementById('sgnew');
if(nb)setInterval(()=>{if(document.hidden)return;fetch('/screenshots?count=1').then(r=>r.json()).then(d=>{if(d.total>total){document.getElementById('sgnewn').textContent=(d.total-total).toLocaleString('en-GB');nb.classList.add('on')}}).catch(()=>{})},30000);
})();
</script>`

// shotImg / shotHref: the picture and the link of one entry (a site's front page, or a single page)
func shotImg(e shotEntry) string {
	if e.Key != "" {
		return "/pshot/" + e.Key + ".jxl"
	}
	return "/shot/" + url.PathEscape(e.Host) + ".jxl"
}

func shotHref(e shotEntry) string {
	if e.Key != "" {
		return e.URL
	}
	return "/search?q=site:" + url.QueryEscape(e.Host)
}

// shotOpen: the page itself (for a site, its front page)
func shotOpen(e shotEntry) string {
	if e.Key != "" {
		return e.URL
	}
	return "https://" + e.Host
}

// shotAgo: "5 min ago", "3 h ago", "2 days ago"
func shotAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 90*time.Second:
		return "just now"
	case d < 90*time.Minute:
		return fmt.Sprintf("%d min ago", int(d.Minutes()+.5))
	case d < 36*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()+.5))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24+.5))
	}
	return t.Format("2 Jan 2006")
}

// shotDay: the heading of the day a picture belongs to
func shotDay(t time.Time) string {
	n := time.Now()
	day := func(x time.Time) string { return x.Format("2006-01-02") }
	switch day(t) {
	case day(n):
		return "Today"
	case day(n.AddDate(0, 0, -1)):
		return "Yesterday"
	}
	return t.Format("Mon 2 January 2006")
}

// shotExtra: the type, order and favourites of the view, carried into every link of the page
func shotExtra(kind, sortBy, favs string) string {
	s := ""
	if kind != "" {
		s += "&kind=" + kind
	}
	if sortBy != "" {
		s += "&sort=" + sortBy
	}
	if favs != "" {
		s += "&favs=" + url.QueryEscape(favs)
	}
	return s
}

func shotHue(h string) int {
	x := 0
	for _, c := range h {
		x = (x*31 + int(c)) % 360
	}
	return x
}

// shotView: everything the page needs to be drawn
type shotView struct {
	Q, Since, Kind, SortBy, Favs string
	Total, Sites, Pages          int
	Today, Week                  int
	Daily                        []int
	Counts                       []int // the pictures in each time window
	NList, Page, Last            int
	Cards                        []shotEntry // the pictures of this page
	FavMode                      bool
	Taken, Waiting, PerDay       int
}

// shotAppHTML: the whole app (sidebar, toolbar, gallery)
func shotAppHTML(v shotView) string {
	var b strings.Builder
	base := func(q, since, kind, sortBy, favs string) string {
		u := "/screenshots?q=" + url.QueryEscape(q)
		if since != "" {
			u += "&since=" + since
		}
		return u + shotExtra(kind, sortBy, favs)
	}
	item := func(icon, label, href string, n string, on bool) string {
		cls := "ap-item"
		if on {
			cls += " on"
		}
		small := ""
		if n != "" {
			small = "<small>" + n + "</small>"
		}
		return fmt.Sprintf(`<a class="%s" href="%s"><i>%s</i>%s%s</a>`, cls, html.EscapeString(href), icon, label, small)
	}
	b.WriteString(`<div class=ap><aside class=ap-side><h2>Screenshots</h2>`)
	fmt.Fprintf(&b, `<form class=ap-search method=get action=/screenshots><input name=q value="%s" placeholder="Find a website  ( / )" aria-label="Find a website">`, html.EscapeString(v.Q))
	for _, h := range [][2]string{{"since", v.Since}, {"kind", v.Kind}, {"sort", v.SortBy}, {"favs", v.Favs}} {
		if h[1] != "" {
			fmt.Fprintf(&b, `<input type=hidden name=%s value="%s">`, h[0], html.EscapeString(h[1]))
		}
	}
	b.WriteString(`</form><div class=ap-h>Library</div>`)
	b.WriteString(item("▦", "Everything", base(v.Q, v.Since, "", v.SortBy, ""), commas(v.Total), v.Kind == "" && !v.FavMode))
	b.WriteString(item("🌐", "Website front pages", base(v.Q, v.Since, "sites", v.SortBy, ""), commas(v.Sites), v.Kind == "sites" && !v.FavMode))
	b.WriteString(item("📄", "Single pages", base(v.Q, v.Since, "pages", v.SortBy, ""), commas(v.Pages), v.Kind == "pages" && !v.FavMode))
	fav := ""
	if v.FavMode {
		fav = " on"
	}
	fmt.Fprintf(&b, `<button class="ap-item%s" id=sgfavs type=button><i>♥</i>Favourites<small>0</small></button>`, fav)
	b.WriteString(`<div class=ap-h>Taken</div>`)
	b.WriteString(item("∞", "Any time", base(v.Q, "", v.Kind, v.SortBy, v.Favs), "", v.Since == ""))
	for i, sw := range shotWindows {
		b.WriteString(item("◷", sw.Label, base(v.Q, sw.Key, v.Kind, v.SortBy, v.Favs), commas(v.Counts[i]), v.Since == sw.Key))
	}
	b.WriteString(`<div class=ap-h>Order</div>`)
	b.WriteString(item("↓", "Newest first", base(v.Q, v.Since, v.Kind, "", v.Favs), "", v.SortBy == ""))
	b.WriteString(item("↑", "Oldest first", base(v.Q, v.Since, v.Kind, "old", v.Favs), "", v.SortBy == "old"))
	b.WriteString(item("A", "A to Z", base(v.Q, v.Since, v.Kind, "az", v.Favs), "", v.SortBy == "az"))
	b.WriteString(`<div class=ap-h>Last 14 days</div><div class=ap-days aria-hidden=true>`)
	mx := 1
	for _, n := range v.Daily {
		mx = max(mx, n)
	}
	for i, n := range v.Daily {
		d := time.Now().AddDate(0, 0, -(13 - i)).Format("2 Jan")
		fmt.Fprintf(&b, `<i style="height:%d%%" title="%s: %s"></i>`, max(6, 100*n/mx), d, commas(n))
	}
	fmt.Fprintf(&b, `</div><div class=ap-stat><div><b>%s</b><small>new in 24 hours</small></div><div><b>%s</b><small>new this week</small></div></div></aside>`, commas(v.Today), commas(v.Week))

	// the toolbar and the gallery
	title := "Everything"
	switch {
	case v.FavMode:
		title = "Favourites"
	case v.Kind == "sites":
		title = "Website front pages"
	case v.Kind == "pages":
		title = "Single pages"
	}
	if v.Q != "" {
		title += " · “" + html.EscapeString(v.Q) + "”"
	}
	fmt.Fprintf(&b, `<section class=ap-main><div class=ap-bar><h1>%s<span class=cnt>%s pictures</span></h1><div class=ap-tools><button class=ap-menu id=sgmenu type=button>☰ Filters</button><label class=ap-zoom>Size <input id=sgzoom type=range min=140 max=480 step=10 value=250 aria-label="Size of the pictures"></label><button id=sgrand type=button>🎲 Random</button><button id=sgview type=button>⧉ Copy link to this view</button></div></div>`, title, commas(v.NList))
	b.WriteString(`<div class=ap-new id=sgnew><span><b id=sgnewn>0</b> new screenshots</span><a href="">Show them</a></div>`)
	favFlag := map[bool]string{true: "1", false: ""}[v.FavMode]
	fmt.Fprintf(&b, `<div class=sg-grid data-page="%d" data-pages="%d" data-total="%d" data-favs="%s" data-qs="%s">`, v.Page, v.Last, v.Total, favFlag,html.EscapeString("q="+url.QueryEscape(v.Q)+"&since="+v.Since+shotExtra(v.Kind, v.SortBy, v.Favs)))
	b.WriteString(shotUIGrid(v.Cards, v.SortBy, ""))
	b.WriteString(`</div>`)
	if len(v.Cards) == 0 {
		msg := "No website matches."
		if v.FavMode {
			msg = "No favourites here yet. Click the ♡ on a picture to save it."
		} else if v.Since != "" {
			msg = "No screenshots were taken in that time. Try a longer time."
		}
		b.WriteString(`<p class=sg-empty>` + msg + `</p>`)
	}
	b.WriteString(`<div class=sg-more id=sgmore hidden><button type=button id=sgmb>Show more</button></div>`)
	if v.Last > 1 {
		link := func(n int, label string) string {
			return fmt.Sprintf(`<a class=pbtn href="/screenshots?q=%s&since=%s&p=%d%s">%s</a>`, url.QueryEscape(v.Q), v.Since, n, shotExtra(v.Kind, v.SortBy, v.Favs), label)
		}
		b.WriteString(`<div class=sg-pg>`)
		if v.Page > 1 {
			b.WriteString(link(v.Page-1, "&larr; Previous"))
		}
		fmt.Fprintf(&b, `<span>Page %d of %d</span>`, v.Page, v.Last)
		if v.Page < v.Last {
			b.WriteString(link(v.Page+1, "Next &rarr;"))
		}
		b.WriteString(`</div>`)
	}
	fmt.Fprintf(&b, `<p class=sg-note>A page gets a screenshot when its result first shows up in a search. Today %d of %d automatic screenshots are used, %d are waiting in line. The pictures are JPEG XL: a browser that supports that format is needed to show them.</p></section></div><nav class=ap-scrub id=sgscrub aria-label="Days" hidden></nav>`, v.Taken, v.PerDay, v.Waiting)
	b.WriteString(shotUIJS)
	return b.String()
}

// shotUICard: one picture of the gallery
func shotUICard(e shotEntry) string {
	host := strings.TrimPrefix(hostOf(e.Host), "www.")
	if host == "" {
		host = strings.TrimPrefix(e.Host, "www.")
	}
	initial := "?"
	if host != "" {
		initial = strings.ToUpper(host[:1])
	}
	title := html.EscapeString(clip(e.Host, 70))
	href := html.EscapeString(shotHref(e))
	img := shotImg(e)
	badge := ""
	if time.Since(e.At) < time.Hour {
		badge = `<span class=new>New</span>`
	}
	fav := fmt.Sprintf(`<span class=sg-fav style="background:hsl(%d 55%% 45%%)">%s<img src="/flowcdn/fav/%s.jxl" alt="" loading=lazy referrerpolicy=no-referrer onerror="this.remove()"></span>`, shotHue(host), html.EscapeString(initial), url.PathEscape(host))
	heart := fmt.Sprintf(`<button class=sg-heart type=button data-h="%s" aria-label="Save to favourites" title="Save to favourites">♡</button>`, html.EscapeString(e.Host))
	return fmt.Sprintf(`<a class=sg-card href="%s" rel="noopener noreferrer" data-img="%s" data-title="%s" data-href="%s" data-open="%s" data-ago="%s" data-h="%s">%s%s<img class=pic src="%s" alt="" loading=lazy width=250 height=156><span class=ov>%s<b>%s</b><small>%s</small></span></a>`,
		href, img, title, href, html.EscapeString(shotOpen(e)), shotAgo(e.At), html.EscapeString(e.Host), badge, heart, img, fav, title, shotAgo(e.At))
}

// shotUIGrid: the pictures of one page of the gallery, with a heading for every new day (lastDay: the day the previous pictures ended with)
func shotUIGrid(list []shotEntry, sortBy, lastDay string) string {
	var b strings.Builder
	for _, e := range list {
		if sortBy != "az" {
			if d := shotDay(e.At); d != lastDay {
				b.WriteString(`<div class=sg-day>` + html.EscapeString(d) + `</div>`)
				lastDay = d
			}
		}
		b.WriteString(shotUICard(e))
	}
	return b.String()
}
