// sgrepack rewrites pages4.jsonl using a NEW text dictionary: decompress each record with the OLD
// embedded dict, recompress with the new one, keep everything else the same.
package main

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"

	"mysearch/sgpack"
)

type stored struct {
	URL         string `json:"u"`
	Title       string `json:"t"`
	Description string `json:"d,omitempty"`
	Z           string `json:"z"`
	Source      string `json:"s,omitempty"`
}

func main() {
	newDictPath := os.Args[1]
	from := os.Args[2]
	to := os.Args[3]
	newDict, err := os.ReadFile(newDictPath)
	if err != nil {
		panic(err)
	}
	packNew := func(text string) string {
		var buf bytes.Buffer
		w, _ := flate.NewWriterDict(&buf, flate.BestCompression, newDict)
		w.Write([]byte(text))
		w.Close()
		return sgpack.Encode(buf.Bytes())
	}

	in, err := os.Open(from)
	if err != nil {
		panic(err)
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		panic(err)
	}
	defer out.Close()

	r := bufio.NewReaderSize(in, 1<<20)
	w := bufio.NewWriterSize(out, 1<<20)
	defer w.Flush()

	type job struct {
		line []byte
	}
	lines := make(chan job, 10000)
	results := make(chan []byte, 10000)
	var wg sync.WaitGroup
	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range lines {
				var p stored
				if json.Unmarshal(j.line, &p) != nil {
					results <- nil
					continue
				}
				text, err := sgpack.UnpackText(p.Z)
				if err != nil {
					results <- nil
					continue
				}
				p.Z = packNew(text)
				b, _ := json.Marshal(p)
				results <- append(b, '\n')
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		n := 0
		for b := range results {
			if b != nil {
				w.Write(b)
				n++
				if n%500000 == 0 {
					fmt.Printf("repacked %d\n", n)
				}
			}
		}
		close(done)
	}()
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			cp := make([]byte, len(line))
			copy(cp, line)
			lines <- job{cp}
		}
		if err == io.EOF {
			break
		} else if err != nil {
			panic(err)
		}
	}
	close(lines)
	wg.Wait()
	close(results)
	<-done
	fmt.Println("repack done")
}
