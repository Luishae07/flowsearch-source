package main

// /api/batches: the list of every batch the ingest has stored (batches.json, written by the ingest after each batch of pages).
// The /live page reads it: the batch list, the batch charts and the moment the page moves all come from this file.
// ?last=N returns only the newest N batches (default 60); "count" and "pages" are over all batches.

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

var batchesPath = fsData + "/batches.json"

type batchOut struct {
	Seq     int   `json:"seq"`
	At      int64 `json:"at"`
	Pages   int   `json:"pages"`
	Added   int   `json:"added"`
	Updated int   `json:"updated"`
	Dup     int   `json:"duplicates"`
	Bad     int   `json:"rejected"`
	Ms      int64 `json:"ms"`
	Total   int   `json:"total"`
}

var (
	bMu    sync.Mutex
	bAt    time.Time
	bList  []batchOut
	bMtime time.Time
)

func loadBatchList() []batchOut {
	bMu.Lock()
	defer bMu.Unlock()
	if time.Since(bAt) < 2*time.Second {
		return bList
	}
	bAt = time.Now()
	if st, err := os.Stat(batchesPath); err == nil && !st.ModTime().Equal(bMtime) {
		if b, err := os.ReadFile(batchesPath); err == nil {
			var l []batchOut
			if json.Unmarshal(b, &l) == nil {
				bList, bMtime = l, st.ModTime()
			}
		}
	}
	return bList
}

func apiBatches(w http.ResponseWriter, r *http.Request) {
	all := loadBatchList()
	last, _ := strconv.Atoi(r.URL.Query().Get("last"))
	if last <= 0 || last > 500 {
		last = 60
	}
	pages := 0
	for _, b := range all {
		pages += b.Pages
	}
	tail := all
	if len(tail) > last {
		tail = tail[len(tail)-last:]
	}
	if tail == nil {
		tail = []batchOut{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{"count": len(all), "pages": pages, "batches": tail})
}
