package main

// botCheckShot asks the botcheck service (cmd/botcheck, 127.0.0.1:1919) whether a screenshot is a bot-check page such as Cloudflare's
// "Verify you are human", by comparing it with reference pictures of those pages. If the service does not answer, the answer is "no":
// a screenshot is never lost because the check was down.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

var (
	errBotCheck    = errors.New("the page showed a bot check instead of the page itself")
	errNoSavedCopy = errors.New("and the Wayback Machine has no usable copy")
	botCheckHTTP   = &http.Client{Timeout: 5 * time.Second}
)

func botCheckShot(img []byte) (bool, float64) {
	resp, err := botCheckHTTP.Post("http://127.0.0.1:1919/check", "image/png", bytes.NewReader(img))
	if err != nil {
		return false, 0
	}
	defer resp.Body.Close()
	var v struct {
		Bot      bool    `json:"bot"`
		Distance float64 `json:"distance"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&v) != nil {
		return false, 0
	}
	return v.Bot, v.Distance
}
