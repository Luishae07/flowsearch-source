package main

// The shared pieces of the new look: a title block, glass cards in a grid, glass form fields and buttons, and a segmented control.
// Every inner page is built from these, so they all look like one product.

// pageHead is the title block at the top of an inner page. title and sub are trusted HTML.
func pageHead(title, sub string) string {
	s := ""
	if sub != "" {
		s = `<p class=psub>` + sub + `</p>`
	}
	return `<div class=phead><h1>` + title + `</h1>` + s + `</div>`
}

// proseShell is an inner page that is mostly text (privacy, documentation): the text sits in one readable glass card.
func proseShell(title, body string) string {
	return page(title, "", `<div class="gc prose">`+body+`</div>`)
}

const uiCSS = `
.prose{max-width:780px;margin:0 auto;padding:28px 34px;border-radius:28px}
.prose h1{font-size:38px;letter-spacing:-.03em;margin:0 0 6px}
.prose h2{font-size:21px;letter-spacing:-.01em;margin:32px 0 8px}
.prose p,.prose li{font-size:16.5px;line-height:1.65}
.prose ul{padding-left:22px}
.prose .sub{font-size:17.5px;margin:0 0 8px}
@media(max-width:640px){.prose{padding:20px 20px}}
main .fcard,main .pcard,main .vblock .gc{border-radius:22px;border-color:rgba(255,255,255,.16);background:var(--card);background:color-mix(in srgb,var(--card) 62%,transparent);backdrop-filter:blur(14px) saturate(1.4);-webkit-backdrop-filter:blur(14px) saturate(1.4);box-shadow:0 14px 34px -26px rgba(0,0,0,.8),0 1px 0 rgba(255,255,255,.08) inset}
main .fchips a,main .fsort{border-color:rgba(255,255,255,.2);background:var(--card);background:color-mix(in srgb,var(--card) 55%,transparent);-webkit-backdrop-filter:blur(10px);backdrop-filter:blur(10px)}
main .fsort{border-radius:99px}
main .seg{border-color:rgba(255,255,255,.2);background:var(--card);background:color-mix(in srgb,var(--card) 50%,transparent);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px)}
main .fbest{border-radius:18px}
.stabs{margin-bottom:16px}
body .wm .box,body .wm .panel,body .wm .gstat,body .wm .stat div{border-radius:22px;border-color:rgba(255,255,255,.14);background:color-mix(in srgb,var(--card) 62%,transparent);backdrop-filter:blur(14px) saturate(1.4);-webkit-backdrop-filter:blur(14px) saturate(1.4);box-shadow:0 14px 34px -26px rgba(0,0,0,.8),0 1px 0 rgba(255,255,255,.08) inset}
body .wm input[type=text],body .wm input[type=password],body .wm select{border-radius:99px;padding:13px 20px;border-color:rgba(255,255,255,.2)}
body .wm .gsigncard{border-radius:32px;border-color:rgba(255,255,255,.16);background:color-mix(in srgb,var(--card) 66%,transparent);backdrop-filter:blur(20px) saturate(1.5);-webkit-backdrop-filter:blur(20px) saturate(1.5)}
body .wm h1{font-weight:600;letter-spacing:-.02em}
body .wm .gside a{border-radius:99px}
body .wm .gside a.on{box-shadow:none}
.chip.on{background:var(--acc);color:var(--acc-ink);border-color:var(--acc);font-weight:700}
.chip.on small{color:inherit;opacity:.8}
.phead{margin:8px 0 28px}
.phead h1{font-size:40px;line-height:1.15;letter-spacing:-.03em;margin:0}
.psub{color:var(--dim);font-size:17.5px;line-height:1.5;margin:8px 0 0;max-width:46em}
.gc{border-radius:24px;padding:20px 22px;border:1px solid rgba(255,255,255,.16);background:var(--card);background:color-mix(in srgb,var(--card) 62%,transparent);backdrop-filter:blur(16px) saturate(1.5);-webkit-backdrop-filter:blur(16px) saturate(1.5);box-shadow:0 18px 44px -30px rgba(0,0,0,.8),0 1px 0 rgba(255,255,255,.1) inset;min-width:0}
.gc h2,.gc h3{margin:0 0 8px;letter-spacing:-.01em}
.gc p{margin:6px 0}
.gcg{display:grid;grid-template-columns:repeat(auto-fit,minmax(260px,1fr));gap:14px;margin:0 0 18px}
.gstack{display:flex;flex-direction:column;gap:14px;margin:0 0 18px}
a.gc{display:block;color:var(--ink);text-decoration:none;transition:transform .18s,border-color .18s}
a.gc:hover{transform:translateY(-3px);border-color:var(--acc)}
.fld{width:100%;font:inherit;font-size:17px;color:var(--ink);padding:13px 18px;border-radius:99px;border:1px solid rgba(255,255,255,.22);background:var(--card);background:color-mix(in srgb,var(--card) 55%,transparent);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);box-shadow:0 1px 0 rgba(255,255,255,.1) inset}
.fld:focus{outline:2px solid var(--acc);outline-offset:1px}
textarea.fld{border-radius:20px;resize:vertical}
.btn{display:inline-block;font:inherit;font-weight:700;font-size:16px;cursor:pointer;text-decoration:none;padding:11px 24px;border-radius:99px;border:1px solid color-mix(in srgb,var(--acc) 70%,black);background:linear-gradient(to bottom,color-mix(in srgb,var(--acc) 82%,white),var(--acc));color:var(--acc-ink);box-shadow:0 8px 20px -10px rgba(0,0,0,.6),0 1px 0 rgba(255,255,255,.4) inset;transition:transform .15s,filter .15s}
.btn:hover{filter:brightness(1.06);transform:translateY(-1px)}
.btn.ghost{background:var(--card);background:color-mix(in srgb,var(--card) 55%,transparent);color:var(--ink);border-color:rgba(255,255,255,.22);box-shadow:none}
.seg2{display:inline-flex;flex-wrap:wrap;gap:4px;padding:5px;border-radius:99px;border:1px solid rgba(255,255,255,.2);background:var(--card);background:color-mix(in srgb,var(--card) 50%,transparent);backdrop-filter:blur(14px);-webkit-backdrop-filter:blur(14px)}
.seg2 a{padding:8px 18px;border-radius:99px;color:var(--dim);text-decoration:none;font-size:15.5px}
.seg2 a:hover{color:var(--ink)}
.seg2 a.on{background:var(--acc);color:var(--acc-ink);font-weight:700}
.kv{display:grid;grid-template-columns:auto 1fr;gap:6px 18px;font-size:15.5px}.kv dt{color:var(--dim)}.kv dd{margin:0;text-align:right;font-variant-numeric:tabular-nums}
.pill2{display:inline-block;padding:2px 11px;border-radius:99px;font-size:13px;border:1px solid rgba(255,255,255,.2);color:var(--dim)}
@media(max-width:640px){.phead h1{font-size:32px}.psub{font-size:16px}}
`
