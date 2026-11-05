package main

// What does a visitor cost this site? The Cost tab of the webmaster tools loads the site's home page the
// way a browser would (the HTML and up to 25 images, scripts, styles and fonts it links to), adds up the
// bytes sent, and turns that into money with a cloud-bandwidth price. It is an estimate of bandwidth only.
// The daily table in the flowsearch repo (SITE-COSTS.md) also runs the page's JavaScript in a real browser.

import (
	"compress/gzip"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const wmCostEgressPerGB = 0.085 // USD per GB sent out: a typical cloud / CDN list price

var wmCostAsset = regexp.MustCompile(`(?i)(?:src|href)=["']([^"']+\.(?:js|css|png|jpe?g|gif|webp|avif|svg|ico|woff2?)(?:\?[^"']*)?)["']`)

type wmCostResult struct {
	At        time.Time
	HTMLBytes int64
	Files     int
	Total     int64
	Err       string
}

var (
	wmCostMu    sync.Mutex
	wmCostCache = map[string]wmCostResult{}
	// a webmaster can type any address, so the crawler-side fetch must never reach our own network
	wmCostClient = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DisableCompression:  true, // we want the bytes as sent
			MaxIdleConnsPerHost: 2,
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second, Control: wmCostNoPrivate}).DialContext,
		},
	}
)

func wmCostNoPrivate(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return errors.New("that address is not allowed")
	}
	return nil
}

func wmCostGet(rawURL, method string, limit int64) (*http.Response, []byte, error) {
	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "MySearchBot-cost/1.0")
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := wmCostClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if method == http.MethodHead {
		return resp, nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return resp, body, err
}

// wmCostMeasure returns the (cached for 6 hours) page weight of https://host/.
func wmCostMeasure(host string, fresh *bool) wmCostResult {
	wmCostMu.Lock()
	if c, ok := wmCostCache[host]; ok && time.Since(c.At) < 6*time.Hour {
		wmCostMu.Unlock()
		return c
	}
	wmCostMu.Unlock()
	*fresh = true

	res := wmCostResult{At: time.Now()}
	resp, body, err := wmCostGet("https://"+host+"/", http.MethodGet, 3_000_000)
	if err != nil {
		res.Err = err.Error()
	} else {
		res.HTMLBytes = int64(len(body))
		text := body
		if resp.Header.Get("Content-Encoding") == "gzip" {
			if zr, err := gzip.NewReader(strings.NewReader(string(body))); err == nil {
				if plain, err := io.ReadAll(io.LimitReader(zr, 6_000_000)); err == nil {
					text = plain
				}
			}
		}
		seen := map[string]bool{}
		var refs []string
		for _, m := range wmCostAsset.FindAllStringSubmatch(string(text), -1) {
			u, err := resp.Request.URL.Parse(m[1])
			if err != nil || seen[u.String()] {
				continue
			}
			seen[u.String()] = true
			refs = append(refs, u.String())
		}
		sort.Strings(refs)
		if len(refs) > 25 {
			refs = refs[:25]
		}
		sizes := make([]int64, len(refs))
		var wg sync.WaitGroup
		sem := make(chan struct{}, 6)
		for i, r := range refs {
			wg.Add(1)
			go func(i int, r string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if hr, _, err := wmCostGet(r, http.MethodHead, 0); err == nil && hr.ContentLength > 0 {
					sizes[i] = hr.ContentLength
				}
			}(i, r)
		}
		wg.Wait()
		res.Total = res.HTMLBytes
		for _, n := range sizes {
			res.Total += n
			if n > 0 {
				res.Files++
			}
		}
	}
	wmCostMu.Lock()
	wmCostCache[host] = res
	wmCostMu.Unlock()
	return res
}

func wmUSD(v float64) string {
	if v < 0.01 {
		return fmt.Sprintf("$%.4f", v)
	}
	if v < 100 {
		return fmt.Sprintf("$%.2f", v)
	}
	return "$" + commas(int(v+0.5))
}

// wmCostHTML is the Cost tab. limited says this visitor already asked for many fresh measurements.
func wmCostHTML(r *http.Request, site string) string {
	visits := 1000
	if n, err := strconv.Atoi(r.URL.Query().Get("visits")); err == nil && n >= 1 && n <= 100_000_000 {
		visits = n
	}
	var fresh bool
	var res wmCostResult
	wmCostMu.Lock()
	c, cached := wmCostCache[site]
	wmCostMu.Unlock()
	if cached && time.Since(c.At) < 6*time.Hour {
		res = c
	} else {
		res = wmCostMeasure(site, &fresh)
	}
	head := `<h1>Cost</h1><p class=lead>What a visitor costs ` + html.EscapeString(site) + ` in bandwidth, from its home page.</p>`
	if res.Err != "" {
		return head + `<div class=panel><p>Could not load https://` + html.EscapeString(site) + `/ to measure it: ` + html.EscapeString(res.Err) + `</p></div>`
	}
	perVisit := float64(res.Total) / 1e9 * wmCostEgressPerGB
	perHour := perVisit * float64(visits)
	box := func(label, value string) string {
		return `<div class=kpi><small>` + label + `</small><b>` + value + `</b></div>`
	}
	return head + `<div class=panel style="display:flex;gap:26px;flex-wrap:wrap">` +
		box("One visit downloads", fmt.Sprintf("%s KB", commas(int(res.Total/1024)))) +
		box("Cost of 1,000 visits", wmUSD(perVisit*1000)) +
		box("Cost per hour", wmUSD(perHour)) +
		box("Cost per 10 hours", wmUSD(perHour*10)) + `</div>
<div class=panel><form method=get action=/webmaster style="display:flex;gap:10px;align-items:center;flex-wrap:wrap">
<input type=hidden name=view value=cost><input type=hidden name=site value="` + html.EscapeString(site) + `">
<label>If your site gets <input type=number name=visits min=1 value="` + strconv.Itoa(visits) + `" style="width:120px"> visits per hour</label><button>Recalculate</button></form></div>
<div class=panel><p>One visit loads the page and ` + strconv.Itoa(res.Files) + ` files it links to (` + commas(int(res.HTMLBytes/1024)) + ` KB of that is the page itself), counted as they are sent. The price is <b>` + wmUSD(wmCostEgressPerGB) + ` per GB</b>, a typical cloud list price. This counts bandwidth only: servers, storage and your time cost extra, and anything your scripts load after the page opens is not included. Measured ` + res.At.Format("2 Jan 15:04") + `; refreshed every 6 hours. The <a href="https://github.com/Luishae07/flowsearch/blob/main/SITE-COSTS.md" target=_blank rel="noopener noreferrer">daily table</a> measures popular sites with their JavaScript running.</p></div>`
}

var _ = url.QueryEscape
