// sgindexd: the Flowsearch index as its own service. It opens the on-disk index ONCE (about a minute for 57 million pages), keeps it loaded,
// keeps adding the new pages the ingest stores, and answers the web server over HTTP on 127.0.0.1:18820 (JSON). The web server no longer
// loads the index itself, so it restarts in a second; this service almost never restarts.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"mysearch/sgindex"
)

var (
	indexDir  = fsData + "/index4"
	pagesFile = fsData + "/pages4.jsonl"
)

const listen = "127.0.0.1:18820"

var ix *sgindex.Index

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func atoi(r *http.Request, k string) int { n, _ := strconv.Atoi(r.URL.Query().Get(k)); return n }

// the two site lists the web server asks for by name (the same rules as enSite / deHost in the web server)
func enSite(host string) bool {
	h := strings.ToLower(host)
	return strings.HasSuffix(h, ".com") || strings.HasSuffix(h, ".org")
}

func deHost(host string) bool {
	h := strings.ToLower(host)
	for _, t := range []string{".de", ".at", ".ch", ".li"} {
		if strings.HasSuffix(h, t) {
			return true
		}
	}
	return false
}

func main() {
	var err error
	ix, err = sgindex.Open(indexDir, pagesFile)
	if err != nil {
		log.Fatal(err)
	}
	ix.Run(nil) // keeps reading the new pages the ingest writes
	http.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"ndocs": ix.NDocs(), "segments": ix.Segments(), "characters": ix.Characters(), "terms": ix.Terms(), "sites": ix.Sites()})
	})
	http.HandleFunc("/recent", func(w http.ResponseWriter, r *http.Request) { reply(w, ix.RecentList()) })
	http.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		groups, hits, total := ix.Search(r.URL.Query().Get("q"))
		reply(w, map[string]any{"groups": groups, "hits": hits, "total": total})
	})
	http.HandleFunc("/doc", func(w http.ResponseWriter, r *http.Request) {
		d, ok := ix.Doc(uint32(atoi(r, "id")))
		reply(w, map[string]any{"doc": d, "ok": ok})
	})
	http.HandleFunc("/hostdocpage", func(w http.ResponseWriter, r *http.Request) {
		ids, total := ix.HostDocPage(r.URL.Query().Get("domain"), atoi(r, "offset"), atoi(r, "limit"))
		reply(w, map[string]any{"ids": ids, "total": total})
	})
	http.HandleFunc("/suggest", func(w http.ResponseWriter, r *http.Request) {
		reply(w, ix.Suggest(r.URL.Query().Get("prefix"), atoi(r, "limit")))
	})
	http.HandleFunc("/spellfix", func(w http.ResponseWriter, r *http.Request) { reply(w, ix.SpellFix(r.URL.Query().Get("word"))) })
	http.HandleFunc("/hostswithsuffix", func(w http.ResponseWriter, r *http.Request) {
		hosts, pages := ix.HostsWithSuffix(r.URL.Query().Get("domain"))
		reply(w, map[string]int{"hosts": hosts, "pages": pages})
	})
	http.HandleFunc("/hostswhere", func(w http.ResponseWriter, r *http.Request) {
		keep := enSite
		if r.URL.Query().Get("pred") == "de" {
			keep = deHost
		}
		hosts, pages, top := ix.HostsWhere(keep, atoi(r, "k"))
		reply(w, map[string]any{"hosts": hosts, "pages": pages, "top": top})
	})
	http.HandleFunc("/hostpagesany", func(w http.ResponseWriter, r *http.Request) { reply(w, ix.HostPagesAny(r.URL.Query().Get("host"))) })
	log.Printf("sgindexd: %d pages loaded, answering on %s", ix.NDocs(), listen)
	log.Fatal(http.ListenAndServe(listen, nil))
}
