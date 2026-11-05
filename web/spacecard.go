package main

// The space card: search "moon", "jupiter", "the sun"... and a card shows NASA's map of that body with a few facts.
// The pictures are the NASA maps kept for the 3D Earth page (/srv/uploads/earthsite/tiles/planets/<name>.jpg). Mars and the Moon have no
// single file there, so their map is put together from the tiles at zoom 2 (8 x 4 tiles of 256 pixels). Every map is handed out as
// JPEG XL like the rest of flowcdn: /flowcdn/space/<name>.jxl, converted once by cjxl and kept in /media/data/flowcdn/space/.

import (
	"encoding/json"
	"fmt"
	"html"
	"image"
	"image/draw"
	"image/jpeg"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	spaceDir   = flowcdnRoot + "/space"
	earthTiles = fsUploads + "/earthsite/tiles"
)

type spaceBody struct {
	Key, Name string
	Names     []string
	Tiles     string // "" = tiles/planets/<key>.jpg; otherwise the tile folder to put the map together from
	Square    bool
	Blurb     string
	Credit    string
}

var spaceBodies = []*spaceBody{
	{Key: "sun", Name: "The Sun", Names: []string{"sun"}, Square: true, Blurb: "The star at the middle of our Solar System. Everything else goes round it.", Credit: "Picture: NASA"},
	{Key: "mercury", Name: "Mercury", Names: []string{"mercury"}, Blurb: "The smallest planet and the closest to the Sun.", Credit: "Pictures: NASA maps"},
	{Key: "venus", Name: "Venus", Names: []string{"venus"}, Blurb: "Thick clouds trap the heat, so it is hotter than Mercury.", Credit: "Pictures: NASA maps"},
	{Key: "earth", Name: "Earth", Names: []string{"earth", "planet earth"}, Blurb: "The only place we know that has life. Seven tenths of it is ocean.", Credit: "Pictures: NASA maps"},
	{Key: "moon", Name: "The Moon", Names: []string{"moon", "luna"}, Tiles: "moon", Blurb: "Earth's only natural moon. It always shows us the same face.", Credit: "Pictures: NASA maps"},
	{Key: "mars", Name: "Mars", Names: []string{"mars"}, Tiles: "mars", Blurb: "The red planet: cold, dusty and dry, with the tallest volcano we know.", Credit: "Pictures: NASA maps"},
	{Key: "jupiter", Name: "Jupiter", Names: []string{"jupiter"}, Blurb: "The biggest planet: all the others would fit inside it. The Great Red Spot is a storm bigger than Earth.", Credit: "Pictures: NASA maps"},
	{Key: "saturn", Name: "Saturn", Names: []string{"saturn"}, Blurb: "Famous for its rings of ice. It is light enough to float in water, if you had a big enough bath.", Credit: "Pictures: NASA maps"},
	{Key: "uranus", Name: "Uranus", Names: []string{"uranus"}, Blurb: "An ice giant that rolls round the Sun on its side.", Credit: "Pictures: NASA maps"},
	{Key: "neptune", Name: "Neptune", Names: []string{"neptune"}, Blurb: "The farthest planet, with the fastest winds in the Solar System.", Credit: "Pictures: NASA maps"},
}

var spaceMu sync.Mutex

func spaceByKey(k string) *spaceBody {
	for _, b := range spaceBodies {
		if b.Key == k {
			return b
		}
	}
	return nil
}

// spaceMatch finds the body a whole query names ("moon", "the moon", "planet mars", "jupiter planet"); "moon landing" is not one.
func spaceMatch(q string) *spaceBody {
	s := strings.Join(strings.Fields(strings.ToLower(q)), " ")
	for _, p := range []string{"the ", "planet "} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.TrimSuffix(s, " planet")
	for _, b := range spaceBodies {
		for _, n := range b.Names {
			if s == n {
				return b
			}
		}
	}
	return nil
}

// ---- the globe: NASA's flat map wrapped round a sphere, lit from the upper left, as a square picture on black ----

type globeView struct {
	lon0, tilt float64 // the longitude in the middle, and how far the north pole leans towards us (degrees)
	rings      bool
	halo       [3]float64
	haloA      float64
}

var globeViews = map[string]globeView{
	"mercury": {0, 0, false, [3]float64{}, 0},
	"venus":   {0, 0, false, [3]float64{255, 230, 170}, 0.35},
	"earth":   {15, 18, false, [3]float64{110, 170, 255}, 0.55},
	"moon":    {0, 4, false, [3]float64{}, 0},
	"mars":    {-70, 14, false, [3]float64{255, 190, 140}, 0.22},
	"jupiter": {20, 3, false, [3]float64{}, 0},
	"saturn":  {0, 24, true, [3]float64{}, 0},
	"uranus":  {0, 8, false, [3]float64{150, 220, 235}, 0.25},
	"neptune": {0, 10, false, [3]float64{110, 150, 255}, 0.25},
}

func ringAlpha(km float64) float64 {
	if km < 74500 || km > 140500 {
		return 0
	}
	var a float64
	switch {
	case km < 92000:
		a = 0.1 + 0.1*(km-74500)/17500 // C ring, faint
	case km < 117500:
		a = 0.78 + 0.2*math.Sin((km-92000)/25500*math.Pi) // B ring, bright
	case km < 122200:
		a = 0.03 // Cassini division
	case km > 133350 && km < 133750:
		a = 0.03 // Encke gap
	case km < 136800:
		a = 0.5 + 0.12*math.Sin(km/400) // A ring
	case km > 140200:
		a = 0.25 // F ring
	}
	return math.Min(1, a*(0.92+0.08*math.Sin(km/37)))
}

func sampleMap(m *image.RGBA, u, v float64) (float64, float64, float64) {
	w, h := m.Bounds().Dx(), m.Bounds().Dy()
	u -= math.Floor(u)
	fx, fy := u*float64(w)-0.5, math.Min(math.Max(v*float64(h)-0.5, 0), float64(h-1))
	x0, y0 := int(math.Floor(fx)), int(fy)
	tx, ty := fx-math.Floor(fx), fy-float64(y0)
	x1, y1 := (x0+1+w)%w, y0+1
	x0 = (x0 + w) % w
	if y1 >= h {
		y1 = h - 1
	}
	var out [3]float64
	for c := 0; c < 3; c++ {
		p := func(x, y int) float64 { return float64(m.Pix[y*m.Stride+x*4+c]) }
		out[c] = (p(x0, y0)*(1-tx)+p(x1, y0)*tx)*(1-ty) + (p(x0, y1)*(1-tx)+p(x1, y1)*tx)*ty
	}
	return out[0], out[1], out[2]
}

func renderGlobe(m *image.RGBA, v globeView, size int) *image.RGBA {
	const ss = 2
	S := size * ss
	cx, cy := float64(S)/2, float64(S)/2
	R := 0.44 * float64(S)
	if v.rings {
		R = float64(S) / 2 / 2.4
	}
	t := v.tilt * math.Pi / 180
	ct, st := math.Cos(t), math.Sin(t)
	lx, ly, lz := -0.55, 0.35, 0.76
	ll := math.Sqrt(lx*lx + ly*ly + lz*lz)
	lx, ly, lz = lx/ll, ly/ll, lz/ll
	big := image.NewRGBA(image.Rect(0, 0, S, S))
	clamp := func(f float64) uint8 { return uint8(math.Min(255, math.Max(0, f))) }
	for py := 0; py < S; py++ {
		for px := 0; px < S; px++ {
			x, y := (float64(px)+0.5-cx)/R, (cy-float64(py)-0.5)/R
			r2 := x*x + y*y
			var cr, cg, cb float64
			hh := uint32((px>>1)*73856093) ^ uint32((py>>1)*19349663)
			hh *= 2654435761
			if hh>>21 < 1400 { // a star (about one pixel in 1500)
				b := 70 + float64((hh>>3)&127)
				cr, cg, cb = b, b, b*1.05
			}
			zs := 0.0
			if r2 < 1 {
				zs = math.Sqrt(1 - r2)
				lat := math.Asin(math.Min(1, math.Max(-1, y*ct+zs*st)))
				lon := math.Atan2(x, -y*st+zs*ct)
				u := (v.lon0+lon*180/math.Pi)/360 + 0.5
				cr, cg, cb = sampleMap(m, u, 0.5-lat/math.Pi)
				lam := math.Max(x*lx+y*ly+zs*lz, 0)
				k := (0.16 + 1.0*math.Pow(lam, 0.8)) * (0.72 + 0.28*math.Pow(zs, 0.35))
				cr, cg, cb = cr*k, cg*k, cb*k
			} else if v.haloA > 0 {
				if rr := math.Sqrt(r2); rr < 1.06 {
					a := 1 - (rr-1)/0.06
					edge := math.Max((x*lx+y*ly)/rr, 0)
					a = a * a * v.haloA * (0.35 + 0.65*edge)
					cr, cg, cb = cr+v.halo[0]*a, cg+v.halo[1]*a, cb+v.halo[2]*a
				}
			}
			if v.rings {
				z := -y * ct / st
				rr := math.Sqrt(r2 + z*z)
				if rr > 74500.0/60268 && rr < 140500.0/60268 && (r2 >= 1 || z > zs) {
					km := rr * 60268
					if a := ringAlpha(km); a > 0 {
						tone := 0.78 + 0.22*math.Sin(km/230)*math.Sin(km/61)
						b := x*lx + y*ly + z*lz
						lit := 0.95
						if b < 0 && b*b-(rr*rr-1) > 0 {
							lit = 0.22 // in the planet's shadow
						}
						cr, cg, cb = cr*(1-a)+236*tone*lit*a, cg*(1-a)+214*tone*lit*a, cb*(1-a)+178*tone*lit*a
					}
				}
			}
			o := big.PixOffset(px, py)
			big.Pix[o], big.Pix[o+1], big.Pix[o+2], big.Pix[o+3] = clamp(cr), clamp(cg), clamp(cb), 255
		}
	}
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var s [3]int
			for dy := 0; dy < ss; dy++ {
				for dx := 0; dx < ss; dx++ {
					o := big.PixOffset(x*ss+dx, y*ss+dy)
					s[0], s[1], s[2] = s[0]+int(big.Pix[o]), s[1]+int(big.Pix[o+1]), s[2]+int(big.Pix[o+2])
				}
			}
			o := out.PixOffset(x, y)
			out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = uint8(s[0]/(ss*ss)), uint8(s[1]/(ss*ss)), uint8(s[2]/(ss*ss)), 255
		}
	}
	return out
}

// spaceMap is NASA's flat map of the body as one RGBA picture.
func spaceMap(b *spaceBody) (*image.RGBA, error) {
	if b.Tiles == "" {
		f, err := os.Open(earthTiles + "/planets/" + b.Key + ".jpg")
		if err != nil {
			return nil, err
		}
		defer f.Close()
		src, err := jpeg.Decode(f)
		if err != nil {
			return nil, err
		}
		m := image.NewRGBA(src.Bounds())
		draw.Draw(m, m.Bounds(), src, src.Bounds().Min, draw.Src)
		return m, nil
	}
	img := image.NewRGBA(image.Rect(0, 0, 2048, 1024))
	draw.Draw(img, img.Bounds(), image.Black, image.Point{}, draw.Src)
	got := 0
	for tx := 0; tx < 8; tx++ {
		for ty := 0; ty < 4; ty++ {
			f, err := os.Open(fmt.Sprintf("%s/%s/2/%d/%d.jpg", earthTiles, b.Tiles, tx, ty))
			if err != nil {
				continue
			}
			t, err := jpeg.Decode(f)
			f.Close()
			if err != nil {
				continue
			}
			draw.Draw(img, image.Rect(tx*256, ty*256, tx*256+256, ty*256+256), t, t.Bounds().Min, draw.Src)
			got++
		}
	}
	if got < 16 {
		return nil, fmt.Errorf("only %d tiles of the %s map found", got, b.Key)
	}
	return img, nil
}

func spacePath(b *spaceBody) string { return spaceDir + "/" + b.Key + "-globe.jxl" }

// makeSpace draws the globe, saves it as a JPEG and turns that into /media/data/flowcdn/space/<key>-globe.jxl (once). cjxl gets the
// JPEG without -q, so the conversion is lossless. The Sun is NASA's own picture of the Sun, converted as it is.
func makeSpace(b *spaceBody) error {
	out := spacePath(b)
	if st, err := os.Stat(out); err == nil && st.Size() > 0 {
		return nil
	}
	if err := os.MkdirAll(spaceDir, 0o755); err != nil {
		return err
	}
	src := earthTiles + "/planets/" + b.Key + ".jpg"
	if b.Key != "sun" {
		m, err := spaceMap(b)
		if err != nil {
			return err
		}
		src = spaceDir + "/" + b.Key + ".globe.jpg"
		f, err := os.Create(src)
		if err != nil {
			return err
		}
		err = jpeg.Encode(f, renderGlobe(m, globeViews[b.Key], 640), &jpeg.Options{Quality: 93})
		f.Close()
		defer os.Remove(src)
		if err != nil {
			return err
		}
	}
	tmp := spaceDir + "/" + b.Key + ".tmp.jxl"
	if msg, err := exec.Command("cjxl", src, tmp).CombinedOutput(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("cjxl: %v: %.200s", err, msg)
	}
	return os.Rename(tmp, out)
}

func flowcdnSpace(w http.ResponseWriter, r *http.Request) {
	b := spaceByKey(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/flowcdn/space/"), ".jxl"))
	if b == nil {
		http.NotFound(w, r)
		return
	}
	spaceMu.Lock()
	err := makeSpace(b)
	spaceMu.Unlock()
	if err != nil {
		log.Println("space picture:", b.Key, err)
		http.Error(w, "no picture", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/jxl")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, spacePath(b))
}

const spaceCSS = `<style>
.spacecard{display:flex;gap:24px;align-items:center;margin:0 0 18px;padding:16px 20px 16px 16px}
.spacecard .spimg{flex:none;width:min(300px,40%);aspect-ratio:1/1;object-fit:cover;border-radius:18px;background:#000;box-shadow:0 8px 28px rgba(0,0,0,.35)}
.spacecard h2{margin:0 0 4px;font-size:28px;letter-spacing:-.02em}
.spacecard .bl{margin:0 0 12px;color:var(--dim);font-size:15.5px;line-height:1.5}
.spacecard dl{display:grid;grid-template-columns:auto 1fr;gap:4px 18px;margin:0;font-size:14.5px}
.spacecard dt{color:var(--dim)}.spacecard dd{margin:0}
.spacecard .cr{margin:12px 0 0;font-size:12px;color:var(--dim)}
@media(max-width:640px){.spacecard{flex-direction:column;align-items:stretch}.spacecard .spimg{width:min(100%,360px);align-self:center}}
</style>`

// spaceCard is the card for a query that names a planet, the Moon or the Sun, or "" for any other query.
func spaceCard(q string) string {
	b := spaceMatch(q)
	if b == nil {
		return ""
	}
	var s strings.Builder
	s.WriteString(spaceCSS)
	cls := "spimg"
	if b.Square {
		cls += " sq"
	}
	fmt.Fprintf(&s, `<aside class="gc spacecard"><img class="%s" src="/flowcdn/space/%s.jxl" alt="%s, from NASA maps" loading=eager><div><h2>%s</h2><p class=bl>%s</p><dl>`, cls, b.Key, html.EscapeString(b.Name), html.EscapeString(b.Name), html.EscapeString(b.Blurb))
	rows, src, date := spaceFactRows(b.Key)
	for _, f := range rows {
		fmt.Fprintf(&s, `<dt>%s</dt><dd>%s</dd>`, html.EscapeString(f[0]), html.EscapeString(f[1]))
	}
	cr := html.EscapeString(b.Credit)
	if src != "" {
		cr = `Facts: <a href="` + html.EscapeString(src) + `" rel="noopener noreferrer">NASA Planetary Fact Sheet</a> (read ` + html.EscapeString(date) + `) · ` + cr
	}
	fmt.Fprintf(&s, `</dl><p class=cr>%s</p></div></aside>`, cr)
	return s.String()
}

// ---- the facts: read from NASA's Planetary Fact Sheets (kept by cmd/spacefacts in /media/data/mysearch/space-facts.json) ----

var spaceFactsFile = fsData + "/space-facts.json"

type nasaFacts struct {
	Mass      float64 `json:"mass_1e24kg"`
	Radius    float64 `json:"radius_km"`
	Gravity   float64 `json:"gravity_ms2"`
	Semimajor float64 `json:"semimajor_1e6km"`
	Orbit     float64 `json:"orbit_days"`
	Synodic   float64 `json:"synodic_days"`
	Day       float64 `json:"day_hours"`
	Rotation  float64 `json:"rotation_hours"`
	BBTemp    float64 `json:"blackbody_k"`
	Lum       float64 `json:"luminosity_1e24w"`
	Moons     *int    `json:"moons"`
	Rings     string  `json:"rings"`
	Spectral  string  `json:"spectral"`
	URL       string  `json:"url"`
}

var (
	spaceFactsMu    sync.Mutex
	spaceFactsMtime time.Time
	spaceFactsAll   map[string]nasaFacts
	spaceFactsDate  string
)

func loadSpaceFacts() (map[string]nasaFacts, string) {
	spaceFactsMu.Lock()
	defer spaceFactsMu.Unlock()
	st, err := os.Stat(spaceFactsFile)
	if err != nil {
		return nil, ""
	}
	if !st.ModTime().Equal(spaceFactsMtime) {
		var doc struct {
			Updated string               `json:"updated"`
			Bodies  map[string]nasaFacts `json:"bodies"`
		}
		if b, err := os.ReadFile(spaceFactsFile); err == nil && json.Unmarshal(b, &doc) == nil {
			spaceFactsAll, spaceFactsDate, spaceFactsMtime = doc.Bodies, doc.Updated, st.ModTime()
		}
	}
	return spaceFactsAll, spaceFactsDate
}

func commasF(v float64, dec int) string {
	s := strconv.FormatFloat(math.Abs(v), 'f', dec, 64)
	whole, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		whole, frac = s[:i], s[i:]
	}
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	if v < 0 {
		whole = "−" + whole
	}
	return whole + frac
}

func times(v float64) string { // "318 times", "0.27 times"
	switch {
	case v >= 100:
		return commasF(v, 0)
	case v >= 10:
		return commasF(v, 1)
	}
	return commasF(v, 2)
}

func hoursText(h float64) string {
	if h < 0 {
		h = -h
	}
	if h < 48 {
		hh := int(h)
		return fmt.Sprintf("%d h %02d min", hh, int((h-float64(hh))*60+0.5))
	}
	return commasF(h/24, 1) + " Earth days"
}

func daysText(d float64) string {
	if d >= 730 {
		return commasF(d/365.25, 1) + " Earth years"
	}
	return commasF(d, 1) + " Earth days"
}

func millionKm(v float64) string {
	switch {
	case v >= 1000:
		return commasF(v/1000, 2) + " billion km"
	case v >= 1:
		return commasF(v, 0) + " million km"
	}
	return commasF(v*1000000, 0) + " km"
}

// spaceFactRows turns NASA's numbers for one body into the rows of the card.
func spaceFactRows(key string) ([][2]string, string, string) {
	all, date := loadSpaceFacts()
	f, ok := all[key]
	if !ok {
		return nil, "", ""
	}
	earth := all["earth"]
	var rows [][2]string
	add := func(k, v string) { rows = append(rows, [2]string{k, v}) }
	if key == "sun" && f.Spectral != "" {
		add("Kind", "A star, type "+f.Spectral)
	}
	if f.Radius > 0 {
		w := commasF(2*f.Radius, 0) + " km"
		if key != "earth" && earth.Radius > 0 {
			w += " (" + times(f.Radius/earth.Radius) + " × Earth)"
		}
		add("Width", w)
	}
	if f.Mass > 0 {
		var m string
		switch {
		case f.Mass >= 10000:
			m = commasF(f.Mass/1e6, 2) + " × 10³⁰ kg"
		case f.Mass >= 0.1:
			m = commasF(f.Mass, 2) + " × 10²⁴ kg"
		default:
			m = commasF(f.Mass*100, 2) + " × 10²² kg"
		}
		if key != "earth" && earth.Mass > 0 {
			m += " (" + times(f.Mass/earth.Mass) + " × Earth)"
		}
		add("Mass", m)
	}
	if f.Gravity > 0 {
		g := commasF(f.Gravity, 2) + " m/s²"
		if key != "earth" && earth.Gravity > 0 {
			g += " (" + times(f.Gravity/earth.Gravity) + " × Earth)"
		}
		add("Gravity", g)
	}
	if f.Semimajor > 0 {
		if key == "moon" {
			add("From Earth", millionKm(f.Semimajor))
		} else {
			add("From the Sun", millionKm(f.Semimajor)+" ("+commasF(f.Semimajor/149.598, 2)+" AU)")
		}
	}
	if f.Day > 0 {
		add("One day", hoursText(f.Day))
	}
	if r := math.Abs(f.Rotation); r > 0 && (f.Day == 0 || math.Abs(r-f.Day)/f.Day > 0.05) {
		s := hoursText(r)
		if f.Rotation < 0 {
			s += ", backwards"
		}
		add("Spins once", s)
	}
	if f.Orbit > 0 {
		if key == "moon" {
			add("Orbit round Earth", commasF(f.Orbit, 2)+" days")
		} else {
			add("One year", daysText(f.Orbit))
		}
	}
	if key == "moon" && f.Synodic > 0 {
		add("Phases repeat", "every "+commasF(f.Synodic, 2)+" days")
	}
	if f.Moons != nil {
		if *f.Moons == 0 {
			add("Moons", "none")
		} else {
			add("Moons", strconv.Itoa(*f.Moons))
		}
	}
	if f.Rings == "Yes" {
		add("Rings", "yes")
	}
	if f.BBTemp > 0 {
		add("Sunlight alone warms it to", commasF(f.BBTemp-273.15, 0)+" °C")
	}
	if f.Lum > 0 {
		add("Light given off", commasF(f.Lum/100, 2)+" × 10²⁶ W")
	}
	return rows, f.URL, date
}
