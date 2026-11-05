package main

// The index of the Forums tab. The posts file (lemmy-posts.jsonl) holds the posts with their top comments; it is far too big to keep in
// memory for hundreds of thousands of posts, and too slow to scan for every search. So the index keeps only:
//   - where each post sits in the file (offset, length) and its score, comment count, date and community,
//   - for every word the list of posts that contain it (anywhere), and a second list for the posts with the word in the title.
// A search finds the posts that contain its words from these lists, picks the best few hundred with a cheap score and reads only those
// from the file; the careful scoring (forumsSearch) then works on that small set. The file only grows, so the index follows it.

import (
	"bufio"
	"encoding/json"
	"hash/fnv"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type fIndex struct {
	mu        sync.RWMutex
	off       int64
	offs      []int64
	lens      []uint32
	score     []int32
	comments  []int32
	pub       []int64
	comm      []uint32
	alive     []bool
	commNames []string
	commID    map[string]uint32
	servers   map[string]bool
	post      map[string][]uint32 // word -> posts that have it anywhere
	title     map[string][]uint32 // word -> posts that have it in the title
	urlSeen   map[uint64]uint32
	n         int // posts that count
	plat      []uint8 // the platform of each post: 0 Lemmy, 1 Discourse, 2 Flowforum
	platN     [3]int  // posts that count, for each platform
	instID    map[string]uint32 // forum site -> number
	instNames []string
	instOf    []uint32 // the forum site of each post
	instN     []int    // posts that count, for each forum site
}

type instCount struct {
	H string `json:"h"`
	N int    `json:"n"`
}

// topInstances: the forum sites with the most posts.
func (x *fIndex) topInstances(n int) []instCount {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := make([]instCount, 0, len(x.instNames))
	for i, h := range x.instNames {
		if x.instN[i] > 0 {
			out = append(out, instCount{h, x.instN[i]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N > out[j].N })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func platCode(p string) uint8 {
	switch p {
	case "discourse":
		return 1
	case "flowforum":
		return 2
	}
	return 0
}

// platformCounts: how many posts the Forums tab holds from each kind of forum.
func (x *fIndex) platformCounts() map[string]int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return map[string]int{"lemmy": x.platN[0], "discourse": x.platN[1], "flowforum": x.platN[2]}
}

// languageCounts: posts of each programming language (Python, Rust, Go, ...), from the language forums.
func (x *fIndex) languageCounts() map[string]int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := map[string]int{}
	for i, h := range x.instNames {
		if x.instN[i] > 0 && isLangHost(h) {
			out[langName(h)] += x.instN[i]
		}
	}
	return out
}

// languagePosts: how many posts the Python / programming language forums (forums-languages.txt) hold.
func (x *fIndex) languagePosts() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	n := 0
	for i, h := range x.instNames {
		if x.instN[i] > 0 && isLangHost(h) {
			n += x.instN[i]
		}
	}
	return n
}

var (
	forumIdx     *fIndex
	forumIdxOnce sync.Once
)

func forumIndex() *fIndex {
	forumIdxOnce.Do(func() {
		// a saved copy of the index (forumsnap.go) is loaded when there is one; then only what the posts file got after it is read
		snap, haveCopy := loadForumSnap()
		if haveCopy {
			forumIdx = snap
		} else {
			forumIdx = &fIndex{instID: map[string]uint32{}, commID: map[string]uint32{}, servers: map[string]bool{}, post: map[string][]uint32{}, title: map[string][]uint32{}, urlSeen: map[uint64]uint32{}}
		}
		forumIdx.update()
		forumReady.Store(true)
		go snapLoop(forumIdx, haveCopy)
		go func() {
			for {
				time.Sleep(5 * time.Second)
				forumIdx.update()
			}
		}()
	})
	return forumIdx
}

func uniqWords(texts ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range texts {
		for _, w := range tabTerms(t) {
			if !seen[w] {
				seen[w] = true
				out = append(out, w)
			}
		}
	}
	return out
}

// update reads the part of the file that is new since the last time.
func (x *fIndex) update() {
	f, err := os.Open(lemmyPostsFile)
	if err != nil {
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	x.mu.RLock()
	off := x.off
	x.mu.RUnlock()
	if st.Size() == off {
		return
	}
	if st.Size() < off { // the file was replaced: start again
		x.mu.Lock()
		x.off, x.offs, x.lens, x.score, x.comments, x.pub, x.comm, x.alive, x.commNames, x.n = 0, nil, nil, nil, nil, nil, nil, nil, nil, 0
		x.commID, x.servers, x.post, x.title, x.urlSeen = map[string]uint32{}, map[string]bool{}, map[string][]uint32{}, map[string][]uint32{}, map[uint64]uint32{}
		x.mu.Unlock()
		x.mu.Lock()
		x.plat, x.platN = nil, [3]int{}
		x.instID, x.instNames, x.instOf, x.instN = map[string]uint32{}, nil, nil, nil
		x.mu.Unlock()
		off = 0
	}
	f.Seek(off, 0)
	rd := bufio.NewReaderSize(f, 1<<20)
	pos := off
	for {
		line, err := rd.ReadBytes('\n')
		if err == io.EOF || len(line) == 0 || line[len(line)-1] != '\n' {
			break // a line the crawler is still writing is read next time
		}
		start := pos
		pos += int64(len(line))
		var p lemmyPost
		if json.Unmarshal(line, &p) != nil || p.U == "" || (len(p.Top) == 0 && p.Platform != "flowforum") { // the Forums tab shows posts with a comments dropdown; our own forum's posts show from the start
			continue
		}
		if adultText(p.T) || adultText(p.Community) || adultText(p.Name) || adultHost(p.Instance) {
			continue
		}
		x.mu.Lock()
		x.add(p, start, uint32(len(line)))
		x.mu.Unlock()
	}
	x.mu.Lock()
	x.off = pos
	x.mu.Unlock()
}

func (x *fIndex) add(p lemmyPost, off int64, ln uint32) {
	h := fnv.New64a()
	h.Write([]byte(p.U))
	key := h.Sum64()
	if old, ok := x.urlSeen[key]; ok && x.alive[old] { // the same post again (a re-read): the newer line wins
		x.alive[old] = false
		x.n--
		x.platN[x.plat[old]]--
		x.instN[x.instOf[old]]--
	}
	id := uint32(len(x.offs))
	x.urlSeen[key] = id
	cid, ok := x.commID[p.Community]
	if !ok {
		cid = uint32(len(x.commNames))
		x.commNames = append(x.commNames, p.Community)
		x.commID[strings.Clone(p.Community)] = cid
	}
	pubT := int64(0)
	if t, err := time.Parse(time.RFC3339, p.Published); err == nil {
		pubT = t.Unix()
	} else if len(p.Published) >= 19 {
		if t, err := time.Parse("2006-01-02T15:04:05", p.Published[:19]); err == nil {
			pubT = t.Unix()
		}
	}
	x.offs = append(x.offs, off)
	x.lens = append(x.lens, ln)
	x.score = append(x.score, int32(p.Score))
	x.comments = append(x.comments, int32(p.Comments))
	x.pub = append(x.pub, pubT)
	x.comm = append(x.comm, cid)
	x.alive = append(x.alive, true)
	x.servers[p.Instance] = true
	x.n++
	pc := platCode(p.Platform)
	x.plat = append(x.plat, pc)
	x.platN[pc]++
	iid, seenInst := x.instID[p.Instance]
	if !seenInst {
		iid = uint32(len(x.instNames))
		name := strings.Clone(p.Instance)
		x.instNames = append(x.instNames, name)
		x.instN = append(x.instN, 0)
		x.instID[name] = iid
	}
	x.instOf = append(x.instOf, iid)
	x.instN[iid]++
	for _, w := range uniqWords(p.T) {
		x.title[x.own(x.title, w)] = append(x.title[w], id)
	}
	var parts []string
	parts = append(parts, p.T, p.Community, p.Name, clip(p.Body, 600))
	for _, c := range p.Top {
		parts = append(parts, clip(c.T, 400))
	}
	for _, w := range uniqWords(parts...) {
		x.post[x.own(x.post, w)] = append(x.post[w], id)
	}
}

// own: the word as a key that does not keep the text it was cut from alive.
func (x *fIndex) own(m map[string][]uint32, w string) string {
	if _, ok := m[w]; ok {
		return w
	}
	return strings.Clone(w)
}

func (x *fIndex) count() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.n
}

// expand: the words of the index that count as a match for a search word (the same rule as wordMatches).
func (x *fIndex) expand(m map[string][]uint32, t string) []string {
	var out []string
	for _, c := range []string{t, t + "s", t + "es"} {
		if _, ok := m[c]; ok {
			out = append(out, c)
		}
	}
	if len(t) >= 4 {
		if strings.HasSuffix(t, "s") {
			if _, ok := m[strings.TrimSuffix(t, "s")]; ok {
				out = append(out, strings.TrimSuffix(t, "s"))
			}
		}
		for w := range m {
			if len(w) > len(t) && strings.HasPrefix(w, t) && w != t+"s" && w != t+"es" {
				out = append(out, w)
			}
		}
	}
	return out
}

func inList(l []uint32, id uint32) bool {
	i := sort.Search(len(l), func(i int) bool { return l[i] >= id })
	return i < len(l) && l[i] == id
}

type fSearch struct {
	Posts      []lemmyPost
	Total      int            // posts that match
	CommCounts map[string]int // matching posts per community
	DF         map[string]int // posts with each word
}

// search: the posts that have at least `need` of the words, the best `limit` of them by mode ("" = cheap match score, "new", "top", "comments").
func (x *fIndex) search(terms []string, need int, mode, comm string, limit int) fSearch {
	x.mu.RLock()
	defer x.mu.RUnlock()
	res := fSearch{CommCounts: map[string]int{}, DF: map[string]int{}}
	if len(terms) == 0 {
		return res
	}
	type tl struct {
		t     string
		lists [][]uint32
		tlist [][]uint32
		size  int
	}
	var ts []tl
	for _, t := range terms {
		e := tl{t: t}
		for _, w := range x.expand(x.post, t) {
			e.lists = append(e.lists, x.post[w])
			e.size += len(x.post[w])
		}
		for _, w := range x.expand(x.title, t) {
			e.tlist = append(e.tlist, x.title[w])
		}
		res.DF[t] = min(e.size, x.n)
		ts = append(ts, e)
	}
	// the pool: every post that has one of the rarest (len-need+1) words; a post that has `need` words must have at least one of them
	order := make([]int, len(ts))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return ts[order[a]].size < ts[order[b]].size })
	pool := map[uint32]bool{}
	for _, oi := range order[:len(ts)-need+1] {
		for _, l := range ts[oi].lists {
			for _, id := range l {
				pool[id] = true
			}
		}
	}
	type cand struct {
		id uint32
		sc float64
	}
	var cs []cand
	for id := range pool {
		if !x.alive[id] {
			continue
		}
		if comm != "" && x.commNames[x.comm[id]] != comm {
			continue
		}
		matched, sc := 0, 0.0
		for _, e := range ts {
			got := false
			for _, l := range e.lists {
				if inList(l, id) {
					got = true
					break
				}
			}
			if !got {
				continue
			}
			matched++
			w := 1.0
			for _, l := range e.tlist {
				if inList(l, id) {
					w = 5 // the word is in the title
					break
				}
			}
			df := float64(e.size)
			sc += w * (1 + 1/(1+df/50))
		}
		if matched < need {
			continue
		}
		res.Total++
		res.CommCounts[x.commNames[x.comm[id]]]++
		cs = append(cs, cand{id, sc})
	}
	key := func(c cand) float64 {
		switch mode {
		case "new":
			return float64(x.pub[c.id])
		case "top":
			return float64(x.score[c.id])
		case "comments":
			return float64(x.comments[c.id])
		}
		return c.sc*1000 + float64(x.pub[c.id])/1e10 // a tie goes to the newer post
	}
	sort.Slice(cs, func(i, j int) bool { return key(cs[i]) > key(cs[j]) })
	if len(cs) > limit {
		cs = cs[:limit]
	}
	f, err := os.Open(lemmyPostsFile)
	if err != nil {
		return res
	}
	defer f.Close()
	for _, c := range cs {
		buf := make([]byte, x.lens[c.id])
		if _, err := f.ReadAt(buf, x.offs[c.id]); err != nil {
			continue
		}
		var p lemmyPost
		if json.Unmarshal(buf, &p) == nil {
			res.Posts = append(res.Posts, p)
		}
	}
	return res
}

// stats for the Live page
func (x *fIndex) stats() (posts, communities, servers int, top []struct {
	C string
	N int
}) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	counts := map[uint32]int{}
	for id, a := range x.alive {
		if a {
			counts[x.comm[id]]++
		}
	}
	for c, n := range counts {
		top = append(top, struct {
			C string
			N int
		}{x.commNames[c], n})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].N > top[j].N || (top[i].N == top[j].N && top[i].C < top[j].C) })
	if len(top) > 25 {
		top = top[:25]
	}
	return x.n, len(counts), len(x.servers), top
}
