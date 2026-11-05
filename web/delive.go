package main

import (
	"sync"
	"time"
)

// The German tab of the live page: the same numbers, only for German-speaking domains (see deHost).

type deMsg struct {
	P      int          `json:"p"`
	S      int          `json:"s"`
	PS     float64      `json:"ps"`
	PM     int          `json:"pm"`
	PH     int          `json:"ph"`
	Top    []hostCount  `json:"top"`
	Recent []recentPage `json:"recent"`
}

var (
	deMu       sync.Mutex
	deSamples  []mark
	deSecShort []mark // last ~1s of samples only, for the per-second rate (deSamples covers up to an hour)
	deRecent   []recentPage
	deSeen     = map[string]bool{}
)

func deCompute() *deMsg {
	deMu.Lock()
	defer deMu.Unlock()
	now := time.Now()
	sites, pages, top := ix.HostsWhere("de", 10)
	deSamples = append(deSamples, mark{now, pages})
	for len(deSamples) > 1 && now.Sub(deSamples[0].t) > time.Hour+5*time.Second {
		deSamples = deSamples[1:]
	}
	since := func(d time.Duration) int {
		base := deSamples[0].n
		for _, s := range deSamples {
			if now.Sub(s.t) >= d {
				base = s.n
			}
		}
		return pages - base
	}
	for _, p := range recentNow() {
		if deHost(p.Host) && !deSeen[p.URL] {
			deSeen[p.URL] = true
			deRecent = append(deRecent, p)
		}
	}
	if len(deRecent) > 20 {
		deRecent = deRecent[len(deRecent)-20:]
	}
	if len(deSeen) > 2000 {
		deSeen = map[string]bool{}
	}
	deSecShort = append(deSecShort, mark{now, pages})
	// the per-second rate is averaged over the last 30 seconds, because the index follows the file
	// in bursts and a shorter window shows those bursts as a much higher rate than is really arriving.
	for len(deSecShort) > 1 && now.Sub(deSecShort[0].t) > 30*time.Second {
		deSecShort = deSecShort[1:]
	}
	rate := 0.0
	if d := now.Sub(deSecShort[0].t).Seconds(); d > 0.2 {
		rate = float64(pages-deSecShort[0].n) / d
	}
	m := &deMsg{P: pages, S: sites, PS: float64(int(rate*10)) / 10, PM: since(time.Minute), PH: since(time.Hour), Recent: append([]recentPage{}, deRecent...)}
	for _, h := range top {
		m.Top = append(m.Top, hostCount{h.Host, h.Pages})
	}
	return m
}
