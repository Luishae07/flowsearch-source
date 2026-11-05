package main

// Unsafe site warnings. After a results page has loaded, a small script sends the addresses of its results to /api/unsafe; the server asks
// Google's Safe Browsing API (from this machine, so Google never sees the visitor) and answers which of them are known to carry malware or
// phishing. A flagged result gets a red label. Nothing is hidden or reordered. Every answer is kept for 30 minutes.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	sbMu    sync.Mutex
	sbKey   string
	sbKeyOK bool
	sbCache = map[string]sbVerdict{}
)

type sbVerdict struct {
	threat string // "" = not on Google's lists
	at     time.Time
}

func sbKeyValue() string {
	sbMu.Lock()
	defer sbMu.Unlock()
	if !sbKeyOK {
		b, _ := os.ReadFile(fsSecrets + "/google_safebrowsing_key")
		sbKey, sbKeyOK = strings.TrimSpace(string(b)), true
	}
	return sbKey
}

// sbCheck returns, for the addresses Google flags, the kind of threat.
func sbCheck(urls []string) map[string]string {
	out := map[string]string{}
	var ask []string
	sbMu.Lock()
	for _, u := range urls {
		if v, ok := sbCache[u]; ok && time.Since(v.at) < 30*time.Minute {
			if v.threat != "" {
				out[u] = v.threat
			}
		} else {
			ask = append(ask, u)
		}
	}
	sbMu.Unlock()
	key := sbKeyValue()
	if len(ask) == 0 || key == "" {
		return out
	}
	entries := make([]map[string]string, len(ask))
	for i, u := range ask {
		entries[i] = map[string]string{"url": u}
	}
	body, _ := json.Marshal(map[string]any{
		"client": map[string]string{"clientId": "flowsearch", "clientVersion": "1.0"},
		"threatInfo": map[string]any{
			"threatTypes":      []string{"MALWARE", "SOCIAL_ENGINEERING", "UNWANTED_SOFTWARE", "POTENTIALLY_HARMFUL_APPLICATION"},
			"platformTypes":    []string{"ANY_PLATFORM"},
			"threatEntryTypes": []string{"URL"},
			"threatEntries":    entries,
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", "https://safebrowsing.googleapis.com/v4/threatMatches:find?key="+key, bytes.NewReader(body))
	if err != nil {
		return out
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	var res struct {
		Matches []struct {
			ThreatType string `json:"threatType"`
			Threat     struct {
				URL string `json:"url"`
			} `json:"threat"`
		} `json:"matches"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&res) != nil {
		return out
	}
	flagged := map[string]string{}
	for _, m := range res.Matches {
		flagged[m.Threat.URL] = m.ThreatType
	}
	sbMu.Lock()
	if len(sbCache) > 20000 {
		sbCache = map[string]sbVerdict{}
	}
	for _, u := range ask {
		sbCache[u] = sbVerdict{flagged[u], time.Now()}
		if t := flagged[u]; t != "" {
			out[u] = t
		}
	}
	sbMu.Unlock()
	return out
}

func apiUnsafe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if limited(r) {
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":"too many requests"}`)
		return
	}
	var urls []string
	for _, u := range r.URL.Query()["u"] {
		if (strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) && len(u) <= 500 && len(urls) < 20 {
			urls = append(urls, u)
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"flagged": sbCheck(urls)})
}

// safeJS is added to the results page: it asks which results Google flags and puts a red label on them.
const safeJS = `<style>.unsafe{margin-left:8px;font-size:11.5px;font-weight:700;padding:1px 8px;border-radius:99px;background:#e5484d;color:#fff}</style><script>(function(){
  var items=[].slice.call(document.querySelectorAll('article.r a.t'));
  if(!items.length)return;
  var q=items.slice(0,20).map(function(a){return 'u='+encodeURIComponent(a.getAttribute('href'))}).join('&');
  fetch('/api/unsafe?'+q).then(function(r){return r.json()}).then(function(d){
    var f=d.flagged||{},names={MALWARE:'malware',SOCIAL_ENGINEERING:'phishing',UNWANTED_SOFTWARE:'unwanted software',POTENTIALLY_HARMFUL_APPLICATION:'harmful app'};
    items.forEach(function(a){
      var t=f[a.getAttribute('href')];
      if(!t)return;
      var s=document.createElement('span');s.className='unsafe';s.title='Google Safe Browsing lists this address';
      s.textContent='⚠ Google Safe Browsing flags this page ('+(names[t]||'dangerous')+')';
      var site=a.closest('article').querySelector('.site');
      (site||a.parentNode).appendChild(s);
    });
  }).catch(function(){});
})();</script>`
