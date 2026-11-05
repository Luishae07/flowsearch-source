// sgdict2 trains a new text dictionary from the CURRENT pages4.jsonl (compressed records), unlike sgdict
// which expects raw plain-text pages.jsonl. It unpacks each record with the OLD embedded dictionary to get
// the text, same word-selection algorithm as sgdict otherwise.
//
//	sgdict2 pages4.jsonl out.dict
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

	"mysearch/sgpack"
)

var wordRe = regexp.MustCompile(`[\p{L}\p{N}][\p{L}\p{N}'’-]*`)

func packed(text string, dict []byte) int {
	var buf bytes.Buffer
	w, _ := flate.NewWriterDict(&buf, flate.BestCompression, dict)
	w.Write([]byte(text))
	w.Close()
	return buf.Len()
}

type oldPage struct {
	Z string `json:"z"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: sgdict2 pages4.jsonl out.dict")
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
			var p oldPage
			if json.Unmarshal(line, &p) == nil && p.Z != "" {
				text, uerr := sgpack.UnpackText(p.Z)
				if uerr == nil && len(text) > 200 {
					n++
					if n <= 50000 {
						for _, w := range wordRe.FindAllString(text, -1) {
							counts[w]++
						}
					} else if n <= 52000 {
						t := text
						if len(t) > 2000 {
							t = t[:2000]
						}
						holdout = append(holdout, t)
					} else {
						break
					}
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
	var dict bytes.Buffer
	for i := len(chosen) - 1; i >= 0; i-- {
		dict.WriteString(chosen[i])
		dict.WriteByte(' ')
	}
	if err := os.WriteFile(os.Args[2], dict.Bytes(), 0o644); err != nil {
		panic(err)
	}
	var raw, without, with, withOld int
	for _, t := range holdout {
		raw += len(t)
		without += packed(t, nil)
		with += packed(t, dict.Bytes())
		withOld += len(sgpack.PackText(t))
	}
	fmt.Printf("dictionary: %d words, %d bytes\n", len(chosen), dict.Len())
	fmt.Printf("on %d held-out pages: text %d bytes -> no dict %d (%.1fx) -> new dict %d (%.1fx, raw deflate) -> old dict (PackText, base85) %d\n",
		len(holdout), raw, without, float64(raw)/float64(without), with, float64(raw)/float64(with), withOld)
}
