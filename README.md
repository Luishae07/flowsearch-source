# Flowsearch

Flowsearch is a web search engine written from scratch: its own crawlers, its own on-disk index, its own ranking and its own result pages.
This repository holds the source code of the search engine and of its crawlers. It does not hold any crawled data (pages, forum posts, screenshots,
products) and no keys.

Licence: [GNU Affero General Public License v3.0](LICENSE). If you run a modified copy as a website, you must offer its source code to the people
who use it.

## What is here

| Path | What it is |
|---|---|
| `main.go`, `forumingest.go`, `amazoningest.go`, `robloxingest.go` | the **ingest** service: takes crawled pages (and forum posts, Amazon products, Roblox games) from the crawlers, saves them and hands the text to the index |
| `web/` | the **search site and API**: result pages, the live page, the Forums tab, the JSON API |
| `sgindex/`, `sgpack/`, `sgseen/` | the disk index, the packed text format, the "seen" sets |
| `cmd/sgindexd` | the index as its own service |
| `cmd/sgcrawl` | the main web crawler (Go) |
| `cmd/lemmycrawl` | the forum crawler: Lemmy, Discourse and Flowforum |
| `cmd/amzdata`, `cmd/amzcrawl` | the Amazon product crawlers |
| `cmd/rbxcrawl` | the Roblox game crawler |
| `cmd/sgconvert`, `sgconvert3`, `sgpurge`, `sgrepack`, `sgdict`, `sgdict2`, `sglive` | tools for the data files and the dictionary |
| `cmd/jevchat`, `jevchatbuild`, `jevbrowse`, `botcheck`, `spacefacts` | smaller helper programs |
| `rustcrawl/` | **flowcrawl**, the Rust crawler that runs on GitHub Actions and sends its pages to the ingest |
| `deploy_web.sh` | builds the web server and switches to it without downtime |

## Build

Go 1.22 or newer; no third-party Go modules.

```
go build -o mysearch-ingest .
go build -o mysearch-web ./web
go build -o sgindexd ./cmd/sgindexd
go build -o lemmycrawl ./cmd/lemmycrawl
```

The Rust crawler:

```
cd rustcrawl && cargo build --release
```

## Where things are kept

Every folder the programs use can be set with an environment variable. Without them, the paths of the original server are used.

| Variable | Default | Holds |
|---|---|---|
| `FLOWSEARCH_DATA_DIR` | `/media/data/mysearch` | the data of the search engine (pages, index, forum posts, products) |
| `FLOWSEARCH_MEDIA_DIR` | `/media/data` | the disk with all data folders |
| `FLOWSEARCH_CDN_DIR` | `/media/data/flowcdn` | favicons and pictures, kept as JPEG XL |
| `FLOWSEARCH_SHOTS_DIR` | `/media/data/flowsearch-wmshots` | screenshots (JPEG XL) |
| `FLOWSEARCH_HOME` | `/srv/uploads/mysearch` | the folder of the programs and their small files |
| `FLOWSEARCH_UPLOADS_DIR` | `/srv/uploads` | the folder with all projects |
| `FLOWSEARCH_SECRETS_DIR` | `/srv/secrets` | keys and tokens, one file each |
| `FLOWSEARCH_RUNTIME_DIR` | `/run` | short-lived files |

## Keys

Keys are read from files in `FLOWSEARCH_SECRETS_DIR`, never from the code. The one every setup needs is `mysearch_token`: the token the crawlers
send to the ingest (`Authorization: Bearer ...`). Other files (for example for a browser service, an AI service or a safe-browsing check) are only
read by the parts that use them. Create the files you need; a part without its key does not start that feature.

## Images

Flowsearch stores and serves its pictures (favicons, screenshots) only as **JPEG XL**. The `cjxl` and `djxl` programs from libjxl must be installed
where the pictures are made.

## Ports

The ingest listens on `127.0.0.1:8811`, the web server on `:8812` (or on the address in `MYSEARCH_ADDR`, which is how `deploy_web.sh` runs two copies
behind a front proxy) and the index service on `127.0.0.1:18820`.
