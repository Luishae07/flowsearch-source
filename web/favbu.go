package main

// favBrowser: a website that Google's favicon service has no icon for gets its icon from a real browser (Browser Use): the front page is opened,
// the icon addresses in it (apple-touch-icon, icon, /favicon.ico) are fetched inside the page, and the first PNG, JPEG or GIF is converted to JPEG XL
// and kept like any other favicon. It runs in the background; the next time the results page asks, the icon is there.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
)

var (
	favBuMu   sync.Mutex
	favBuBusy = map[string]bool{}
	favBuSlot = make(chan struct{}, 2) // two browsers at most
)

const favBuJS = `(async()=>{
const out=[];
for(const l of document.querySelectorAll('link[rel*=icon]')){const r=(l.rel||'').toLowerCase();out.push([r.includes('apple')?0:1,l.href])}
out.sort((a,b)=>a[0]-b[0]);
const urls=out.map(x=>x[1]);urls.push(location.origin+'/favicon.ico');
for(const u of urls.slice(0,6)){
 try{const r=await fetch(u);if(!r.ok)continue;const b=await r.blob();
  if(!/^image\/(png|jpeg|gif)/.test(b.type)||b.size<100||b.size>1500000)continue;
  const d=await new Promise(ok=>{const f=new FileReader();f.onload=()=>ok(f.result);f.readAsDataURL(b)});return d}catch(e){}
}
return ''})()`

func favBrowser(host, path string) {
	favBuMu.Lock()
	if favBuBusy[host] {
		favBuMu.Unlock()
		return
	}
	favBuBusy[host] = true
	favBuMu.Unlock()
	defer func() { favBuMu.Lock(); delete(favBuBusy, host); favBuMu.Unlock() }()
	favBuSlot <- struct{}{}
	defer func() { <-favBuSlot }()
	var data string
	err := buRun(host, true, func(c *cdpConn) error {
		r, e := c.call("Runtime.evaluate", map[string]any{"expression": favBuJS, "awaitPromise": true, "returnByValue": true})
		if e != nil {
			return e
		}
		var d struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		json.Unmarshal(r, &d)
		data = d.Result.Value
		return nil
	})
	i := strings.Index(data, "base64,")
	if err != nil || i < 0 {
		return
	}
	img, e := base64.StdEncoding.DecodeString(data[i+7:])
	if e != nil || len(img) < 100 {
		return
	}
	var out bytes.Buffer
	enc := exec.Command("cjxl", "-", "-", "-d", "0", "-e", "7")
	enc.Stdin, enc.Stdout = bytes.NewReader(img), &out
	if enc.Run() != nil || out.Len() == 0 {
		return
	}
	os.MkdirAll(flowcdnRoot+"/fav", 0o755)
	if os.WriteFile(path+".tmp", out.Bytes(), 0o644) == nil {
		os.Rename(path+".tmp", path)
	}
}
