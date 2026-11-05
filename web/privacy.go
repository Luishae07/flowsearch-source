package main

import "net/http"

// privacyPage: what the site stores and processes, in English and (on the German site) in German.
func privacyPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if isDE(r) {
		w.Write([]byte(proseShell("Datenschutz - Flowsearch",`<h1>Datenschutz</h1>
<p class=sub>Kurz gesagt: Flowsearch verfolgt dich nicht.</p>
<h2>Kein Tracking</h2>
<p>Wir benutzen keine Analyse-, Werbe- oder Tracking-Dienste und erstellen keine Besucherprofile.</p>
<h2>Cookies</h2>
<ul>
<li><b>surprise_seen</b>: wird gesetzt, wenn du auf „Überrasch mich“ drückst. Es enthält kurze Prüfsummen der zuletzt gezeigten Seiten (höchstens 30), damit du nicht zweimal dieselbe Seite bekommst. Es enthält keine Kennung, wird nur in deinem Browser gespeichert und läuft nach 30 Tagen ab.</li>
<li><b>amazon_store</b>: merkt sich, ob du in den Einkaufsergebnissen amazon.de oder amazon.com sehen willst (in den Einstellungen gewählt).</li>
<li><b>dvd</b>: merkt sich, ob du das hüpfende Banner auf der Startseite eingeschaltet hast (in den Einstellungen gewählt; standardmäßig aus).</li>
<li><b>Anmeldung der Webmaster-Werkzeuge</b>: nur wenn du dich dort selbst anmeldest, damit du angemeldet bleibst.</li>
</ul>
<p>Außerdem merkt sich dein Browser (localStorage) die zuletzt gewählte „Überrasch mich“-Art. Das bleibt auf deinem Gerät. Alle diese Einträge sind für eine Funktion nötig, die du selbst nutzt, und du kannst sie jederzeit im Browser löschen. Deshalb gibt es kein Cookie-Banner.</p>
<h2>Technischer Betrieb</h2>
<p>Damit die Seite ausgeliefert werden kann, wird bei jeder Anfrage technisch die IP-Adresse verarbeitet. Die Seite wird über Cloudflare ausgeliefert, das Anfragen dabei als Dienstleister verarbeitet. Wir nutzen die IP-Adresse nur, um die Anfrage zu beantworten und zu viele Anfragen zu begrenzen (dafür wird sie höchstens eine Minute im Arbeitsspeicher gehalten), nicht um dich wiederzuerkennen. Wir führen keine Liste, wer was gesucht hat.</p>
<h2>Suchanfragen und KI</h2>
<p>Deine Suchanfrage wird verarbeitet, um dir Ergebnisse zu zeigen. Wir bauen daraus keine Profile. Damit die Ergebnisse besser sind, senden wir den Suchtext (ohne deine IP-Adresse und ohne Kennung) zusammen mit Titeln und Textauszügen der Treffer an KI-Dienste:</p>
<ul>
<li><b>KI-Sortierdienst</b> (Jev von TypeSafe) oder, wenn der nicht antwortet, <b>Cloudflare Workers AI</b>: sortieren Ergebnisse, wählen die Art der Zusatzbox (Produkte, Foren, Videos, Roblox), den besten Kommentar und prüfen Seiten auf nicht jugendfreie Inhalte.</li>
<li><b>DeepSeek</b>: schreibt die KI-Antwort über den Ergebnissen und kann dafür eine Webseite abrufen. Im KI-Modus (/chat) bekommt DeepSeek die Nachrichten des Gesprächs (ohne IP-Adresse und ohne Kennung), sucht damit in Flowsearch und schreibt die Antwort. Das Gespräch bleibt nur in deinem Browser; auf dem Server wird es nicht gespeichert.</li>
<li><b>OpenStreetMap (Nominatim)</b>: bei Suchen nach Orten wird der Suchtext für die Karte nachgeschlagen. Die Kartenkacheln liefern wir selbst aus.</li>
</ul>
<p>Antworten und Urteile der KI werden anhand des Suchtextes zwischengespeichert, ohne IP-Adresse. Flowsearch zeigt keine nicht jugendfreien Inhalte.</p>
<h2>Warnungen vor unsicheren Seiten</h2>
<p>Nachdem eine Ergebnisseite geladen wurde, werden die Adressen ihrer Treffer von unserem Server an Google Safe Browsing geschickt. Das sagt, ob eine Seite für Schadsoftware oder Phishing bekannt ist. Es werden nur die Adressen gesendet, ohne deine IP-Adresse und ohne Kennung. Eine Seite, die Google listet, bekommt eine rote Warnung; nichts wird ausgeblendet. Die Antworten werden 30 Minuten gespeichert.</p>
<h2>Suche nach Aussehen</h2>
<p>Wenn du auf „Suche nach Aussehen“ ein Bild ablegst, wird es an Cloudflare Workers AI geschickt, das den Inhalt in einem Satz beschreibt. Wir speichern das Bild nicht. Auch eine eingetippte Beschreibung wird dort in Zahlen umgewandelt.</p>
<h2>Bilder und Schriften</h2>
<p>Symbole, Karten, Vorschaubilder und Produktbilder werden von unserem eigenen Server ausgeliefert. Dein Browser fragt dafür keine fremden Dienste an.</p>`)))
		return
	}
	w.Write([]byte(proseShell("Privacy - Flowsearch",`<h1>Privacy</h1>
<p class=sub>In short: Flowsearch does not track you.</p>
<h2>No tracking</h2>
<p>We use no analytics, advertising or tracking services, and we do not build visitor profiles.</p>
<h2>Cookies</h2>
<ul>
<li><b>surprise_seen</b>: set when you press “Surprise me”. It holds short checksums of the last pages shown (at most 30), so you do not get the same page twice. It has no ID, is stored only in your browser and expires after 30 days.</li>
<li><b>amazon_store</b>: remembers whether you want amazon.de or amazon.com in the shopping results (chosen in Settings).</li>
<li><b>dvd</b>: remembers whether you turned on the bouncing banner on the home page (chosen in Settings; it is off by default).</li>
<li><b>Webmaster tools login</b>: only if you log in there yourself, so that you stay logged in.</li>
</ul>
<p>Your browser also remembers your last “Surprise me” choice (localStorage). That stays on your device. All of these are needed for a feature you use yourself, and you can delete them in your browser at any time. That is why there is no cookie banner.</p>
<h2>Running the site</h2>
<p>To deliver the page, your IP address is processed technically with every request. The site is delivered through Cloudflare, which processes requests as a service provider. We use the IP address only to answer the request and to limit too many requests (it is held in memory for at most a minute for that), not to recognise you. We keep no list of who searched for what.</p>
<h2>Searches and AI</h2>
<p>Your search is processed to show you results. We do not build profiles from it. To make results better we send the search text (without your IP address and without any ID), together with titles and text excerpts of the matches, to AI services:</p>
<ul>
<li><b>AI sorting service</b> (Jev by TypeSafe) or, when it does not answer, <b>Cloudflare Workers AI</b>: they order results, pick the kind of extra box (products, forums, videos, Roblox), pick the best comment, and check pages for adult content.</li>
<li><b>DeepSeek</b>: writes the AI answer above the results and can fetch a web page for it. In AI mode (/chat), DeepSeek gets the messages of the conversation (without your IP address and without any ID), searches Flowsearch with them and writes the answer. The conversation stays only in your browser; the server does not save it.</li>
<li><b>OpenStreetMap (Nominatim)</b>: for searches about places, the search text is looked up for the map. The map tiles are served by us.</li>
</ul>
<p>The AI's answers and verdicts are cached by the search text, without an IP address. Flowsearch does not show adult content.</p>
<h2>Unsafe site warnings</h2>
<p>After a results page has loaded, the addresses of its results are sent from our server to Google Safe Browsing, which says whether a page is known for malware or phishing. Only the addresses are sent, without your IP address and without any ID. A page that Google lists gets a red warning label; nothing is hidden. The answers are kept for 30 minutes.</p>
<h2>Search by looks</h2>
<p>If you drop a picture on “Search by looks”, it is sent to Cloudflare Workers AI, which describes it in one sentence. We do not store the picture. A description you type is turned into numbers there as well.</p>
<h2>Images and fonts</h2>
<p>Icons, maps, preview images and product pictures are served by our own server. Your browser does not ask outside services for them.</p>`)))
}
