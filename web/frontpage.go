package main

import (
	"regexp"
	"strings"
)

var (
	frontPathRe = regexp.MustCompile(`^/([a-z]{2}(-[A-Za-z]{2,4})?)?/?$`) // "/" or a language front page like "/de/" or "/en-US/"
	doctypeRe   = regexp.MustCompile(`(?is)^\s*<!doctype[^>]*>\s*`)
)

// isFrontPageOf: the search is just the name of the site ("firefox" for firefox.com) and this page is that site's front page,
// in the home language or another one. Such a page comes before the site's other pages.
func isFrontPageOf(host, path string, qt []string) bool {
	if len(qt) != 1 {
		return false
	}
	h := strings.TrimPrefix(strings.ToLower(host), "www.")
	parts := strings.Split(h, ".")
	return len(parts) == 2 && parts[0] == qt[0] && frontPathRe.MatchString(path)
}

// cleanSnippet takes the stray "<!doctype html>" off the start of a text that was read from a page.
func cleanSnippet(s string) string {
	return strings.TrimSpace(doctypeRe.ReplaceAllString(s, ""))
}
