package main

// More instant answers (see instant.go): text and code tools, number tricks, a timer and a stopwatch.
//   * hashes / encoding:  sha256 hello   md5 hello   base64 hello   base64 decode aGVsbG8=   url encode a b&c   rot13 hello
//   * number bases:       255 to binary   255 to hex   0xff to decimal   2024 in roman   MMXXIV to decimal
//   * text:               reverse hello   uppercase hello   length of hello world   word count the quick brown fox   morse sos
//   * maths:              gcd 12 18   lcm 4 6   factorial 10   is 97 prime   prime factors 360   fibonacci 50   average 3 5 8   median 1 9 4
//   * others:             is 2028 a leap year   20% tip on 85   bmi 70kg 180cm   day of 2026-12-25   timer 5 minutes   stopwatch

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"math"
	"math/big"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	hashRe     = regexp.MustCompile(`(?i)^(md5|sha-?1|sha-?256|sha-?512)\s+(.+)$`)
	b64Re      = regexp.MustCompile(`(?i)^base64(?:\s+(encode|decode))?\s+(.+)$`)
	urlEncRe   = regexp.MustCompile(`(?i)^url\s+(encode|decode)\s+(.+)$`)
	rot13Re    = regexp.MustCompile(`(?i)^rot13\s+(.+)$`)
	baseRe     = regexp.MustCompile(`^(\d{1,15})\s+(?:in|to|as)\s+(binary|hex|hexadecimal|octal)$`)
	fromBaseRe = regexp.MustCompile(`^(0x[0-9a-f]+|0b[01]+|0o[0-7]+)\s+(?:in|to|as)\s+(?:decimal|number)$`)
	romanOutRe = regexp.MustCompile(`^(\d{1,4})\s+(?:in|to|as)\s+roman(?:\s+numerals?)?$`)
	romanInRe  = regexp.MustCompile(`^roman\s+(?:numeral\s+)?([ivxlcdm]+)$|^([ivxlcdm]+)\s+(?:in|to)\s+(?:decimal|number|arabic)$`) // always with "roman" or "to decimal": "mix" or "civic" are words
	textRe     = regexp.MustCompile(`(?i)^(reverse|uppercase|lowercase|upper|lower|length of|word count|count words in|character count of|characters in)\s+["“'](.+)["”']$`) // the text must be in quotes: "reverse engineering" is a search, not a command
	morseRe    = regexp.MustCompile(`(?i)^morse(?:\s+code)?\s+(?:of\s+|for\s+)?(.+)$`)
	gcdRe      = regexp.MustCompile(`^(gcd|lcm)\s+([0-9 ,]+)$`)
	factRe     = regexp.MustCompile(`^(?:factorial\s+(\d{1,3})|(\d{1,3})!)$`)
	primeRe    = regexp.MustCompile(`^is\s+(\d{1,15})\s+(?:a\s+)?prime(?:\s+number)?$`)
	factorsRe  = regexp.MustCompile(`^prime\s+factors(?:\s+of)?\s+(\d{1,15})$`)
	fibRe      = regexp.MustCompile(`^fibonacci(?:\s+number)?\s+(\d{1,4})$`)
	statRe     = regexp.MustCompile(`^(average|mean|median|sum|min|max)\s+(?:of\s+)?(-?[0-9., -]+)$`)
	leapRe     = regexp.MustCompile(`^is\s+(\d{1,4})\s+a\s+leap\s+year$`)
	tipRe      = regexp.MustCompile(`^(\d{1,3})\s*%\s*tip\s+on\s+(\d+(?:\.\d+)?)$`)
	bmiRe      = regexp.MustCompile(`^bmi\s+(\d+(?:\.\d+)?)\s*kg\s+(\d+(?:\.\d+)?)\s*cm$`)
	weekdayRe  = regexp.MustCompile(`^(?:what\s+)?day\s+(?:of\s+the\s+week\s+)?(?:is|was)\s+(\d{4}-\d{2}-\d{2})$|^(?:weekday|day)\s+of\s+(\d{4}-\d{2}-\d{2})$`)
	timerRe    = regexp.MustCompile(`^(?:set\s+a\s+)?(?:timer|countdown)(?:\s+for)?(?:\s+(\d{1,4})\s*(seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h))?$`)
)

func iaMore(q, low string) string {
	if m := hashRe.FindStringSubmatch(q); m != nil {
		txt := []byte(m[2])
		var sum []byte
		name := strings.ToLower(strings.ReplaceAll(m[1], "-", ""))
		switch name {
		case "md5":
			s := md5.Sum(txt)
			sum = s[:]
		case "sha1":
			s := sha1.Sum(txt)
			sum = s[:]
		case "sha256":
			s := sha256.Sum256(txt)
			sum = s[:]
		case "sha512":
			s := sha512.Sum512(txt)
			sum = s[:]
		}
		return iaCardSmall(strings.ToUpper(name), hex.EncodeToString(sum), "of “"+clip(m[2], 60)+"”")
	}
	if m := b64Re.FindStringSubmatch(q); m != nil {
		if strings.EqualFold(m[1], "decode") {
			b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(m[2]))
			if err != nil {
				if b, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(m[2])); err != nil {
					return ""
				}
			}
			if !utf8.Valid(b) {
				return ""
			}
			return iaCardSmall("Base64 decoded", string(b), "")
		}
		return iaCardSmall("Base64", base64.StdEncoding.EncodeToString([]byte(m[2])), "of “"+clip(m[2], 60)+"”")
	}
	if m := urlEncRe.FindStringSubmatch(q); m != nil {
		if strings.EqualFold(m[1], "decode") {
			s, err := url.QueryUnescape(m[2])
			if err != nil {
				return ""
			}
			return iaCardSmall("URL decoded", s, "")
		}
		return iaCardSmall("URL encoded", url.QueryEscape(m[2]), "")
	}
	if m := rot13Re.FindStringSubmatch(q); m != nil {
		return iaCardSmall("ROT13", strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z':
				return 'a' + (r-'a'+13)%26
			case r >= 'A' && r <= 'Z':
				return 'A' + (r-'A'+13)%26
			}
			return r
		}, m[1]), "")
	}
	if m := baseRe.FindStringSubmatch(low); m != nil {
		n, _ := strconv.ParseUint(m[1], 10, 64)
		switch m[2] {
		case "binary":
			return iaCardSmall("Binary", strconv.FormatUint(n, 2), m[1]+" in base 2")
		case "octal":
			return iaCardSmall("Octal", strconv.FormatUint(n, 8), m[1]+" in base 8")
		default:
			return iaCardSmall("Hexadecimal", strings.ToUpper(strconv.FormatUint(n, 16)), m[1]+" in base 16")
		}
	}
	if m := fromBaseRe.FindStringSubmatch(low); m != nil {
		n, err := strconv.ParseInt(m[1], 0, 64)
		if err != nil {
			return ""
		}
		return iaCardSmall("Decimal", strconv.FormatInt(n, 10), m[1])
	}
	if m := romanOutRe.FindStringSubmatch(low); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n < 1 || n > 3999 {
			return ""
		}
		return iaCardSmall("Roman numeral", toRoman(n), m[1])
	}
	if m := romanInRe.FindStringSubmatch(low); m != nil {
		r := strings.ToUpper(m[1] + m[2])
		if n := fromRoman(r); n > 0 && toRoman(n) == r {
			return iaCardSmall("Decimal", strconv.Itoa(n), r)
		}
	}
	if m := textRe.FindStringSubmatch(q); m != nil {
		t := m[2]
		switch strings.ToLower(m[1]) {
		case "reverse":
			r := []rune(t)
			for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
				r[i], r[j] = r[j], r[i]
			}
			return iaCardSmall("Reversed", string(r), "")
		case "uppercase", "upper":
			return iaCardSmall("Uppercase", strings.ToUpper(t), "")
		case "lowercase", "lower":
			return iaCardSmall("Lowercase", strings.ToLower(t), "")
		case "word count", "count words in":
			return iaCardSmall("Words", strconv.Itoa(len(strings.Fields(t))), "in “"+clip(t, 60)+"”")
		default:
			return iaCardSmall("Characters", strconv.Itoa(utf8.RuneCountInString(t)), "in “"+clip(t, 60)+"” (spaces count)")
		}
	}
	if m := morseRe.FindStringSubmatch(q); m != nil && low != "morse code" {
		var out []string
		for _, r := range strings.ToLower(m[1]) {
			if r == ' ' {
				out = append(out, "/")
			} else if c, ok := morseTable[r]; ok {
				out = append(out, c)
			}
		}
		if len(out) == 0 {
			return ""
		}
		return iaCardSmall("Morse code", strings.Join(out, " "), "“"+clip(m[1], 60)+"”")
	}
	if m := gcdRe.FindStringSubmatch(low); m != nil {
		var ns []int64
		for _, f := range strings.FieldsFunc(m[2], func(r rune) bool { return r == ' ' || r == ',' }) {
			n, err := strconv.ParseInt(f, 10, 64)
			if err != nil || n <= 0 {
				return ""
			}
			ns = append(ns, n)
		}
		if len(ns) < 2 || len(ns) > 12 {
			return ""
		}
		res := ns[0]
		for _, n := range ns[1:] {
			g := gcd64(res, n)
			if m[1] == "gcd" {
				res = g
			} else {
				res = res / g * n
				if res <= 0 || res > 1e15 {
					return ""
				}
			}
		}
		lab := map[string]string{"gcd": "Greatest common divisor", "lcm": "Least common multiple"}[m[1]]
		return iaCardSmall(lab, commas(int(res)), strings.TrimSpace(m[2]))
	}
	if m := factRe.FindStringSubmatch(low); m != nil {
		s := m[1] + m[2]
		n, _ := strconv.Atoi(s)
		f := new(big.Int).MulRange(1, int64(max(n, 1)))
		return iaCardSmall("Factorial", f.String(), s+"!")
	}
	if m := primeRe.FindStringSubmatch(low); m != nil {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		if isPrime(n) {
			return iaCardSmall("Prime number", "Yes", m[1]+" is prime")
		}
		return iaCardSmall("Prime number", "No", m[1]+" = "+factorString(n))
	}
	if m := factorsRe.FindStringSubmatch(low); m != nil {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		if n < 2 {
			return ""
		}
		return iaCardSmall("Prime factors", factorString(n), m[1])
	}
	if m := fibRe.FindStringSubmatch(low); m != nil {
		n, _ := strconv.Atoi(m[1])
		a, b := big.NewInt(0), big.NewInt(1)
		for i := 0; i < n; i++ {
			a.Add(a, b)
			a, b = b, a
		}
		return iaCardSmall("Fibonacci number "+m[1], a.String(), "")
	}
	if m := statRe.FindStringSubmatch(low); m != nil {
		var xs []float64
		for _, f := range strings.FieldsFunc(m[2], func(r rune) bool { return r == ' ' || r == ',' }) {
			v, err := strconv.ParseFloat(f, 64)
			if err != nil {
				return ""
			}
			xs = append(xs, v)
		}
		if len(xs) < 2 || len(xs) > 500 {
			return ""
		}
		sum, mn, mx := 0.0, xs[0], xs[0]
		for _, v := range xs {
			sum += v
			mn, mx = math.Min(mn, v), math.Max(mx, v)
		}
		var res float64
		switch m[1] {
		case "sum":
			res = sum
		case "min":
			res = mn
		case "max":
			res = mx
		case "median":
			s := append([]float64(nil), xs...)
			sort.Float64s(s)
			if len(s)%2 == 1 {
				res = s[len(s)/2]
			} else {
				res = (s[len(s)/2-1] + s[len(s)/2]) / 2
			}
		default:
			res = sum / float64(len(xs))
		}
		return iaCard(strings.ToUpper(m[1][:1])+m[1][1:], fmtNum(res), html.EscapeString(fmt.Sprintf("of %d numbers", len(xs))))
	}
	if m := leapRe.FindStringSubmatch(low); m != nil {
		y, _ := strconv.Atoi(m[1])
		leap := y%4 == 0 && (y%100 != 0 || y%400 == 0)
		return iaCardSmall("Leap year", map[bool]string{true: "Yes", false: "No"}[leap], m[1]+map[bool]string{true: " has 366 days", false: " has 365 days"}[leap])
	}
	if m := tipRe.FindStringSubmatch(low); m != nil {
		p, _ := strconv.ParseFloat(m[1], 64)
		b, _ := strconv.ParseFloat(m[2], 64)
		tip := b * p / 100
		return iaCard("Tip", fmt.Sprintf("%.2f", tip), html.EscapeString(fmt.Sprintf("%s%% of %s · total %.2f", m[1], m[2], b+tip)))
	}
	if m := bmiRe.FindStringSubmatch(low); m != nil {
		kg, _ := strconv.ParseFloat(m[1], 64)
		cm, _ := strconv.ParseFloat(m[2], 64)
		if kg <= 0 || cm < 50 || cm > 260 {
			return ""
		}
		bmi := kg / ((cm / 100) * (cm / 100))
		cat := "healthy weight"
		switch {
		case bmi < 18.5:
			cat = "underweight"
		case bmi >= 30:
			cat = "obese"
		case bmi >= 25:
			cat = "overweight"
		}
		return iaCard("Body mass index", fmt.Sprintf("%.1f", bmi), html.EscapeString(cat+" for an adult (a rough guide, not medical advice)"))
	}
	if m := weekdayRe.FindStringSubmatch(low); m != nil {
		d := m[1] + m[2]
		t, err := time.Parse("2006-01-02", d)
		if err != nil {
			return ""
		}
		return iaCard("Day of the week", t.Format("Monday"), html.EscapeString(t.Format("2 January 2006")))
	}
	if low == "stopwatch" || low == "online stopwatch" {
		return timerCard(0, true)
	}
	if m := timerRe.FindStringSubmatch(low); m != nil {
		secs := 300
		if m[1] != "" {
			n, _ := strconv.Atoi(m[1])
			switch m[2][0] {
			case 'h':
				secs = n * 3600
			case 'm':
				secs = n * 60
			default:
				secs = n
			}
		}
		if secs < 1 || secs > 86400 {
			return ""
		}
		return timerCard(secs, false)
	}
	return ""
}

// iaCardSmall: like iaCard, for long results (a hash, a text): smaller letters, broken anywhere
func iaCardSmall(label, big, sub string) string {
	s := ""
	if sub != "" {
		s = `<div class=sub>` + html.EscapeString(sub) + `</div>`
	}
	return instantCSS + `<div class=ia><small>` + html.EscapeString(label) + `</small><div class=big style="font-size:` + map[bool]string{true: "20px", false: "30px"}[len(big) > 24] + `;word-break:break-all">` + html.EscapeString(big) + `</div>` + s + `</div>`
}

func gcd64(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func isPrime(n int64) bool {
	if n < 2 {
		return false
	}
	for i := int64(2); i*i <= n; i++ {
		if n%i == 0 {
			return false
		}
	}
	return true
}

func factorString(n int64) string {
	var parts []string
	for p := int64(2); p*p <= n; p++ {
		c := 0
		for n%p == 0 {
			n /= p
			c++
		}
		if c == 1 {
			parts = append(parts, strconv.FormatInt(p, 10))
		} else if c > 1 {
			parts = append(parts, fmt.Sprintf("%d^%d", p, c))
		}
	}
	if n > 1 {
		parts = append(parts, strconv.FormatInt(n, 10))
	}
	return strings.Join(parts, " × ")
}

var romanVals = []struct {
	v int
	s string
}{{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"}, {100, "C"}, {90, "XC"}, {50, "L"}, {40, "XL"}, {10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"}}

func toRoman(n int) string {
	var b strings.Builder
	for _, r := range romanVals {
		for n >= r.v {
			b.WriteString(r.s)
			n -= r.v
		}
	}
	return b.String()
}

func fromRoman(s string) int {
	val := map[byte]int{'I': 1, 'V': 5, 'X': 10, 'L': 50, 'C': 100, 'D': 500, 'M': 1000}
	total := 0
	for i := 0; i < len(s); i++ {
		v, ok := val[s[i]]
		if !ok {
			return 0
		}
		if i+1 < len(s) && val[s[i+1]] > v {
			total -= v
		} else {
			total += v
		}
	}
	return total
}

var morseTable = map[rune]string{
	'a': ".-", 'b': "-...", 'c': "-.-.", 'd': "-..", 'e': ".", 'f': "..-.", 'g': "--.", 'h': "....", 'i': "..", 'j': ".---", 'k': "-.-", 'l': ".-..", 'm': "--",
	'n': "-.", 'o': "---", 'p': ".--.", 'q': "--.-", 'r': ".-.", 's': "...", 't': "-", 'u': "..-", 'v': "...-", 'w': ".--", 'x': "-..-", 'y': "-.--", 'z': "--..",
	'0': "-----", '1': ".----", '2': "..---", '3': "...--", '4': "....-", '5': ".....", '6': "-....", '7': "--...", '8': "---..", '9': "----.",
}

// timerCard: a countdown (secs > 0) or a stopwatch; it runs in the page
func timerCard(secs int, stopwatch bool) string {
	label, start := "Timer", strconv.Itoa(secs)
	if stopwatch {
		label, start = "Stopwatch", "0"
	}
	return instantCSS + `<div class=ia id=tm data-sw="` + map[bool]string{true: "1", false: ""}[stopwatch] + `" data-s="` + start + `"><small>` + label + `</small><div class=big id=tmv>0:00</div><div class=row><button class=btn id=tms type=button>Start</button><button class=btn id=tmr type=button>Reset</button></div></div>` +
		`<script>(function(){const box=document.getElementById('tm'),sw=!!box.dataset.sw,init=+box.dataset.s,v=document.getElementById('tmv'),bs=document.getElementById('tms');let t=init,id=null,end=0;` +
		`const f=x=>{x=Math.max(0,Math.round(x));const h=Math.floor(x/3600),m=Math.floor(x%3600/60),s=x%60;return (h?h+':'+String(m).padStart(2,'0'):m)+':'+String(s).padStart(2,'0')};` +
		`function show(){v.textContent=f(t)}show();` +
		`function beep(){try{const ac=new (window.AudioContext||window.webkitAudioContext)(),o=ac.createOscillator(),g=ac.createGain();o.frequency.value=880;g.gain.value=.15;o.connect(g);g.connect(ac.destination);o.start();setTimeout(()=>o.stop(),900)}catch(e){}}` +
		`function tick(){const el=(Date.now()-end)/1000;if(sw){t=base+el}else{t=base-el;if(t<=0){t=0;stop();v.textContent='0:00 · time is up';document.title='Time is up';beep();return}}show()}` +
		`let base=init;function stop(){clearInterval(id);id=null;bs.textContent='Start'}` +
		`bs.onclick=()=>{if(id){base=t;stop();return}end=Date.now();base=t;id=setInterval(tick,200);bs.textContent='Pause'};` +
		`document.getElementById('tmr').onclick=()=>{stop();t=init;base=init;show();document.title=document.title.replace(/^Time is up/,'Timer')};` +
		`})();</script>`
}
