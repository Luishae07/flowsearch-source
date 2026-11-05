package main

import (
	"fmt"
	"net/http"
)

func statusPage(w http.ResponseWriter, r *http.Request) {
	st := statsJSON()
	tile := func(label, val, note string) string {
		return `<div class=gc style="padding:16px 20px"><small style="color:var(--dim);letter-spacing:.06em;text-transform:uppercase;font-size:12px">` + label + `</small><b style="display:block;font-size:34px;letter-spacing:-.02em;margin-top:4px;font-variant-numeric:tabular-nums">` + val + `</b><span style="color:var(--dim);font-size:14px">` + note + `</span></div>`
	}
	body := `<main class="wrap" style="max-width:900px">` + pageHead("Status", "Is Flowsearch working? Live numbers, straight from the search engine.") +
		`<div class=gc style="display:flex;align-items:center;gap:14px;margin:0 0 14px"><span class=sdot style="background:#30a46c;width:14px;height:14px;box-shadow:0 0 0 6px rgba(48,164,108,.2)"></span><div><b style="font-size:19px">Everything is working</b><br><span style="color:var(--dim);font-size:14.5px">Search and the API are answering.</span></div></div>` +
		`<div class=gcg>` +
		tile("Pages known", humanN(st["pages"].(int)), "in the search index") +
		tile("Websites", humanN(st["sites"].(int)), "covered") +
		tile("New this round", humanN(st["pages_this_run"].(int)), "pages collected so far") +
		tile("Per minute", humanN(st["pages_per_minute"].(int)), "pages collected right now") +
		`</div><p><a class=btn href=/live>See it live</a></p></main>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, page("Status - Flowsearch", "", body))
}
