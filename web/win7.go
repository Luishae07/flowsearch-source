package main

import "net/http"

const win7CSS = `*{box-sizing:border-box}
html,body{margin:0;height:100%;overflow:hidden;font-family:"Segoe UI",system-ui,sans-serif}
.desktop{height:100vh;width:100vw;background:
  radial-gradient(ellipse at 50% 30%,#6fa8e0 0%,#1b5aa8 55%,#0a2e5c 100%);
  position:relative}
.icons{position:absolute;top:18px;left:18px;display:flex;flex-direction:column;gap:22px;z-index:1}
.icon{display:flex;flex-direction:column;align-items:center;gap:4px;width:84px;text-decoration:none;color:#fff;text-shadow:0 1px 2px rgba(0,0,0,.8);font-size:12.5px;text-align:center;cursor:pointer;background:none;border:0;font-family:inherit}
.icon .glyph{width:48px;height:48px;border-radius:10px;background:linear-gradient(to bottom,#eaf4ff,#aed0f5);border:1px solid #6fa0d8;box-shadow:0 2px 5px rgba(0,0,0,.4);display:flex;align-items:center;justify-content:center;font-size:22px}
.icon:hover .glyph{outline:1px dotted rgba(255,255,255,.7)}
.taskbar{position:absolute;left:0;right:0;bottom:0;height:42px;display:flex;align-items:center;z-index:50;
  background:linear-gradient(to bottom,#2a6fd1 0%,#1c4fa0 45%,#123a7c 100%);
  border-top:1px solid #6fa8e0;box-shadow:0 -1px 0 rgba(255,255,255,.25) inset}
.start{height:42px;width:94px;display:flex;align-items:center;justify-content:center;gap:6px;
  background:linear-gradient(to bottom,#4caf3c,#2e7d1e);border-radius:0 10px 10px 0;
  color:#fff;font-weight:700;font-size:14px;text-decoration:none;box-shadow:0 1px 0 rgba(255,255,255,.4) inset;border:0;cursor:pointer;font-family:inherit}
.start:hover{filter:brightness(1.1)}
.task-sep{width:1px;align-self:stretch;background:rgba(255,255,255,.2);margin:6px 8px}
.task-items{flex:1;display:flex;gap:6px;overflow:hidden}
.task-item{height:32px;padding:0 14px;display:flex;align-items:center;border-radius:4px;color:#fff;font-size:13px;background:rgba(255,255,255,.08);border:1px solid rgba(255,255,255,.12);cursor:pointer;font-family:inherit;max-width:160px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.task-item:hover,.task-item.active{background:rgba(255,255,255,.22)}
.clock{color:#fff;font-size:13px;padding:0 16px;text-shadow:0 1px 1px rgba(0,0,0,.4)}
.back{position:absolute;top:14px;right:18px;color:#fff;text-decoration:none;font-size:13px;background:rgba(0,0,0,.25);padding:6px 12px;border-radius:4px;z-index:60}
.back:hover{background:rgba(0,0,0,.4)}
.win{position:absolute;top:60px;left:120px;width:720px;height:520px;background:#f0f4f8;border-radius:8px 8px 4px 4px;box-shadow:0 10px 30px rgba(0,0,0,.5);overflow:hidden;display:flex;flex-direction:column;border:1px solid #0a2e5c}
.win-title{height:32px;display:flex;align-items:center;padding:0 8px;gap:8px;cursor:move;user-select:none;
  background:linear-gradient(to bottom,#3f87dd 0%,#1c5bb0 50%,#0f3f8c 100%);color:#fff;font-size:13px;font-weight:600;text-shadow:0 1px 1px rgba(0,0,0,.4)}
.win-title .t{flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.win-btn{width:24px;height:22px;border-radius:3px;border:1px solid rgba(255,255,255,.3);background:rgba(255,255,255,.12);color:#fff;cursor:pointer;font-size:13px;line-height:1;display:flex;align-items:center;justify-content:center}
.win-btn:hover{background:rgba(255,255,255,.28)}
.win-btn.close:hover{background:#e04040}
.win iframe{flex:1;border:0;width:100%;background:#fff}
.win.min{display:none}
.addr{display:flex;gap:6px;padding:6px;background:#dde6f0;border-bottom:1px solid #b9c7d8}
.addr input{flex:1;padding:6px 10px;border-radius:4px;border:1px solid #9ab0c8;font:13px/1 inherit}
.addr button{padding:6px 12px;border-radius:4px;border:1px solid #6fa0d8;background:linear-gradient(to bottom,#eaf4ff,#aed0f5);cursor:pointer;font:13px inherit}
.notepad textarea{flex:1;border:0;resize:none;padding:10px;font:13px/1.5 Consolas,monospace;outline:none}
.startmenu{position:absolute;left:0;bottom:42px;width:280px;background:#eef3f8;border:1px solid #0a2e5c;border-radius:6px 6px 0 0;box-shadow:0 -4px 20px rgba(0,0,0,.4);z-index:55;overflow:hidden;display:none}
.startmenu.open{display:block}
.startmenu .hd{background:linear-gradient(to bottom,#3f87dd,#0f3f8c);color:#fff;padding:12px 14px;font-weight:700}
.startmenu .item{display:flex;align-items:center;gap:10px;padding:9px 14px;cursor:pointer;font-size:13.5px;color:#13253d}
.startmenu .item:hover{background:#cfe0f2}
.startmenu .item .g{font-size:18px;width:22px;text-align:center}`

const win7JS = `(function(){
function tick(){
  var d=new Date();
  var h=d.getHours()%12||12,m=('0'+d.getMinutes()).slice(-2);
  document.getElementById('clk').textContent=h+':'+m;
}
tick();setInterval(tick,10000);

var zTop=10,wins={},count=0;

function openWindow(url,title,kind){
  if(wins[url]){
    var w=wins[url];
    w.el.classList.remove('min');
    w.el.style.zIndex=++zTop;
    setActive(url);
    return;
  }
  count++;
  var el=document.createElement('div');
  el.className='win';
  el.style.zIndex=++zTop;
  el.style.left=(80+count*24)+'px';
  el.style.top=(50+count*24)+'px';
  var body='';
  if(kind==='browser'){
    body='<div class=addr><button class=go-back>&larr;</button><input class=addr-in value="'+url+'"><button class=go>Go</button></div><iframe class=frame src="'+url+'"></iframe>';
  }else if(kind==='notepad'){
    body='<div class=notepad style="display:flex;flex:1"><textarea class=np-text placeholder="Type anything. Saved automatically."></textarea></div>';
  }else{
    body='<iframe class=frame src="'+url+'"></iframe>';
  }
  el.innerHTML='<div class=win-title data-url="'+url+'"><span class=t>'+title+'</span>'+
    '<button class="win-btn min-btn">&#8211;</button>'+
    '<button class="win-btn close">&times;</button></div>'+body;
  document.body.appendChild(el);
  wins[url]={el:el,title:title};

  if(kind==='browser'){
    var frame=el.querySelector('.frame'),input=el.querySelector('.addr-in');
    function nav(u){if(!/^https?:\/\//.test(u))u='https://'+u;frame.src=u;input.value=u}
    el.querySelector('.go').addEventListener('click',function(){nav(input.value)});
    input.addEventListener('keydown',function(e){if(e.key==='Enter')nav(input.value)});
    el.querySelector('.go-back').addEventListener('click',function(){try{frame.contentWindow.history.back()}catch(e){}});
  }
  if(kind==='notepad'){
    var ta=el.querySelector('.np-text');
    try{ta.value=localStorage.getItem('win7-notepad')||''}catch(e){}
    ta.addEventListener('input',function(){try{localStorage.setItem('win7-notepad',ta.value)}catch(e){}});
  }

  var bar=el.querySelector('.win-title');
  bar.addEventListener('mousedown',function(e){
    if(e.target.classList.contains('win-btn'))return;
    el.style.zIndex=++zTop;setActive(url);
    var sx=e.clientX,sy=e.clientY,ox=el.offsetLeft,oy=el.offsetTop;
    function mv(e2){el.style.left=(ox+e2.clientX-sx)+'px';el.style.top=Math.max(0,oy+e2.clientY-sy)+'px'}
    function up(){document.removeEventListener('mousemove',mv);document.removeEventListener('mouseup',up)}
    document.addEventListener('mousemove',mv);document.addEventListener('mouseup',up);
  });
  el.querySelector('.close').addEventListener('click',function(){
    el.remove();delete wins[url];renderTasks();
  });
  el.querySelector('.min-btn').addEventListener('click',function(){
    el.classList.add('min');renderTasks();
  });
  el.addEventListener('mousedown',function(){el.style.zIndex=++zTop;setActive(url)});
  renderTasks();
  setActive(url);
}

function setActive(url){
  document.querySelectorAll('.task-item').forEach(function(t){t.classList.toggle('active',t.dataset.url===url)});
}

function renderTasks(){
  var box=document.getElementById('tasks');
  box.innerHTML='';
  Object.keys(wins).forEach(function(url){
    var b=document.createElement('button');
    b.className='task-item';b.dataset.url=url;b.textContent=wins[url].title;
    b.addEventListener('click',function(){
      var w=wins[url];
      if(w.el.classList.contains('min')){w.el.classList.remove('min');w.el.style.zIndex=++zTop;setActive(url)}
      else{w.el.classList.add('min')}
    });
    box.appendChild(b);
  });
}

document.querySelectorAll('[data-open]').forEach(function(el){
  el.addEventListener('click',function(){
    openWindow(el.dataset.open,el.dataset.title,el.dataset.kind||'page');
    document.getElementById('startmenu').classList.remove('open');
  });
});

var startBtn=document.getElementById('startbtn'),menu=document.getElementById('startmenu');
startBtn.addEventListener('click',function(e){e.stopPropagation();menu.classList.toggle('open')});
document.addEventListener('click',function(e){if(!menu.contains(e.target)&&e.target!==startBtn)menu.classList.remove('open')});
})();`

func win7Page(w http.ResponseWriter, r *http.Request) {
	html := `<!doctype html><html lang=en><head><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1"><title>Flowsearch - Classic</title>` + svgIcon + `<style>` + win7CSS + `</style></head><body>
<div class=desktop>
  <a class=back href=/>&times; exit</a>
  <div class=icons>
    <button class=icon data-open=/search data-title=Search data-kind=page><span class=glyph>&#128269;</span>Search</button>
    <button class=icon data-open=/ data-title=Browser data-kind=browser><span class=glyph>&#127760;</span>Browser</button>
    <button class=icon data-open=notepad data-title=Notepad data-kind=notepad><span class=glyph>&#128221;</span>Notepad</button>
    <button class=icon data-open=/live data-title="Live stats" data-kind=page><span class=glyph>&#128202;</span>Live stats</button>
    <button class=icon data-open=/api data-title=API data-kind=page><span class=glyph>&#9881;</span>API</button>
    <button class=icon data-open=/webmaster data-title=Webmaster data-kind=page><span class=glyph>&#128187;</span>Webmaster</button>
    <button class=icon data-open=/news data-title=News data-kind=page><span class=glyph>&#128240;</span>News</button>
    <button class=icon data-open=/de data-title=Deutsch data-kind=page><span class=glyph>&#127465;&#127466;</span>Deutsch</button>
  </div>
  <div id=startmenu class=startmenu>
    <div class=hd>Flowsearch</div>
    <button class=item data-open=/search data-title=Search data-kind=page><span class=g>&#128269;</span>Search</button>
    <button class=item data-open=/ data-title=Browser data-kind=browser><span class=g>&#127760;</span>Browser</button>
    <button class=item data-open=notepad data-title=Notepad data-kind=notepad><span class=g>&#128221;</span>Notepad</button>
    <button class=item data-open=/live data-title="Live stats" data-kind=page><span class=g>&#128202;</span>Live stats</button>
    <button class=item data-open=/api data-title=API data-kind=page><span class=g>&#9881;</span>API</button>
    <button class=item data-open=/webmaster data-title=Webmaster data-kind=page><span class=g>&#128187;</span>Webmaster</button>
    <button class=item data-open=/news data-title=News data-kind=page><span class=g>&#128240;</span>News</button>
    <button class=item data-open=/de data-title=Deutsch data-kind=page><span class=g>&#127465;&#127466;</span>Deutsch</button>
  </div>
  <div class=taskbar>
    <button class=start id=startbtn><span>&#10047;</span> Start</button>
    <div class=task-sep></div>
    <div class=task-items id=tasks></div>
    <div class=clock id=clk></div>
  </div>
</div>
<script>` + win7JS + `</script>
</body></html>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
}
