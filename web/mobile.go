package main

// /mobile: the phone version of the home page. One big glass search box, a row to pick what to search (web, videos, shopping, Roblox,
// forums: the same tabs as the results page), Surprise me, and tiles for everything else Flowsearch has.

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const mobileCSS = `<style>
body{background:radial-gradient(520px 340px at 0% -60px,rgba(31,95,196,.4),transparent 70%),radial-gradient(520px 340px at 100% -80px,var(--glow),transparent 70%),var(--bg);background-attachment:fixed}
.m{max-width:520px;margin:0 auto;padding:22px 16px 44px;display:flex;flex-direction:column;gap:20px}
.m .logo,.m h1{text-align:center;align-self:center}
.m .tagm{text-align:center;color:var(--dim);margin:-8px 0 0;font-size:15.5px}
.m form.ms{display:flex;flex-direction:column;gap:12px}
.m .box{position:relative}
.m .box input[name=q]{width:100%;font-size:18px;padding:17px 88px 17px 22px;border-radius:99px;border:1px solid rgba(255,255,255,.28);background:var(--card);background:color-mix(in srgb,var(--card) 45%,transparent);backdrop-filter:blur(18px) saturate(1.6);-webkit-backdrop-filter:blur(18px) saturate(1.6);color:inherit;box-shadow:0 14px 36px -18px rgba(0,0,0,.6),0 1px 0 rgba(255,255,255,.3) inset}
.m .box input[name=q]:focus{outline:2px solid var(--acc);outline-offset:2px}
.m .box button{position:absolute;right:7px;top:7px;bottom:7px;font-size:16px;padding:0 20px;border-radius:99px;border:0;background:linear-gradient(to bottom,color-mix(in srgb,var(--acc) 82%,white),var(--acc));color:var(--acc-ink);font-weight:700}
.m .seg{display:flex;gap:6px;overflow-x:auto;scrollbar-width:none;padding:2px 0;justify-content:center}
.m .seg::-webkit-scrollbar{display:none}
.m .seg label{flex:0 0 auto}
.m .seg input{position:absolute;opacity:0;pointer-events:none}
.m .seg span{display:block;padding:8px 15px;border-radius:99px;border:1px solid rgba(255,255,255,.2);background:var(--card);background:color-mix(in srgb,var(--card) 50%,transparent);backdrop-filter:blur(10px);-webkit-backdrop-filter:blur(10px);font-size:14.5px;white-space:nowrap}
.m .seg input:checked+span{background:var(--acc);border-color:var(--acc);color:var(--acc-ink);font-weight:700}
.m .tiles{display:grid;grid-template-columns:repeat(3,1fr);gap:10px}
.m .tiles a{display:flex;flex-direction:column;align-items:center;gap:6px;padding:14px 4px;border:1px solid rgba(255,255,255,.16);border-radius:22px;background:var(--card);background:color-mix(in srgb,var(--card) 58%,transparent);backdrop-filter:blur(14px) saturate(1.5);-webkit-backdrop-filter:blur(14px) saturate(1.5);box-shadow:0 12px 28px -22px rgba(0,0,0,.8);text-decoration:none;color:inherit;font-size:13px}
.m .tiles a b{display:flex;height:30px;align-items:center;font-weight:400;color:var(--acc)}.m .tiles img{border-radius:7px}
.m .sur{display:flex;gap:8px}
.m .sur select{flex:1;min-width:0;padding:12px 14px;border-radius:99px;font-size:15px;border:1px solid rgba(255,255,255,.22);background:var(--card);background:color-mix(in srgb,var(--card) 50%,transparent);color:inherit}
.m .sur button{padding:0 20px;border-radius:99px;font-size:15px}
.m h4{margin:0 0 8px;font-size:12px;letter-spacing:.06em;text-transform:uppercase;opacity:.6}
.m .chips{display:flex;flex-wrap:wrap;gap:8px}
.m .stats{text-align:center;font-size:12.5px;opacity:.65;margin:0}
</style>`

func mobilePage(w http.ResponseWriter, r *http.Request) {
	st := statsJSON()
	var tries strings.Builder
	for _, q := range []string{"linux kernel", "python asyncio", "mac mini", "openstreetmap organic maps", "minecraft mace pvp"} {
		tries.WriteString(fmt.Sprintf(`<a class=chip href="/search?q=%s">%s</a>`, url.QueryEscape(q), q))
	}
	seg := func(val, label string, on bool) string {
		c := ""
		if on {
			c = " checked"
		}
		return `<label><input type=radio name=tab value="` + val + `"` + c + `><span>` + label + `</span></label>`
	}
	fav := func(host string) string {
		return `<img src="/flowcdn/fav/` + host + `.jxl" width=28 height=28 alt="" loading=lazy>`
	}
	svg := func(path string) string {
		return `<svg width=28 height=28 viewBox="0 0 24 24" fill=none stroke=currentColor stroke-width=1.8 stroke-linecap=round stroke-linejoin=round aria-hidden=true>` + path + `</svg>`
	}
	tile := func(href, icon, label string) string {
		return `<a href="` + href + `"><b>` + icon + `</b>` + label + `</a>`
	}
	body := mobileCSS + `<main class=m>` + logoHTML() +
		`<p class=tagm>Search the web without being tracked.</p>` +
		`<form class=ms action=/search method=get><div class=box><input name=q type=search placeholder="Search the web" aria-label=Search autocomplete=off autocapitalize=off enterkeyhint=search><button type=submit>Go</button></div>` +
		`<div class=seg>` + seg("", "All", true) + seg("videos", "Videos", false) + seg("shopping", "Shopping", false) + seg("roblox", "Roblox", false) + seg("forums", "Forums", false) + seg("stack", "Stack Overflow", false) + `</div></form>` +
		`<form class=sur action=/surprise method=get><select name=pick aria-label="Surprise me"><option value=page>A random page</option><option value=amazon>Amazon</option><option value=products>Amazon Germany</option><option value=youtube>YouTube video</option><option value=music>Random music</option><option value=mace>Minecraft mace PvP</option><option value=minecraft>Minecraft PvP</option><option value=connector>Claude connector</option></select><button class=chip type=submit>Surprise me</button></form>` +
		`<div class=tiles>` + tile("/chat", svg(`<path d="M21 12a8 8 0 0 1-11.6 7.1L4 20l1-4.6A8 8 0 1 1 21 12z"/>`), "AI mode") +
		tile("/ask", svg(`<circle cx=9 cy=8 r=3 /><path d="M3 20c0-3.3 2.7-6 6-6s6 2.7 6 6"/><path d="M16 5.5a3 3 0 0 1 0 5M18 14.2c1.8.8 3 2.6 3 4.8"/>`), "Ask people") +
		tile("/looks", svg(`<circle cx=11 cy=11 r=6 /><path d="M20 20l-4-4"/>`), "By looks") +
		tile("/search?q=minecraft&tab=videos", fav("youtube.com"), "Videos") + tile("/search?q=laptop&tab=shopping", fav("amazon.de"), "Shopping") + tile("/search?q=obby&tab=roblox", fav("roblox.com"), "Roblox") +
		tile("/minecraft", fav("minecraft.net"), "Minecraft") + tile("/live", svg(`<path d="M5 12a10 10 0 0 1 14 0M8 15a6 6 0 0 1 8 0"/><circle cx=12 cy=19 r=1.2 />`), "Live") + tile("/screenshots", svg(`<rect x=3 y=4 width=18 height=16 rx=2 /><circle cx=9 cy=10 r=1.5 /><path d="M21 16l-5-5-8 8"/>`), "Screenshots") +
		tile("/quiz", svg(`<circle cx=12 cy=12 r=9 /><circle cx=12 cy=12 r=4 /><circle cx=12 cy=12 r=1 />`), "Guess the site") + tile("/wall", svg(`<rect x=3 y=3 width=7 height=11 rx=1.5 /><rect x=14 y=3 width=7 height=7 rx=1.5 /><rect x=3 y=17 width=7 height=4 rx=1.5 /><rect x=14 y=13 width=7 height=8 rx=1.5 />`), "Wall") + tile("/tools", svg(`<path d="M14 6a4 4 0 0 0 5 5l-9 9a2 2 0 0 1-3-3l9-9a4 4 0 0 0-2-2z"/>`), "Tools") +
		tile("/news", svg(`<rect x=4 y=4 width=16 height=16 rx=2 /><path d="M8 9h8M8 13h8M8 17h5"/>`), "What's new") + tile("/webmaster", svg(`<path d="M14 6a4 4 0 0 0 5 5l-9 9a2 2 0 0 1-3-3l9-9a4 4 0 0 0-2-2z"/>`), "Site owners") + tile("/settings", svg(`<circle cx=12 cy=12 r=3 /><path d="M12 3v3M12 18v3M3 12h3M18 12h3M5.6 5.6l2.1 2.1M16.3 16.3l2.1 2.1M18.4 5.6l-2.1 2.1M7.7 16.3l-2.1 2.1"/>`), "Settings") + `</div>` +
		`<div><h4>Try searching for</h4><div class=chips>` + tries.String() + `</div></div>` + forumsTrendingHTML() +
		fmt.Sprintf(`<p class=stats><b>%s</b> pages from <b>%s</b> websites · <a href="/api">For developers</a> · <a href="/">Desktop</a></p>`, humanN(st["pages"].(int)), humanN(st["sites"].(int))) + `</main>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, shell("Flowsearch", body))
}
