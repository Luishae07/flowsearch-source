# Flowsearch

Flowsearch is an open-source web search engine. It consists of a set of crawlers, an ingest service, an on-disk index, a web server with a result
page and a JSON API, and a few supporting tools. The server side is written in Go (standard library only); one of the crawlers is written in Rust.

This repository contains the source code only. It does not contain any crawled data, screenshots or credentials.

## Contents

- [Quick start](#quick-start)
- [Architecture](#architecture)
- [Repository layout](#repository-layout)
- [Requirements](#requirements)
- [Building](#building)
- [Configuration](#configuration)
- [Credentials](#credentials)
- [Images](#images)
- [Contributing](#contributing)
- [License](#license)

## Quick start

```
python3 start.py
```

`start.py` (Python 3.8 or newer, standard library only) asks a series of questions about folders, the website address, the crawler token,
optional keys and a first crawl, then builds the programs with Go and starts them in the right order: ingest, index service, website and, if
chosen, a crawler. It writes the settings to `flowsearch.env` and the logs to the `logs` folder inside the data folder.

| Command | What it does |
|---|---|
| `python3 start.py` | Asks the questions, builds and starts |
| `python3 start.py --reuse` | Starts again with the answers of the last run, without asking |
| `python3 start.py --defaults` | Accepts every default answer |
| `python3 start.py --dry-run` | Shows what would be done and changes nothing |
| `python3 start.py status` | Shows what is running |
| `python3 start.py stop` | Stops everything that `start.py` started |

The ingest and the index service use fixed ports (8811 and 18820), so only one instance can run on a machine at a time. The ingest address can be changed with `MYSEARCH_INGEST_ADDR`.

Question 0 of `start.py` also offers **helping to crawl**: run only a crawler on this machine that sends pages to somebody else's Flowsearch server.
See [CRAWLING.md](CRAWLING.md) for how open crawling works, for contributors and for operators.

## Architecture

```
  crawlers  ──HTTP, bearer token──▶  ingest  ──▶  index service  ◀──  web server (site + JSON API)
 (Go, Rust)                         (saves pages)  (on-disk index)
```

1. **Crawlers** fetch pages, forum posts, product pages and similar content, and send them to the ingest in batches.
2. The **ingest** service authenticates the crawler, saves the data as JSON Lines files and passes the text to the index.
3. The **index** is stored on disk in segments and can be searched while new pages are being added.
4. The **web server** answers searches (HTML pages and a JSON API) and also serves auxiliary pages such as the live statistics page.

## Repository layout

| Path | Description |
|---|---|
| `main.go`, `forumingest.go`, `amazoningest.go`, `robloxingest.go` | Ingest service: pages, forum posts, product pages and game pages |
| `web/` | Web server: result pages, JSON API, live statistics page, forum search |
| `sgindex/`, `sgpack/`, `sgseen/` | Disk index, packed text format, sets of already-seen addresses |
| `cmd/sgindexd` | The index as a separate service |
| `cmd/sgcrawl` | General web crawler (Go) |
| `cmd/lemmycrawl` | Forum crawler for Lemmy, Discourse and Flowforum |
| `cmd/amzdata`, `cmd/amzcrawl` | Product page crawlers |
| `cmd/rbxcrawl` | Game page crawler |
| `contrib.go`, `contribverify.go` | **Open crawling** on the server: contributor tokens, a queue of work, signed tickets, upload checks and sample verification (see [CRAWLING.md](CRAWLING.md)) |
| `cmd/fscontrib` | The official client for open crawling: asks for work, checks it against its hash, runs `sgcrawl` on it |
| `cmd/sgconvert`, `cmd/sgconvert3`, `cmd/sgpurge`, `cmd/sgrepack`, `cmd/sgdict`, `cmd/sgdict2`, `cmd/sglive` | Maintenance tools for the data files and the dictionary |
| `cmd/jevchat`, `cmd/jevchatbuild`, `cmd/jevbrowse`, `cmd/botcheck`, `cmd/spacefacts` | Helper programs |
| `rustcrawl/` | `flowcrawl`, the Rust crawler, intended to run on CI runners such as GitHub Actions |
| `deploy_web.sh` | Builds the web server and switches to the new build without downtime |

## Requirements

### Software

- Linux (developed and run on Debian 13)
- Go 1.22 or newer
- Rust (stable) for `rustcrawl/`
- `cjxl` and `djxl` from [libjxl](https://github.com/libjxl/libjxl) for image handling
- Chrome or Chromium, or Firefox with geckodriver, for the crawlers that render JavaScript pages

### Hardware

| Component | Requirement | Notes |
|---|---|---|
| CPU | x86-64, 4 cores or more recommended | Searching and crawling are not CPU-heavy. The reference machine is a laptop with an Intel Core i7-10510U (4 cores, 8 threads) that runs every service at once. Other architectures supported by Go and Rust should work but have not been tested. |
| GPU | not needed | No code uses a GPU. |
| APU / integrated graphics | not needed | Everything runs headless. Browser-based crawlers use headless Chrome or Firefox, which need no display. |
| RAM | grows with the index; 16 GB is comfortable | Reference, with an index of about 60 million pages: web server 1.3 GB, index service 1.3 GB, ingest 0.5 GB. Each headless browser started by a crawler needs additional memory. |
| Disk | SSD recommended; about 100 GB for the reference index | About 60 million pages take roughly 62 GB for the page store, 10 GB for the index and 2 GB for forum posts, plus pictures. |
| Network | stable connection | Bandwidth depends on how fast the crawlers run. |

## Building

```
go build -o mysearch-ingest .
go build -o mysearch-web ./web
go build -o sgindexd ./cmd/sgindexd
go build -o lemmycrawl ./cmd/lemmycrawl

cd rustcrawl && cargo build --release
```

Run `go vet ./...` before sending changes.

## Configuration

The programs read their folders from environment variables. The defaults are the paths of a typical Linux server installation.

| Variable | Default | Purpose |
|---|---|---|
| `FLOWSEARCH_DATA_DIR` | `/media/data/mysearch` | Search data: pages, index, forum posts, products |
| `FLOWSEARCH_MEDIA_DIR` | `/media/data` | Disk that holds all data folders |
| `FLOWSEARCH_CDN_DIR` | `/media/data/flowcdn` | Favicons and pictures (JPEG XL) |
| `FLOWSEARCH_SHOTS_DIR` | `/media/data/flowsearch-wmshots` | Screenshots (JPEG XL) |
| `FLOWSEARCH_HOME` | `/srv/uploads/mysearch` | Program folder and small state files |
| `FLOWSEARCH_UPLOADS_DIR` | `/srv/uploads` | Folder that holds related projects |
| `FLOWSEARCH_SECRETS_DIR` | `/srv/secrets` | Credentials, one file each |
| `FLOWSEARCH_RUNTIME_DIR` | `/run` | Short-lived files |
| `MYSEARCH_ADDR` | `:8812` | Listen address of the web server |

Default listen addresses: ingest `127.0.0.1:8811`, web server `:8812`, index service `127.0.0.1:18820`.
`deploy_web.sh` starts two copies of the web server on alternating ports behind a front proxy by setting `MYSEARCH_ADDR`.

## Credentials

Credentials are read from files in `FLOWSEARCH_SECRETS_DIR`; they are never part of the source code.

- `mysearch_token` is required: the bearer token that crawlers send to the ingest.
- Other files (for example keys for a browser service, an AI service or a safe-browsing check) are read only by the features that use them.
  A feature without its credential file is not started.

## Images

Favicons, screenshots and other pictures are stored and served as **JPEG XL** only. Install `cjxl` and `djxl` on the machine that creates them.

## Contributing

Issues and pull requests are welcome. Please keep changes small and focused, format Go code with `gofmt`, and make sure `go vet ./...` passes.
Do not include crawled data or credentials in a contribution.

## License

Flowsearch is released under the [GNU Affero General Public License v3.0](LICENSE). If you run a modified version as a network service,
you must make its source code available to the users of that service.
