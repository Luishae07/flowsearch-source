#!/usr/bin/env python3
"""
start.py - sets up and starts Flowsearch on this machine.

    python3 start.py              asks a series of questions, builds the programs and starts them
    python3 start.py --reuse      starts again with the answers of the last run (no questions)
    python3 start.py status       shows what is running
    python3 start.py stop         stops everything that start.py started
    python3 start.py --defaults   accepts every default answer (no questions)
    python3 start.py --dry-run    shows what would be done, and changes nothing

It needs Python 3.8 or newer (standard library only) and Go 1.22 or newer. Keys are typed hidden and saved in files with mode 600 in the
secrets folder; they are never written to any other file.
"""

import argparse
import getpass
import json
import os
import re
import secrets as pysecrets
import shutil
import signal
import socket
import subprocess
import sys
import time
import webbrowser
from pathlib import Path

REPO = Path(__file__).resolve().parent
STATE = REPO / ".flowsearch-start.json"  # the answers of the last run (no keys in it)
BIN = REPO / "bin"
INGEST_PORT = 8811  # these two ports are fixed in the code
INDEX_PORT = 18820

# files that are read from the secrets folder: (file name, what it is, what it is for)
KEYS = [
    ("browseruse_key", "Browser Use API key", "lets crawlers that need a real browser use a cloud browser"),
    ("typesafe_api_key", "TypeSafe API key", "used by the Jev features (ordering and cleaning of results)"),
    ("deepseek_api_key", "DeepSeek API key", "used by the AI answer feature"),
    ("google_safebrowsing_key", "Google Safe Browsing key", "checks result links against Google's list of unsafe sites"),
    ("github_token.txt", "GitHub token", "raises the GitHub API limit for the crawl status on the live page"),
]

ARGS = None


# ---------------------------------------------------------------------------------------------------------------- talking to the user

def say(text=""):
    print(text)


def heading(text):
    print("\n" + text)
    print("-" * len(text))


def ask(prompt, default="", check=None, secret=False):
    """Asks one question. An empty answer takes the default. `check` returns an error text, or "" when the answer is fine."""
    while True:
        shown = "%s [%s]: " % (prompt, default) if default != "" else "%s: " % prompt
        if ARGS.defaults:
            value = default
            print(shown + (default if not secret else ""))
        else:
            try:
                value = (getpass.getpass(shown) if secret else input(shown)).strip()
            except EOFError:
                sys.exit("\nNo more input. Run this command in a terminal, or use --defaults / --reuse.")
            value = value or default
        if check:
            problem = check(value)
            if problem:
                print("   " + problem)
                if ARGS.defaults:
                    sys.exit("The default answer is not usable; run without --defaults to choose another.")
                continue
        return value


def yesno(prompt, default=True):
    suffix = "Y/n" if default else "y/N"
    while True:
        if ARGS.defaults:
            print("%s [%s]: %s" % (prompt, suffix, "yes" if default else "no"))
            return default
        try:
            value = input("%s [%s]: " % (prompt, suffix)).strip().lower()
        except EOFError:
            sys.exit("\nNo more input. Run this command in a terminal, or use --defaults / --reuse.")
        if value == "":
            return default
        if value in ("y", "yes"):
            return True
        if value in ("n", "no"):
            return False
        print("   Please answer y or n.")


def need_int(low, high):
    def check(v):
        if not re.fullmatch(r"\d+", v) or not (low <= int(v) <= high):
            return "Please enter a whole number from %d to %d." % (low, high)
        return ""
    return check


def need_addr(v):
    if not re.fullmatch(r"([A-Za-z0-9_.\-]*:)?\d{1,5}", v):
        return "Use a port (8812), :8812 or host:port (127.0.0.1:8812)."
    port = int(v.rsplit(":", 1)[-1])
    if not 1 <= port <= 65535:
        return "The port must be from 1 to 65535."
    return ""


def norm_addr(v):
    return ":" + v if v.isdigit() else v


def need_duration(v):
    return "" if re.fullmatch(r"\d+(\.\d+)?(ms|s|m)", v) else "Use a number and a unit, for example 500ms, 1s or 2m."


def need_token(v):
    return "" if len(v) >= 20 else "The token must have at least 20 characters."


def need_lang(v):
    return "" if v in ("en", "de", "any") else "Choose en, de or any."


# ---------------------------------------------------------------------------------------------------------------- looking at the machine

def run_out(argv):
    try:
        return subprocess.run(argv, capture_output=True, text=True, timeout=20).stdout.strip()
    except Exception:
        return ""


def check_machine():
    heading("Checking this machine")
    if not sys.platform.startswith("linux"):
        say("! Flowsearch is developed and run on Linux. On %s some parts may not work." % sys.platform)
    out = run_out(["go", "version"])
    m = re.search(r"go(\d+)\.(\d+)", out)
    if not m:
        sys.exit("Go was not found. Install Go 1.22 or newer (https://go.dev/dl/) and run this again.")
    if (int(m.group(1)), int(m.group(2))) < (1, 22):
        sys.exit("Go %s.%s is too old; Go 1.22 or newer is needed." % (m.group(1), m.group(2)))
    say("  Go            %s" % out)
    optional = [
        ("cjxl / djxl", ["cjxl", "djxl"], "needed to make and read JPEG XL pictures (favicons, screenshots)"),
        ("Chrome", ["google-chrome", "google-chrome-stable", "chromium", "chromium-browser"], "for crawlers that render JavaScript pages"),
        ("Firefox + geckodriver", ["firefox", "geckodriver"], "the other way to render JavaScript pages"),
        ("Rust (cargo)", ["cargo"], "to build the Rust crawler in rustcrawl/"),
    ]
    for name, programs, why in optional:
        if name.startswith("Chrome"):
            found = any(shutil.which(p) for p in programs)
        else:
            found = all(shutil.which(p) for p in programs)
        say("  %-14s %s  (%s)" % (name, "found" if found else "not found", why))
    say("  CPU cores     %s" % (os.cpu_count() or "?"))
    try:
        with open("/proc/meminfo") as f:
            kb = int(f.readline().split()[1])
        say("  Memory        %.1f GB" % (kb / 1048576))
    except Exception:
        pass
    free = shutil.disk_usage(str(Path.home())).free / 1073741824
    say("  Free disk     %.0f GB in your home folder" % free)


def port_busy(port):
    with socket.socket() as s:
        s.settimeout(0.5)
        return s.connect_ex(("127.0.0.1", port)) == 0


# ---------------------------------------------------------------------------------------------------------------- the questions

def need_server(v):
    return "" if re.fullmatch(r"https?://[^\s/]+(/\S*)?", v) else "Use the full address, for example https://search.example.org"


def need_ctoken(v):
    return "" if v.startswith("fsc_") and len(v) >= 20 else "A contributor token starts with fsc_ (the operator of the server gives it to you)."


def contributor_questions():
    """Open crawling: this machine only runs a crawler that sends pages to somebody else's Flowsearch server."""
    a = {"mode": "contribute"}
    heading("Help to crawl")
    say("You will run the official crawler on this machine. The server gives it a list of sites to crawl; the crawler sends the pages it reads to that server.")
    a["server"] = ask("Address of the Flowsearch server", "", need_server).rstrip("/")
    a["token"] = ask("Your contributor token (hidden)", "", need_ctoken, secret=True)
    a["folder"] = ask("Folder for the crawler's work files and the token", str(Path.home() / "flowsearch-contrib"))
    a["workers"] = ask("How many sites to crawl at the same time (a small number is polite to your connection)", "20", need_int(1, 200))
    a["background"] = yesno("Run in the background (keeps running after this window is closed)?", True)
    heading("The rules")
    rules = ""
    try:
        import urllib.request
        with urllib.request.urlopen(a["server"] + "/api/contrib/policy", timeout=15) as r:
            rules = r.read(20000).decode("utf-8", "replace").strip()
    except Exception as e:
        say("   (the rules could not be loaded from the server: %s)" % e)
    if rules:
        say(rules)
    say("\nThe sites you crawl will see your computer's IP address.")
    if not yesno("Do you accept these rules?", False):
        sys.exit("Not started. Contributing needs the rules to be accepted.")
    return a


def contributor_run(a):
    heading("Writing the files")
    folder = Path(a["folder"])
    folder.mkdir(parents=True, exist_ok=True)
    os.chmod(folder, 0o700)
    tok = folder / "fscontrib.token"
    if a.get("token"):
        tok.write_text(a["token"] + "\n")
        os.chmod(tok, 0o600)
        say("  saved the token in %s" % tok)
    elif not tok.exists():
        sys.exit("There is no token yet (%s). Run python3 start.py without --reuse once." % tok)
    saved = {k: v for k, v in a.items() if k != "token"}
    STATE.write_text(json.dumps(saved, indent=2) + "\n")
    heading("Building")
    BIN.mkdir(exist_ok=True)
    for name, pkg in (("sgcrawl", "./cmd/sgcrawl"), ("fscontrib", "./cmd/fscontrib")):
        say("  go build %-10s ..." % name)
        r = subprocess.run(["go", "build", "-o", str(BIN / name), pkg], cwd=str(REPO), capture_output=True, text=True)
        if r.returncode != 0:
            sys.exit("\nBuilding %s failed:\n%s" % (name, r.stdout + r.stderr))
    heading("Starting")
    log = open(folder / "fscontrib.log", "ab")
    argv = [str(BIN / "fscontrib"), "-server", a["server"], "-token-file", str(tok), "-crawler", str(BIN / "sgcrawl"),
            "-dir", str(folder / "work"), "-workers", str(a["workers"])]
    p = subprocess.Popen(argv, cwd=str(folder), stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
    (folder / "pids.json").write_text(json.dumps({"fscontrib": p.pid}))
    time.sleep(2)
    if p.poll() is not None:
        sys.exit("The client stopped right away. Its log (%s):\n%s" % (folder / "fscontrib.log", (folder / "fscontrib.log").read_text(errors="replace")[-1500:]))
    heading("You are contributing")
    say("  Log            %s" % (folder / "fscontrib.log"))
    say("  Stop with      python3 start.py stop")
    say("  Status with    python3 start.py status")
    say("  The client asks the server for work; if other contributors are ahead of you it waits in line and says so in the log.")
    if not a["background"]:
        say("\nRunning in this window. Press Ctrl-C to stop.")
        try:
            while p.poll() is None:
                time.sleep(1)
        except KeyboardInterrupt:
            say("\nStopping ...")
            try:
                os.killpg(p.pid, signal.SIGTERM)
            except Exception:
                pass
            (folder / "pids.json").unlink(missing_ok=True)


def questions():
    heading("0. What do you want to do?")
    say("  1  Run a complete Flowsearch server on this machine")
    say("  2  Help to crawl: run a crawler here that sends pages to a Flowsearch server somebody else runs")
    if ask("Choose 1 or 2", "1", lambda v: "" if v in ("1", "2") else "Enter 1 or 2.") == "2":
        return contributor_questions()
    a = {"mode": "server"}
    heading("1. Folders")
    say("Everything Flowsearch stores goes into folders you choose here. Press Enter to accept the suggestion in [brackets].")
    base = Path(ask("Main folder for Flowsearch", str(Path.home() / "flowsearch"))).expanduser()
    a["data"] = ask("Folder for the search data (pages, index, forum posts)", str(base / "data"))
    a["media"] = ask("Folder that holds the other data folders", str(base / "media"))
    a["cdn"] = ask("Folder for favicons and pictures (JPEG XL)", str(Path(a["media"]) / "pictures"))
    a["shots"] = ask("Folder for screenshots (JPEG XL)", str(Path(a["media"]) / "screenshots"))
    a["secrets"] = ask("Folder for keys and tokens", str(base / "secrets"))
    a["uploads"] = ask("Folder for related projects", str(base / "projects"))
    a["runtime"] = ask("Folder for short-lived files (process list, locks)", str(base / "run"))
    a["home"] = ask("Program folder (this repository)", str(REPO))

    heading("2. Network")
    say("The ingest (port %d) and the index service (port %d) always listen on this machine only; their ports are fixed in the code." % (INGEST_PORT, INDEX_PORT))
    a["web"] = norm_addr(ask("Address the website listens on (:8812 = every network interface, 127.0.0.1:8812 = this machine only)", ":8812", need_addr))

    heading("3. Ingest token")
    say("Crawlers prove who they are to the ingest with a secret token (at least 20 characters).")
    if yesno("Generate a random token?", True):
        a["token"] = pysecrets.token_urlsafe(32)
        say("   A token was generated. It will be saved in %s." % (Path(a["secrets"]) / "mysearch_token"))
    else:
        a["token"] = ask("Your token", "", need_token, secret=True)

    heading("4. Optional keys")
    say("Each key switches on one feature. Press Enter to skip a key; whatever you type is hidden. Skipped features are simply not started.")
    a["keys"] = {}
    for fname, label, why in KEYS:
        say("\n  %s: %s" % (label, why))
        value = ask("  %s (Enter = skip)" % label, "", None, secret=True)
        if value:
            a["keys"][fname] = value

    heading("5. What to start")
    a["start_ingest"] = yesno("Start the ingest (receives pages from the crawlers)?", True)
    a["start_index"] = yesno("Start the index service (searches the pages)?", True)
    a["start_web"] = yesno("Start the website and API?", True)
    a["build_crawlers"] = yesno("Also build the other crawlers (forums, Amazon, Roblox) into the bin folder?", True)
    a["build_rust"] = False
    if shutil.which("cargo"):
        a["build_rust"] = yesno("Also build the Rust crawler (flowcrawl)?", False)

    heading("6. First crawl")
    a["crawl"] = yesno("Start a first crawl now?", True)
    if a["crawl"]:
        a["seeds"] = ask("Sites to crawl (domains separated by commas)", "example.org")
        a["lang"] = ask("Keep pages in which language (en, de or any)", "en", need_lang)
        a["workers"] = ask("How many sites to crawl at the same time", "20", need_int(1, 5000))
        a["maxpages"] = ask("Most pages to take from one site (0 = no limit)", "200", need_int(0, 100000000))
        a["delay"] = ask("Pause after every page, per site", "1s", need_duration)
        a["watch"] = yesno("Keep the crawler running and pick up sites you add to the seeds file later?", False)

    heading("7. Running")
    a["background"] = yesno("Run in the background (keeps running after this window is closed)?", True)
    a["open_browser"] = yesno("Open the website in your browser when it is ready?", False)
    return a


def summary(a):
    heading("Summary")
    for k, label in [("data", "search data"), ("media", "media folders"), ("cdn", "pictures"), ("shots", "screenshots"), ("secrets", "keys"),
                     ("uploads", "projects"), ("runtime", "short-lived files"), ("home", "program folder")]:
        say("  %-16s %s" % (label, a[k]))
    say("  %-16s %s" % ("website", a["web"]))
    say("  %-16s %s" % ("keys given", ", ".join(a["keys"]) or "none"))
    parts = [n for n, f in [("ingest", a["start_ingest"]), ("index service", a["start_index"]), ("website", a["start_web"])] if f]
    say("  %-16s %s" % ("will start", ", ".join(parts) or "nothing"))
    if a.get("crawl"):
        say("  %-16s %s (language %s, %s sites at once, %s pages per site)" % ("first crawl", a["seeds"], a["lang"], a["workers"], a["maxpages"]))
    say("  %-16s %s" % ("run", "in the background" if a["background"] else "in this window (Ctrl-C stops everything)"))


# ---------------------------------------------------------------------------------------------------------------- doing it

def environment(a):
    env = dict(os.environ)
    env.update({
        "FLOWSEARCH_DATA_DIR": a["data"], "FLOWSEARCH_MEDIA_DIR": a["media"], "FLOWSEARCH_CDN_DIR": a["cdn"], "FLOWSEARCH_SHOTS_DIR": a["shots"],
        "FLOWSEARCH_HOME": a["home"], "FLOWSEARCH_UPLOADS_DIR": a["uploads"], "FLOWSEARCH_SECRETS_DIR": a["secrets"],
        "FLOWSEARCH_RUNTIME_DIR": a["runtime"], "MYSEARCH_ADDR": a["web"],
    })
    return env


def write_files(a):
    heading("Writing the folders and files")
    for k in ("data", "media", "cdn", "shots", "uploads", "runtime"):
        Path(a[k]).mkdir(parents=True, exist_ok=True)
    Path(a["data"], "logs").mkdir(exist_ok=True)
    sec = Path(a["secrets"])
    sec.mkdir(parents=True, exist_ok=True)
    os.chmod(sec, 0o700)
    token_file = sec / "mysearch_token"
    if a.get("token"):
        token_file.write_text(a["token"] + "\n")
        os.chmod(token_file, 0o600)
        say("  saved the ingest token in %s" % token_file)
    elif not token_file.exists():
        sys.exit("There is no ingest token yet (%s). Run python3 start.py without --reuse once." % token_file)
    for fname, value in a.get("keys", {}).items():
        p = sec / fname
        p.write_text(value + "\n")
        os.chmod(p, 0o600)
        say("  saved %s" % p)
    Path(a["data"], "pages4.jsonl").touch()  # the index service needs this file; the ingest appends to it
    if a.get("crawl"):
        seeds = [s.strip() for s in a["seeds"].split(",") if s.strip()]
        Path(a["data"], "seeds.txt").write_text("\n".join(seeds) + "\n")
        say("  saved %d sites in %s" % (len(seeds), Path(a["data"], "seeds.txt")))
    env = environment(a)
    with open(REPO / "flowsearch.env", "w") as f:
        f.write("# made by start.py - load it with:  set -a; . ./flowsearch.env; set +a\n")
        for k in sorted(k for k in env if k.startswith("FLOWSEARCH_") or k == "MYSEARCH_ADDR"):
            f.write("export %s=%s\n" % (k, json.dumps(env[k])))
    say("  saved flowsearch.env (the settings for starting the programs by hand)")
    saved = {k: v for k, v in a.items() if k not in ("token", "keys")}
    STATE.write_text(json.dumps(saved, indent=2) + "\n")
    say("  saved the answers in %s (without any key)" % STATE.name)


def build(a):
    heading("Building")
    BIN.mkdir(exist_ok=True)
    targets = []
    if a["start_ingest"]:
        targets.append(("mysearch-ingest", "."))
    if a["start_index"]:
        targets.append(("sgindexd", "./cmd/sgindexd"))
    if a["start_web"]:
        targets.append(("mysearch-web", "./web"))
    if a.get("crawl"):
        targets.append(("sgcrawl", "./cmd/sgcrawl"))
    if a["build_crawlers"]:
        targets += [("lemmycrawl", "./cmd/lemmycrawl"), ("amzdata", "./cmd/amzdata"), ("rbxcrawl", "./cmd/rbxcrawl")]
    seen = set()
    for name, pkg in targets:
        if name in seen:
            continue
        seen.add(name)
        say("  go build %-16s ..." % name)
        r = subprocess.run(["go", "build", "-o", str(BIN / name), pkg], cwd=str(REPO), capture_output=True, text=True)
        if r.returncode != 0:
            sys.exit("\nBuilding %s failed:\n%s" % (name, r.stdout + r.stderr))
    if a["build_rust"]:
        say("  cargo build --release (flowcrawl) ... this takes a few minutes")
        r = subprocess.run(["cargo", "build", "--release"], cwd=str(REPO / "rustcrawl"), capture_output=True, text=True)
        if r.returncode != 0:
            say("  ! the Rust crawler did not build (the rest goes on):\n" + (r.stdout + r.stderr)[-1500:])
    say("  done")


class Runner:
    def __init__(self, a):
        self.a = a
        self.env = environment(a)
        self.procs = {}
        self.pidfile = Path(a["runtime"]) / "flowsearch-pids.json"
        self.logdir = Path(a["data"]) / "logs"

    def start(self, name, argv):
        log = open(self.logdir / (name + ".log"), "ab")
        p = subprocess.Popen(argv, cwd=self.a["home"], env=self.env, stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        self.procs[name] = p
        self.pidfile.write_text(json.dumps({n: pr.pid for n, pr in self.procs.items()}))
        return p

    def wait_port(self, name, port, seconds):
        p = self.procs[name]
        end = time.time() + seconds
        while time.time() < end:
            if p.poll() is not None:
                tail = ""
                try:
                    tail = (self.logdir / (name + ".log")).read_text(errors="replace")[-1500:]
                except Exception:
                    pass
                sys.exit("\n%s stopped right after starting. Its log (%s):\n%s" % (name, self.logdir / (name + ".log"), tail))
            if port_busy(port):
                return True
            time.sleep(0.5)
        say("  ! %s did not open port %d within %d seconds; it may still be loading (log: %s)" % (name, port, seconds, self.logdir / (name + ".log")))
        return False

    def stop_all(self):
        for p in self.procs.values():
            try:
                os.killpg(p.pid, signal.SIGTERM)
            except Exception:
                pass


def start_all(a):
    heading("Starting")
    web_port = int(a["web"].rsplit(":", 1)[-1])
    wanted = []
    if a["start_ingest"]:
        wanted.append(("the ingest", INGEST_PORT))
    if a["start_index"]:
        wanted.append(("the index service", INDEX_PORT))
    if a["start_web"]:
        wanted.append(("the website", web_port))
    busy = [(n, p) for n, p in wanted if port_busy(p)]
    if busy:
        say("These ports are already in use on this machine: " + ", ".join("%s (%d)" % b for b in busy))
        say("Another copy of Flowsearch may be running. Stop it first, or answer 'no' to start without these parts.")
        if not yesno("Continue anyway (the parts whose port is in use will fail to start)?", False):
            sys.exit("Nothing was started.")
    r = Runner(a)
    try:
        if a["start_ingest"]:
            r.start("ingest", [str(BIN / "mysearch-ingest")])
            say("  ingest        starting ...")
            r.wait_port("ingest", INGEST_PORT, 60)
        if a["start_index"]:
            r.start("index", [str(BIN / "sgindexd")])
            say("  index service starting ... (a large index takes a minute to load)")
            r.wait_port("index", INDEX_PORT, 600)
        if a["start_web"]:
            r.start("web", [str(BIN / "mysearch-web")])
            say("  website       starting ...")
            r.wait_port("web", web_port, 120)
        if a.get("crawl"):
            argv = [str(BIN / "sgcrawl"), "-ingest", "http://127.0.0.1:%d/api/ingest" % INGEST_PORT,
                    "-token-file", str(Path(a["secrets"]) / "mysearch_token"), "-seeds", str(Path(a["data"]) / "seeds.txt"),
                    "-lang", a["lang"], "-workers", str(a["workers"]), "-maxpages", str(a["maxpages"]), "-delay", a["delay"]]
            if a.get("watch"):
                argv.append("-watch")
            r.start("crawler", argv)
            say("  crawler       started")
    except BaseException:
        r.stop_all()
        raise
    host = "localhost" if a["web"].startswith(":") else a["web"].rsplit(":", 1)[0]
    url = "http://%s:%d/" % (host, web_port)
    heading("Flowsearch is running")
    if a["start_web"]:
        say("  Website        " + url)
    if a["start_ingest"]:
        say("  Ingest health  http://127.0.0.1:%d/health" % INGEST_PORT)
    say("  Logs           %s" % r.logdir)
    say("  Settings       %s" % (REPO / "flowsearch.env"))
    say("  Stop with      python3 start.py stop")
    say("  Status with    python3 start.py status")
    if a["start_web"] and a.get("open_browser"):
        webbrowser.open(url)
    if not a["background"]:
        say("\nRunning in this window. Press Ctrl-C to stop everything.")
        try:
            while True:
                time.sleep(1)
        except KeyboardInterrupt:
            say("\nStopping ...")
            r.stop_all()
            r.pidfile.unlink(missing_ok=True)


# ---------------------------------------------------------------------------------------------------------------- stop and status

def load_state():
    if not STATE.exists():
        sys.exit("There are no saved answers yet. Run python3 start.py first.")
    return json.loads(STATE.read_text())


def pids_of(a):
    if a.get("mode") == "contribute":
        f = Path(a["folder"]) / "pids.json"
    else:
        f = Path(a["runtime"]) / "flowsearch-pids.json"
    return f, (json.loads(f.read_text()) if f.exists() else {})


def alive(pid):
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


def cmd_status():
    a = load_state()
    f, pids = pids_of(a)
    heading("Status")
    if not pids:
        say("  Nothing was started by start.py (or it was stopped).")
    for n, pid in pids.items():
        say("  %-10s %s (process %d)" % (n, "running" if alive(pid) else "not running", pid))
    if a.get("mode") == "contribute":
        say("  server     %s" % a["server"])
        say("  log        %s" % (Path(a["folder"]) / "fscontrib.log"))
        return
    web_port = int(a["web"].rsplit(":", 1)[-1])
    for n, p in (("ingest", INGEST_PORT), ("index", INDEX_PORT), ("website", web_port)):
        say("  port %-6d %s: %s" % (p, n, "open" if port_busy(p) else "closed"))


def cmd_stop():
    a = load_state()
    f, pids = pids_of(a)
    heading("Stopping")
    if not pids:
        say("  Nothing to stop.")
        return
    for n, pid in pids.items():
        if alive(pid):
            try:
                os.killpg(pid, signal.SIGTERM)
            except Exception:
                pass
            say("  asked %s (process %d) to stop" % (n, pid))
    end = time.time() + 15
    while time.time() < end and any(alive(p) for p in pids.values()):
        time.sleep(0.5)
    for n, pid in pids.items():
        if alive(pid):
            try:
                os.killpg(pid, signal.SIGKILL)
            except Exception:
                pass
            say("  %s did not stop in time and was ended" % n)
    f.unlink(missing_ok=True)
    say("  stopped")


# ---------------------------------------------------------------------------------------------------------------- main

def main():
    global ARGS
    ap = argparse.ArgumentParser(description="Set up and start Flowsearch.")
    ap.add_argument("action", nargs="?", default="start", choices=["start", "stop", "status"])
    ap.add_argument("--reuse", action="store_true", help="start again with the answers of the last run, without asking")
    ap.add_argument("--defaults", action="store_true", help="accept every default answer, without asking")
    ap.add_argument("--dry-run", action="store_true", help="show what would be done and change nothing")
    ARGS = ap.parse_args()
    if ARGS.action == "status":
        return cmd_status()
    if ARGS.action == "stop":
        return cmd_stop()

    say("Flowsearch setup")
    say("================")
    if not (sys.stdin.isatty() or ARGS.defaults or ARGS.reuse):
        sys.exit("This needs a terminal to ask its questions. Run it in a terminal, or use --defaults or --reuse.")
    check_machine()
    if ARGS.reuse:
        a = load_state()
        a.setdefault("keys", {})
        a["token"] = ""
        say("\nUsing the answers saved by the last run.")
    else:
        say("\nI am going to ask a number of questions. You can stop at any time with Ctrl-C; nothing is written until the end.")
        a = questions()
    if a.get("mode") == "contribute":
        heading("Summary")
        say("  Sends pages to  %s" % a["server"])
        say("  Folder          %s" % a["folder"])
        say("  Sites at once   %s" % a["workers"])
        if ARGS.dry_run:
            say("\nDry run: nothing was written, built or started.")
            return
        if not ARGS.reuse and not ARGS.defaults and not yesno("\nBuild and start with these settings?", True):
            sys.exit("Nothing was done.")
        return contributor_run(a)
    summary(a)
    if ARGS.dry_run:
        say("\nDry run: nothing was written, built or started.")
        return
    if not ARGS.reuse and not ARGS.defaults and not yesno("\nBuild and start with these settings?", True):
        sys.exit("Nothing was done.")
    write_files(a)
    build(a)
    start_all(a)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit("\nStopped.")
