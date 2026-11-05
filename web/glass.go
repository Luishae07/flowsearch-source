package main

// Liquid glass search box (home page and header, wide screens). The logo, links and search box are frosted glass in plain CSS. On top of
// that liquidGL (MIT, https://github.com/naughtyduk/liquidGL, kept on this machine in /flowcdn/vendor/liquidgl/) draws a WebGL lens that
// bends the page behind the search box. It is started a few seconds after the page has loaded, because the page snapshot has to be taken
// when the AI answer and the chips have settled.
//
// The lens is bound to a separate, empty .glassPane element behind the search form, never to the form itself: liquidGL sets
// pointer-events:none and opacity:0 on whatever element it is given, which made the real search box impossible to click or type in.
// Without WebGL, on narrow screens, with "reduce motion" or if the library fails to load, the CSS glass stays as it is.

const glassJS = `<script>(function(){
  var box=document.querySelector('.top .sbox, .hero .sbox');
  if(!box||innerWidth<900||(window.matchMedia&&matchMedia('(prefers-reduced-motion: reduce)').matches))return;
  try{var c=document.createElement('canvas');if(!(c.getContext('webgl2')||c.getContext('webgl')))return}catch(e){return}
  function go(){
    // the form is left out of the lens' picture of the page: otherwise the lens bends a stale copy of the placeholder and the Search button
    box.setAttribute('data-liquid-ignore','');
    var pane=document.createElement('div');
    pane.className='glassPane';pane.setAttribute('aria-hidden','true');
    pane.style.borderRadius=(box.offsetHeight/2)+'px';
    box.insertBefore(pane,box.firstChild);
    import('/flowcdn/vendor/liquidgl/liquidGL.js').then(function(m){
      var lq=m.default||window.liquidGL;
      lq({target:'.sbox > .glassPane',snapshot:'body',resolution:1,refraction:0.012,bevelDepth:0.14,bevelWidth:0.32,frost:0.4,
          shadow:true,specular:true,reveal:'fade',tilt:false,magnify:1,
          on:{init:function(){document.documentElement.classList.add('lg-on')}}});
    }).catch(function(){pane.remove()});
  }
  function later(){setTimeout(go,2500)}
  if(document.readyState==='complete')later();else addEventListener('load',later);
})();</script>`
