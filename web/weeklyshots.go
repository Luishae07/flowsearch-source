package main

// Once a week every site that a webmaster added gets a fresh screenshot (JPEG XL, same file as the screenshot
// button), taken three at a time.

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var weeklyStamp = fsShots + "/.weekly"

func allWebmasterSites() []string {
	seen := map[string]bool{}
	if f, err := os.Open(ownedPath); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var o ownedRow
			if json.Unmarshal(sc.Bytes(), &o) == nil && o.S != "" && !isBlocked(o.S) {
				seen[o.S] = true
			}
		}
		f.Close()
	}
	var out []string
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func weeklyShots() {
	for {
		last := int64(0)
		if b, err := os.ReadFile(weeklyStamp); err == nil {
			last, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		}
		if time.Since(time.Unix(last, 0)) >= 7*24*time.Hour {
			sites := allWebmasterSites()
			log.Printf("weekly screenshots: %d sites", len(sites))
			var wg sync.WaitGroup
			jobs := make(chan string)
			for i := 0; i < 3; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for s := range jobs {
						err := takeScreenshot(s)
						if err != nil {
							err = takeScreenshot(s)
						}
						if err != nil {
							log.Printf("weekly screenshot of %s failed: %v", s, err)
						}
					}
				}()
			}
			for _, s := range sites {
				jobs <- s
			}
			close(jobs)
			wg.Wait()
			os.MkdirAll(fsShots + "", 0o755)
			os.WriteFile(weeklyStamp, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o644)
		}
		time.Sleep(time.Hour)
	}
}
