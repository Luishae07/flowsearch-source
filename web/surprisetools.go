package main

// "Another one" for the page Surprise me has sent you to. That page belongs to someone else (Amazon, YouTube...), so Flowsearch cannot put a
// button on it. Instead a bookmark (a bookmarklet) or a few lines pasted into the console adds the way back: Surprise me tags the address it
// sends you to with #flowsurprise=<the kind of pick> (a fragment: it never goes to the site), and the bookmark reads the tag and asks
// Flowsearch for the next page of the same kind.

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
)

// withReroll adds the tag to an address; the kind of pick comes from the request that Surprise me answered.
func withReroll(target string, r *http.Request) string {
	if strings.Contains(target, "#") {
		return target
	}
	q := r.URL.Query()
	q.Del("json")
	if len(q) == 0 {
		q.Set("pick", "page")
	}
	return target + "#flowsurprise=" + url.QueryEscape(q.Encode())
}

func ownOrigin(r *http.Request) string {
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "http"
	}
	return proto + "://" + r.Host
}

// the code of the bookmark: reads the tag of the address (or uses a random page) and goes to Surprise me
func bookmarkletCode(origin string) string {
	return `(function(){var m=location.hash.match(/flowsurprise=([^&]+)/);var q=m?decodeURIComponent(m[1]):'pick=page';location.href='` + origin + `/surprise?'+q})()`
}

// the code for the console: a floating button on the page you are looking at
func consoleCode(origin string) string {
	return `(function(){var m=location.hash.match(/flowsurprise=([^&]+)/);var q=m?decodeURIComponent(m[1]):'pick=page';var b=document.createElement('button');b.textContent='🎲 Another one';b.style.cssText='position:fixed;right:16px;bottom:16px;z-index:2147483647;padding:12px 18px;border:0;border-radius:99px;background:#4c8dff;color:#fff;font:600 15px system-ui;box-shadow:0 4px 16px rgba(0,0,0,.35);cursor:pointer';b.onclick=function(){location.href='` + origin + `/surprise?'+q};document.body.appendChild(b)})()`
}

func surpriseBookmarklet(w http.ResponseWriter, r *http.Request) {
	origin := ownOrigin(r)
	bm := "javascript:" + bookmarkletCode(origin)
	amzAny := "javascript:(function(){var p=Math.random()<0.5?'products':'amazon';location.href='" + origin + "/surprise?pick='+p})()"
	amzDE := "javascript:location.href='" + origin + "/surprise?pick=products'"
	amzCom := "javascript:location.href='" + origin + "/surprise?pick=amazon'"
	body := `<style>.sb{max-width:760px}.sb .bm{display:inline-block;padding:11px 22px;border-radius:99px;background:linear-gradient(to bottom,color-mix(in srgb,var(--acc) 82%,white),var(--acc));color:var(--acc-ink);font-weight:700;text-decoration:none;margin:6px 6px 8px 0;cursor:grab;box-shadow:0 8px 20px -10px rgba(0,0,0,.6),0 1px 0 rgba(255,255,255,.4) inset}
.sb textarea{width:100%;height:150px;font:12.5px ui-monospace,Menlo,monospace;padding:12px 14px;border-radius:18px;border:1px solid rgba(255,255,255,.22);background:var(--bg);color:inherit}
.sb ol{padding-left:20px;margin:8px 0}.sb li{margin:6px 0}</style><main class="wrap sb">` + pageHead("Another one, on any page", "Surprise me sends you to a page on someone else's website, and Flowsearch cannot put a button on that page. Add the button yourself in one of two ways.") + `
<div class=gstack>
<section class=gc><h2>1. A bookmark (one click)</h2>
<ol><li>Drag this button to your bookmarks bar: <a class=bm href="` + html.EscapeString(bm) + `" onclick="return false">Another one</a></li>
<li>Press Surprise me. On the page it sends you to, click the bookmark. You get the next page of the same kind (for example Amazon Germany products again).</li></ol>
<p style="color:var(--dim)">On a page that did not come from Surprise me, the bookmark opens a random page.</p></section>
<section class=gc><h2>Random Amazon bookmarks</h2>
<p style="color:var(--dim)">Three more bookmarks to drag to your bookmarks bar. One click opens a random Amazon product page:</p>
<p><a class=bm href="` + html.EscapeString(amzAny) + `" onclick="return false">Amazon (German or normal)</a><a class=bm href="` + html.EscapeString(amzDE) + `" onclick="return false">Amazon Germany</a><a class=bm href="` + html.EscapeString(amzCom) + `" onclick="return false">Amazon.com</a></p></section>
<section class=gc><h2>2. A button on the page (paste into the console)</h2>
<p style="color:var(--dim)">Open the console with F12 (the “Console” tab), paste this and press Enter. A floating “Another one” button appears on that page:</p>
<textarea readonly onclick="this.select()">` + html.EscapeString(consoleCode(origin)) + `</textarea>
<p style="color:var(--dim);font-size:14px">Some websites (with a strict Content-Security-Policy) refuse bookmarks and pasted code, and there the bookmark does nothing. The tag <code>#flowsurprise=…</code> at the end of the address is never sent to the website.</p></section>
</div></main>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, page("Another one - Flowsearch", "", body))
}
