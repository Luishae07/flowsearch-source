# Open crawling

Open crawling lets people run a crawler on their own computer and send the pages it reads to a Flowsearch server. The server decides what is
crawled, checks what comes back, and only then adds it to the index.

This document has two parts: [for contributors](#for-contributors) (people who run a crawler) and [for operators](#for-operators)
(people who run the server).

## How it works

```
 contributor                                   server (ingest)
 ───────────                                   ───────────────
 fscontrib ── "give me work" ───────────────▶  queue of slices of the seed list
           ◀── signed ticket + the sites ────  (first come, first served)
 checks that the list matches its SHA-256
 sgcrawl crawls exactly those sites
           ── pages (token + ticket) ───────▶  checks every page against the ticket
                                               holds a new contributor's pages back,
                                               fetches a sample again, compares
           ◀── accepted / held back ─────────  stores the batch if the sample matches
 fscontrib ── "done" ───────────────────────▶  slice is finished
```

- **A queue of work.** The operator's list of sites is cut into slices. The first contributor to ask gets the first free slice, the next gets the
  next one. A slice whose ticket ran out goes back to the front. When all work is taken, contributors wait in line and are served in the order
  they asked.
- **Signed tickets.** A ticket names the contributor, the slice, the SHA-256 of the slice's site list, an expiry time and page limits. The server
  signs it, so it cannot be changed.
- **Everything is checked on the server.** A program on somebody else's computer can always be modified, so the server never trusts it.
  A page is accepted only if it is on a site of the ticket, has no script or markup in its text, is not on the blocklist, and fits the limits.
- **Sample checks.** The pages of a new contributor are held back. The server fetches a sample of them again itself and compares the text. If the
  sample matches, the batch is stored and the contributor earns trust; if it does not, the batch is thrown away and trust falls. A contributor who
  is trusted has their pages stored at once, and a sample is still checked afterwards.

## For contributors

### What you need

- A contributor token from the operator of a Flowsearch server. It starts with `fsc_`.
- Go 1.22 or newer.
- A computer that can stay on for a while. Crawling uses little CPU; it uses your internet connection.

### Start

```
python3 start.py
```

Choose **2** (help to crawl), enter the server address and your token, and accept the rules. `start.py` builds the crawler and the client and starts
them. Use `python3 start.py status` and `python3 start.py stop` to look at it or stop it. The log is in the folder you chose (`fscontrib.log`).

Without `start.py`:

```
go build -o sgcrawl ./cmd/sgcrawl
go build -o fscontrib ./cmd/fscontrib
echo "fsc_your_token" > fscontrib.token && chmod 600 fscontrib.token
./fscontrib -server https://search.example.org -token-file fscontrib.token
```

### What to expect

- If other contributors are ahead of you, the log says `waiting in line: number N`. Nothing is wrong; you are served in order.
- A slice usually takes a few hours at most. The client asks for the next one by itself.
- Your first batches are held back until the server has checked a sample. This takes a few minutes per batch.
- If the server says your client is too old or not an official build, download the current release.

### The rules

The server publishes them at `/api/contrib/policy`. In short: crawl only the sites you were given, obey robots.txt (the crawler does), do not send
HTML, scripts or personal data, and accept that the sites you crawl see your computer's IP address.

## For operators

### Turn it on

Open crawling is part of the ingest and is on all the time. It does nothing until you give it a seed list and create contributors.

1. **The seed list.** Put the sites to crawl, one domain per line, in `<data folder>/contrib/seeds.txt`. The file is read again every minute.
2. **Create a contributor.** The token is shown once and stored only as a hash:

   ```
   curl -X POST https://your-ingest/api/contrib/admin/create \
        -H "Authorization: Bearer $MAIN_INGEST_TOKEN" -d '{"name":"alice"}'
   ```

3. Give the contributor the token and the server address.

Other admin calls (same main token): `POST /api/contrib/admin/revoke` with `{"id": "...", "status": "revoked"}` (also `suspended` or `active`),
and `GET /api/contrib/admin/list` for every contributor with pages, matches, failures and trust.

### Settings

`<data folder>/contrib/config.json` (all optional; read again every minute):

| Key | Default | Meaning |
|---|---|---|
| `slice_size` | 50 | Sites in one slice |
| `ticket_hours` | 6 | How long a ticket is valid |
| `max_pages_per_site` | 200 | The crawler stops a site after this many pages |
| `max_pages_per_ticket` | 20000 | Most pages one ticket may send |
| `delay_ms` | 1000 | Pause after every page, per site |
| `lang` | `en` | `en`, `de` or `any` |
| `daily_cap` | 50000 | Most pages one contributor may send in a day |
| `trusted_batches` | 3 | Matching batches before a contributor's pages are stored at once |
| `sample_percent` | 5 | Share of a batch that is fetched again (at least 3 pages, at most 25) |
| `min_version` | `1.0.0` | Oldest client version that may ask for work |
| `allowed_client_sha256` | empty | SHA-256 of official `fscontrib` builds; empty accepts any build |
| `allowed_crawler_sha256` | empty | SHA-256 of official `sgcrawl` builds; empty accepts any build |
| `recycle_days` | 30 | A finished slice comes up again after this many days |
| `ip_per_minute` | 150 | Requests one IP address may make to the contributor API in a minute |
| `ticket_per_minute` | 12 | Of those, requests for work |
| `admin_per_minute` | 30 | Of those, admin calls |
| `bad_auth_limit` | 10 | Wrong tokens from one IP address within 10 minutes before it is blocked |
| `block_minutes` | 15 | How long such an address is blocked |

Hosts that must never be accepted go in `<data folder>/contrib/blocklist.txt`, one per line.

### Build allowlist

The build hash is a convenience: it catches old or accidentally changed copies. It cannot stop someone who changes the program and reports the
original hash, so the real protection is the server-side checks above. To use it, publish release builds, put their SHA-256 values in
`allowed_client_sha256` and `allowed_crawler_sha256`, and raise `min_version` when you release a new one.

### Trust

Trust is `(matches + 1) / (matches + failures + 2)`, starting at 0.5. A contributor is trusted when at least `trusted_batches` batches matched and
trust is at least 0.85. If trust falls below 0.85 they are held back again; below 0.3 after at least 10 checked pages, the account is suspended.

### Monitoring

- `GET /api/contrib/stats` (public): contributors, accepted pages, slices done and in progress, people waiting in line, batches held back, and a
  leaderboard of the top 20.
- The ingest log lines starting with `contrib:` show every slice handed out, every batch stored or thrown away, and suspended accounts.

### Safety notes

- The server fetches addresses chosen by contributors (to check the sample). Every connection refuses private, loopback, link-local and other
  non-public addresses, also after a redirect.
- Tickets are signed with the key in `<secrets folder>/contrib_hmac_key`, created on first start. If you replace it, all open tickets stop working.
- **Rate limiting is built in**, no proxy is needed. Every IP address has a number of requests a minute for the contributor API (less for work
  requests and admin calls), and an address that sends too many wrong tokens is blocked for a while, which stops guessing of tokens. Besides this,
  every contributor has their own limits: uploads a minute and pages a day.
- The visitor's address is read from the tunnel's header (`CF-Connecting-IP`, `X-Real-IP` or `X-Forwarded-For`), and only when the connection itself
  comes from the machine the ingest runs on. Requests from that machine without such a header are not limited. If you open the ingest to the
  internet directly, the connection's own address is used.
- The ingest also carries the main token and the admin calls. Wrong main tokens count as wrong tokens and lead to a block as well.
- A contributor's pages are only as trustworthy as your seed list: contributors can only send pages from the sites you put in it.
