package main

import (
	"net/http"
	"strconv"
	"strings"
)

// German version: /de (home), /de/search, /de/webmaster. The language is kept in a cookie, so every
// later page (results, webmaster forms, redirects) stays German. German text is swapped in on the way out.

var deReplacer = strings.NewReplacer(
	"<html lang=en>", "<html lang=de>",
	`<a href=/de>Deutsch</a>`, `<a href=/en>English</a>`,
	"Webmaster tools · Logged in as ", "Webmaster-Tools · Angemeldet als ",
	"Webmaster tools", "Webmaster-Tools",
	"<h1 id=gh>Log in</h1><p class=sub>to use Webmaster tools</p>", "<h1 id=gh>Anmelden</h1><p class=sub>für die Webmaster-Tools</p>",
	"<a data-t=signup>Create account</a>", "<a data-t=signup>Konto erstellen</a>",
	"<a data-t=login>Log in instead</a>", "<a data-t=login>Stattdessen anmelden</a>",
	"dataset.s||'Create account'", "dataset.s||'Konto erstellen'",
	"dataset.l||'Log in'", "dataset.l||'Anmelden'",
	"dataset.s='Create account'", "dataset.s='Konto erstellen'",
	"dataset.l='Log in'", "dataset.l='Anmelden'",
	">Overview</a>", ">Übersicht</a>",
	">Pages</a>", ">Seiten</a>",
	">Removals</a>", ">Entfernen</a>",
	">Sitemaps</a>", ">Sitemaps</a>",
	"<h1>robots.txt</h1><p class=lead>The file at ", "<h1>robots.txt</h1><p class=lead>Die Datei unter ",
	"/robots.txt</code>, read the way the Flowsearch crawler (MySearchBot) reads it.", "/robots.txt</code>, so gelesen, wie der Flowsearch-Crawler (MySearchBot) sie liest.",
	"<small>File</small>", "<small>Datei</small>",
	"<small>Flowsearch crawler</small>", "<small>Flowsearch-Crawler</small>",
	"Could not be reached", "Nicht erreichbar",
	"Not found: everything may be crawled", "Nicht gefunden: alles darf durchsucht werden",
	"The site answered with an error", "Die Website hat mit einem Fehler geantwortet",
	">Found<", ">Gefunden<",
	"Flowsearch is not allowed to crawl this site", "Flowsearch darf diese Website nicht durchsuchen",
	"Allowed, except some paths", "Erlaubt, außer einigen Pfaden",
	"Everything may be crawled", "Alles darf durchsucht werden",
	" (rules for MySearchBot)", " (Regeln für MySearchBot)",
	"<h2>Test a page</h2>", "<h2>Seite testen</h2>",
	"<button>Test</button>", "<button>Testen</button>",
	"<b class=ok>Allowed</b>", "<b class=ok>Erlaubt</b>",
	"<b class=bad>Blocked</b>", "<b class=bad>Gesperrt</b>",
	"Not crawled: the site says search=no", "Nicht durchsucht: die Website sagt search=no",
	"</b> of the file says <code>Content-Signal: search=no</code>, in the group for <code>", "</b> der Datei sagt <code>Content-Signal: search=no</code>, in der Gruppe für <code>",
	". That means the site does not want to be in search indexes, so Flowsearch does not crawl any page of it.", ". Das heißt, die Website will in keinen Suchindex, deshalb durchsucht Flowsearch keine ihrer Seiten.",
	"<h2>Content signals</h2>", "<h2>Content-Signale</h2>",
	"building a search index and showing links and short excerpts", "einen Suchindex aufbauen und Links und kurze Auszüge zeigen",
	"giving content to AI models for answers", "Inhalte an KI-Modelle für Antworten geben",
	"training AI models", "KI-Modelle trainieren",
	"No rule in the file applies to Flowsearch, so every path is allowed.", "Keine Regel in der Datei gilt für Flowsearch, also ist jeder Pfad erlaubt.",
	"No rule matches this path, so it is allowed.", "Keine Regel passt zu diesem Pfad, also ist er erlaubt.",
	"Why: line <b>", "Warum: Zeile <b>",
	" Reason given in the file: <i>", " Grund laut Datei: <i>",
	"</b> of the file says <code>", "</b> der Datei sagt <code>",
	"</code>, in the group for <code>", "</code>, in der Gruppe für <code>",
	". It matches the start of this path, and no longer Allow rule overrides it.", ". Sie passt zum Anfang dieses Pfads, und keine längere Allow-Regel hebt sie auf.",
	". It is more specific than the Disallow rule that also matches (<code>", ". Sie ist genauer als die Disallow-Regel, die ebenfalls passt (<code>",
	"</code>), so the path is allowed.", "</code>), also ist der Pfad erlaubt.",
	", so the path is allowed.", ", also ist der Pfad erlaubt.",
	" The file has a group for MySearchBot, so its rules for everyone (<code>*</code>) are ignored.", " Die Datei hat eine Gruppe für MySearchBot, deshalb werden ihre Regeln für alle (<code>*</code>) ignoriert.",
	"<h2>Sitemaps listed in the file</h2>", "<h2>In der Datei genannte Sitemaps</h2>",
	"No sitemap is listed in this file.", "In dieser Datei ist keine Sitemap genannt.",
	"<h2>The file</h2>", "<h2>Die Datei</h2>",
	"This site has no robots.txt file.", "Diese Website hat keine robots.txt-Datei.",
	"A sitemap is a file that lists the pages of ", "Eine Sitemap ist eine Datei, die die Seiten von ",
	". Send its address and Flowsearch reads it and crawls the pages in it, usually within a minute.", " auflistet. Sende ihre Adresse, dann liest Flowsearch sie und durchsucht die Seiten darin, meist innerhalb einer Minute.",
	"<button>Send sitemap</button>", "<button>Sitemap senden</button>",
	"<h2>Sitemaps you sent</h2>", "<h2>Gesendete Sitemaps</h2>",
	"No sitemap sent yet.", "Noch keine Sitemap gesendet.",
	"Add the site first.", "Füge zuerst die Website hinzu.",
	"That was a lot of sitemaps in an hour. Please try again later.", "Das waren zu viele Sitemaps in einer Stunde. Bitte versuche es später noch einmal.",
	"That doesn&#39;t look like a sitemap address.", "Das sieht nicht wie eine Sitemap-Adresse aus.",
	"The sitemap has to be on ", "Die Sitemap muss zu dieser Website gehören: ",
	"Flowsearch could not read that sitemap.", "Flowsearch konnte diese Sitemap nicht lesen.",
	"That address is not a sitemap (it needs to be XML with a list of pages).", "Das ist keine Sitemap (sie muss XML mit einer Liste von Seiten sein).",
	"Sitemap received: ", "Sitemap erhalten: ",
	" addresses found. The crawl starts within about 20 seconds.", " Adressen gefunden. Das Durchsuchen startet in etwa 20 Sekunden.",
	">Add a site</a>", ">Website hinzufügen</a>",
	"<h1>Overview</h1>", "<h1>Übersicht</h1>",
	"<h1>Add a site</h1>", "<h1>Website hinzufügen</h1>",
	"<h1>Indexed pages</h1>", "<h1>Indexierte Seiten</h1>",
	"<h1>Removals</h1>", "<h1>Entfernen</h1>",
	"<small>Indexed pages</small>", "<small>Indexierte Seiten</small>",
	"<small>Ownership</small>", "<small>Besitz</small>",
	"<small>In results</small>", "<small>In den Ergebnissen</small>",
	"<small>Visibility</small>", "<small>Sichtbarkeit</small>",
	">Verified</span>", ">Bestätigt</span>",
	">Not verified</span>", ">Nicht bestätigt</span>",
	">Shown</span>", ">Angezeigt</span>",
	">Removed</span>", ">Entfernt</span>",
	">Shown in results</span>", ">In den Ergebnissen angezeigt</span>",
	">Removed from results</span>", ">Aus den Ergebnissen entfernt</span>",
	"<h2>Some of your pages</h2>", "<h2>Einige deiner Seiten</h2>",
	">See all indexed pages</a>", ">Alle indexierten Seiten ansehen</a>",
	" pages indexed. The best 50 are listed.", " Seiten indexiert. Die besten 50 stehen hier.",
	"<th>Page</th>", "<th>Seite</th>",
	"Take ", "Nimm ",
	" out of Flowsearch, or bring it back. You have to show that the site is yours.", " aus Flowsearch heraus oder hole sie zurück. Du musst zeigen, dass die Website dir gehört.",
	"Put a code on your site, then press a button. The next page walks you through it.", "Lege einen Code auf deine Website und drücke dann einen Knopf. Die nächste Seite führt dich Schritt für Schritt.",
	"<h2>Keep the crawler out</h2>", "<h2>Crawler aussperren</h2>",
	"Keep the crawler out</summary>", "Crawler aussperren</summary>",
	"<button class=alt style=\"padding:9px 18px\">Log out</button>", "<button class=alt style=\"padding:9px 18px\">Abmelden</button>",

	// the new look: home page, tiles, header and footer
	"Search the web without being tracked. Ask a question and get an answer with sources.", "Suche im Web, ohne verfolgt zu werden. Stelle eine Frage und erhalte eine Antwort mit Quellen.",
	`placeholder="Search the web"`, `placeholder="Im Web suchen"`,
	"<b>AI mode</b><span>Ask a question and get a written answer, with the websites it came from.</span>", "<b>KI-Modus</b><span>Stelle eine Frage und erhalte eine geschriebene Antwort mit den Websites, aus denen sie stammt.</span>",
	"<b>Surprise me</b><span>Open a random page from the web. Great when you are bored.</span>", "<b>Überrasch mich</b><span>Öffne eine zufällige Seite aus dem Web. Perfekt gegen Langeweile.</span>",
	"<b>Ask real people</b><span>Find answers in forum discussions where people helped each other.</span>", "<b>Echte Menschen fragen</b><span>Finde Antworten in Forendiskussionen, in denen sich Menschen gegenseitig geholfen haben.</span>",
	"<b>Search by looks</b><span>Describe how a website looks, or drop a picture, and find similar sites.</span>", "<b>Suche nach Aussehen</b><span>Beschreibe, wie eine Website aussieht, oder lege ein Bild ab, und finde ähnliche Websites.</span>",
	"type=submit>Surprise me</button>", "type=submit>Überrasch mich</button>",
	`Get the "Another one" button`, "Den „Nochmal“-Knopf holen",
	"<h4>Try searching for</h4>", "<h4>Ausprobieren</h4>",
	"<h4>Popular websites</h4>", "<h4>Beliebte Websites</h4>",
	" websites · press ", " Websites · drücke ",
	" to start searching", " zum Suchen",
	">AI mode</a>", ">KI-Modus</a>",
	">Settings</a>", ">Einstellungen</a>",
	">Live stats</a>", ">Live-Zahlen</a>",
	">Search by looks</a>", ">Suche nach Aussehen</a>",
	">News</a>", ">Neuigkeiten</a>",
	">Classic look</a>", ">Klassischer Look</a>",
	">For developers</a>", ">Für Entwickler</a>",
	">For site owners</a>", ">Für Website-Betreiber</a>",
	"an independent search engine", "eine unabhängige Suchmaschine",
	" · sorted for relevance in ", " · nach Relevanz sortiert in ",
	" repeats or unrelated pages hidden", " Wiederholungen oder unpassende Seiten ausgeblendet",
	"· suggested for this search", "· für diese Suche vorgeschlagen",
	" · this search looks for recent posts, so newer ones rank a little higher", " · diese Suche will neue Beiträge, deshalb stehen neuere etwas weiter oben",
	" · this search looks for older posts, so older ones rank a little higher", " · diese Suche will ältere Beiträge, deshalb stehen ältere etwas weiter oben",

	"our own crawler, index and search", "eigener Crawler, eigener Index, eigene Suche",
	"A search engine we made ourselves: its own crawler, its own index, its own ranking.", "Eine Suchmaschine, die wir selbst gebaut haben: eigener Crawler, eigener Index, eigenes Ranking. Sie findet englischsprachige Seiten.",
	"<button>Search</button>", "<button>Suchen</button>",
	"aria-label=Search ", "aria-label=Suche ",
	`placeholder="Search Flowsearch"`, `placeholder="Flowsearch durchsuchen"`,
	`placeholder="Search `, `placeholder="Suche in `,
	` pages"`, ` Seiten"`,
	">Surprise me</a>", ">Überrasch mich</a>",
	">Privacy</a>", ">Datenschutz</a>",
	"<h4>Try</h4>", "<h4>Ausprobieren</h4>",
	"<h4>Biggest sites</h4>", "<h4>Größte Websites</h4>",
	" pages from ", " Seiten von ",
	" sites · press ", " Websites · drücke ",
	"Slow down a little", "Etwas langsamer bitte",
	"That was a lot of searches in a minute. Try again in a moment.", "Das waren viele Suchen in einer Minute. Versuche es gleich noch einmal.",
	"Did you mean ", "Meintest du ",
	"Nothing found for", "Nichts gefunden für",
	"Some things to try:", "Das kannst du versuchen:",
	"check the spelling, or use fewer words", "Rechtschreibung prüfen oder weniger Wörter verwenden",
	"search one site: ", "eine Website durchsuchen: ",
	"try a more general word", "einen allgemeineren Begriff probieren",
	"class=meta>About ", "class=meta>Etwa ",
	" results · ", " Ergebnisse · ",
	" seconds</p>", " Sekunden</p>",
	"Narrow to ", "Einschränken auf ",
	"Related searches", "Ähnliche Suchen",
	"Read on Wikipedia", "Auf Wikipedia lesen",
	"AI summary is off", "KI-Zusammenfassung ist aus",
	"turn on", "einschalten",
	"turn off", "ausschalten",
	"AI summary", "KI-Zusammenfassung",
	"Written by AI. It can be wrong.", "Von einer KI geschrieben. Sie kann sich irren.",
	"No runs yet", "Noch keine Läufe",

	// webmaster
	"For people who run a website: add it to Flowsearch, see how it looks in the results, or take it out. You need an account.", "Für alle, die eine Website betreiben: Füge sie zu Flowsearch hinzu, sieh nach, wie sie in den Ergebnissen aussieht, oder nimm sie wieder heraus. Du brauchst dafür ein Konto.",
	"<h2>Log in</h2>", "<h2>Anmelden</h2>",
	"<h2>Create an account</h2>", "<h2>Konto erstellen</h2>",
	"A name, and a password of at least 8 characters. No email address needed.", "Ein Name und ein Passwort mit mindestens 8 Zeichen. Eine E-Mail-Adresse brauchst du nicht.",
	`placeholder="username"`, `placeholder="Benutzername"`,
	`placeholder="password"`, `placeholder="Passwort"`,
	`placeholder="choose a username"`, `placeholder="Benutzername wählen"`,
	`placeholder="the same password again"`, `placeholder="Passwort wiederholen"`,
	`aria-label=Username`, `aria-label=Benutzername`,
	`aria-label=Password`, `aria-label=Passwort`,
	`aria-label="New username"`, `aria-label="Neuer Benutzername"`,
	`aria-label="Password again"`, `aria-label="Passwort wiederholen"`,
	"<button>Log in</button>", "<button>Anmelden</button>",
	"<button>Create account</button>", "<button>Konto erstellen</button>",
	"<button class=alt style=\"padding:6px 14px\">Log out</button>", "<button class=alt style=\"padding:6px 14px\">Abmelden</button>",
	"Logged in as ", "Angemeldet als ",
	"<h2>Your sites</h2>", "<h2>Deine Websites</h2>",
	"You have not added a site yet.", "Du hast noch keine Website hinzugefügt.",
	"added, not yet proved as yours", "hinzugefügt, noch nicht als deine bestätigt",
	"ownership proved", "Besitz bestätigt",
	", hidden from results", ", in den Ergebnissen ausgeblendet",
	">Check</a>", ">Prüfen</a>",
	">Remove or restore</a>", ">Entfernen oder wiederherstellen</a>",
	"<h2>Add a site</h2>", "<h2>Website hinzufügen</h2>",
	"Flowsearch crawls up to 5,000 pages of it, usually within a minute, and only keeps pages that are in English. It follows your ", "Flowsearch durchsucht bis zu 5.000 Seiten davon, meist innerhalb einer Minute, und behält nur Seiten auf Deutsch. Es beachtet deine ",
	`aria-label="Your site address"`, `aria-label="Adresse deiner Website"`,
	"<button>Add</button>", "<button>Hinzufügen</button>",
	"<h2>Keep the crawler out</h2>", "<h2>Crawler aussperren</h2>",
	"Add this to your <code>robots.txt</code> and Flowsearch will not crawl your site:", "Trage das in deine <code>robots.txt</code> ein, dann durchsucht Flowsearch deine Website nicht:",

	// messages
	"Please log in first.", "Bitte melde dich zuerst an.",
	"That was a lot of submissions in an hour. Please try again later.", "Das waren zu viele Einsendungen in einer Stunde. Bitte versuche es später noch einmal.",
	"An account can add up to 120 sites.", "Ein Konto kann bis zu 120 Websites hinzufügen.",
	"That doesn&#39;t look like a website address. Try something like example.com.", "Das sieht nicht wie eine Website-Adresse aus. Versuche es mit etwas wie beispiel.de.",
	"That doesn&#39;t look like a website address.", "Das sieht nicht wie eine Website-Adresse aus.",
	" was removed from Flowsearch by its owner. The owner can bring it back with “Remove or restore your site”.", " wurde von der Inhaberin oder dem Inhaber aus Flowsearch entfernt. Sie oder er kann es mit „Entfernen oder wiederherstellen“ zurückholen.",
	"Flowsearch could not reach ", "Flowsearch konnte nicht erreichen: ",
	". Is the address right, and is the site public?", ". Stimmt die Adresse, und ist die Website öffentlich?",
	" is already on the list. It is crawled again about once a week.", " steht schon auf der Liste. Sie wird etwa einmal pro Woche erneut durchsucht.",
	"Thank you. ", "Danke. ",
	" is queued: its own crawler starts within about 20 seconds, and the pages show up in search as they are found. Check progress with “Check your site”.", " ist eingereiht: Der eigene Crawler startet in etwa 20 Sekunden, und die Seiten erscheinen in der Suche, sobald sie gefunden werden. Den Stand siehst du unter „Prüfen“.",
	"Too many tries from your address. Please wait an hour.", "Zu viele Versuche von deiner Adresse. Bitte warte eine Stunde.",
	"Choose a name of 3 to 24 letters, numbers, - or _.", "Wähle einen Namen mit 3 bis 24 Buchstaben, Zahlen, - oder _.",
	"The password needs at least 8 characters.", "Das Passwort braucht mindestens 8 Zeichen.",
	"The two passwords are different.", "Die beiden Passwörter sind verschieden.",
	"Could not make the account: ", "Das Konto konnte nicht erstellt werden: ",
	"Welcome, ", "Willkommen, ",
	". Your account is ready.", ". Dein Konto ist bereit.",
	"Wrong name or password.", "Falscher Name oder falsches Passwort.",

	// check page
	"Removed from results by its owner.", "Vom Inhaber aus den Ergebnissen entfernt.",
	"Submitted: it is on the list of sites Flowsearch crawls.", "Eingereicht: Sie steht auf der Liste der Websites, die Flowsearch durchsucht.",
	"Not submitted. If pages show below, they were found by the main crawler.", "Nicht eingereicht. Falls unten Seiten stehen, hat sie der Haupt-Crawler gefunden.",
	"No pages of this site are in Flowsearch yet.", "Von dieser Website sind noch keine Seiten in Flowsearch.",
	"<small>pages indexed</small>", "<small>Seiten im Index</small>",
	"<small>address(es), like www.", "<small>Adresse(n), z. B. www.",
	"Remove or restore this site</a>", "Website entfernen oder wiederherstellen</a>",
	">Back</a>", ">Zurück</a>",

	// owner page
	"This site is ", "Diese Website wird ",
	"currently shown in results", "derzeit in den Ergebnissen angezeigt",
	"currently removed from results", "derzeit nicht in den Ergebnissen angezeigt",
	". To change that, show that you own it.", ". Um das zu ändern, bestätige, dass sie dir gehört.",
	"The page expired. Please try again.", "Die Seite ist abgelaufen. Bitte versuche es noch einmal.",
	"Too many tries in an hour. Please wait a while.", "Zu viele Versuche in einer Stunde. Bitte warte etwas.",
	"Unknown action.", "Unbekannte Aktion.",
	"Flowsearch could not find the code on ", "Flowsearch konnte den Code nicht finden auf ",
	". Put it in place as described below, then try again.", ". Lege ihn wie unten beschrieben ab und versuche es dann noch einmal.",
	"Done. ", "Erledigt. ",
	" no longer appears in Flowsearch results.", " erscheint nicht mehr in den Flowsearch-Ergebnissen.",
	" can appear in Flowsearch results again.", " kann wieder in den Flowsearch-Ergebnissen erscheinen.",
	"<h2>1. Put this code on your site</h2>", "<h2>1. Lege diesen Code auf deiner Website ab</h2>",
	"Either create the file <code>/sg225-verify.txt</code> containing only the code, or put the code anywhere on your front page, for example in a comment:", "Lege entweder die Datei <code>/sg225-verify.txt</code> an, die nur den Code enthält, oder setze den Code irgendwo auf deine Startseite, zum Beispiel in einen Kommentar:",
	"<h2>2. Choose</h2>", "<h2>2. Auswählen</h2>",
	"Flowsearch looks for the code when you press a button.", "Flowsearch sucht den Code, wenn du auf einen Knopf drückst.",
	">Remove from results</button>", ">Aus den Ergebnissen entfernen</button>",
	">Show in results again</button>", ">Wieder in den Ergebnissen zeigen</button>",
)

type deWriter struct {
	http.ResponseWriter
	buf  []byte
	code int
}

func (d *deWriter) WriteHeader(c int)           { d.code = c }
func (d *deWriter) Write(b []byte) (int, error) { d.buf = append(d.buf, b...); return len(b), nil }

func (d *deWriter) finish() {
	body := d.buf
	if strings.HasPrefix(d.Header().Get("Content-Type"), "text/html") {
		body = []byte(translateOutsideResults(string(body)))
	}
	if d.Header().Get("Content-Length") != "" || len(body) > 0 {
		d.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	if d.code != 0 {
		d.ResponseWriter.WriteHeader(d.code)
	}
	d.ResponseWriter.Write(body)
}

func setLang(w http.ResponseWriter, l string) {
	http.SetCookie(w, &http.Cookie{Name: "lang", Value: l, Path: "/", MaxAge: 365 * 86400, SameSite: http.SameSiteLaxMode})
}

// langMux wraps the site: it picks the language and, for German, translates HTML pages.
func langMux(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/en" {
			setLang(w, "en")
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		de := false
		if p == "/de" || strings.HasPrefix(p, "/de/") {
			de = true
			setLang(w, "de")
			r2 := *r
			u := *r.URL
			u.Path = strings.TrimPrefix(p, "/de")
			if u.Path == "" {
				u.Path = "/"
			}
			r2.URL = &u
			r = &r2
		} else if c, err := r.Cookie("lang"); err == nil {
			de = c.Value == "de" // an explicit choice (/de or /en) wins
		} else {
			de = prefersGerman(r.Header.Get("Accept-Language")) // otherwise the browser's language
		}
		p = r.URL.Path
		w.Header().Add("Vary", "Accept-Language, Cookie")
		if !de || strings.HasPrefix(p, "/api") || strings.HasPrefix(p, "/ws/") || p == "/live" || strings.HasPrefix(p, "/favicon") || p == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		r.Header.Set("X-Lang", "de")
		dw := &deWriter{ResponseWriter: w}
		next.ServeHTTP(dw, r)
		dw.finish()
	})
}

// prefersGerman: is German the language the browser likes best (Accept-Language: de-DE,de;q=0.9,en;q=0.8)?
func prefersGerman(h string) bool {
	best, bestQ := "", -1.0
	for _, part := range strings.Split(h, ",") {
		f := strings.Split(strings.TrimSpace(part), ";")
		tag := strings.ToLower(strings.TrimSpace(f[0]))
		if tag == "" || tag == "*" {
			continue
		}
		q := 1.0
		for _, a := range f[1:] {
			if v, ok := strings.CutPrefix(strings.TrimSpace(a), "q="); ok {
				if x, err := strconv.ParseFloat(v, 64); err == nil {
					q = x
				}
			}
		}
		if q > bestQ {
			best, bestQ = tag, q
		}
	}
	return bestQ > 0 && (best == "de" || strings.HasPrefix(best, "de-"))
}

// translateOutsideResults translates the page but leaves the search results (<article class=r> ... </article>)
// alone, so a phrase inside a result's own text is never changed.
func translateOutsideResults(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "<article class=r>")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], "</article>")
		if j < 0 {
			break
		}
		j += i + len("</article>")
		b.WriteString(deReplacer.Replace(s[:i]))
		b.WriteString(s[i:j])
		s = s[j:]
	}
	b.WriteString(deReplacer.Replace(s))
	return b.String()
}
