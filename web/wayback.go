package main

// A page that shows a bot-check (see botcheck.go) gets its preview from the Wayback Machine instead: the newest saved copy of the page
// (the internet archive's own, found through archive.org's availability API) is opened and photographed. Such a preview is marked with a
// ".wb" file next to the picture, and the results page says "Saved copy" when you point at it. Only the page's address goes to archive.org.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"time"
)

var waybackHTTP = &http.Client{Timeout: 12 * time.Second}

// waybackTarget is where the newest saved copy of the page can be opened, in the form buShot takes
// ("web.archive.org/web/<timestamp>if_/<address>", the if_ form has no archive toolbar), or "" if the archive has none.
func waybackTarget(raw string) string {
	resp, err := waybackHTTP.Get("https://archive.org/wayback/available?url=" + url.QueryEscape(raw))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var v struct {
		Archived struct {
			Closest struct {
				Available bool   `json:"available"`
				Status    string `json:"status"`
				Timestamp string `json:"timestamp"`
			} `json:"closest"`
		} `json:"archived_snapshots"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&v) != nil {
		return ""
	}
	c := v.Archived.Closest
	if !c.Available || c.Status != "200" || len(c.Timestamp) != 14 {
		return ""
	}
	return "web.archive.org/web/" + c.Timestamp + "if_/" + raw
}

// savedCopyShot photographs the Wayback copy of a page; it fails if there is none or if the copy is a bot-check page too.
func savedCopyShot(raw string) ([]byte, error) {
	t := waybackTarget(raw)
	if t == "" {
		return nil, errNoSavedCopy
	}
	img, err := buShot(t)
	if err != nil {
		return nil, err
	}
	if bot, _ := botCheckShot(img); bot {
		return nil, errNoSavedCopy
	}
	return img, nil
}

func markSavedCopy(path string, saved bool) {
	if saved {
		os.WriteFile(path, nil, 0o644)
	} else {
		os.Remove(path)
	}
}

// savedCopyTitle is the tooltip of a preview that comes from the Wayback Machine, or "".
func savedCopyTitle(marker string) string {
	if _, err := os.Stat(marker); err == nil {
		return ` title="Saved copy from the Wayback Machine, not the live page"`
	}
	return ""
}
