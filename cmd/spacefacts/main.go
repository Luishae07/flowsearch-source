package main

// spacefacts: reads NASA's Planetary Fact Sheets (nssdc.gsfc.nasa.gov/planetary/factsheet/) for the Sun, the planets and the Moon and keeps the
// numbers the Flowsearch space card shows in /media/data/mysearch/space-facts.json. NASA's pages are plain HTML tables, so each number is found
// by its row label in the page text. The old file is kept when a page looks wrong. Run by flowsearch-space-facts.timer once a month.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var out = fsData + "/space-facts.json"

var bodies = []string{"sun", "mercury", "venus", "earth", "moon", "mars", "jupiter", "saturn", "uranus", "neptune"}

type Facts struct {
	Mass      float64 `json:"mass_1e24kg,omitempty"`
	Radius    float64 `json:"radius_km,omitempty"`
	Gravity   float64 `json:"gravity_ms2,omitempty"`
	Semimajor float64 `json:"semimajor_1e6km,omitempty"`
	Orbit     float64 `json:"orbit_days,omitempty"`
	Synodic   float64 `json:"synodic_days,omitempty"`
	Day       float64 `json:"day_hours,omitempty"`
	Rotation  float64 `json:"rotation_hours,omitempty"`
	BBTemp    float64 `json:"blackbody_k,omitempty"`
	Lum       float64 `json:"luminosity_1e24w,omitempty"`
	Moons     *int    `json:"moons,omitempty"`
	Rings     string  `json:"rings,omitempty"`
	Spectral  string  `json:"spectral,omitempty"`
	URL       string  `json:"url"`
}

var (
	tagRe   = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>`)
	anyTag  = regexp.MustCompile(`<[^>]+>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

func flatten(h string) string {
	h = tagRe.ReplaceAllString(h, " ")
	h = anyTag.ReplaceAllString(h, " ")
	h = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&times;", "x").Replace(h)
	return spaceRe.ReplaceAllString(h, " ")
}

func num(text, pattern string) float64 {
	m := regexp.MustCompile(pattern).FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	s := strings.TrimRight(strings.NewReplacer(",", "", "*", "").Replace(m[1]), ".")
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func str(text, pattern string) string {
	if m := regexp.MustCompile(pattern).FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

func main() {
	res := map[string]Facts{}
	cl := &http.Client{Timeout: 40 * time.Second}
	for _, b := range bodies {
		u := "https://nssdc.gsfc.nasa.gov/planetary/factsheet/" + b + "fact.html"
		req, _ := http.NewRequest("GET", u, nil)
		req.Header.Set("User-Agent", "flowsearch-spacefacts")
		r, err := cl.Do(req)
		if err != nil {
			fmt.Fprintln(os.Stderr, b, err)
			os.Exit(1)
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		r.Body.Close()
		t := flatten(string(body))
		f := Facts{URL: u,
			Mass: num(t, `Mass \(10 ?24 kg\) ([\d,\.]+)`), Radius: num(t, `Volumetric mean radius \(km\) ([\d,\.]+)`),
			Gravity:   num(t, `(?:Surface gravity|Gravity)(?: \([^)]*\))* ?\(m/s ?2 ?\) ([\d,\.]+)`),
			Semimajor: num(t, `Semimajor axis \(10 ?6 km\) ([\d,\.]+)`), Synodic: num(t, `Synodic period \(days\) ([\d,\.]+)`),
			Orbit: num(t, `Sidereal orbit period \(days\) ([\d,\.]+)`), Day: num(t, `Length of day \(hrs\) ([\d,\.]+)`),
			Rotation: num(t, `Sidereal rotation period \(hrs\)\*? (-?[\d,\.]+)`), BBTemp: num(t, `Black-body temperature \(K\) ([\d,\.]+)`),
			Lum: num(t, `Luminosity \(10 ?24 J/s\) ([\d,\.]+)`), Rings: str(t, `Planetary ring system (Yes|No)`), Spectral: str(t, `Spectral type ([A-Z]\d [IV]+)`)}
		if b == "moon" {
			f.Orbit = num(t, `Revolution period \(days\) ([\d,\.]+)`)
		}
		if m := regexp.MustCompile(`Number of natural satellites (\d+)`).FindStringSubmatch(t); m != nil {
			n, _ := strconv.Atoi(m[1])
			f.Moons = &n
		}
		if f.Mass == 0 || f.Radius == 0 {
			fmt.Fprintln(os.Stderr, b, "page looks wrong: keeping the old file")
			os.Exit(1)
		}
		res[b] = f
		fmt.Printf("%-8s radius %-9.1f mass %-12.5g gravity %-6.2f dist %-9.3f orbit %-10.3f day %-9.2f rot %-9.3f bb %-6.1f moons %v\n", b, f.Radius, f.Mass, f.Gravity, f.Semimajor, f.Orbit, f.Day, f.Rotation, f.BBTemp, f.Moons)
		time.Sleep(time.Second)
	}
	doc := map[string]any{"source": "NASA NSSDC Planetary Fact Sheets", "updated": time.Now().Format("2006-01-02"), "bodies": res}
	b, _ := json.MarshalIndent(doc, "", " ")
	if err := os.WriteFile(out+".tmp", b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Rename(out+".tmp", out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("saved ->", out)
}
