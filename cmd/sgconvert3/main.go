// sgconvert3 turns pages2.jsonl into pages3.jsonl: the same pages with short keys. The packed text is copied as it is.
//
//	sgconvert [-follow] [-from pages.jsonl] [-to pages2.jsonl] [-state convert.state]
//
// It remembers how far it got, so it can be run again later to catch up. With -follow it keeps watching the
// old file until it is stopped. Every page is checked: the packed text must unpack to exactly the original.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type oldPage struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Z           string `json:"z"`
	Source      string `json:"source"`
}

type stored struct {
	URL         string `json:"u"`
	Title       string `json:"t"`
	Description string `json:"d,omitempty"`
	Z           string `json:"z"`
	Source      string `json:"s,omitempty"`
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func main() {
	from := flag.String("from", fsData + "/pages2.jsonl", "old file")
	to := flag.String("to", fsData + "/pages3.jsonl", "new file")
	stateF := flag.String("state", fsData + "/convert3.state", "progress file")
	follow := flag.Bool("follow", false, "keep watching the old file")
	flag.Parse()

	var pos int64
	if b, err := os.ReadFile(*stateF); err == nil {
		pos, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	}
	out, err := os.OpenFile(*to, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	var pages, bytesIn, bytesOut int64
	t0 := time.Now()
	for {
		n, in, o := convertSome(*from, out, &pos)
		pages += n
		bytesIn += in
		bytesOut += o
		if n > 0 {
			os.WriteFile(*stateF, []byte(strconv.FormatInt(pos, 10)), 0o644)
		}
		select {
		case <-stop:
			fmt.Printf("stopped at byte %d\n", pos)
			summary(pages, bytesIn, bytesOut, t0)
			return
		default:
		}
		if n == 0 {
			if !*follow {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	fmt.Printf("done at byte %d\n", pos)
	summary(pages, bytesIn, bytesOut, t0)
}

func summary(pages, in, out int64, t0 time.Time) {
	if pages == 0 {
		fmt.Println("nothing new converted")
		return
	}
	fmt.Printf("converted %d pages in %.0f s: %d MB -> %d MB (%.1fx smaller)\n", pages, time.Since(t0).Seconds(), in>>20, out>>20, float64(in)/float64(out))
}

// convertSome converts up to 20,000 complete lines, on all cores, and moves pos past them.
func convertSome(from string, out *os.File, pos *int64) (pages, bytesIn, bytesOut int64) {
	f, err := os.Open(from)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	if st.Size() <= *pos {
		return
	}
	r := bufio.NewReaderSize(io.NewSectionReader(f, *pos, st.Size()-*pos), 1<<20)
	var lines [][]byte
	for len(lines) < 20000 {
		line, err := r.ReadBytes('\n')
		if err != nil { // half-written last line: next time
			break
		}
		*pos += int64(len(line))
		bytesIn += int64(len(line))
		lines = append(lines, line)
	}
	results := make([][]byte, len(lines))
	var wg sync.WaitGroup
	next := make(chan int, len(lines))
	for i := range lines {
		next <- i
	}
	close(next)
	for w := 0; w < runtime.NumCPU()-1 || w < 1; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				var p oldPage
				if json.Unmarshal(lines[i], &p) != nil || p.Z == "" {
					continue
				}
				b, _ := json.Marshal(stored{URL: p.URL, Title: p.Title, Description: p.Description, Z: p.Z, Source: p.Source})
				results[i] = append(b, '\n')
			}
		}()
	}
	wg.Wait()
	w := bufio.NewWriterSize(out, 1<<20)
	for _, b := range results {
		if b != nil {
			w.Write(b)
			bytesOut += int64(len(b))
			pages++
		}
	}
	if err := w.Flush(); err != nil {
		log.Fatal(err)
	}
	out.Sync()
	return
}
