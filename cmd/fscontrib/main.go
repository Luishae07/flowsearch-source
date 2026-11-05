// fscontrib is the official client for open crawling: it lets your computer help to fill a Flowsearch index.
//
// It asks a Flowsearch server for work (a ticket with a slice of sites), checks that the slice matches the hash the server signed, runs the crawler
// (sgcrawl) on exactly those sites, and tells the server when it is done. Then it asks for the next slice. When the server has no free work, the client waits
// in line; the contributors who asked first are served first.
//
//	fscontrib -server https://example.org -token-file fscontrib.token
//
// The token comes from the operator of the server. The server checks everything again on its side; this program is a convenience, not the protection.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const version = "1.0.0"

var (
	server string
	token  string
	hc     = &http.Client{Timeout: 60 * time.Second}
)

func fileSHA(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type ticketReply struct {
	Ticket     string `json:"ticket"`
	Slice      int    `json:"slice"`
	Expires    int64  `json:"expires"`
	Seeds      string `json:"seeds"`
	SeedsSHA   string `json:"seeds_sha256"`
	MaxPages   int    `json:"max_pages"`
	MaxPerSite int    `json:"max_pages_per_site"`
	DelayMs    int    `json:"delay_ms"`
	Lang       string `json:"lang"`
	Wait       bool   `json:"wait"`
	Position   int    `json:"position"`
	RetryAfter int    `json:"retry_after"`
	Message    string `json:"message"`
	Error      string `json:"error"`
}

func call(path string, body any, ticket string) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest("POST", server+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "fscontrib/"+version)
	if ticket != "" {
		req.Header.Set("X-Flowsearch-Ticket", ticket)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, b, nil
}

// ticketSeedsSHA reads the hash that is inside the signed ticket (the client cannot check the signature, but it can check that both hashes agree).
func ticketSeedsSHA(ticket string) string {
	p := strings.Split(ticket, ".")
	if len(p) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(p[1])
	if err != nil {
		return ""
	}
	var t struct {
		SeedsSHA string `json:"seeds_sha256"`
	}
	json.Unmarshal(b, &t)
	return t.SeedsSHA
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func main() {
	flag.StringVar(&server, "server", "", "address of the Flowsearch server, for example https://example.org (required)")
	tokenFile := flag.String("token-file", "fscontrib.token", "file with your contributor token")
	crawler := flag.String("crawler", "", "path of the sgcrawl program (default: next to this program)")
	dir := flag.String("dir", "fscontrib-data", "folder for the work files")
	workers := flag.Int("workers", 20, "sites crawled at the same time")
	once := flag.Bool("once", false, "do one slice of work and stop")
	showVersion := flag.Bool("version", false, "print the version and stop")
	flag.Parse()
	if *showVersion {
		fmt.Println("fscontrib", version)
		return
	}
	log.SetFlags(log.Ltime)
	if server == "" {
		log.Fatal("Use -server https://... (the address of the Flowsearch server).")
	}
	server = strings.TrimRight(server, "/")
	tb, err := os.ReadFile(*tokenFile)
	if err != nil {
		log.Fatalf("No token file %s. The operator of the server gives you a token; save it in that file.", *tokenFile)
	}
	token = strings.TrimSpace(string(tb))
	if !strings.HasPrefix(token, "fsc_") {
		log.Fatal("The token in " + *tokenFile + " is not a contributor token (it should start with fsc_).")
	}
	self, _ := os.Executable()
	if *crawler == "" {
		*crawler = filepath.Join(filepath.Dir(self), "sgcrawl")
	}
	if _, err := os.Stat(*crawler); err != nil {
		log.Fatalf("The crawler program %s was not found. Build it with: go build -o sgcrawl ./cmd/sgcrawl", *crawler)
	}
	absToken, _ := filepath.Abs(*tokenFile)
	absCrawler, _ := filepath.Abs(*crawler)
	os.MkdirAll(*dir, 0o755)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hello := map[string]string{"client_version": version, "client_sha256": fileSHA(self), "crawler_sha256": fileSHA(absCrawler)}
	log.Printf("fscontrib %s, server %s", version, server)

	backoff := 10 * time.Second
loop:
	for ctx.Err() == nil {
		code, body, err := call("/api/contribute/ticket", hello, "")
		if err != nil {
			log.Printf("could not reach the server (%v); trying again in %s", err, backoff)
			if !sleepCtx(ctx, backoff) {
				break
			}
			if backoff < 5*time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = 10 * time.Second
		var tk ticketReply
		json.Unmarshal(body, &tk)
		switch {
		case code == 202 && tk.Wait:
			log.Printf("waiting in line: number %d. %s", tk.Position, "The server gives the next free work to the one who asked first.")
			if !sleepCtx(ctx, time.Duration(max(tk.RetryAfter, 5))*time.Second) {
				break loop
			}
			continue
		case code == 503:
			log.Printf("the server has no work yet (%s); asking again in a minute", tk.Error)
			if !sleepCtx(ctx, time.Minute) {
				break loop
			}
			continue
		case code == 401 || code == 403 || code == 426:
			log.Fatalf("The server refused: %s", tk.Error)
		case code != 200 || tk.Ticket == "":
			log.Printf("unexpected answer (HTTP %d): %s; trying again in a minute", code, strings.TrimSpace(string(body)))
			if !sleepCtx(ctx, time.Minute) {
				break loop
			}
			continue
		}

		// the hash has to match: the slice we got, the hash we got with it, and the hash inside the signed ticket
		sum := sha256.Sum256([]byte(tk.Seeds))
		got := hex.EncodeToString(sum[:])
		if got != tk.SeedsSHA || got != ticketSeedsSHA(tk.Ticket) {
			log.Fatal("The list of sites does not match its hash. Refusing to crawl it. (Something between you and the server changed it.)")
		}
		work := filepath.Join(*dir, "slice-"+strconv.Itoa(tk.Slice))
		os.RemoveAll(work)
		os.MkdirAll(work, 0o755)
		seedsPath := filepath.Join(work, "seeds.txt")
		ticketPath := filepath.Join(work, "ticket.txt")
		os.WriteFile(seedsPath, []byte(tk.Seeds), 0o644)
		os.WriteFile(ticketPath, []byte(tk.Ticket+"\n"), 0o600)
		sites := strings.Count(tk.Seeds, "\n")
		hours := float64(tk.Expires-time.Now().Unix())/3600 - 0.1
		if hours < 0.1 {
			hours = 0.1
		}
		log.Printf("work received: slice %d, %d sites, at most %d pages per site, valid for %.1f hours. Crawling ...", tk.Slice, sites, tk.MaxPerSite, hours)
		args := []string{"-ingest", server + "/api/contribute/submit", "-token-file", absToken, "-ticket-file", ticketPath, "-seeds", seedsPath,
			"-lang", tk.Lang, "-workers", strconv.Itoa(*workers), "-maxpages", strconv.Itoa(tk.MaxPerSite), "-delay", strconv.Itoa(tk.DelayMs) + "ms",
			"-hours", strconv.FormatFloat(hours, 'f', 2, 64)}
		cmd := exec.CommandContext(ctx, absCrawler, args...)
		cmd.Dir = work
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		runErr := cmd.Run()
		if ctx.Err() != nil {
			log.Printf("stopped; the slice stays open until its ticket runs out")
			break loop
		}
		if ee, ok := runErr.(*exec.ExitError); ok && ee.ExitCode() == 3 {
			log.Printf("the server refused the ticket while crawling; asking for new work")
			continue
		}
		if runErr != nil {
			log.Printf("the crawler ended with an error: %v (its log is in %s)", runErr, filepath.Join(work, "sgcrawl.log"))
		}
		if c, b, err := call("/api/contribute/complete", nil, tk.Ticket); err == nil && c == 200 {
			log.Printf("slice %d finished and reported: %s", tk.Slice, strings.TrimSpace(string(b)))
		} else {
			log.Printf("could not report slice %d as finished (%v %s)", tk.Slice, err, strings.TrimSpace(string(b)))
		}
		os.RemoveAll(work)
		if *once {
			break loop
		}
	}
	log.Printf("bye")
}
