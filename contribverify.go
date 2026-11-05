package main

// Checking what contributors send. A batch from a contributor who is not trusted yet waits in <data folder>/contrib/quarantine/. The verifier takes the oldest
// batch, fetches a sample of its pages again itself, and compares the text:
//   - a page that is gone (404/410), forbidden by robots.txt, or whose text does not match counts as a failure
//   - a page that cannot be judged (timeout, 403, 429, a server error) is skipped
//   - if too many of the sample fail, the whole batch is thrown away and the contributor's trust falls; otherwise the batch is stored and trust rises
// The server fetches addresses chosen by strangers, so every connection goes through a client that refuses private, loopback, link-local and other
// non-public addresses (also after a redirect), so a contributor cannot use this to reach the machine's own network.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const verifierUA = "MySearchBot/1.0 (+Flowsearch verifier; obeys robots.txt)"

func publicIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96"} {
		if _, n, err := net.ParseCIDR(cidr); err == nil && n.Contains(ip) {
			return false
		}
	}
	return true
}

func safeClient() *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil || len(ips) == 0 {
				return nil, fmt.Errorf("no such host")
			}
			for _, ip := range ips {
				if !publicIP(ip.IP) {
					return nil, errors.New("refused: not a public address")
				}
			}
			return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		DisableKeepAlives:     true,
	}
	return &http.Client{Transport: tr, Timeout: 25 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		return nil
	}}
}

// robotsAllows: does the host's robots.txt let MySearchBot fetch this path? A robots.txt that cannot be read counts as allowed.
func robotsAllows(cl *http.Client, cache map[string][2][]string, scheme, host, path string) bool {
	rules, ok := cache[host]
	if !ok {
		var star, own []string // lines "allow:/x" or "disallow:/x"
		req, _ := http.NewRequest("GET", scheme+"://"+host+"/robots.txt", nil)
		req.Header.Set("User-Agent", verifierUA)
		if resp, err := cl.Do(req); err == nil {
			if resp.StatusCode == 200 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 300_000))
				group := ""
				for _, line := range strings.Split(string(b), "\n") {
					line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
					k, v, found := strings.Cut(line, ":")
					if !found {
						continue
					}
					k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
					switch k {
					case "user-agent":
						switch lv := strings.ToLower(v); {
						case lv == "*":
							group = "*"
						case strings.Contains(lv, "mysearchbot"):
							group = "own"
						default:
							group = "other"
						}
					case "allow", "disallow":
						if v == "" {
							continue
						}
						if group == "*" {
							star = append(star, k+":"+v)
						} else if group == "own" {
							own = append(own, k+":"+v)
						}
					}
				}
			}
			resp.Body.Close()
		}
		rules = [2][]string{star, own}
		cache[host] = rules
	}
	use := rules[0]
	if len(rules[1]) > 0 {
		use = rules[1]
	}
	best, allowed := -1, true
	for _, r := range use {
		k, v, _ := strings.Cut(r, ":")
		if strings.HasPrefix(path, strings.TrimSuffix(v, "$")) && len(v) > best {
			best, allowed = len(v), k == "allow"
		}
	}
	return allowed
}

var (
	reScript = regexp.MustCompile(`(?is)<(script|style|noscript)[^>]*>.*?</(script|style|noscript)>|<!--.*?-->`)
	reTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	reWord   = regexp.MustCompile(`[\p{L}\p{N}]{3,}`)
)

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range reWord.FindAllString(strings.ToLower(s), -1) {
		m[w] = true
	}
	return m
}

// verifyPage: 1 = matches, 0 = does not match, -1 = cannot be judged
func verifyPage(cl *http.Client, robots map[string][2][]string, p Page) (int, string) {
	u, err := url.Parse(p.URL)
	if err != nil || u.Host == "" {
		return 0, "bad address"
	}
	if !robotsAllows(cl, robots, u.Scheme, u.Host, u.EscapedPath()) {
		return 0, "forbidden by robots.txt"
	}
	req, _ := http.NewRequest("GET", p.URL, nil)
	req.Header.Set("User-Agent", verifierUA)
	req.Header.Set("Accept", "text/html,text/plain;q=0.9,*/*;q=0.5")
	resp, err := cl.Do(req)
	if err != nil {
		return -1, "could not fetch: " + err.Error()
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 404 || resp.StatusCode == 410:
		return 0, fmt.Sprintf("the page does not exist (HTTP %d)", resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return -1, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); ct != "" && !strings.Contains(ct, "html") && !strings.Contains(ct, "text/") && !strings.Contains(ct, "xml") {
		return -1, "not a text page"
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1_500_000))
	page := html.UnescapeString(reTag.ReplaceAllString(reScript.ReplaceAllString(string(b), " "), " "))
	have := wordSet(page)
	mine := wordSet(p.Title + " " + clip(p.Text, 2000))
	if len(mine) == 0 {
		return 0, "no text"
	}
	found := 0
	for w := range mine {
		if have[w] {
			found++
		}
	}
	ratio := float64(found) / float64(len(mine))
	need := 0.6
	if len(mine) < 5 {
		need = 0.5
	}
	if ratio < need {
		return 0, fmt.Sprintf("the text does not match (%.0f%% of the words found)", ratio*100)
	}
	return 1, ""
}

// sampleOf picks which pages of a batch to check: about SamplePercent percent, at least 3, at most 25.
func sampleOf(n int, percent int) []int {
	k := n * percent / 100
	if k < 3 {
		k = 3
	}
	if k > 25 {
		k = 25
	}
	if k > n {
		k = n
	}
	return rand.Perm(n)[:k]
}

// checkPages fetches the chosen pages again and returns how many matched, failed and could not be judged.
func checkPages(pages []Page, idx []int) (ok, fail, skip int) {
	cl := safeClient()
	robots := map[string][2][]string{}
	for _, i := range idx {
		res, why := verifyPage(cl, robots, pages[i])
		switch res {
		case 1:
			ok++
		case 0:
			fail++
			log.Printf("contrib: check failed for %s: %s", pages[i].URL, why)
		default:
			skip++
		}
		time.Sleep(700 * time.Millisecond)
	}
	return
}

// addResult changes a contributor's record after a check.
func addResult(cid string, ok, fail int, batchOK bool) {
	cMu.Lock()
	defer cMu.Unlock()
	c := contribs[cid]
	if c == nil {
		return
	}
	c.OK += ok
	c.Fail += fail
	if batchOK {
		c.BatchesOK++
	}
	if c.OK+c.Fail >= 10 && c.trust() < 0.3 && c.Status == "active" {
		c.Status = "suspended"
		log.Printf("contrib: %s (%s) is suspended: too many of the checked pages did not match", c.Name, c.ID)
	}
	saveContribs()
}

// auditSample checks a few pages of a batch that was stored at once (for a trusted contributor) and updates the trust.
func auditSample(cid string, pages []Page) {
	idx := sampleOf(len(pages), 2)
	if len(idx) > 3 {
		idx = idx[:3]
	}
	ok, fail, _ := checkPages(pages, idx)
	addResult(cid, ok, fail, false)
	if fail > 0 {
		log.Printf("contrib: audit of a stored batch from %s: %d matched, %d did not", cid, ok, fail)
	}
}

func verifyLoop() {
	attempts := map[string]int{}
	for {
		time.Sleep(15 * time.Second)
		files, _ := filepath.Glob(cDir + "/quarantine/*.json")
		if len(files) == 0 {
			continue
		}
		sort.Strings(files)
		path := files[0]
		b, err := os.ReadFile(path)
		var batch qBatch
		if err != nil || json.Unmarshal(b, &batch) != nil || len(batch.Pages) == 0 {
			os.Remove(path)
			continue
		}
		cMu.Lock()
		conf := loadConfig()
		cMu.Unlock()
		idx := sampleOf(len(batch.Pages), conf.SamplePercent)
		ok, fail, skip := checkPages(batch.Pages, idx)
		judged := ok + fail
		need := 2
		if len(idx) < need {
			need = len(idx)
		}
		switch {
		case judged < need && attempts[path] < 3:
			attempts[path]++
			log.Printf("contrib: batch %s could not be judged yet (%d matched, %d failed, %d skipped); trying again later", filepath.Base(path), ok, fail, skip)
			time.Sleep(time.Minute)
		case judged < need || fail*100 > judged*30:
			delete(attempts, path)
			os.Remove(path)
			addResult(batch.Contrib, ok, fail, false)
			log.Printf("contrib: batch of %d pages from %s thrown away (%d matched, %d failed, %d skipped)", len(batch.Pages), batch.Contrib, ok, fail, skip)
		default:
			delete(attempts, path)
			if err := contribStore(batch.Pages, true); err != nil {
				log.Printf("contrib: could not store a checked batch: %v", err)
				continue
			}
			os.Remove(path)
			addResult(batch.Contrib, ok, fail, true)
			log.Printf("contrib: batch of %d pages from %s stored (%d matched, %d failed, %d skipped)", len(batch.Pages), batch.Contrib, ok, fail, skip)
		}
	}
}
