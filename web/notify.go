package main

// Daily auto-recrawl of every webmaster-submitted site, plus a small notification center so
// webmasters can see when their site was last recrawled without having to go check the Pages tab.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	notifDir       = fsData + "/notifications/"
	notifSeenDir   = fsData + "/notif-seen/"
	lastRecrawlLog = fsData + "/last-daily-recrawl.txt"
)

const maxNotifsKept = 200

type notification struct {
	Time int64  `json:"t"`
	Site string `json:"site"`
	Msg  string `json:"msg"`
}

var notifyMu sync.Mutex

func notifyPath(user string) string {
	os.MkdirAll(notifDir, 0o755)
	return notifDir + user + ".jsonl"
}

func seenPath(user string) string {
	os.MkdirAll(notifSeenDir, 0o755)
	return notifSeenDir + user + ".txt"
}

// addNotification appends one entry for a user, trimming to the most recent maxNotifsKept.
func addNotification(user, site, msg string) {
	notifyMu.Lock()
	defer notifyMu.Unlock()
	list := loadNotificationsLocked(user)
	list = append(list, notification{Time: time.Now().Unix(), Site: site, Msg: msg})
	if len(list) > maxNotifsKept {
		list = list[len(list)-maxNotifsKept:]
	}
	f, err := os.Create(notifyPath(user))
	if err != nil {
		return
	}
	defer f.Close()
	for _, n := range list {
		b, _ := json.Marshal(n)
		f.Write(append(b, '\n'))
	}
}

func loadNotificationsLocked(user string) []notification {
	var out []notification
	f, err := os.Open(notifyPath(user))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1<<20)
	for sc.Scan() {
		var n notification
		if json.Unmarshal(sc.Bytes(), &n) == nil {
			out = append(out, n)
		}
	}
	return out
}

func loadNotifications(user string) []notification {
	notifyMu.Lock()
	defer notifyMu.Unlock()
	return loadNotificationsLocked(user)
}

func lastSeen(user string) int64 {
	b, err := os.ReadFile(seenPath(user))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

func markSeenNow(user string) {
	os.WriteFile(seenPath(user), []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o644)
}

func unreadCount(user string) int {
	seen := lastSeen(user)
	n := 0
	for _, x := range loadNotifications(user) {
		if x.Time > seen {
			n++
		}
	}
	return n
}

// dailyRecrawlLoop recrawls every webmaster-submitted site once a day, using the same per-site
// GitHub Actions workflow the manual "Recrawl now" button uses. A marker file on disk remembers
// the last date it ran, so a restart mid-day doesn't trigger a second run for the same day.
func dailyRecrawlLoop() {
	runIfDue := func() {
		today := time.Now().Format("2006-01-02")
		if b, err := os.ReadFile(lastRecrawlLog); err == nil && strings.TrimSpace(string(b)) == today {
			return
		}
		runDailyRecrawl()
		os.WriteFile(lastRecrawlLog, []byte(today), 0o644)
	}
	runIfDue()
	tick := time.NewTicker(1 * time.Hour)
	defer tick.Stop()
	for range tick.C {
		runIfDue()
	}
}

func runDailyRecrawl() {
	for _, dir := range []string{submittedUsersDir, submittedUsersDeDir} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
				continue
			}
			user := strings.TrimSuffix(e.Name(), ".txt")
			f, err := os.Open(dir + e.Name())
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(f)
			done := map[string]bool{}
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if line == "" {
					continue
				}
				site, _, _ := strings.Cut(line, "\t")
				if site == "" || done[site] {
					continue
				}
				done[site] = true
				recordSiteHistory(site)
				triggerSiteCrawl(site, "any", line)
				addNotification(user, site, "Daily recrawl started")
				time.Sleep(2 * time.Second) // stagger GitHub Actions triggers instead of firing hundreds at once
			}
			f.Close()
		}
	}
}

func wmNotificationsView(user string) string {
	list := loadNotifications(user)
	markSeenNow(user)
	if len(list) == 0 {
		return `<h1>Notifications</h1><p class=lead>Nothing yet. Every site you submit gets recrawled automatically once a day, and shows up here when it happens.</p>`
	}
	var rows strings.Builder
	for i := len(list) - 1; i >= 0; i-- {
		n := list[i]
		t := time.Unix(n.Time, 0).Format("2006-01-02 15:04")
		rows.WriteString(fmt.Sprintf(`<tr><td>%s</td><td><a href="/webmaster?view=overview&site=%s">%s</a></td><td>%s</td></tr>`,
			html.EscapeString(t), url.QueryEscape(n.Site), html.EscapeString(n.Site), html.EscapeString(n.Msg)))
	}
	return `<h1>Notifications</h1><p class=lead>Every site you submit gets recrawled automatically once a day.</p>
<div class=panel><table class=gt><tr><th>When</th><th>Site</th><th>Event</th></tr>` + rows.String() + `</table></div>`
}

var siteHistoryDir = fsData + "/site-history/"

type historyPoint struct {
	Date  string `json:"d"`
	Pages int    `json:"p"`
}

// recordSiteHistory appends today's page count for a site, skipping if today was already recorded
// (so a redeploy mid-day, which re-runs the recrawl loop, doesn't double up on points).
func recordSiteHistory(site string) {
	os.MkdirAll(siteHistoryDir, 0o755)
	path := siteHistoryDir + site + ".jsonl"
	today := time.Now().Format("2006-01-02")
	if pts := loadSiteHistory(site); len(pts) > 0 && pts[len(pts)-1].Date == today {
		return
	}
	_, pages := ix.HostsWithSuffix(site)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(historyPoint{Date: today, Pages: pages})
	f.Write(append(b, '\n'))
}

func loadSiteHistory(site string) []historyPoint {
	var out []historyPoint
	f, err := os.Open(siteHistoryDir + site + ".jsonl")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var p historyPoint
		if json.Unmarshal(sc.Bytes(), &p) == nil {
			out = append(out, p)
		}
	}
	if len(out) > 90 {
		out = out[len(out)-90:]
	}
	return out
}

// siteHistorySVG draws a tiny inline line chart of a site's page count over time, no JS or chart library.
func siteHistorySVG(site string) string {
	pts := loadSiteHistory(site)
	if len(pts) == 0 {
		return ""
	}
	if len(pts) == 1 {
		return `<div class=panel><h2>Pages over time</h2><p class=small style="color:var(--dim)">Only one day recorded so far (` +
			html.EscapeString(pts[0].Date) + `: ` + commas(pts[0].Pages) + ` pages). Check back tomorrow for a trend.</p></div>`
	}
	const w, h, pad = 560, 120, 10
	min, max := pts[0].Pages, pts[0].Pages
	for _, p := range pts {
		if p.Pages < min {
			min = p.Pages
		}
		if p.Pages > max {
			max = p.Pages
		}
	}
	if max == min {
		max = min + 1
	}
	var path strings.Builder
	n := len(pts)
	for i, p := range pts {
		x := pad + float64(i)*float64(w-2*pad)/float64(n-1)
		y := float64(h-pad) - float64(p.Pages-min)*float64(h-2*pad)/float64(max-min)
		if i == 0 {
			path.WriteString(fmt.Sprintf("M%.1f,%.1f", x, y))
		} else {
			path.WriteString(fmt.Sprintf(" L%.1f,%.1f", x, y))
		}
	}
	first, last := pts[0], pts[n-1]
	return fmt.Sprintf(`<div class=panel><h2>Pages over time</h2>
<svg viewBox="0 0 %d %d" width="100%%" height="%d" preserveAspectRatio="none" style="display:block">
<path d="%s" fill="none" stroke="var(--acc)" stroke-width="2"/>
</svg>
<p class=small style="color:var(--dim);margin-top:6px">%s: %s pages &rarr; %s: %s pages</p>
</div>`, w, h, h, path.String(), first.Date, commas(first.Pages), last.Date, commas(last.Pages))
}

func registerNotify() {
	go dailyRecrawlLoop()
	http.HandleFunc("/webmaster/notifications/markread", func(w http.ResponseWriter, r *http.Request) {
		user := currentUser(r)
		if user == "" {
			http.Error(w, "forbidden", 403)
			return
		}
		markSeenNow(user)
		w.WriteHeader(204)
	})
}
