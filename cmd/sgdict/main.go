// sgdict trains the Flowsearch text dictionary from the pages crawled so far.
//
//	sgdict pages.jsonl out.dict
//
// It counts the words in the first 20,000 pages, and writes the most common ones, rarest first (deflate
// finds matches nearer the end of a dictionary more cheaply), up to 32 KB. It then reports how much
// better text compresses on 2,000 other pages.
package main

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
)

var wordRe = regexp.MustCompile(`[\p{L}\p{N}][\p{L}\p{N}'’-]*`)

func packed(text string, dict []byte) int {
	var buf bytes.Buffer
	w, _ := flate.NewWriterDict(&buf, flate.BestCompression, dict)
	w.Write([]byte(text))
	w.Close()
	return buf.Len()
}

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: sgdict pages.jsonl out.dict")
		os.Exit(1)
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	counts := map[string]int{}
	var holdout []string
	n := 0
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var p struct {
				Title string `json:"title"`
				Text  string `json:"text"`
			}
			if json.Unmarshal(line, &p) == nil && len(p.Text) > 200 {
				n++
				if n <= 20000 {
					for _, w := range wordRe.FindAllString(p.Text, -1) {
						counts[w]++
					}
				} else if n <= 22000 {
					t := p.Text
					if len(t) > 3000 {
						t = t[:3000]
					}
					holdout = append(holdout, t)
				} else {
					break
				}
			}
		}
		if err != nil {
			break
		}
	}
	type wc struct {
		w string
		c int
	}
	var list []wc
	for w, c := range counts {
		if c >= 5 && len(w) >= 3 {
			list = append(list, wc{w, c})
		}
	}
	// the words that save the most bytes (count x length) first, until 32 KB
	sort.Slice(list, func(i, j int) bool { return list[i].c*len(list[i].w) > list[j].c*len(list[j].w) })
	var chosen []string
	size := 0
	for _, x := range list {
		if size+len(x.w)+1 > 32768 {
			break
		}
		chosen = append(chosen, x.w)
		size += len(x.w) + 1
	}
	// best words last
	var dict bytes.Buffer
	for i := len(chosen) - 1; i >= 0; i-- {
		dict.WriteString(chosen[i])
		dict.WriteByte(' ')
	}
	if err := os.WriteFile(os.Args[2], dict.Bytes(), 0o644); err != nil {
		panic(err)
	}
	var raw, without, with int
	for _, t := range holdout {
		raw += len(t)
		without += packed(t, nil)
		with += packed(t, dict.Bytes())
	}
	fmt.Printf("dictionary: %d words, %d bytes\n", len(chosen), dict.Len())
	fmt.Printf("on %d held-out pages: text %d bytes -> deflate %d (%.1fx) -> with dictionary %d (%.1fx); as base-85 text: %d\n",
		len(holdout), raw, without, float64(raw)/float64(without), with, float64(raw)/float64(with), with*5/4)
}
