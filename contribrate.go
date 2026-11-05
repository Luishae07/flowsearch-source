package main

// Rate limiting for the contributor API (/api/contrib/), done inside the ingest - no proxy needed.
//   - every IP address gets a number of requests a minute: for everything together, and less for work requests and admin calls
//   - an address that sends too many wrong tokens (HTTP 401) is blocked for a while (this stops guessing of tokens)
//   - requests from this machine itself (no forwarding header) are not limited
// The ingest listens on 127.0.0.1 only; visitors come through a tunnel, which puts the real address in a header (CF-Connecting-IP, X-Real-IP or
// X-Forwarded-For). Those headers are believed only when the connection itself comes from this machine. If the ingest were ever opened to the internet
// directly, the connection's own address is used instead.
// Besides this, every contributor has their own limits (uploads a minute, pages a day) in contrib.go.

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ipState struct {
	hits    map[string][]int64 // kind of request -> times (unix seconds) in the last minute
	fails   []int64            // times of wrong tokens in the last 10 minutes
	blocked int64              // blocked until (unix seconds)
	last    int64
}

var (
	rlMu  sync.Mutex
	rlIPs = map[string]*ipState{}
)

func init() {
	go func() {
		for range time.Tick(5 * time.Minute) {
			now := time.Now().Unix()
			rlMu.Lock()
			for ip, s := range rlIPs {
				if now-s.last > 1800 && s.blocked < now {
					delete(rlIPs, ip)
				}
			}
			if len(rlIPs) > 200000 { // far more than normal: start over rather than grow without end
				rlIPs = map[string]*ipState{}
			}
			rlMu.Unlock()
		}
	}()
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// clientIP: the address of the visitor, and whether it is this machine itself.
func clientIP(r *http.Request) (string, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !isLoopback(host) {
		return host, false
	}
	for _, h := range []string{"CF-Connecting-IP", "X-Real-IP"} {
		if v := strings.TrimSpace(r.Header.Get(h)); net.ParseIP(v) != nil {
			return v, false
		}
	}
	if x := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); net.ParseIP(x) != nil {
		return x, false
	}
	return host, true // no forwarding header: a request from this machine
}

func rateKind(path string) string {
	switch p := strings.TrimPrefix(path, "/api/contrib/"); {
	case p == "ticket":
		return "ticket"
	case strings.HasPrefix(p, "admin/"):
		return "admin"
	}
	return "all"
}

// rateAllow counts one request of this kind from this IP. It returns false (and for how many seconds to wait) when the address is blocked or over a limit.
func rateAllow(ip, kind string, conf contribConfig) (bool, int) {
	now := time.Now().Unix()
	rlMu.Lock()
	defer rlMu.Unlock()
	s := rlIPs[ip]
	if s == nil {
		s = &ipState{hits: map[string][]int64{}}
		rlIPs[ip] = s
	}
	s.last = now
	if s.blocked > now {
		return false, int(s.blocked - now)
	}
	prune := func(l []int64) []int64 {
		out := l[:0]
		for _, t := range l {
			if now-t < 60 {
				out = append(out, t)
			}
		}
		return out
	}
	limit := map[string]int{"all": conf.IPPerMinute, "ticket": conf.TicketPerMinute, "admin": conf.AdminPerMinute}
	for _, k := range []string{"all", kind} {
		s.hits[k] = prune(s.hits[k])
		if lim := limit[k]; lim > 0 && len(s.hits[k]) >= lim {
			wait := 60 - int(now-s.hits[k][0])
			if wait < 1 {
				wait = 1
			}
			return false, wait
		}
	}
	s.hits["all"] = append(s.hits["all"], now)
	if kind != "all" {
		s.hits[kind] = append(s.hits[kind], now)
	}
	return true, 0
}

// rateFail notes a wrong token from this IP; too many in 10 minutes block the address.
func rateFail(ip string, conf contribConfig) {
	now := time.Now().Unix()
	rlMu.Lock()
	defer rlMu.Unlock()
	s := rlIPs[ip]
	if s == nil {
		s = &ipState{hits: map[string][]int64{}}
		rlIPs[ip] = s
	}
	kept := s.fails[:0]
	for _, t := range s.fails {
		if now-t < 600 {
			kept = append(kept, t)
		}
	}
	s.fails = append(kept, now)
	if conf.BadAuthLimit > 0 && len(s.fails) >= conf.BadAuthLimit {
		s.blocked = now + int64(conf.BlockMinutes)*60
		s.fails = nil
	}
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) {
	s.code = c
	s.ResponseWriter.WriteHeader(c)
}

// contribLimited is the handler of /api/contrib/: the limits first, then the real router.
func contribLimited(w http.ResponseWriter, r *http.Request) {
	ip, local := clientIP(r)
	if local {
		contribRouter(w, r)
		return
	}
	cMu.Lock()
	conf := loadConfig()
	cMu.Unlock()
	if ok, wait := rateAllow(ip, rateKind(r.URL.Path), conf); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(wait))
		jsonErr(w, 429, "too many requests from your address; wait "+strconv.Itoa(wait)+" seconds")
		return
	}
	rec := &statusRecorder{ResponseWriter: w, code: 200}
	contribRouter(rec, r)
	if rec.code == 401 {
		rateFail(ip, conf)
	}
}
