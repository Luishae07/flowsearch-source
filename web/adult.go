package main

// The adult filter. Flowsearch does not show adult sites, anywhere: not in the results, the API, the suggestions, the tabs,
// the screenshots or Surprise me. Three layers, cheapest first:
//  1. words and lists: a list of adult hosts, adult hosts' name parts (porn, xxx...), strong adult words in a title or text;
//  2. a decision model (Jev, or Cloudflare's clef-flash when Jev is down) judges the results of a page that the words did
//     not catch; the verdict is kept per page, and a site that is clearly adult goes on the list for good;
//  3. a search that asks for adult content gets no results at all.
// Informational pages (an encyclopedia article, a health page) are not meant to be hidden: the words are strong ones only.

import (
	"bufio"
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
	adultHostsFile    = fsData + "/adult-hosts.txt"
	adultVerdictsFile = fsData + "/adult-verdicts.jsonl"
)

var adultSeedHosts = strings.Fields(`pornhub.com xvideos.com xnxx.com xhamster.com redtube.com youporn.com tube8.com spankbang.com eporner.com
motherless.com brazzers.com onlyfans.com fansly.com chaturbate.com stripchat.com bongacams.com livejasmin.com cam4.com myfreecams.com
rule34.xxx rule34.paheal.net e-hentai.org exhentai.org nhentai.net hentai2read.com e621.net gelbooru.com danbooru.donmai.us
xvideos2.com xnxx.tv beeg.com tnaflix.com txxx.com hclips.com porntrex.com thumbzilla.com fapello.com erome.com sex.com
literotica.com adultfriendfinder.com ashleymadison.com backpage.com eroprofile.com 4tube.com porn.com pornone.com daftsex.com
nsfw.xxx reddit.com/r/nsfw hotmovies.com adameve.com pornpics.com scrolller.com spicychat.ai`)

// words that, as a whole word in a host name, title or description, mark a page as adult
var adultStrong = map[string]bool{
	"porn": true, "porno": true, "pornography": true, "pornographic": true, "pornhub": true, "pornstar": true, "pornstars": true, "xxx": true, "hentai": true, "nsfw": true,
	"onlyfans": true, "xvideos": true, "xnxx": true, "xhamster": true, "camgirl": true, "camgirls": true, "camwhore": true,
	"milf": true, "bdsm": true, "blowjob": true, "blowjobs": true, "handjob": true, "cumshot": true, "creampie": true, "gangbang": true,
	"nudes": true, "nude": true, "nudist": true, "escorts": true, "escort": true, "erotica": true, "fetish": true, "orgasm": true,
	"sexcam": true, "sexcams": true, "livesex": true, "sexting": true, "camsex": true, "anal": true, "bukkake": true, "deepthroat": true,
	"threesome": true, "stripper": true, "strippers": true, "lesbian": true, "shemale": true, "tranny": true, "incest": true,
}

// host name parts that are adult wherever they sit in the name
var adultHostParts = []string{"porn", "xxx", "hentai", "xvideo", "xnxx", "xhamster", "nsfw", "camgirl", "sexcam", "onlyfans", "fuck", "milf", "bdsm", "nudes", "erotic"}
var adultTLDs = []string{".xxx", ".porn", ".sex", ".adult", ".sexy"}

type adultState struct {
	mu       sync.Mutex
	hosts    map[string]bool
	verdicts map[string]adultVerdict // by page address
	loaded   bool
	queue    chan struct{}
}

type adultVerdict struct {
	P  float64   `json:"p"`
	At time.Time `json:"at"`
}

var adult = &adultState{hosts: map[string]bool{}, verdicts: map[string]adultVerdict{}}

func (a *adultState) load() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.loaded {
		return
	}
	a.loaded = true
	for _, h := range adultSeedHosts {
		a.hosts[h] = true
	}
	if f, err := os.Open(adultHostsFile); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if h := strings.ToLower(strings.TrimSpace(sc.Text())); h != "" && !strings.HasPrefix(h, "#") {
				a.hosts[h] = true
			}
		}
		f.Close()
	}
	if f, err := os.Open(adultVerdictsFile); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var j struct {
				U string
				adultVerdict
			}
			if json.Unmarshal(sc.Bytes(), &j) == nil && j.U != "" {
				a.verdicts[j.U] = j.adultVerdict
			}
		}
		f.Close()
	}
}

func adultWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
}

// adultText: a strong adult word as a whole word.
func adultText(s string) bool {
	for _, w := range adultWords(s) {
		if adultStrong[w] {
			return true
		}
	}
	return false
}

// adultHost: on the list (the site or a parent domain), an adult name part, or an adult top-level domain.
func adultHost(host string) bool {
	adult.load()
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	if host == "" {
		return false
	}
	for _, t := range adultTLDs {
		if strings.HasSuffix(host, t) {
			return true
		}
	}
	for _, p := range adultHostParts {
		if strings.Contains(host, p) {
			return true
		}
	}
	adult.mu.Lock()
	defer adult.mu.Unlock()
	for h := host; h != ""; {
		if adult.hosts[h] {
			return true
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			break
		}
		h = h[i+1:]
	}
	return false
}

// adultPage is the quick check (no network) used on every candidate of a search.
func adultPage(host, title, desc string) bool {
	return adultHost(host) || adultText(title) || adultText(desc)
}

// adultQuery: a search that asks for adult content.
func adultQuery(q string) bool { return adultText(q) }

func adultQueryPage(q string) string {
	return `<div class=empty><h2>No results</h2><p>Flowsearch does not show adult content.</p></div>`
}

type adultItem struct{ URL, Host, Title, Desc string }

// adultJudge asks the decision model about pages that the words did not catch. It returns, per page, whether to hide it.
// Pages already judged cost nothing; pages the model cannot judge in time are shown (and judged for the next search).
func adultJudge(items []adultItem, wait time.Duration) []bool {
	adult.load()
	hide := make([]bool, len(items))
	var todo []int
	adult.mu.Lock()
	for i, it := range items {
		if v, ok := adult.verdicts[it.URL]; ok && time.Since(v.At) < 30*24*time.Hour {
			hide[i] = v.P >= 0.5
		} else {
			todo = append(todo, i)
		}
	}
	adult.mu.Unlock()
	if len(todo) == 0 || jevKeyValue() == "" {
		return hide
	}
	done := make(chan map[int]float64, 1)
	go func() { done <- adultAsk(items, todo) }()
	select {
	case got := <-done:
		for i, p := range got {
			hide[i] = p >= 0.5
		}
	case <-time.After(wait):
		// the answer still lands in the cache for the next search
	}
	return hide
}

func adultAsk(items []adultItem, todo []int) map[int]float64 {
	var st strings.Builder
	st.WriteString("Web pages from a search engine for all ages:\n")
	qs := map[string]any{}
	for n, i := range todo {
		it := items[i]
		fmt.Fprintf(&st, "[%d] %s | %s | %s\n", n+1, clipRunes(it.Host, 60), clipRunes(it.Title, 100), clipRunes(it.Desc, 160))
		qs[fmt.Sprintf("a%d", n+1)] = map[string]any{
			"type":         "noul",
			"instructions": fmt.Sprintf("Is page [%d] adult content: pornography, sexual content, nudity, escort or sex dating services, or a site only for adults?", n+1),
			"criteria": map[string]string{
				"true":  "Adult or sexual content, not suitable for all ages",
				"false": "Ordinary content, safe for everyone (news, education and health information are fine)",
			},
		}
	}
	body, _ := json.Marshal(map[string]any{"model": jevModel, "state": st.String(), "questions": qs})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", jevURL, bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+jevKeyValue())
	req.Header.Set("Content-Type", "application/json")
	resp, err := jevClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var out struct {
		Answers map[string]struct {
			Noul *float64 `json:"noul"`
		} `json:"answers"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil {
		return nil
	}
	got := map[int]float64{}
	adult.mu.Lock()
	var add []string
	f, _ := os.OpenFile(adultVerdictsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	for n, i := range todo {
		a, ok := out.Answers[fmt.Sprintf("a%d", n+1)]
		if !ok || a.Noul == nil {
			continue
		}
		p := *a.Noul
		got[i] = p
		it := items[i]
		adult.verdicts[it.URL] = adultVerdict{p, time.Now()}
		if f != nil {
			b, _ := json.Marshal(map[string]any{"u": it.URL, "p": p, "at": time.Now()})
			f.Write(append(b, '\n'))
		}
		if p >= 0.9 && !adult.hosts[strings.ToLower(strings.TrimPrefix(it.Host, "www."))] && !strings.Contains(it.Host, "youtube.") && !strings.Contains(it.Host, "reddit.") && !strings.Contains(it.Host, "wikipedia.") {
			h := strings.ToLower(strings.TrimPrefix(it.Host, "www."))
			adult.hosts[h] = true // a site this sure is adult stays out of the results
			add = append(add, h)
		}
	}
	if f != nil {
		f.Close()
	}
	adult.mu.Unlock()
	if len(add) > 0 {
		if hf, err := os.OpenFile(adultHostsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			hf.WriteString(strings.Join(add, "\n") + "\n")
			hf.Close()
		}
	}
	return got
}

// adultScrub takes adult pages out of the results of one page of an answer.
func adultScrub(a answer) answer {
	if len(a.Results) == 0 {
		return a
	}
	items := make([]adultItem, len(a.Results))
	for i, r := range a.Results {
		items[i] = adultItem{r.URL, r.Host, r.Title, r.Description}
	}
	hide := adultJudge(items, 2500*time.Millisecond)
	keep := a.Results[:0:0]
	for i, r := range a.Results {
		if !hide[i] && !adultHost(r.Host) {
			keep = append(keep, r)
		}
	}
	a.Results = keep
	var rel, nar []string
	for _, q := range a.Related {
		if !adultQuery(q) {
			rel = append(rel, q)
		}
	}
	for _, h := range a.Narrow {
		if !adultHost(h) {
			nar = append(nar, h)
		}
	}
	a.Related, a.Narrow = rel, nar
	return a
}

// adultRecord keeps a verdict that came along with another request (the re-ranking asks the adult question too).
func adultRecord(pageURL string, p float64) {
	adult.load()
	adult.mu.Lock()
	adult.verdicts[pageURL] = adultVerdict{p, time.Now()}
	adult.mu.Unlock()
	if f, err := os.OpenFile(adultVerdictsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		b, _ := json.Marshal(map[string]any{"u": pageURL, "p": p, "at": time.Now()})
		f.Write(append(b, '\n'))
		f.Close()
	}
}
