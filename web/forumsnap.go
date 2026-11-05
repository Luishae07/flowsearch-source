package main

// A saved copy of the Forums index (forums-index.snap), so that a start of the web server does not have to read the whole posts file again (1.7 GB,
// several minutes). On start the copy is loaded and only the part of the posts file after it is read. The copy is written in the background every
// 10 minutes (written to a new file and then renamed, so a half-written copy is never used). The posts file only grows, so a copy stays valid.

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"os"
	"time"
)

var forumSnapFile = fsData + "/forums-index.snap"

const forumSnapMagic = "FIDX2\n"

var snapLE = binary.LittleEndian

type snapW struct {
	w   *bufio.Writer
	err error
}

func (s *snapW) data(v any) {
	if s.err == nil {
		s.err = binary.Write(s.w, snapLE, v)
	}
}
func (s *snapW) n(v int) { s.data(uint64(v)) }
func (s *snapW) str(t string) {
	s.n(len(t))
	if s.err == nil {
		_, s.err = s.w.WriteString(t)
	}
}
func snapPut[T any](s *snapW, v []T) {
	s.n(len(v))
	if len(v) > 0 {
		s.data(v)
	}
}

type snapR struct {
	r   *bufio.Reader
	err error
}

func (s *snapR) data(v any) {
	if s.err == nil {
		s.err = binary.Read(s.r, snapLE, v)
	}
}
func (s *snapR) n() int {
	var v uint64
	s.data(&v)
	if s.err == nil && v > 1<<34 {
		s.err = errors.New("snapshot: a number is too big")
	}
	return int(v)
}
func (s *snapR) str() string {
	n := s.n()
	if s.err != nil || n > 1<<20 {
		if s.err == nil {
			s.err = errors.New("snapshot: a text is too long")
		}
		return ""
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(s.r, b); err != nil {
		s.err = err
	}
	return string(b)
}
func snapGet[T any](s *snapR) []T {
	n := s.n()
	if s.err != nil || n == 0 {
		return nil
	}
	out := make([]T, n)
	s.data(out)
	return out
}

// saveSnap writes the whole index to forums-index.snap (the index is read-locked while it is written; searches go on, new posts wait).
func (x *fIndex) saveSnap() error {
	x.mu.RLock()
	defer x.mu.RUnlock()
	tmp := forumSnapFile + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	s := &snapW{w: bufio.NewWriterSize(f, 1<<20)}
	s.w.WriteString(forumSnapMagic)
	s.n(int(x.off))
	snapPut(s, x.offs)
	snapPut(s, x.lens)
	snapPut(s, x.score)
	snapPut(s, x.comments)
	snapPut(s, x.pub)
	snapPut(s, x.comm)
	snapPut(s, x.alive)
	snapPut(s, x.plat)
	snapPut(s, x.instOf)
	s.n(len(x.commNames))
	for _, c := range x.commNames {
		s.str(c)
	}
	s.n(len(x.instNames))
	for _, c := range x.instNames {
		s.str(c)
	}
	inN := make([]int32, len(x.instN))
	for i, v := range x.instN {
		inN[i] = int32(v)
	}
	snapPut(s, inN)
	s.n(len(x.servers))
	for h := range x.servers {
		s.str(h)
	}
	s.n(x.n)
	for _, v := range x.platN {
		s.n(v)
	}
	keys, vals := make([]uint64, 0, len(x.urlSeen)), make([]uint32, 0, len(x.urlSeen))
	for k, v := range x.urlSeen {
		keys, vals = append(keys, k), append(vals, v)
	}
	snapPut(s, keys)
	snapPut(s, vals)
	for _, m := range []map[string][]uint32{x.post, x.title} {
		s.n(len(m))
		for w, ids := range m {
			s.str(w)
			snapPut(s, ids)
		}
	}
	s.w.WriteString(forumSnapMagic)
	if s.err == nil {
		s.err = s.w.Flush()
	}
	if cerr := f.Close(); s.err == nil {
		s.err = cerr
	}
	if s.err != nil {
		os.Remove(tmp)
		return s.err
	}
	return os.Rename(tmp, forumSnapFile)
}

// loadForumSnap reads the saved copy; ok is false when there is none or it is not complete (then the posts file is read from the start).
func loadForumSnap() (*fIndex, bool) {
	f, err := os.Open(forumSnapFile)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	s := &snapR{r: bufio.NewReaderSize(f, 1<<20)}
	head := make([]byte, len(forumSnapMagic))
	if _, err := io.ReadFull(s.r, head); err != nil || string(head) != forumSnapMagic {
		return nil, false
	}
	x := &fIndex{instID: map[string]uint32{}, commID: map[string]uint32{}, servers: map[string]bool{}, post: map[string][]uint32{}, title: map[string][]uint32{}, urlSeen: map[uint64]uint32{}}
	x.off = int64(s.n())
	x.offs = snapGet[int64](s)
	x.lens = snapGet[uint32](s)
	x.score = snapGet[int32](s)
	x.comments = snapGet[int32](s)
	x.pub = snapGet[int64](s)
	x.comm = snapGet[uint32](s)
	x.alive = snapGet[bool](s)
	x.plat = snapGet[uint8](s)
	x.instOf = snapGet[uint32](s)
	for i, n := 0, s.n(); i < n && s.err == nil; i++ {
		c := s.str()
		x.commID[c] = uint32(len(x.commNames))
		x.commNames = append(x.commNames, c)
	}
	for i, n := 0, s.n(); i < n && s.err == nil; i++ {
		c := s.str()
		x.instID[c] = uint32(len(x.instNames))
		x.instNames = append(x.instNames, c)
	}
	for _, v := range snapGet[int32](s) {
		x.instN = append(x.instN, int(v))
	}
	for i, n := 0, s.n(); i < n && s.err == nil; i++ {
		x.servers[s.str()] = true
	}
	x.n = s.n()
	for i := range x.platN {
		x.platN[i] = s.n()
	}
	keys, vals := snapGet[uint64](s), snapGet[uint32](s)
	if len(keys) != len(vals) {
		return nil, false
	}
	for i, k := range keys {
		x.urlSeen[k] = vals[i]
	}
	for _, m := range []map[string][]uint32{x.post, x.title} {
		for i, n := 0, s.n(); i < n && s.err == nil; i++ {
			w := s.str()
			m[w] = snapGet[uint32](s)
		}
	}
	tail := make([]byte, len(forumSnapMagic))
	if s.err != nil {
		log.Printf("forums: the saved index copy is not usable (%v); reading the posts file", s.err)
		return nil, false
	}
	if _, err := io.ReadFull(s.r, tail); err != nil || string(tail) != forumSnapMagic {
		return nil, false
	}
	// the arrays must agree with each other
	np := len(x.offs)
	if len(x.lens) != np || len(x.score) != np || len(x.comments) != np || len(x.pub) != np || len(x.comm) != np || len(x.alive) != np || len(x.plat) != np || len(x.instOf) != np {
		return nil, false
	}
	if st, err := os.Stat(lemmyPostsFile); err != nil || st.Size() < x.off {
		return nil, false
	}
	log.Printf("forums: loaded the saved index copy: %d posts, then reading the posts file from byte %d", x.n, x.off)
	return x, true
}

// snapLoop writes the copy now and then (the first time after the index is ready if there was no copy, then every 10 minutes if the file grew).
func snapLoop(x *fIndex, haveCopy bool) {
	var savedOff int64 = -1
	if haveCopy {
		x.mu.RLock()
		savedOff = x.off
		x.mu.RUnlock()
	}
	for {
		x.mu.RLock()
		off := x.off
		x.mu.RUnlock()
		if off != savedOff {
			t := time.Now()
			if err := x.saveSnap(); err != nil {
				log.Printf("forums: saving the index copy failed: %v", err)
			} else {
				savedOff = off
				log.Printf("forums: saved the index copy (%s)", time.Since(t).Round(time.Second))
			}
		}
		time.Sleep(10 * time.Minute)
	}
}
