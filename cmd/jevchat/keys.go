package main

// The keyboard mode that cannot misspell and tends to make sense. Jev still picks one key at a time, but:
//   - it is only offered the keys that keep the word it is typing a possible word (after "go" it can pick "o", not "x");
//   - at the start of a word the possible words are the likely next words (what follows the last two words in Wikipedia, the words of the
//     conversation, the commonest words); the key "more words" switches that one word to the whole 79,600-word dictionary;
//   - a space, a comma or a full stop only appears when the letters typed are a whole word.
// So every word is a real word that fits the sentence so far; whether it is the right word is still up to Jev.

import (
	"regexp"
	"strings"
	"sync"
)

var good map[string]bool // the words that are kept: real words, not abbreviations or typos from the articles

var twoLetter = map[string]bool{}

func init() {
	for _, w := range strings.Fields("am an as at be by do go he hi if in is it me my no of oh ok on or so to up us we") {
		twoLetter[w] = true
	}
}

// realWord keeps the commonest words and drops the rare short ones ("oa", "gg"). i is the word's rank by frequency.
func realWord(w string, i int) bool {
	switch len(w) {
	case 1:
		return w == "a" || w == "i"
	case 2:
		return twoLetter[w]
	case 3:
		return i < 20000
	case 4:
		return i < 50000
	}
	if strings.Contains(w, "'") {
		return i < 20000
	}
	return true
}

type space struct{ prefix, word map[string]bool }

func newSpace(ws []string) *space {
	s := &space{map[string]bool{}, map[string]bool{}}
	for _, w := range ws {
		s.word[w] = true
		for i := 1; i <= len(w); i++ {
			s.prefix[w[:i]] = true
		}
	}
	return s
}

var (
	fullOnce  sync.Once
	fullSpace *space // the whole dictionary
)

const letters = "abcdefghijklmnopqrstuvwxyz'"

func keyName(c byte) string {
	if c == '\'' {
		return "apostrophe"
	}
	return string(c)
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c == '\'' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }

var tokOnly = regexp.MustCompile(`[a-z]+(?:'[a-z]+)?|[.,?!]`)

// typedWords lists the finished and unfinished words of the text, lowercase.
func typedWords(typed string) []string {
	return strings.FieldsFunc(strings.ToLower(typed), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r == '\'' || r >= '0' && r <= '9') })
}

// repeats: the word was among the last two written, or the same pair of words was written before ("is the is the").
func repeats(ws []string, w string) bool {
	n := len(ws)
	for i := max(0, n-2); i < n; i++ {
		if ws[i] == w {
			return true
		}
	}
	for i := 1; i < n; i++ {
		if ws[i] == w && ws[i-1] == ws[n-1] {
			return true
		}
	}
	return false
}

// likelyWords: the words that can come next after the text before the word being typed.
func likelyWords(history []turn, before string) *space {
	r := &reply{toks: tokOnly.FindAllString(strings.ToLower(before), -1)}
	for _, t := range r.toks {
		if _, isMark := markKey[t]; !isMark {
			r.nWords++
		}
	}
	var ws []string
	for k := range choices(history, r) {
		if !strings.HasPrefix(k, "#") {
			ws = append(ws, k)
		}
	}
	return newSpace(ws)
}

var endKeys = []string{"space", "comma", "dot", "question", "excl"}

// allowedKeys returns the keys Jev may pick now. sp is the set of words allowed for the word being typed; canMore says whether the
// "more words" key may be offered (at the start of a word, while the likely words are in use).
func allowedKeys(typed string, sp *space, canMore bool) map[string]string {
	c := map[string]string{}
	allow := func(k string) { c[k] = keyDesc[k] }
	ws := typedWords(typed)
	last := byte(' ')
	if typed != "" {
		last = typed[len(typed)-1]
	}
	// the word being typed: the letters (or digits) at the end
	i := len(typed)
	for i > 0 && isLetter(typed[i-1]) {
		i--
	}
	cur := typed[i:]
	j := len(typed)
	for j > 0 && isDigit(typed[j-1]) {
		j--
	}
	num := typed[j:]
	finished := ws
	if cur != "" || num != "" {
		finished = ws[:len(ws)-1] // the last one is still being typed
	}
	canEnd := len(ws) >= 1 && last != ','
	switch {
	case cur != "":
		ext := false
		for k := 0; k < len(letters); k++ {
			if sp.prefix[cur+string(letters[k])] {
				allow(keyName(letters[k]))
				ext = true
			}
		}
		if sp.word[cur] && (!repeats(finished, cur) || !ext) {
			for _, k := range endKeys {
				allow(k)
			}
			if canEnd {
				allow("send")
			}
		}
	case num != "":
		for d := byte('0'); d <= '9'; d++ {
			allow("d" + string(d))
		}
		for _, k := range endKeys {
			allow(k)
		}
		if canEnd {
			allow("send")
		}
	case last == ',' || last == '.' || last == '?' || last == '!':
		allow("space")
		if canEnd {
			allow("send")
		}
	default: // the start of the reply or of a word
		for k := 0; k < 26; k++ {
			if sp.prefix[string(letters[k])] {
				allow(keyName(letters[k]))
			}
		}
		for d := byte('0'); d <= '9'; d++ {
			allow("d" + string(d))
		}
		if canMore {
			c["morewords"] = "More words: a word that is not among the usual choices (the whole dictionary is used for the next word)"
		}
	}
	return c
}

const keyInstr = "Which key comes next in Jev's reply? Only keys that keep the word spelled correctly are offered. The reply must answer the user's last message and make a sentence; choose Send when it is complete."

// writeKeys lets Jev type its reply key by key and calls emit for every change.
func writeKeys(history []turn, done func() bool, emit func(any)) error {
	fullOnce.Do(func() { fullSpace = newSpace(words) })
	typed := ""
	full := false // the current word comes from the whole dictionary
	for i := 0; i < maxKeys; i++ {
		if done() {
			return nil
		}
		// the text before the word being typed decides which words are likely
		cut := len(typed)
		for cut > 0 && isLetter(typed[cut-1]) {
			cut--
		}
		sp := fullSpace
		if !full {
			sp = likelyWords(history, typed[:cut])
		}
		opts := allowedKeys(typed, sp, !full)
		if len(opts) == 0 {
			return nil
		}
		var k string
		if len(opts) == 1 { // only one possible key: no need to ask
			for only := range opts {
				k = only
			}
		} else {
			var err error
			if k, err = pick(chatState(history, typed), keyInstr, opts); err != nil {
				return err
			}
		}
		if k == "send" || k == "" || opts[k] == "" {
			return nil
		}
		if k == "morewords" {
			full = true
			continue
		}
		ch := keyChar[k]
		show := ch
		// a capital letter at the start and after . ? !
		t := strings.TrimRight(typed, " ")
		if typed == "" || (strings.HasSuffix(typed, " ") && (strings.HasSuffix(t, ".") || strings.HasSuffix(t, "?") || strings.HasSuffix(t, "!"))) {
			show = strings.ToUpper(ch)
		}
		typed += ch
		for _, e := range endKeys {
			if k == e {
				full = false // the word is finished; the next one starts from the likely words again
			}
		}
		emit(map[string]any{"c": show})
		if k == "dot" || k == "question" || k == "excl" {
			return nil // a reply is one sentence
		}
	}
	return nil
}
