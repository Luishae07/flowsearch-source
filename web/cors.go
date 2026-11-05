package main

// The fixed address of Flowsearch (https://luishae07.github.io/flowsearch/) is a page on GitHub Pages that fetches this site's pages and shows them itself.
// For that, a browser must be allowed to read this site's answers from that one address. corsMux adds the header for requests that come from it (and only it);
// an answer that already has its own Access-Control-Allow-Origin keeps it, so nothing is sent twice.

import (
	"bufio"
	"errors"
	"net"
	"net/http"
)

const corsOrigin = "https://luishae07.github.io"

type corsWriter struct {
	http.ResponseWriter
	done bool
}

func (c *corsWriter) add() {
	if c.done {
		return
	}
	c.done = true
	h := c.Header()
	if h.Get("Access-Control-Allow-Origin") == "" {
		h.Set("Access-Control-Allow-Origin", corsOrigin)
	}
	h.Add("Vary", "Origin")
}

func (c *corsWriter) WriteHeader(code int) {
	c.add()
	c.ResponseWriter.WriteHeader(code)
}

func (c *corsWriter) Write(b []byte) (int, error) {
	c.add()
	return c.ResponseWriter.Write(b)
}

func (c *corsWriter) Flush() {
	c.add()
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack keeps WebSockets working (the live page hijacks the connection).
func (c *corsWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := c.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijacking is not supported")
}

func (c *corsWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func corsMux(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != corsOrigin {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodOptions {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", corsOrigin)
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, Accept")
			h.Set("Access-Control-Max-Age", "600")
			h.Add("Vary", "Origin")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(&corsWriter{ResponseWriter: w}, r)
	})
}
