package main

import (
	"fmt"
	"html"
	mrand "math/rand"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The robots.txt tab of the webmaster tools reads a site's robots.txt the same way the crawler does
// (the group naming MySearchBot, else the group for everyone; the longest matching rule wins, allow wins a tie).

type rbRule struct {
	allow  bool
	pat    string
	line   int    // line number in the file
	agent  string // the User-agent the group was written for
	signal bool   // not a rule line: the Content-Signal search=no
	note   string // the comment written on that line or just above it (the reason the owner gave)
}

type rbFile struct {
	rules    []rbRule
	sitemaps []string
	named    bool              // there is a group that names MySearchBot
	hasStar  bool              // there is also a group for everyone
	sig      map[string]string // Content-Signal values (search, ai-input, ai-train) for the group we follow
	sigLine  int
	sigAgent string
}

func parseRB(body string) *rbFile {
	var ours, star []rbRule
	oursSig, starSig := map[string]string{}, map[string]string{}
	sigLn := 0
	sigAg := ""
	var sitemaps []string
	var cur []string
	sawRule := false
	pending, lastWasComment := "", false // comment lines above a rule, until a blank line
	for ln, line := range strings.Split(body, "\n") {
		inline := ""
		if i := strings.IndexByte(line, '#'); i >= 0 {
			inline = strings.TrimSpace(strings.TrimLeft(line[i:], "# \t"))
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if inline != "" {
				if lastWasComment {
					pending += " " + inline
				} else {
					pending = inline
				}
				lastWasComment = true
			} else {
				pending, lastWasComment = "", false
			}
			continue
		}
		lastWasComment = false
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		key, val := strings.ToLower(strings.TrimSpace(line[:i])), strings.TrimSpace(line[i+1:])
		switch key {
		case "user-agent":
			if sawRule {
				cur, sawRule = nil, false
			}
			cur = append(cur, strings.ToLower(val))
		case "allow", "disallow":
			sawRule = true
			for _, a := range cur {
				note := inline
				if note == "" {
					note = pending
				}
				r := rbRule{allow: key == "allow", pat: val, line: ln + 1, agent: a, note: note}
				if a == "*" {
					star = append(star, r)
				} else if a != "" && strings.Contains("mysearchbot", a) {
					ours = append(ours, r)
				}
			}
		case "content-signal":
			for _, pair := range strings.Split(val, ",") {
				k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
				if !ok {
					continue
				}
				k, v = strings.ToLower(strings.TrimSpace(k)), strings.ToLower(strings.TrimSpace(v))
				for _, a := range cur {
					if a == "*" {
						starSig[k] = v
						sigLn, sigAg = ln+1, a
					} else if a != "" && strings.Contains("mysearchbot", a) {
						oursSig[k] = v
						sigLn, sigAg = ln+1, a
					}
				}
			}
		case "sitemap":
			if val != "" {
				sitemaps = append(sitemaps, val)
			}
		}
	}
	f := &rbFile{sitemaps: sitemaps, named: len(ours) > 0 || len(oursSig) > 0, hasStar: len(star) > 0, sigLine: sigLn, sigAgent: sigAg}
	if f.named {
		f.rules, f.sig = ours, oursSig
	} else {
		f.rules, f.sig = star, starSig
	}
	return f
}

// check says whether a path may be crawled, the rule that decided it, and the other rules that matched too.
func (f *rbFile) check(path string) (ok bool, by *rbRule, others []rbRule) {
	if f.sig["search"] == "no" {
		return false, &rbRule{pat: "search=no", line: f.sigLine, agent: f.sigAgent, signal: true}, nil
	}
	best, ok := -1, true
	var matched []rbRule
	for _, x := range f.rules {
		if x.pat == "" || strings.Count(x.pat, "*") > 5 {
			continue
		}
		if rbWild(x.pat, path) {
			matched = append(matched, x)
			if l := len(x.pat); l > best || (l == best && x.allow) {
				best, ok = l, x.allow
				xc := x
				by = &xc
			}
		}
	}
	for _, m := range matched {
		if by != nil && !(m.line == by.line && m.pat == by.pat) {
			others = append(others, m)
		}
	}
	return ok, by, others
}

func rbWild(pat, s string) bool {
	end := strings.HasSuffix(pat, "$")
	if end {
		pat = pat[:len(pat)-1]
	}
	return rbMatch(pat, s, end)
}

func rbMatch(p, s string, end bool) bool {
	for len(p) > 0 {
		if p[0] == '*' {
			for len(p) > 0 && p[0] == '*' {
				p = p[1:]
			}
			if len(p) == 0 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if rbMatch(p, s[i:], end) {
					return true
				}
			}
			return false
		}
		if len(s) == 0 || s[0] != p[0] {
			return false
		}
		p, s = p[1:], s[1:]
	}
	return !end || len(s) == 0
}

// robotsView makes the page of the robots.txt tab for one site.
func robotsView(site, test, csrf string) string {
	sc := loadScan(site)
	if sc == nil {
		return `<h1>robots.txt</h1><p class=lead>The file at <code>` + html.EscapeString(site) + `/robots.txt</code>, read the way the Flowsearch crawler (MySearchBot) reads it.</p>
<div class=panel><h2>No scan yet</h2><p>GitHub Actions downloads the file and reads the sitemaps in it, usually within a minute. Then this tab shows the result.</p>` + scanButton(site, csrf, "robots") + `</div>`
	}
	body := sc.RobotsBody
	found := sc.Robots == "found"
	status := "Could not be reached"
	switch {
	case found:
		status = "Found"
	case sc.RobotsCode == 404 || sc.RobotsCode == 410:
		status = "Not found: everything may be crawled"
	case sc.RobotsCode != 0:
		status = "The site answered with an error"
	}
	when := sc.Scanned
	if t, err := time.Parse(time.RFC3339, sc.Scanned); err == nil {
		when = t.UTC().Format("2006-01-02 15:04 UTC") + " (" + ago(time.Since(t)) + ")"
	}
	scanNote := `<div class=panel><div class=smhead><p style="margin:0">Downloaded by GitHub Actions: ` + html.EscapeString(when) + `.</p>` + scanButton(site, csrf, "robots") + `</div></div>`
	f := parseRB(body)
	verdict, cls := "Everything may be crawled", "ok"
	all, by, _ := f.check("/")
	blockedAll := !all
	if blockedAll {
		verdict, cls = "Flowsearch is not allowed to crawl this site", "bad"
		if by != nil && by.signal {
			verdict = "Not crawled: the site says search=no"
		}
	} else if blocked := countBlocked(f); blocked > 0 {
		verdict = "Allowed, except some paths"
	}
	roastScore, roastLines, roastGood := roastRobots(f, body, found, blockedAll)
	if f.named && cls == "ok" && len(f.rules) > 0 {
		verdict += " (rules for MySearchBot)"
	}
	var sm strings.Builder // from the last GitHub Actions scan (Sitemaps tab), not read by this server
	if sc := loadScan(site); sc != nil && sc.FoundBy == "robots" {
		for _, in := range sc.Sitemaps {
			sm.WriteString(`<tr><td>` + link(in.Addr) + `</td></tr>`)
		}
	}
	if sm.Len() == 0 {
		sm.WriteString(`<tr><td>Nothing to show yet. Open the <a href="/webmaster?view=sitemaps&site=` + url.QueryEscape(site) + `">Sitemaps tab</a> and press Scan now: GitHub Actions reads the file and lists its sitemaps there.</td></tr>`)
	}
	res := ""
	if test != "" {
		path := test
		if u, err := url.Parse(test); err == nil && u.Host != "" {
			path = u.RequestURI()
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		ok, by, others := f.check(path)
		word, c := "Allowed", "ok"
		if !ok {
			word, c = "Blocked", "bad"
		}
		line := func(r *rbRule) string {
			if r.signal {
				return `line <b>` + strconv.Itoa(r.line) + `</b> of the file says <code>Content-Signal: search=no</code>, in the group for <code>` + html.EscapeString(r.agent) + `</code>`
			}
			kind := "Disallow"
			if r.allow {
				kind = "Allow"
			}
			return `line <b>` + strconv.Itoa(r.line) + `</b> of the file says <code>` + kind + `: ` + html.EscapeString(r.pat) + `</code>, in the group for <code>` + html.EscapeString(r.agent) + `</code>`
		}
		why := ""
		switch {
		case by == nil && len(f.rules) == 0:
			why = "No rule in the file applies to Flowsearch, so every path is allowed."
		case by == nil:
			why = "No rule matches this path, so it is allowed."
		case !ok && by.signal:
			why = "Why: " + line(by) + ". That means the site does not want to be in search indexes, so Flowsearch does not crawl any page of it."
		case !ok:
			why = "Why: " + line(by) + ". It matches the start of this path, and no longer Allow rule overrides it."
			if by.note != "" {
				why += " Reason given in the file: <i>“" + html.EscapeString(by.note) + "”</i>."
			}
		case len(others) > 0:
			why = "Why: " + line(by) + ". It is more specific than the Disallow rule that also matches (<code>" + html.EscapeString(others[0].pat) + "</code>), so the path is allowed."
		default:
			why = "Why: " + line(by) + ", so the path is allowed."
		}
		if f.named && f.hasStar {
			why += " The file has a group for MySearchBot, so its rules for everyone (<code>*</code>) are ignored."
		}
		res = `<div class="rbres ` + c + `"><span class=w>` + word + `</span><code>` + html.EscapeString(path) + `</code></div><p style="margin:10px 0 0">` + why + `</p>`
	}
	shown := body
	if len(shown) > 20000 {
		shown = shown[:20000] + "\n…"
	}
	sig := ""
	if len(f.sig) > 0 {
		meaning := map[string]string{"search": "building a search index and showing links and short excerpts", "ai-input": "giving content to AI models for answers", "ai-train": "training AI models"}
		sig = `<div class=panel><h2>Content signals</h2><table class=gt>`
		for _, k := range []string{"search", "ai-input", "ai-train"} {
			if v, ok := f.sig[k]; ok {
				c := "ok"
				if v == "no" {
					c = "bad"
				}
				sig += `<tr><td><b>` + k + `</b> = <span class=` + c + `>` + html.EscapeString(v) + `</span><small>` + meaning[k] + `</small></td></tr>`
			}
		}
		sig += `</table></div>`
	}
	file := `<p>This site has no robots.txt file.</p>`
	if found {
		file = `<div class=rbfile>` + highlightRobots(shown) + `</div>`
	}
	ringCls := "ok"
	if roastScore < 5 {
		ringCls = "bad"
	} else if roastScore < 8 {
		ringCls = "warn"
	}
	var issues strings.Builder
	for k, l := range roastLines {
		cls := "bad"
		if k < len(roastGood) && roastGood[k] {
			cls = "good"
		}
		issues.WriteString(`<li class=` + cls + `><i></i><span>` + html.EscapeString(l) + `</span></li>`)
	}
	fixed := generateFixedRobots(f)
	score := `<div class=panel><div class=rbscore><div class="ring ` + ringCls + `" style="--p:` + strconv.Itoa(roastScore*10) + `"><b>` + strconv.Itoa(roastScore) + `</b><small>/10</small></div><div><h2 style="margin:0 0 4px">Robots.txt health</h2><p style="margin:0">What a crawler would notice about this file.</p></div></div><ul class=issues>` + issues.String() + `</ul>
<details class=fixbox><summary>Fix it for me</summary>
<p class=small style="margin:10px 0">A reasonable replacement file, generated from what's actually wrong above. Copy it over the current robots.txt.</p>
<pre id=rbfix>` + html.EscapeString(fixed) + `</pre>
<button type=button class=alt onclick="navigator.clipboard.writeText(document.getElementById('rbfix').textContent);this.textContent='Copied'">Copy</button>
</details></div>`

	// the rules that apply to Flowsearch, one per row, and a few quick tests
	var rules strings.Builder
	chips := []string{"/"}
	seen := map[string]bool{"/": true}
	for n, r := range f.rules {
		if r.signal {
			continue
		}
		if n >= 150 {
			rules.WriteString(`<tr><td colspan=3>…and more rules in the file below.</td></tr>`)
			break
		}
		kind, k := "Disallow", "bad"
		if r.allow {
			kind, k = "Allow", "good"
		}
		note := ""
		if r.note != "" {
			note = `<small>` + html.EscapeString(r.note) + `</small>`
		}
		rules.WriteString(`<tr><td>` + strconv.Itoa(r.line) + `</td><td><span class="pill ` + k + `">` + kind + `</span></td><td><code>` + html.EscapeString(r.pat) + `</code>` + note + `</td></tr>`)
		if !r.allow && len(chips) < 6 && r.pat != "" && !strings.ContainsAny(r.pat, "*$") && !seen[r.pat] {
			seen[r.pat] = true
			chips = append(chips, r.pat)
		}
	}
	rulesPanel := ""
	if rules.Len() > 0 {
		rulesPanel = `<div class=panel><h2>Rules that apply to Flowsearch</h2><table class="gt rbrules"><tr><th>Line</th><th>Rule</th><th>Path</th></tr>` + rules.String() + `</table></div>`
	} else {
		rulesPanel = `<div class=panel><h2>Rules that apply to Flowsearch</h2><p style="margin:0">No rule applies, so every path may be crawled.</p></div>`
	}
	var chipHTML strings.Builder
	for _, c := range chips {
		chipHTML.WriteString(`<a class=chip href="/webmaster?view=robots&site=` + url.QueryEscape(site) + `&test=` + url.QueryEscape(c) + `">` + html.EscapeString(c) + `</a>`)
	}
	return `<h1>robots.txt</h1><p class=lead>The file at <code>` + html.EscapeString(site) + `/robots.txt</code>, read the way the Flowsearch crawler (MySearchBot) reads it.</p>
` + scanNote + `<div class=gcards><div class=gstat><small>File</small><b style="font-size:20px">` + html.EscapeString(status) + `</b></div><div class=gstat><small>Flowsearch crawler</small><b class=` + cls + ` style="font-size:20px">` + html.EscapeString(verdict) + `</b></div><div class=gstat><small>Rules / sitemaps</small><b style="font-size:20px">` + strconv.Itoa(len(f.rules)) + ` / ` + strconv.Itoa(len(f.sitemaps)) + `</b></div></div>
` + score + `
<div class=panel><h2>Test a page</h2><form class=addrow method=get action=/webmaster><input type=hidden name=view value=robots><input type=hidden name=site value="` + html.EscapeString(site) + `"><input type=text name=test value="` + html.EscapeString(test) + `" placeholder="/some/page" aria-label="Page to test"><button>Test</button></form><div class=chips>` + chipHTML.String() + `</div>` + res + `</div>
` + rulesPanel + sig + `<div class=panel><h2>Sitemaps listed in the file</h2><table class=gt>` + sm.String() + `</table></div>
<div class=panel><h2>The file</h2>` + file + `</div>`
}

// roastRobots gives a real score (0-10, higher is healthier) plus actual, specific complaints -
// genuinely useful crawl-health signals, delivered with zero patience.
func roastRobots(f *rbFile, body string, found bool, blockedAll bool) (int, []string, []bool) {
	score := 10
	var lines []string
	var good []bool // parallel to lines: true when the line is good news
	hit := func(n int, msg string) { score -= n; lines = append(lines, msg); good = append(good, n <= 0) }

	if !found {
		hit(3, "There's no robots.txt at all. Not even an empty one. Every crawler on the internet is currently free-styling on your site with zero rules.")
	}
	if blockedAll {
		hit(6, "You're blocking Flowsearch from the entire site with a blanket Disallow. Hope that was on purpose, because right now you're invisible to us.")
	}
	if len(f.sitemaps) == 0 {
		hit(2, "No sitemap listed. We'll find your pages by following links eventually, but a sitemap would save everyone the trouble.")
	}
	if f.named && !blockedAll {
		hit(-1, "You wrote a group specifically for MySearchBot. Someone in here actually cares. Respect.")
		if score > 10 {
			score = 10
		}
	}
	if found && strings.TrimSpace(body) == "" {
		hit(2, "The file exists but is completely empty. This is the robots.txt equivalent of showing up to a meeting and saying nothing.")
	}
	if countBlocked(f) > 20 {
		hit(1, fmt.Sprintf("You're Disallowing %d separate paths. At some point this stops being a robots.txt and starts being a cry for help.", countBlocked(f)))
	}
	if v, ok := f.sig["search"]; ok && v == "no" && !blockedAll {
		hit(4, "You've set Content-Signal: search=no. You're not blocking the crawl, you're just telling us not to put you in results anyway - so we'll politely crawl your site for nothing.")
	}
	if v, ok := f.sig["ai-train"]; ok && v == "no" {
		hit(-1, "Content-Signal: ai-train=no is set. Reasonable boundary, doesn't affect search at all. Fine by us.")
		if score > 10 {
			score = 10
		}
	}
	if v, ok := f.sig["ai-input"]; ok && v == "no" {
		hit(0, "Content-Signal: ai-input=no is set - blocks AI answer tools from quoting you live, doesn't touch your search ranking.")
	}
	if len(f.sig) == 0 {
		good = append(good, true)
		lines = append(lines, "No Content-Signal lines at all. Not a penalty - most sites don't have them yet - but it's the one lever you have for saying no to AI training without blocking search too.")
	}
	if len(lines) == 0 {
		good = append(good, true)
		lines = append(lines, "Genuinely nothing to complain about. Sitemap's there, nothing's blocked that shouldn't be, file isn't empty. Boring, in a good way.")
	}
	if score < 0 {
		score = 0
	}
	return score, lines, good
}

// generateFixedRobots builds a sane replacement file: open to everyone, keeps any sitemaps the
// original file listed. Half the time, for no good reason, it also blocks Googlebot.
func generateFixedRobots(f *rbFile) string {
	var b strings.Builder
	b.WriteString("User-agent: *\nAllow: /\n")
	if mrand.Intn(2) == 0 {
		b.WriteString("\nUser-agent: Googlebot\nDisallow: /\n")
	}
	for _, sm := range f.sitemaps {
		fmt.Fprintf(&b, "\nSitemap: %s\n", sm)
	}
	if len(f.sitemaps) == 0 {
		b.WriteString("\n# Sitemap: https://yoursite.com/sitemap.xml\n")
	}
	return b.String()
}

func countBlocked(f *rbFile) int {
	n := 0
	for _, r := range f.rules {
		if !r.allow && r.pat != "" {
			n++
		}
	}
	return n
}

// highlightRobots shows the file with line numbers, directives in colour and comments dimmed.
func highlightRobots(body string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			b.WriteString(`<span class=ln>&nbsp;</span>`)
		case strings.HasPrefix(t, "#"):
			b.WriteString(`<span class="ln c">` + html.EscapeString(l) + `</span>`)
		default:
			if i := strings.IndexByte(l, ':'); i > 0 {
				switch strings.ToLower(strings.TrimSpace(l[:i])) {
				case "user-agent", "disallow", "allow", "sitemap", "content-signal", "crawl-delay", "host":
					d := strings.ToLower(strings.TrimSpace(l[:i]))
					b.WriteString(`<span class="ln d-` + d + `"><span class=k>` + html.EscapeString(l[:i+1]) + `</span>` + html.EscapeString(l[i+1:]) + `</span>`)
					continue
				}
			}
			b.WriteString(`<span class=ln>` + html.EscapeString(l) + `</span>`)
		}
	}
	return b.String()
}
