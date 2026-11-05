// sglive: shows page counts of some sites live in the terminal, over the site's own WebSocket (/ws/hosts,
// updates every 50 ms). Usage: sglive host1,host2,...   (installed as `lm`)
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"time"
)

const defaultHosts = "lemmy.world,lemmy.ml,sh.itjust.works,lemmy.dbzer0.com,programming.dev,lemmy.zip,lemm.ee,beehaw.org,sopuli.xyz,slrpnk.net,lemmy.blahaj.zone,feddit.org,feddit.de,discuss.tchncs.de,lemmy.ca,lemmy.today,lemmus.org,midwest.social"

func main() {
	hosts := defaultHosts
	if len(os.Args) > 1 {
		hosts = os.Args[1]
	}
	conn, err := net.Dial("tcp", "127.0.0.1:8812")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Fprintf(conn, "GET /ws/hosts?h=%s HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", hosts)
	br := bufio.NewReader(conn)
	if l, _ := br.ReadString('\n'); !strings.Contains(l, "101") {
		fmt.Println("server said:", strings.TrimSpace(l))
		return
	}
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			return
		}
		if l == "\r\n" {
			break
		}
	}
	type snap struct {
		t time.Time
		n map[string]int
	}
	var hist []snap
	fmt.Print("\033[2J")
	for {
		var h [2]byte
		if _, err := io.ReadFull(br, h[:]); err != nil {
			fmt.Println("connection closed")
			return
		}
		n := int64(h[1] & 0x7f)
		if n == 126 {
			var b [2]byte
			io.ReadFull(br, b[:])
			n = int64(binary.BigEndian.Uint16(b[:]))
		} else if n == 127 {
			var b [8]byte
			io.ReadFull(br, b[:])
			n = int64(binary.BigEndian.Uint64(b[:]))
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(br, payload); err != nil {
			return
		}
		if h[0]&0x0f == 8 {
			return
		}
		if h[0]&0x0f != 1 {
			continue
		}
		var m struct {
			P int            `json:"p"`
			H map[string]int `json:"h"`
		}
		if json.Unmarshal(payload, &m) != nil {
			continue
		}
		now := time.Now()
		hist = append(hist, snap{now, m.H})
		for len(hist) > 1 && now.Sub(hist[0].t) > 10*time.Second {
			hist = hist[1:]
		}
		names := make([]string, 0, len(m.H))
		total := 0
		for k, v := range m.H {
			names = append(names, k)
			total += v
		}
		sort.Slice(names, func(a, b int) bool { return m.H[names[a]] > m.H[names[b]] })
		var b strings.Builder
		b.WriteString("\033[H")
		fmt.Fprintf(&b, "%-26s %10s %9s\033[K\n", "site", "pages", "per sec")
		span := now.Sub(hist[0].t).Seconds()
		for _, k := range names {
			rate := 0.0
			if span > 0.5 {
				rate = float64(m.H[k]-hist[0].n[k]) / span
			}
			fmt.Fprintf(&b, "%-26s %10d %9.1f\033[K\n", k, m.H[k], rate)
		}
		fmt.Fprintf(&b, "%-26s %10d\033[K\n\033[Kall of Flowsearch: %d pages   (Ctrl+C to quit)\033[K\n", "total", total, m.P)
		os.Stdout.WriteString(b.String())
	}
}
