package main

// Instant answers: a few kinds of searches are answered by a card above the results, worked out here, with no other site involved.
//   * maths:   12*(3+4)   sqrt(144)   2^10   15% of 80   calculate 7/3
//   * units:   5 km in miles   100 f to c   2 kg to lb   60 mph in km/h   3 gb to mb
//   * time:    time in tokyo   new york time   what time is it in berlin
//   * dice:    roll a dice   2d6   d20   flip a coin   random number 1 to 50
//   * colour:  #ff8800   rgb(255,136,0)
//   * other:   password 20   uuid   today   unix time   days until 2026-12-25   days until christmas

import (
	"crypto/rand"
	"fmt"
	"html"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const instantCSS = `<style>.ia{max-width:640px;margin:18px 0;padding:16px 22px;border-radius:22px;border:1px solid rgba(255,255,255,.16);background:var(--card);box-shadow:0 14px 34px -26px rgba(0,0,0,.8)}.ia small{display:block;color:var(--dim);letter-spacing:.08em;text-transform:uppercase;font-size:11.5px;margin:0 0 4px}.ia .big{font-size:34px;font-weight:700;letter-spacing:-.02em;font-variant-numeric:tabular-nums;word-break:break-all}.ia .sub{color:var(--dim);font-size:14.5px;margin-top:4px}.ia .sw{width:100%;height:74px;border-radius:14px;margin:6px 0 10px;border:1px solid var(--line)}.ia code{font:inherit;font-family:ui-monospace,Menlo,monospace;font-size:15px}.ia .row{display:flex;gap:18px;flex-wrap:wrap;margin-top:6px}</style>`

func iaCard(label, big, sub string) string {
	s := ""
	if sub != "" {
		s = `<div class=sub>` + sub + `</div>`
	}
	return instantCSS + `<div class=ia><small>` + html.EscapeString(label) + `</small><div class=big>` + html.EscapeString(big) + `</div>` + s + `</div>`
}

// instantCard returns the card for the query, or "" when the query is not one of the kinds above.
func instantCard(q string) string {
	q = strings.TrimSpace(q)
	if q == "" || len(q) > 120 {
		return ""
	}
	low := strings.ToLower(q)
	for _, f := range []func(string, string) string{iaMore, iaMath, iaUnits, iaTime, iaDice, iaColour, iaOther} {
		if c := f(q, low); c != "" {
			return c
		}
	}
	return ""
}

// ---------- maths ----------

var (
	percentOfRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*%\s*of\s*([0-9]+(?:\.[0-9]+)?)$`)
	mathOnlyRe  = regexp.MustCompile(`^[0-9a-z+\-*/^%().,\s×÷]+$`)
)

type mathParser struct {
	s   string
	pos int
	err bool
}

func (p *mathParser) ws() {
	for p.pos < len(p.s) && p.s[p.pos] == ' ' {
		p.pos++
	}
}

func (p *mathParser) peek() byte {
	p.ws()
	if p.pos < len(p.s) {
		return p.s[p.pos]
	}
	return 0
}

func (p *mathParser) expr() float64 {
	v := p.term()
	for {
		switch p.peek() {
		case '+':
			p.pos++
			v += p.term()
		case '-':
			p.pos++
			v -= p.term()
		default:
			return v
		}
	}
}

func (p *mathParser) term() float64 {
	v := p.power()
	for {
		switch p.peek() {
		case '*':
			p.pos++
			v *= p.power()
		case '/':
			p.pos++
			d := p.power()
			if d == 0 {
				p.err = true
				return 0
			}
			v /= d
		case '%':
			p.pos++
			d := p.power()
			if d == 0 {
				p.err = true
				return 0
			}
			v = math.Mod(v, d)
		default:
			return v
		}
	}
}

func (p *mathParser) power() float64 {
	b := p.unary()
	if p.peek() == '^' {
		p.pos++
		e := p.power() // right to left: 2^3^2 = 2^(3^2)
		if math.Abs(e) > 1000 || math.Abs(b) > 1e100 {
			p.err = true
			return 0
		}
		return math.Pow(b, e)
	}
	return b
}

func (p *mathParser) unary() float64 {
	switch p.peek() {
	case '-':
		p.pos++
		return -p.unary()
	case '+':
		p.pos++
		return p.unary()
	}
	return p.atom()
}

func (p *mathParser) atom() float64 {
	c := p.peek()
	if c == '(' {
		p.pos++
		v := p.expr()
		if p.peek() != ')' {
			p.err = true
			return 0
		}
		p.pos++
		return v
	}
	if c >= '0' && c <= '9' || c == '.' {
		st := p.pos
		for p.pos < len(p.s) && (p.s[p.pos] >= '0' && p.s[p.pos] <= '9' || p.s[p.pos] == '.') {
			p.pos++
		}
		v, err := strconv.ParseFloat(p.s[st:p.pos], 64)
		if err != nil {
			p.err = true
		}
		return v
	}
	if c >= 'a' && c <= 'z' {
		st := p.pos
		for p.pos < len(p.s) && p.s[p.pos] >= 'a' && p.s[p.pos] <= 'z' {
			p.pos++
		}
		name := p.s[st:p.pos]
		switch name {
		case "pi":
			return math.Pi
		case "e":
			return math.E
		}
		if p.peek() != '(' {
			p.err = true
			return 0
		}
		p.pos++
		a := p.expr()
		if p.peek() != ')' {
			p.err = true
			return 0
		}
		p.pos++
		switch name {
		case "sqrt":
			if a < 0 {
				p.err = true
				return 0
			}
			return math.Sqrt(a)
		case "abs":
			return math.Abs(a)
		case "sin":
			return math.Sin(a * math.Pi / 180)
		case "cos":
			return math.Cos(a * math.Pi / 180)
		case "tan":
			return math.Tan(a * math.Pi / 180)
		case "ln":
			if a <= 0 {
				p.err = true
				return 0
			}
			return math.Log(a)
		case "log":
			if a <= 0 {
				p.err = true
				return 0
			}
			return math.Log10(a)
		case "round":
			return math.Round(a)
		case "floor":
			return math.Floor(a)
		case "ceil":
			return math.Ceil(a)
		}
	}
	p.err = true
	return 0
}

func fmtNum(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		s := commasF(v, 0)
		if v < 0 && !strings.HasPrefix(s, "-") {
			s = "-" + s
		}
		return s
	}
	s := strconv.FormatFloat(v, 'g', 12, 64)
	if strings.ContainsAny(s, "e") {
		return s
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && math.Abs(f) < 1e15 {
		s = strconv.FormatFloat(f, 'f', -1, 64)
	}
	return s
}

func iaMath(q, low string) string {
	if m := percentOfRe.FindStringSubmatch(low); m != nil {
		p, _ := strconv.ParseFloat(m[1], 64)
		n, _ := strconv.ParseFloat(m[2], 64)
		return iaCard("Percent", fmtNum(p*n/100), html.EscapeString(m[1]+"% of "+m[2]))
	}
	expr := low
	for _, pre := range []string{"calculate ", "calc ", "what is ", "what's ", "="} {
		expr = strings.TrimPrefix(expr, pre)
	}
	expr = strings.TrimSpace(strings.NewReplacer("×", "*", "÷", "/", ",", "").Replace(expr))
	if expr == "" || !mathOnlyRe.MatchString(expr) {
		return ""
	}
	// it has to look like a calculation: an operator between two things, or a function
	hasOp := strings.ContainsAny(expr, "+*/^%") || regexp.MustCompile(`[0-9)]\s*-\s*[0-9(]`).MatchString(expr)
	hasFn := regexp.MustCompile(`\b(sqrt|abs|sin|cos|tan|ln|log|round|floor|ceil)\(`).MatchString(expr)
	if !hasOp && !hasFn {
		return ""
	}
	p := &mathParser{s: expr}
	v := p.expr()
	if p.peek() != 0 || p.err || math.IsNaN(v) || math.IsInf(v, 0) {
		return ""
	}
	return iaCard("Calculator", "= "+fmtNum(v), html.EscapeString(strings.TrimSpace(q)))
}

// ---------- units ----------

type unitDef struct {
	kind string
	f    float64 // how many base units
	name string
}

var unitTable = map[string]unitDef{}

func init() {
	add := func(kind string, f float64, name string, aliases ...string) {
		for _, a := range aliases {
			unitTable[a] = unitDef{kind, f, name}
		}
	}
	add("length", 1, "metres", "m", "meter", "meters", "metre", "metres")
	add("length", 1000, "kilometres", "km", "kilometer", "kilometers", "kilometre", "kilometres")
	add("length", 0.01, "centimetres", "cm", "centimeter", "centimeters", "centimetre", "centimetres")
	add("length", 0.001, "millimetres", "mm", "millimeter", "millimeters", "millimetre", "millimetres")
	add("length", 1609.344, "miles", "mi", "mile", "miles")
	add("length", 0.9144, "yards", "yd", "yard", "yards")
	add("length", 0.3048, "feet", "ft", "foot", "feet")
	add("length", 0.0254, "inches", "in", "inch", "inches")
	add("length", 1852, "nautical miles", "nmi", "nm")
	add("mass", 1, "kilograms", "kg", "kilogram", "kilograms", "kilo", "kilos")
	add("mass", 0.001, "grams", "g", "gram", "grams")
	add("mass", 0.000001, "milligrams", "mg", "milligram", "milligrams")
	add("mass", 1000, "tonnes", "t", "tonne", "tonnes", "ton", "tons")
	add("mass", 0.45359237, "pounds", "lb", "lbs", "pound", "pounds")
	add("mass", 0.028349523125, "ounces", "oz", "ounce", "ounces")
	add("mass", 6.35029318, "stone", "st", "stone")
	add("volume", 1, "litres", "l", "liter", "liters", "litre", "litres")
	add("volume", 0.001, "millilitres", "ml", "milliliter", "milliliters", "millilitre", "millilitres")
	add("volume", 3.785411784, "US gallons", "gal", "gallon", "gallons")
	add("volume", 0.946352946, "US quarts", "qt", "quart", "quarts")
	add("volume", 0.473176473, "US pints", "pt", "pint", "pints")
	add("volume", 0.2365882365, "US cups", "cup", "cups")
	add("volume", 0.0295735295625, "US fluid ounces", "floz", "fl oz")
	add("volume", 0.01478676478, "tablespoons", "tbsp", "tablespoon", "tablespoons")
	add("volume", 0.00492892159, "teaspoons", "tsp", "teaspoon", "teaspoons")
	add("speed", 1, "m/s", "m/s", "mps")
	add("speed", 1.0/3.6, "km/h", "km/h", "kmh", "kph", "kmph")
	add("speed", 0.44704, "mph", "mph", "mi/h")
	add("speed", 0.514444444, "knots", "kn", "kt", "knot", "knots")
	add("speed", 0.3048, "ft/s", "ft/s", "fps")
	add("area", 1, "m²", "m2", "m²", "sqm")
	add("area", 1e6, "km²", "km2", "km²")
	add("area", 10000, "hectares", "ha", "hectare", "hectares")
	add("area", 4046.8564224, "acres", "acre", "acres")
	add("area", 0.09290304, "ft²", "ft2", "ft²", "sqft")
	add("area", 2589988.110336, "mi²", "mi2", "mi²")
	add("data", 1, "bytes", "b", "byte", "bytes")
	add("data", 1e3, "kB", "kb", "kilobyte", "kilobytes")
	add("data", 1e6, "MB", "mb", "megabyte", "megabytes")
	add("data", 1e9, "GB", "gb", "gigabyte", "gigabytes")
	add("data", 1e12, "TB", "tb", "terabyte", "terabytes")
	add("data", 1024, "KiB", "kib")
	add("data", 1048576, "MiB", "mib")
	add("data", 1073741824, "GiB", "gib")
	add("time", 1, "seconds", "s", "sec", "second", "seconds")
	add("time", 60, "minutes", "min", "minute", "minutes")
	add("time", 3600, "hours", "h", "hr", "hour", "hours")
	add("time", 86400, "days", "day", "days")
	add("time", 604800, "weeks", "week", "weeks")
	add("temp", 1, "°C", "c", "°c", "celsius")
	add("temp", 2, "°F", "f", "°f", "fahrenheit")
	add("temp", 3, "K", "k", "kelvin")
}

var unitRe = regexp.MustCompile(`^(-?[0-9]+(?:[.,][0-9]+)?)\s*([a-z°²/ ]+?)\s+(?:in|to|into|as)\s+([a-z°²/ ]+)$`)

func toTemp(v float64, from int) float64 { // to Celsius
	switch from {
	case 2:
		return (v - 32) * 5 / 9
	case 3:
		return v - 273.15
	}
	return v
}

func fromTemp(c float64, to int) float64 {
	switch to {
	case 2:
		return c*9/5 + 32
	case 3:
		return c + 273.15
	}
	return c
}

func iaUnits(q, low string) string {
	m := unitRe.FindStringSubmatch(low)
	if m == nil {
		return ""
	}
	v, err := strconv.ParseFloat(strings.Replace(m[1], ",", ".", 1), 64)
	if err != nil {
		return ""
	}
	from, ok1 := unitTable[strings.TrimSpace(m[2])]
	to, ok2 := unitTable[strings.TrimSpace(m[3])]
	if !ok1 || !ok2 || from.kind != to.kind {
		return ""
	}
	var res float64
	if from.kind == "temp" {
		res = fromTemp(toTemp(v, int(from.f)), int(to.f))
	} else {
		res = v * from.f / to.f
	}
	sig := strconv.FormatFloat(res, 'g', 8, 64)
	if f, err := strconv.ParseFloat(sig, 64); err == nil && math.Abs(f) < 1e15 && math.Abs(f) >= 1e-6 {
		sig = strconv.FormatFloat(f, 'f', -1, 64)
	}
	return iaCard("Conversion", sig+" "+to.name, html.EscapeString(fmtNum(v)+" "+from.name+" ="))
}

// ---------- time in a city ----------

var cityZones = map[string]string{
	"london": "Europe/London", "paris": "Europe/Paris", "berlin": "Europe/Berlin", "madrid": "Europe/Madrid", "rome": "Europe/Rome", "amsterdam": "Europe/Amsterdam",
	"vienna": "Europe/Vienna", "zurich": "Europe/Zurich", "stockholm": "Europe/Stockholm", "oslo": "Europe/Oslo", "helsinki": "Europe/Helsinki", "athens": "Europe/Athens",
	"istanbul": "Europe/Istanbul", "moscow": "Europe/Moscow", "kyiv": "Europe/Kyiv", "lisbon": "Europe/Lisbon", "dublin": "Europe/Dublin", "warsaw": "Europe/Warsaw",
	"new york": "America/New_York", "washington": "America/New_York", "boston": "America/New_York", "miami": "America/New_York", "chicago": "America/Chicago", "dallas": "America/Chicago",
	"denver": "America/Denver", "los angeles": "America/Los_Angeles", "san francisco": "America/Los_Angeles", "seattle": "America/Los_Angeles", "toronto": "America/Toronto",
	"vancouver": "America/Vancouver", "mexico city": "America/Mexico_City", "sao paulo": "America/Sao_Paulo", "buenos aires": "America/Argentina/Buenos_Aires", "bogota": "America/Bogota",
	"lima": "America/Lima", "santiago": "America/Santiago", "honolulu": "Pacific/Honolulu", "anchorage": "America/Anchorage",
	"tokyo": "Asia/Tokyo", "seoul": "Asia/Seoul", "beijing": "Asia/Shanghai", "shanghai": "Asia/Shanghai", "hong kong": "Asia/Hong_Kong", "singapore": "Asia/Singapore",
	"bangkok": "Asia/Bangkok", "jakarta": "Asia/Jakarta", "manila": "Asia/Manila", "delhi": "Asia/Kolkata", "mumbai": "Asia/Kolkata", "dubai": "Asia/Dubai", "tehran": "Asia/Tehran",
	"karachi": "Asia/Karachi", "dhaka": "Asia/Dhaka", "riyadh": "Asia/Riyadh", "tel aviv": "Asia/Jerusalem", "jerusalem": "Asia/Jerusalem",
	"sydney": "Australia/Sydney", "melbourne": "Australia/Melbourne", "perth": "Australia/Perth", "auckland": "Pacific/Auckland",
	"cairo": "Africa/Cairo", "lagos": "Africa/Lagos", "nairobi": "Africa/Nairobi", "johannesburg": "Africa/Johannesburg", "cape town": "Africa/Johannesburg", "casablanca": "Africa/Casablanca",
	"utc": "UTC", "gmt": "UTC",
}

var timeRe = regexp.MustCompile(`^(?:what(?:'s| is) the )?(?:current )?(?:local )?time(?: is it)?(?: right now)? in ([a-z ]+)$|^([a-z ]+?) (?:local )?time(?: now)?$`)

func iaTime(q, low string) string {
	m := timeRe.FindStringSubmatch(low)
	if m == nil {
		return ""
	}
	city := strings.TrimSpace(m[1] + m[2])
	zone, ok := cityZones[city]
	if !ok {
		return ""
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return ""
	}
	n := time.Now().In(loc)
	name, off := n.Zone()
	sign := "+"
	if off < 0 {
		sign = "-"
		off = -off
	}
	title := strings.Title(city) //nolint:staticcheck // city names only
	if city == "utc" || city == "gmt" {
		title = "UTC"
	}
	return iaCard("Time in "+title, n.Format("15:04"), html.EscapeString(n.Format("Monday 2 January 2006")+" · "+name+" (UTC"+sign+fmt.Sprintf("%02d:%02d", off/3600, off%3600/60)+")"))
}

// ---------- dice, coins, random numbers ----------

func randInt(lo, hi int) int { // lo..hi, both included
	if hi < lo {
		lo, hi = hi, lo
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(hi-lo+1)))
	if err != nil {
		return lo
	}
	return lo + int(n.Int64())
}

var (
	diceRe   = regexp.MustCompile(`^(?:roll (?:a |an )?)?(\d{0,2})d(\d{1,3})$|^roll (?:a |an )?(?:dice|die)$|^(?:throw|roll) (?:the )?dice$`)
	randNumRe = regexp.MustCompile(`^(?:random number|random)(?: (?:between )?(-?\d{1,9}) (?:to|and|-) (-?\d{1,9}))?$`)
)

func iaDice(q, low string) string {
	switch low {
	case "flip a coin", "coin flip", "toss a coin", "heads or tails", "flip coin":
		r := "Heads"
		if randInt(0, 1) == 1 {
			r = "Tails"
		}
		return iaCard("Coin flip", r, "Reload the page for another flip.")
	}
	if m := diceRe.FindStringSubmatch(low); m != nil {
		count, sides := 1, 6
		if m[2] != "" {
			if m[1] != "" {
				count, _ = strconv.Atoi(m[1])
			}
			sides, _ = strconv.Atoi(m[2])
		}
		if count < 1 || count > 20 || sides < 2 || sides > 1000 {
			return ""
		}
		var parts []string
		sum := 0
		for i := 0; i < count; i++ {
			r := randInt(1, sides)
			sum += r
			parts = append(parts, strconv.Itoa(r))
		}
		sub := fmt.Sprintf("%dd%d · reload the page to roll again", count, sides)
		if count > 1 {
			sub = strings.Join(parts, " + ") + " · " + sub
		}
		return iaCard("Dice", strconv.Itoa(sum), html.EscapeString(sub))
	}
	if m := randNumRe.FindStringSubmatch(low); m != nil {
		lo, hi := 1, 100
		if m[1] != "" {
			lo, _ = strconv.Atoi(m[1])
			hi, _ = strconv.Atoi(m[2])
		}
		if lo > hi {
			lo, hi = hi, lo
		}
		return iaCard("Random number", strconv.Itoa(randInt(lo, hi)), html.EscapeString(fmt.Sprintf("between %d and %d · reload the page for another", lo, hi)))
	}
	return ""
}

// ---------- colours ----------

var (
	hexRe = regexp.MustCompile(`^#([0-9a-f]{6}|[0-9a-f]{3})$`)
	rgbRe = regexp.MustCompile(`^rgb\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*\)$`)
)

func iaColour(q, low string) string {
	var r, g, b int
	if m := hexRe.FindStringSubmatch(low); m != nil {
		h := m[1]
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		v, _ := strconv.ParseUint(h, 16, 32)
		r, g, b = int(v>>16&255), int(v>>8&255), int(v&255)
	} else if m := rgbRe.FindStringSubmatch(low); m != nil {
		r, _ = strconv.Atoi(m[1])
		g, _ = strconv.Atoi(m[2])
		b, _ = strconv.Atoi(m[3])
		if r > 255 || g > 255 || b > 255 {
			return ""
		}
	} else {
		return ""
	}
	// hsl
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	mx, mn := math.Max(rf, math.Max(gf, bf)), math.Min(rf, math.Min(gf, bf))
	l := (mx + mn) / 2
	var h, s float64
	if mx != mn {
		d := mx - mn
		if l > 0.5 {
			s = d / (2 - mx - mn)
		} else {
			s = d / (mx + mn)
		}
		switch mx {
		case rf:
			h = math.Mod((gf-bf)/d, 6)
		case gf:
			h = (bf-rf)/d + 2
		default:
			h = (rf-gf)/d + 4
		}
		h *= 60
		if h < 0 {
			h += 360
		}
	}
	hex := fmt.Sprintf("#%02x%02x%02x", r, g, b)
	return instantCSS + `<div class=ia><small>Colour</small><div class=sw style="background:` + hex + `"></div><div class=row><span><code>` + hex + `</code></span><span><code>rgb(` +
		strconv.Itoa(r) + `, ` + strconv.Itoa(g) + `, ` + strconv.Itoa(b) + `)</code></span><span><code>hsl(` + fmt.Sprintf("%.0f, %.0f%%, %.0f%%", h, s*100, l*100) + `)</code></span></div></div>`
}

// ---------- passwords, ids, dates ----------

var (
	pwRe    = regexp.MustCompile(`^(?:generate |random |strong )?password(?: (\d{1,2}))?$`)
	untilRe = regexp.MustCompile(`^(?:days|how many days) (?:until|till|to) (\d{4}-\d{2}-\d{2}|christmas|new year|new years|halloween|valentines day|easter)$`)
)

func iaOther(q, low string) string {
	switch low {
	case "uuid", "generate uuid", "random uuid", "guid":
		b := make([]byte, 16)
		rand.Read(b)
		b[6] = b[6]&0x0f | 0x40
		b[8] = b[8]&0x3f | 0x80
		return iaCard("UUID (version 4)", fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), "Reload the page for another one.")
	case "unix time", "unix timestamp", "timestamp", "epoch", "epoch time":
		return iaCard("Unix time", strconv.FormatInt(time.Now().Unix(), 10), "seconds since 1 January 1970 (UTC)")
	case "today", "date", "today's date", "what is the date", "what is the date today", "what day is it", "what day is it today", "current date":
		n := time.Now()
		y, w := n.ISOWeek()
		return iaCard("Today", n.Format("Monday 2 January 2006"), fmt.Sprintf("week %d of %d · day %d of the year", w, y, n.YearDay()))
	}
	if m := pwRe.FindStringSubmatch(low); m != nil {
		n := 16
		if m[1] != "" {
			n, _ = strconv.Atoi(m[1])
		}
		if n < 8 || n > 64 {
			n = 16
		}
		const set = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789!@#$%&*?"
		out := make([]byte, n)
		for i := range out {
			out[i] = set[randInt(0, len(set)-1)]
		}
		return iaCard("Password", string(out), fmt.Sprintf("%d characters, made on this server and not stored · reload the page for another one", n))
	}
	if m := untilRe.FindStringSubmatch(low); m != nil {
		now := time.Now()
		var t time.Time
		switch m[1] {
		case "christmas", "new year", "new years", "halloween", "valentines day", "easter":
			md := map[string][2]int{"christmas": {12, 25}, "new year": {1, 1}, "new years": {1, 1}, "halloween": {10, 31}, "valentines day": {2, 14}}[m[1]]
			if m[1] == "easter" {
				return ""
			}
			t = time.Date(now.Year(), time.Month(md[0]), md[1], 0, 0, 0, 0, now.Location())
			if !t.After(now.Truncate(24 * time.Hour)) {
				t = t.AddDate(1, 0, 0)
			}
		default:
			var err error
			if t, err = time.ParseInLocation("2006-01-02", m[1], now.Location()); err != nil {
				return ""
			}
		}
		days := int(math.Round(t.Sub(now.Truncate(24*time.Hour)).Hours() / 24))
		lab := "days"
		if days == 1 || days == -1 {
			lab = "day"
		}
		sub := t.Format("Monday 2 January 2006")
		if days < 0 {
			sub += " · this was " + strconv.Itoa(-days) + " " + lab + " ago"
		}
		return iaCard("Days until "+m[1], commas(days)+" "+lab, html.EscapeString(sub))
	}
	return ""
}
