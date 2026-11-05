package main

// jevchatbuild: reads the Wikipedia articles in /srv/uploads/big_kw and writes /media/data/jevchat/vocab.json for the Jev chat:
//   words: the most common words, most common first
//   next:  for "<s>" (start of a sentence), "," and the 20,000 most common words, the 60 tokens that most often follow them
//   next2: for a pair of words ("<s> the", "of the", ", and"...) the 25 tokens that most often follow the pair
// Pass 1 counts the words, pass 2 counts which token follows which. Words from Flowsearch's adult word list are left out.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	srcDir   = fsUploads + "/big_kw"
	out      = fsMedia + "/jevchat/vocab.json"
	adultSrc = fsHome + "/web/adult.go"
)

const (
	minCount = 8
	maxWords = 150000
	ctxWords = 20000 // words that get a list of followers
	triWords = 5000  // words that can be part of a pair context
	follow   = 30000 // followers are taken from the most common words
	perCtx   = 60
	perPair  = 25
	minPair  = 8 // a pair must occur this often to get a list
)

var (
	tokRe = regexp.MustCompile(`[a-z]+(?:'[a-z]+)?|[.!?,]`)
	urlRe = regexp.MustCompile(`https?://\S+`)
)

// texts calls f for every line of every article.
func texts(f func(string)) error {
	files, err := filepath.Glob(srcDir + "/*.txt")
	if err != nil || len(files) == 0 {
		return fmt.Errorf("no articles in %s", srcDir)
	}
	for _, fn := range files {
		b, err := os.ReadFile(fn)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			f(l)
		}
	}
	return nil
}

func tokens(s string) []string {
	s = strings.ToLower(strings.NewReplacer("’", "'", "‘", "'").Replace(urlRe.ReplaceAllString(s, " ")))
	return tokRe.FindAllString(s, -1)
}

func isMark(t string) bool { return t == "." || t == "!" || t == "?" || t == "," }
func isEnd(t string) bool  { return t == "." || t == "!" || t == "?" }

// adultWords reads the strong adult words out of adult.go (the map adultStrong).
func adultWords() map[string]bool {
	b, err := os.ReadFile(adultSrc)
	if err != nil {
		return nil
	}
	s := string(b)
	i := strings.Index(s, "var adultStrong = map[string]bool{")
	if i < 0 {
		return nil
	}
	s = s[i:]
	if j := strings.Index(s, "\n}"); j > 0 {
		s = s[:j]
	}
	w := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([a-z']+)"`).FindAllStringSubmatch(s, -1) {
		w[m[1]] = true
	}
	return w
}

func main() {
	bad := adultWords()
	fmt.Println("adult words left out:", len(bad))
	count := map[string]int32{}
	if err := texts(func(s string) {
		for _, t := range tokens(s) {
			if !isMark(t) {
				count[t]++
			}
		}
	}); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	var words []string
	for w, n := range count {
		if n >= minCount && len(w) <= 20 && !bad[w] && (len(w) >= 2 || w == "a" || w == "i") {
			words = append(words, w)
		}
	}
	sort.Slice(words, func(i, j int) bool { return count[words[i]] > count[words[j]] || (count[words[i]] == count[words[j]] && words[i] < words[j]) })
	if len(words) > maxWords {
		words = words[:maxWords]
	}
	fmt.Println("pass 1 done:", len(count), "different words,", len(words), "kept")

	// followers: the first `follow` words and the four marks
	fid := map[string]int{}
	var ftok []string
	for i, w := range words {
		if i < follow {
			fid[w] = len(ftok)
			ftok = append(ftok, w)
		}
	}
	for _, m := range []string{".", "!", "?", ","} {
		fid[m] = len(ftok)
		ftok = append(ftok, m)
	}
	nf := uint64(len(ftok))
	// one-word contexts: "<s>", "," and the first ctxWords words
	cid := map[string]int{"<s>": 0, ",": 1}
	ctok := []string{"<s>", ","}
	for i, w := range words {
		if i < ctxWords {
			cid[w] = len(ctok)
			ctok = append(ctok, w)
		}
	}
	// two-word contexts: both parts are "<s>", "," or one of the first triWords words
	tid := map[string]int{"<s>": 0, ",": 1}
	ttok := []string{"<s>", ","}
	for i, w := range words {
		if i < triWords {
			tid[w] = len(ttok)
			ttok = append(ttok, w)
		}
	}
	nt := uint64(len(ttok))
	pairs := map[uint64]int32{}
	tri := map[uint64]int32{}
	if err := texts(func(s string) {
		p1, p2 := "<s>", "" // the last word and the one before it
		for _, t := range tokens(s) {
			f, ok := fid[t]
			if !ok {
				p1, p2 = "", "" // a rare word: no context for the next ones
				continue
			}
			if c, ok := cid[p1]; ok {
				pairs[uint64(c)*nf+uint64(f)]++
			}
			if a, ok := tid[p2]; ok {
				if b, ok := tid[p1]; ok {
					tri[(uint64(a)*nt+uint64(b))*nf+uint64(f)]++
				}
			}
			if isEnd(t) {
				p1, p2 = "<s>", ""
			} else {
				p2, p1 = p1, t
			}
		}
	}); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println("pass 2 done:", len(pairs), "pairs,", len(tri), "triples")

	type pc struct {
		f int
		n int32
	}
	top := func(l []pc, n int) []string {
		sort.Slice(l, func(i, j int) bool { return l[i].n > l[j].n })
		if len(l) > n {
			l = l[:n]
		}
		r := make([]string, len(l))
		for i, x := range l {
			r[i] = ftok[x.f]
		}
		return r
	}
	per := map[int][]pc{}
	for k, n := range pairs {
		per[int(k/nf)] = append(per[int(k/nf)], pc{int(k % nf), n})
	}
	next := map[string][]string{}
	for c, l := range per {
		next[ctok[c]] = top(l, perCtx)
	}
	per2 := map[uint64][]pc{}
	tot := map[uint64]int32{}
	for k, n := range tri {
		ctx := k / nf
		per2[ctx] = append(per2[ctx], pc{int(k % nf), n})
		tot[ctx] += n
	}
	next2 := map[string][]string{}
	for ctx, l := range per2 {
		if tot[ctx] < minPair {
			continue
		}
		a, b := ttok[ctx/nt], ttok[ctx%nt]
		next2[a+" "+b] = top(l, perPair)
	}
	os.MkdirAll(fsMedia + "/jevchat", 0o755)
	b, _ := json.Marshal(map[string]any{"words": words, "next": next, "next2": next2})
	if err := os.WriteFile(out, b, 0o644); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println("wrote", out, len(b)>>20, "MB;", len(next2), "word pairs with followers")
}
