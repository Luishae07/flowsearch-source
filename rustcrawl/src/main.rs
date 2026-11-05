// flowcrawl: the Flowsearch crawler, written for GitHub Actions.
//
//   flowcrawl --seeds FILE --start N --count 500 [--pages 10] [--conc 500] [--ingest URL] [--render 4]
//
// One batch = `count` sites of the seed list. Every site is read at the same time as all the others (--conc),
// each site politely, one request after the other. For a site:
//   1. robots.txt first (a page it disallows for "MySearchBot" is never read),
//   2. up to --pages pages of that site only (links to other sites are ignored),
//   3. a page is read with a plain request first (fast). If it has almost no text (a page that builds itself with JavaScript),
//      it is opened in a real headless Firefox instead (--render Firefox windows, started through geckodriver).
// At the end ALL the pages of the batch go to the Flowsearch ingest in ONE request: POST URL/api/ingest, Bearer token from $MYSEARCH_TOKEN.

use flate2::{write::GzEncoder, Compression};
use std::io::Write;
use std::sync::atomic::{AtomicUsize, Ordering};
use scraper::{node::Node, Html, Selector};
use serde::{Deserialize, Serialize};
use std::collections::{HashSet, VecDeque};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::sync::{mpsc, oneshot};
use url::Url;

const AGENT: &str = "MySearchBot";
const UA: &str = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0 (compatible; MySearchBot/1.0; +https://github.com/Luishae07/flowsearch)";
const SKIP_EXT: &[&str] = &[
    ".pdf", ".jpg", ".jpeg", ".png", ".gif", ".webp", ".svg", ".zip", ".gz", ".mp4", ".mp3", ".css", ".js", ".xml", ".ico", ".woff", ".woff2", ".exe", ".dmg", ".apk",
];
const BAD_TITLES: &[&str] = &["just a moment", "attention required", "access denied", "are you a robot"];
const MAX_BODY: usize = 2 * 1024 * 1024;
const THIN: usize = 200; // fewer characters than this: the page probably needs JavaScript

const JS: &str = r#"var d=document, m=d.querySelector('meta[name=description]')||d.querySelector('meta[property="og:description"]');
var links=[].slice.call(d.querySelectorAll('a[href]')).map(function(a){return a.href}).slice(0,400);
return JSON.stringify({title:d.title||'',desc:m?m.content:'',text:(d.body?d.body.innerText:'').slice(0,6000),links:links});"#;

#[derive(Serialize, Clone)]
struct Page {
    url: String,
    title: String,
    description: String,
    text: String,
    source: String,
}

#[derive(Deserialize, Default)]
struct Seen {
    #[serde(default)]
    title: String,
    #[serde(default)]
    desc: String,
    #[serde(default)]
    text: String,
    #[serde(default)]
    links: Vec<String>,
}

struct Args {
    seeds: String,
    start: usize,
    count: usize,
    pages: usize,
    conc: usize,
    render: usize,
    ingest: String,
    follow: usize, // how many sites found in links (other websites) may be read in addition to the seed slice
    max_minutes: u64, // 0 = no limit; otherwise no new site is started after this many minutes (GitHub stops a job after 6 hours)
}

fn parse_args() -> Args {
    let mut a = Args { seeds: String::new(), start: 0, count: 500, pages: 10, conc: 500, render: 4, ingest: String::new(), follow: 0, max_minutes: 0 };
    let v: Vec<String> = std::env::args().skip(1).collect();
    let mut i = 0;
    while i + 1 < v.len() {
        match v[i].as_str() {
            "--seeds" => a.seeds = v[i + 1].clone(),
            "--start" => a.start = v[i + 1].parse().unwrap_or(0),
            "--count" => a.count = v[i + 1].parse().unwrap_or(500),
            "--pages" => a.pages = v[i + 1].parse().unwrap_or(10),
            "--conc" => a.conc = v[i + 1].parse().unwrap_or(500),
            "--render" => a.render = v[i + 1].parse().unwrap_or(4),
            "--ingest" => a.ingest = v[i + 1].clone(),
            "--follow" => a.follow = v[i + 1].parse().unwrap_or(0),
            "--max-minutes" => a.max_minutes = v[i + 1].parse().unwrap_or(0),
            _ => {}
        }
        i += 2;
    }
    if a.seeds.is_empty() {
        eprintln!("usage: flowcrawl --seeds FILE --start N --count 500 [--pages 10] [--conc 500] [--render 4] [--ingest URL]");
        std::process::exit(2);
    }
    a
}

fn trunc(s: &str, n: usize) -> String {
    s.chars().take(n).collect()
}

fn bare_host(u: &Url) -> String {
    u.host_str().unwrap_or("").to_lowercase().trim_start_matches("www.").to_string()
}

/// the text a person would read: everything except script, style, head and template content
fn page_text(doc: &Html) -> String {
    let mut out = String::new();
    for node in doc.tree.nodes() {
        if let Node::Text(t) = node.value() {
            let mut skip = false;
            let mut p = node.parent();
            while let Some(pn) = p {
                if let Node::Element(e) = pn.value() {
                    if matches!(e.name(), "script" | "style" | "noscript" | "template" | "head" | "title" | "svg") {
                        skip = true;
                        break;
                    }
                }
                p = pn.parent();
            }
            if !skip {
                out.push_str(t);
                out.push(' ');
                if out.len() > 12000 {
                    break;
                }
            }
        }
    }
    out.split_whitespace().collect::<Vec<_>>().join(" ")
}

async fn fetch_html(client: &reqwest::Client, url: &str) -> Option<String> {
    let mut resp = client.get(url).send().await.ok()?;
    if !resp.status().is_success() {
        return None;
    }
    let ct = resp.headers().get("content-type").and_then(|v| v.to_str().ok()).unwrap_or("").to_lowercase();
    if !ct.is_empty() && !ct.contains("html") {
        return None;
    }
    let mut body: Vec<u8> = Vec::new();
    while let Ok(Some(chunk)) = resp.chunk().await {
        body.extend_from_slice(&chunk);
        if body.len() > MAX_BODY {
            break;
        }
    }
    Some(String::from_utf8_lossy(&body).into_owned())
}

type Job = (String, oneshot::Sender<Option<Seen>>);

/// one WebDriver call (plain JSON over HTTP); the answer, or None if it failed
async fn wd(client: &reqwest::Client, method: &str, url: String, body: Option<serde_json::Value>) -> Option<serde_json::Value> {
    let rb = match method {
        "POST" => client.post(&url),
        "DELETE" => client.delete(&url),
        _ => client.get(&url),
    }
    .timeout(Duration::from_secs(60));
    let rb = match body {
        Some(b) => rb.header("Content-Type", "application/json").body(b.to_string()),
        None => rb,
    };
    let r = rb.send().await.ok()?;
    let ok = r.status().is_success();
    let t = r.text().await.ok()?;
    let v: serde_json::Value = serde_json::from_str(&t).ok()?;
    if !ok {
        if url.ends_with("/session") {
            eprintln!("webdriver {} {} -> {}", method, url, trunc(&t, 300)); // only the opening of a Firefox window is worth a line; a page that cannot load is normal
        }
        return None;
    }
    Some(v)
}

/// the Firefox windows: each one is a geckodriver + a headless Firefox, taking page addresses from one shared queue
async fn start_firefox(n: usize) -> Option<mpsc::Sender<Job>> {
    if n == 0 {
        return None;
    }
    let (tx, rx) = mpsc::channel::<Job>(256);
    let rx = Arc::new(tokio::sync::Mutex::new(rx));
    let driver = std::env::var("GECKOWEBDRIVER").map(|d| format!("{}/geckodriver", d)).unwrap_or_else(|_| "geckodriver".to_string());
    let mut up = 0;
    for i in 0..n {
        let port = 4444 + i as u16;
        let child = tokio::process::Command::new(&driver).arg("--port").arg(port.to_string()).kill_on_drop(true).stdout(std::process::Stdio::null()).stderr(std::process::Stdio::null()).spawn();
        let Ok(child) = child else {
            eprintln!("geckodriver did not start");
            continue;
        };
        std::mem::forget(child);
        tokio::time::sleep(Duration::from_millis(800)).await;
        // talk to geckodriver directly (the WebDriver protocol is a few plain HTTP requests): wait until it answers, then open ONE session
        let http = reqwest::Client::builder().no_proxy().build().expect("webdriver client");
        let base = format!("http://127.0.0.1:{}", port);
        let mut ready = false;
        for _ in 0..30 {
            if http.get(format!("{}/status", base)).timeout(Duration::from_secs(2)).send().await.map(|r| r.status().is_success()).unwrap_or(false) {
                ready = true;
                break;
            }
            tokio::time::sleep(Duration::from_millis(500)).await;
        }
        if !ready {
            eprintln!("geckodriver {} did not answer", i);
            continue;
        }
        let caps = serde_json::json!({"capabilities": {"alwaysMatch": {"browserName": "firefox", "moz:firefoxOptions": {"args": ["-headless"], "prefs": {"permissions.default.image": 2}}}}});
        let Some(v) = wd(&http, "POST", format!("{}/session", base), Some(caps)).await else {
            eprintln!("Firefox window {} did not open", i);
            continue;
        };
        let Some(sid) = v["value"]["sessionId"].as_str().map(|s| s.to_string()) else {
            eprintln!("Firefox window {}: no session id in the answer", i);
            continue;
        };
        let _ = wd(&http, "POST", format!("{}/session/{}/timeouts", base, sid), Some(serde_json::json!({"pageLoad": 20000, "script": 15000, "implicit": 0}))).await;
        up += 1;
        let rx = rx.clone();
        tokio::spawn(async move {
            loop {
                let job = { rx.lock().await.recv().await };
                let Some((url, back)) = job else { break };
                let (http, base, sid) = (&http, &base, &sid);
                let r = tokio::time::timeout(Duration::from_secs(40), async move {
                    wd(http, "POST", format!("{}/session/{}/url", base, sid), Some(serde_json::json!({"url": url}))).await?;
                    tokio::time::sleep(Duration::from_millis(1800)).await;
                    let v = wd(http, "POST", format!("{}/session/{}/execute/sync", base, sid), Some(serde_json::json!({"script": JS, "args": []}))).await?;
                    serde_json::from_str::<Seen>(v["value"].as_str()?).ok()
                })
                .await
                .ok()
                .flatten();
                let _ = back.send(r);
            }
            let _ = wd(&http, "DELETE", format!("{}/session/{}", base, sid), None).await;
        });
    }
    println!("{} Firefox windows are open", up);
    if up == 0 {
        None
    } else {
        Some(tx)
    }
}

async fn render(ff: &Option<mpsc::Sender<Job>>, url: &str) -> Option<Seen> {
    let tx = ff.as_ref()?;
    let (b, r) = oneshot::channel();
    tx.send((url.to_string(), b)).await.ok()?;
    r.await.ok().flatten()
}

/// a site name worth keeping as a new seed: a real domain name (no address, no odd characters)
fn good_host(h: &str) -> bool {
    h.len() >= 4 && h.len() <= 80 && h.contains('.') && !h.starts_with('.') && !h.ends_with('.') && h.parse::<std::net::IpAddr>().is_err()
        && h.chars().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '-' || c == '.')
}

/// names that are machines or services of a site, not a front page worth reading (the same idea as the seed-list cleaner on the server)
fn infra(h: &str) -> bool {
    const KEYWORDS: &[&str] = &[
        "cdn", "akamai", "akadns", "edgekey", "edgesuite", "fastly", "cloudfront", "amazonaws", "azureedge", "azurewebsites", "trafficmanager", "gstatic", "googleapis",
        "googleusercontent", "googlesyndication", "googletagmanager", "google-analytics", "doubleclick", "ytimg", "fbcdn", "twimg", "mzstatic", "adnxs", "adsrvr",
        "criteo", "scorecardresearch", "omtrdc", "demdex", "taboola", "outbrain", "telemetry", "analytics", "tracking", "tracker", "sentry.io", "workers.dev", "pages.dev",
        "herokuapp", "firebaseio", "cloudapp", "nflxvideo", "alicdn", "aliyuncs", "myqcloud", "bdstatic", "dns-parking", "namebright", "2mdn", "awsdns", "root-servers",
    ];
    const PREFIXES: &[&str] = &[
        "cdn", "static", "assets", "img", "images", "media", "content", "files", "download", "edge", "cache", "api", "gw", "ws", "push", "auth", "ns", "ns1", "ns2", "dns",
        "mail", "smtp", "mx", "pop", "imap", "ftp", "vpn", "proxy", "update", "updates", "mirror", "storage", "s3", "track", "stats", "metrics", "log", "logs", "pixel", "ads",
        "telemetry", "status", "dev", "test", "staging", "internal", "admin", "origin",
    ];
    let labels: Vec<&str> = h.split('.').collect();
    KEYWORDS.iter().any(|k| h.contains(k)) || (labels.len() > 2 && PREFIXES.contains(&labels[0])) || labels.len() > 4
}

async fn crawl_site(client: reqwest::Client, ff: Option<mpsc::Sender<Job>>, seed: String, pages: usize, out: Arc<Mutex<Vec<Page>>>, ext: Arc<Mutex<HashSet<String>>>, wake: Arc<tokio::sync::Notify>, frontier: Arc<Mutex<VecDeque<String>>>, known: Arc<Mutex<HashSet<String>>>, extra_left: Arc<AtomicUsize>, deadline: Option<std::time::Instant>) -> usize {
    let seed = if seed.starts_with("http://") || seed.starts_with("https://") { seed } else { format!("https://{}", seed) };
    let Ok(start) = Url::parse(&seed) else { return 0 };
    let host = bare_host(&start);
    if host.is_empty() {
        return 0;
    }
    let base = format!("{}://{}", start.scheme(), start.host_str().unwrap_or(""));

    let mut delay = 0.7f32;
    let mut robot: Option<texting_robots::Robot> = None;
    if let Ok(r) = client.get(format!("{}/robots.txt", base)).timeout(Duration::from_secs(10)).send().await {
        if r.status().is_success() {
            if let Ok(b) = r.bytes().await {
                if let Ok(rb) = texting_robots::Robot::new(AGENT, &b) {
                    if let Some(d) = rb.delay {
                        delay = d.clamp(0.5, 5.0);
                    }
                    robot = Some(rb);
                }
            }
        }
    }
    let allowed = |u: &str| robot.as_ref().map(|r| r.allowed(u)).unwrap_or(true);
    if !allowed(&seed) {
        println!("{host}: robots.txt asks crawlers to stay away: skipped");
        return 0;
    }

    let sel_title = Selector::parse("title").unwrap();
    let sel_desc = Selector::parse("meta[name=description], meta[property='og:description']").unwrap();
    let sel_a = Selector::parse("a[href]").unwrap();

    let mut queue: VecDeque<String> = VecDeque::new();
    let mut seen: HashSet<String> = HashSet::new();
    queue.push_back(seed.clone());
    seen.insert(seed.clone());
    let mut sent = 0usize;
    let mut tries = 0usize;
    let mut js_pages = 0usize;

    while let Some(url) = queue.pop_front() {
        if sent >= pages || tries >= pages * 3 || deadline.map(|d| std::time::Instant::now() >= d).unwrap_or(false) {
            break;
        }
        tries += 1;
        let mut got: Option<Seen> = None;
        if let Some(html) = fetch_html(&client, &url).await {
            let doc = Html::parse_document(&html);
            let title = doc.select(&sel_title).next().map(|t| t.text().collect::<String>()).unwrap_or_default();
            let desc = doc.select(&sel_desc).next().and_then(|m| m.value().attr("content")).unwrap_or("").to_string();
            let text = page_text(&doc);
            let links: Vec<String> = doc.select(&sel_a).filter_map(|a| a.value().attr("href")).map(|s| s.to_string()).take(400).collect();
            got = Some(Seen { title, desc, text, links });
        }
        // a page that came back almost empty (it builds itself with JavaScript) is opened in Firefox. A site that did not answer at all
        // (no such name, refused, timed out) is NOT: Firefox would only meet the same error.
        if got.as_ref().map(|g| g.text.chars().count() < THIN).unwrap_or(false) {
            if let Some(r) = render(&ff, &url).await {
                if r.text.chars().count() >= got.as_ref().map(|g| g.text.chars().count()).unwrap_or(0) {
                    js_pages += 1;
                    got = Some(r);
                }
            }
        }
        let Some(g) = got else {
            tokio::time::sleep(Duration::from_millis(300)).await;
            continue;
        };
        let title = g.title.split_whitespace().collect::<Vec<_>>().join(" ");
        let text = g.text.split_whitespace().collect::<Vec<_>>().join(" ");
        let low = title.to_lowercase();
        if !text.is_empty() && !BAD_TITLES.iter().any(|b| low.starts_with(b)) {
            let page = Page { url: url.clone(), title: trunc(&title, 250), description: trunc(g.desc.trim(), 400), text: trunc(&text, 3000), source: String::new() };
            if let Ok(mut w) = out.lock() {
                w.push(page);
                if w.len() >= BATCH_PAGES {
                    wake.notify_one(); // 500 pages are ready: the flusher sends them
                }
            }
            sent += 1;
        }
        if let Ok(cur) = Url::parse(&url) {
            for href in &g.links {
                if queue.len() > 5000 {
                    break;
                }
                let Ok(mut l) = cur.join(href) else { continue };
                l.set_fragment(None);
                if !(l.scheme() == "http" || l.scheme() == "https") {
                    continue;
                }
                let lh = bare_host(&l);
                if lh != host {
                    // a link to another site: its name is kept as a new seed for later, and the crawler goes there too (it is added to the sites
                    // still to read), as long as it is a real site name, not one seen before, and the --follow allowance is not used up
                    if good_host(&lh) && !infra(&lh) {
                        if let Ok(mut e) = ext.lock() {
                            if e.len() < 150_000 {
                                e.insert(lh.clone());
                            }
                        }
                        let fresh = known.lock().map(|mut k| k.insert(lh.clone())).unwrap_or(false);
                        if fresh && extra_left.fetch_update(Ordering::SeqCst, Ordering::SeqCst, |v| v.checked_sub(1)).is_ok() {
                            if let Ok(mut q) = frontier.lock() {
                                q.push_back(lh);
                            }
                        }
                    }
                    continue;
                }
                let ls = l.to_string();
                let path = l.path().to_lowercase();
                if SKIP_EXT.iter().any(|e| path.ends_with(e)) || seen.contains(&ls) || !allowed(&ls) {
                    continue;
                }
                seen.insert(ls.clone());
                queue.push_back(ls);
            }
        }
        tokio::time::sleep(Duration::from_millis((delay * 1000.0) as u64)).await;
    }
    println!("{host}: {sent} pages ({js_pages} through Firefox)");
    sent
}

#[tokio::main]
async fn main() {
    let a = parse_args();
    let text = std::fs::read_to_string(&a.seeds).expect("cannot read the seed file");
    let sites: Vec<String> = text
        .lines()
        .map(|l| l.trim())
        .filter(|l| !l.is_empty() && !l.starts_with('#'))
        .skip(a.start)
        .take(a.count)
        .map(|l| l.to_string())
        .collect();
    println!("{} sites (from number {}), up to {} pages each, {} at the same time", sites.len(), a.start, a.pages, a.conc);

    let client = reqwest::Client::builder()
        .user_agent(UA)
        .timeout(Duration::from_secs(15))
        .connect_timeout(Duration::from_secs(8))
        .redirect(reqwest::redirect::Policy::limited(5))
        .pool_max_idle_per_host(1)
        .build()
        .expect("http client");
    let ff = start_firefox(a.render).await;
    let out: Arc<Mutex<Vec<Page>>> = Arc::new(Mutex::new(Vec::new()));
    let ext: Arc<Mutex<HashSet<String>>> = Arc::new(Mutex::new(HashSet::new()));
    let wake = Arc::new(tokio::sync::Notify::new());
    let finished = Arc::new(std::sync::atomic::AtomicBool::new(false));
    let failed = Arc::new(std::sync::atomic::AtomicBool::new(false));
    let token = std::env::var("MYSEARCH_TOKEN").unwrap_or_default();
    let url = format!("{}/api/ingest", a.ingest.trim_end_matches('/'));

    // the ingest gets a request ONLY when BATCH_PAGES (500) pages are ready: exactly 500 pages each time (the last one may be smaller)
    let flusher = if a.ingest.is_empty() {
        None
    } else {
        let (client, out, wake, finished, failed) = (client.clone(), out.clone(), wake.clone(), finished.clone(), failed.clone());
        Some(tokio::spawn(async move {
            let mut sent_total = 0usize;
            loop {
                let last = finished.load(std::sync::atomic::Ordering::SeqCst);
                loop {
                    let chunk: Vec<Page> = {
                        let mut g = out.lock().unwrap();
                        if g.len() >= BATCH_PAGES || (last && !g.is_empty()) {
                            let n = g.len().min(BATCH_PAGES);
                            g.drain(..n).collect()
                        } else {
                            Vec::new()
                        }
                    };
                    if chunk.is_empty() {
                        break;
                    }
                    let mut requests = 0usize;
                    if post_pages(&client, &url, &token, &chunk, &mut requests).await {
                        sent_total += chunk.len();
                        println!(">>> {} pages sent to the ingest ({} so far)", chunk.len(), sent_total);
                    } else {
                        failed.store(true, std::sync::atomic::Ordering::SeqCst);
                    }
                }
                if last {
                    break;
                }
                wake.notified().await; // nothing on a timer: it wakes only when 500 pages are ready (or at the end of the run)
            }
        }))
    };

    // the sites to read: the seed list slice first; sites found in links of other sites are added to the end (at most --follow of them)
    let pages = a.pages;
    let t0 = std::time::Instant::now();
    let host_of = |s: &str| s.trim_start_matches("https://").trim_start_matches("http://").trim_start_matches("www.").split('/').next().unwrap_or("").to_lowercase();
    let n_seeds = sites.len();
    let popped = Arc::new(AtomicUsize::new(0));
    let deadline = if a.max_minutes > 0 { Some(t0 + Duration::from_secs(a.max_minutes * 60)) } else { None };
    let known: Arc<Mutex<HashSet<String>>> = Arc::new(Mutex::new(sites.iter().map(|s| host_of(s)).collect()));
    let frontier: Arc<Mutex<VecDeque<String>>> = Arc::new(Mutex::new(sites.into_iter().collect()));
    let extra_left = Arc::new(AtomicUsize::new(a.follow));
    let active = Arc::new(AtomicUsize::new(0));
    let total_pages = Arc::new(AtomicUsize::new(0));
    let crawled_sites = Arc::new(AtomicUsize::new(0));
    let mut workers = Vec::new();
    for _ in 0..a.conc {
        let (c, o, f, e, w) = (client.clone(), out.clone(), ff.clone(), ext.clone(), wake.clone());
        let (fr, kn, xl, act, tot, cs, pp) = (frontier.clone(), known.clone(), extra_left.clone(), active.clone(), total_pages.clone(), crawled_sites.clone(), popped.clone());
        workers.push(tokio::spawn(async move {
            loop {
                if deadline.map(|d| std::time::Instant::now() >= d).unwrap_or(false) {
                    break; // the time is up: no new site is started (the ones being read are finished)
                }
                let (site, done) = {
                    let mut q = fr.lock().unwrap();
                    match q.pop_front() {
                        Some(s) => {
                            act.fetch_add(1, Ordering::SeqCst);
                            pp.fetch_add(1, Ordering::SeqCst);
                            (Some(s), false)
                        }
                        None => (None, act.load(Ordering::SeqCst) == 0),
                    }
                };
                match site {
                    Some(s) => {
                        let site_limit = Duration::from_secs((pages as u64 * 4).max(300)); // time for one site grows with the pages wanted from it
                        let n = tokio::time::timeout(site_limit, crawl_site(c.clone(), f.clone(), s, pages, o.clone(), e.clone(), w.clone(), fr.clone(), kn.clone(), xl.clone(), deadline)).await.unwrap_or(0);
                        tot.fetch_add(n, Ordering::SeqCst);
                        cs.fetch_add(1, Ordering::SeqCst);
                        act.fetch_sub(1, Ordering::SeqCst);
                    }
                    None => {
                        if done {
                            break;
                        }
                        tokio::time::sleep(Duration::from_millis(300)).await;
                    }
                }
            }
        }));
    }
    for h in workers {
        let _ = h.await;
    }
    let total = total_pages.load(Ordering::SeqCst);
    println!("crawled: {} pages from {} sites in {} s", total, crawled_sites.load(Ordering::SeqCst), t0.elapsed().as_secs());
    // where the next run should start: after the seed sites that were started (a run that did not get through its slice carries on there)
    let next_start = a.start + popped.load(Ordering::SeqCst).min(n_seeds);
    std::fs::write("next_start.txt", next_start.to_string()).ok();
    println!("the seed list was read up to site number {}", next_start);

    {   // the new site names found in links, one per line (the seed-list bot on the server picks the file up)
        let mut hs: Vec<String> = ext.lock().map(|g| g.iter().cloned().collect()).unwrap_or_default();
        hs.sort();
        println!("{} new site names found in links", hs.len());
        std::fs::write("discovered.txt", hs.join("\n")).ok();
    }
    finished.store(true, std::sync::atomic::Ordering::SeqCst);
    wake.notify_one();
    match flusher {
        Some(f) => {
            let _ = f.await;
            if failed.load(std::sync::atomic::Ordering::SeqCst) {
                eprintln!("some pages could not be sent to the ingest");
                std::process::exit(1);
            }
        }
        None => {
            let all: Vec<Page> = out.lock().map(|g| g.clone()).unwrap_or_default();
            std::fs::write("pages.json", serde_json::to_vec(&all).unwrap_or_default()).ok();
            println!("no --ingest given: the pages are in pages.json");
        }
    }
}

const BATCH_PAGES: usize = 10_000; // pages per request to the ingest

const WIRE_MAX: usize = 60_000_000; // the ingest reads 64 MB at most (a batch of 10,000 pages packs to about 8 MB)

fn gzip_pages(pages: &[Page]) -> Vec<u8> {
    let json = serde_json::to_vec(pages).unwrap_or_default();
    let mut z = GzEncoder::new(Vec::new(), Compression::fast());
    let _ = z.write_all(&json);
    z.finish().unwrap_or_default()
}

fn post_pages<'a>(client: &'a reqwest::Client, url: &'a str, token: &'a str, pages: &'a [Page], requests: &'a mut usize) -> std::pin::Pin<Box<dyn std::future::Future<Output = bool> + Send + 'a>> {
    Box::pin(async move {
        if pages.is_empty() {
            return true;
        }
        let body = gzip_pages(pages);
        if body.len() > WIRE_MAX && pages.len() > 1 {
            let mid = pages.len() / 2;
            let a = post_pages(client, url, token, &pages[..mid], requests).await;
            let b = post_pages(client, url, token, &pages[mid..], requests).await;
            return a && b;
        }
        for attempt in 1..=12u64 { // a busy ingest (503) or a proxy timeout (524) is waited out: pages that already arrived are just skipped as duplicates
            *requests += 1;
            println!("sending {} pages ({} KB packed)", pages.len(), body.len() / 1024);
            match client.post(url).bearer_auth(token).header("Content-Type", "application/json").header("Content-Encoding", "gzip").body(body.clone()).timeout(Duration::from_secs(600)).send().await {
                Ok(r) if r.status().is_success() => return true,
                Ok(r) => {
                    let st = r.status();
                    let t = r.text().await.unwrap_or_default();
                    eprintln!("attempt {}: the ingest answered HTTP {} {}", attempt, st, trunc(&t, 200));
                    if st.as_u16() == 400 || st.as_u16() == 401 {
                        return false; // a retry cannot fix these
                    }
                }
                Err(e) => eprintln!("attempt {}: the ingest could not be reached: {}", attempt, e),
            }
            tokio::time::sleep(Duration::from_secs((10 * attempt).min(60))).await;
        }
        false
    })
}
