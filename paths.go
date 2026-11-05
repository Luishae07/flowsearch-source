package main

// The places this program reads and writes. Each one can be set with an environment variable; without it, the usual place on the Flowsearch server is used.
//   FLOWSEARCH_DATA_DIR     the data of the search engine (posts, games, products, indexes)       default /media/data/mysearch
//   FLOWSEARCH_MEDIA_DIR    the disk that holds all data folders                                   default /media/data
//   FLOWSEARCH_CDN_DIR      favicons and pictures, kept as JPEG XL                                 default /media/data/flowcdn
//   FLOWSEARCH_SHOTS_DIR    screenshots (JPEG XL)                                                  default /media/data/flowsearch-wmshots
//   FLOWSEARCH_HOME         the folder of the programs and their small files                       default /srv/uploads/mysearch
//   FLOWSEARCH_UPLOADS_DIR  the folder that holds all projects                                     default /srv/uploads
//   FLOWSEARCH_SECRETS_DIR  the keys and tokens, one file for each                                 default /srv/secrets
//   FLOWSEARCH_RUNTIME_DIR  short-lived files                                                      default /run

import (
	"os"
	"strings"
)

func fsEnv(name, def string) string {
	if v := strings.TrimRight(os.Getenv(name), "/"); v != "" {
		return v
	}
	return def
}

var (
	fsData    = fsEnv("FLOWSEARCH_DATA_DIR", "/media/data/mysearch")
	fsMedia   = fsEnv("FLOWSEARCH_MEDIA_DIR", "/media/data")
	fsCDN     = fsEnv("FLOWSEARCH_CDN_DIR", "/media/data/flowcdn")
	fsShots   = fsEnv("FLOWSEARCH_SHOTS_DIR", "/media/data/flowsearch-wmshots")
	fsHome    = fsEnv("FLOWSEARCH_HOME", "/srv/uploads/mysearch")
	fsUploads = fsEnv("FLOWSEARCH_UPLOADS_DIR", "/srv/uploads")
	fsSecrets = fsEnv("FLOWSEARCH_SECRETS_DIR", "/srv/secrets")
	fsRun     = fsEnv("FLOWSEARCH_RUNTIME_DIR", "/run")

	// the address the ingest listens on (only this program uses it)
	ingestAddr = fsEnv("MYSEARCH_INGEST_ADDR", "127.0.0.1:8811")
)
