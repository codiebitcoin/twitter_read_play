# Codie 1.5.3 HTTP API — Integrationsleitfaden für den Betatest

Für Tester, die per Skript, Desktop- oder Mobilanwendung im lokalen Netzwerk auf ein Codie-Gerät mit Version 1.5.3 zugreifen. Grundlage ist der Quellcode des Branches 1.5.3. Dies bedeutet weder, dass alle Geräte diese Version erhalten haben, noch, dass alle Operationen auf einem Gerät geprüft wurden.

## Das Dokumentationspaket verwenden

Entpacken Sie `Codie-HTTP-API-1.5.3.zip` und öffnen Sie `index.html`. Alle 103 HTTP-Operationen auf 83 Pfaden lassen sich offline nachschlagen und durchsuchen, einschließlich Parametern, Anfrage- und Antwortstrukturen sowie benötigten Funktionsberechtigungen. Ein Server oder Zugriff auf die Laborseite des Geräts ist nicht erforderlich.

| Datei | Zweck |
| --- | --- |
| `index.html` | Vollständige Offline-API-Referenz mit Suche und Browserdruck |
| `guide.zh-CN.html` / `GUIDE.zh-CN.md` | Chinesischer Leitfaden |
| `guide.ja.html` / `GUIDE.ja.md` | Japanischer Leitfaden |
| `guide.en.html` / `GUIDE.en.md` | Englischer Leitfaden |
| `guide.de.html` / `GUIDE.de.md` | Deutscher Leitfaden |
| `guide.fr.html` / `GUIDE.fr.md` | Französischer Leitfaden |
| `openapi.json` | OpenAPI-3.1-Beschreibung; API-Werkzeuge müssen HMAC-Signaturen zusätzlich implementieren |
| `examples/local_api.py` | Python-3-Beispiel ohne externe Abhängigkeiten |
| `examples/local_api.mjs` | Beispiel für Node.js 18.17 oder neuer, ohne externe Abhängigkeiten |
| `manifest.json` | Geräteversion, Quellcode-Commit, Exportzeit und SHA-256-Prüfsummen |

Alle fünf Leitfäden liegen als HTML und Markdown bei. Die Sprachauswahl befindet sich oben in `index.html`. Die vollständige API-Referenz und die OpenAPI-Beschreibungstexte sind auf Chinesisch. Pfade, Feldnamen, Enum-Werte und Beispielcode bleiben sprachübergreifend gleich.

OpenAPI `info.version=1` bezeichnet die bestehende Version der API-Beschreibung. Die Gerätesoftwareversion 1.5.3 steht separat in `x-device-version` und im Manifest.

## 1. Gerät vorbereiten

1. Prüfen Sie, dass das Gerät Version 1.5.3 verwendet und bei einem Benutzerkonto angemeldet ist.
2. Verbinden Sie Computer und Gerät über ein LAN, in dem sie einander erreichen können. Ermitteln Sie die Geräte-IP. Standard ist `http://DEVICE_IP:8081`; bei abweichender Konfiguration gilt der tatsächliche Port.
3. Aktivieren und entsperren Sie das Gerät vor einer Unterhaltung. Anfragen verwenden den aktuellen Benutzer und den aktuellen Charakter-Slot des Geräts.
4. Prüfen Sie die Verbindung und führen Sie anschließend ein Beispiel zur Kopplung aus.

```sh
curl http://DEVICE_IP:8081/v1/api/auth/ping
```

Der HTTP-Basispfad lautet `/v1/api`. Eine vollständige URL ist etwa `http://DEVICE_IP:8081/v1/api/data/user/system`. Übergeben Sie den Beispielskripten nur Schema, Host und Port, ohne `/v1/api`.

## 2. Eine Textunterhaltung ausführen

Führen Sie im Stammverzeichnis des entpackten Pakets eines der Beispiele aus:

```sh
python examples/local_api.py http://DEVICE_IP:8081 "Hallo" --credentials credentials.json
node examples/local_api.mjs http://DEVICE_IP:8081 "Hallo" --credentials credentials.json
```

Geben Sie beim ersten Aufruf den am Gerät angezeigten Kopplungscode im Terminal ein. Das Beispiel speichert die Zugangsdaten, liest den aktuellen Slot, erstellt einen Textdialogschritt und fragt dessen Zustand ab, bis die Antwort ausgegeben wird. Weitere Aufrufe verwenden dieselbe Zugangsdaten-Datei.

Für die Wiedergabe der Antwort am Gerät ergänzen Sie `--speech device`:

```sh
python examples/local_api.py http://DEVICE_IP:8081 "Hallo" --credentials credentials.json --speech device --timeout 180
node examples/local_api.mjs http://DEVICE_IP:8081 "Hallo" --credentials credentials.json --speech device --timeout 180
```

Standard ist `--speech none` ohne Sprachausgabe und mit 120 Sekunden Wartezeit. Bei `device` bedeutet `completed`, dass Generierung und Wiedergabe beendet sind. Ein Client-Timeout bricht den serverseitigen Dialogschritt nicht ab. Die ausgegebene Turn-ID ermöglicht weitere Abfragen.

Die Beispiele fordern ausdrücklich `session`-Zugangsdaten an. Diese gelten 12 Stunden und werden beim Neustart des Gerätedienstes ungültig. Für die vorhandenen langfristigen Zugangsdaten ergänzen Sie bei der ersten Kopplung `--auth-scope permanent`. Beide Varianten gewähren denselben Zugriff. Bestehende Dateien werden weder überschrieben noch automatisch umgestellt. Verwenden Sie bei erneuter Kopplung einen neuen Dateinamen. Bewahren Sie die Zugangsdaten vertraulich auf; fügen Sie sie weder Fehlerberichten noch dem weitergegebenen Paket bei.

## 3. Kopplung selbst implementieren

Nur diese fünf öffentlichen HTTP-Operationen benötigen keine HMAC-Signatur:

| Methode | Pfad einschließlich Basispfad | Zweck |
| --- | --- | --- |
| GET | `/v1/api/auth/ping` | Erreichbarkeit prüfen |
| GET | `/v1/api/auth/bootstrap` | Aktuelle `user_id`, Anmeldestatus und Sprachen abrufen |
| POST | `/v1/api/auth/pair/request` | Kopplungscode anzeigen; liefert `challenge_id`; 5 Minuten gültig |
| POST | `/v1/api/auth/pair/confirm` | Code übermitteln und Zugangsdaten erhalten |
| POST | `/v1/api/auth/pair/cancel` | Kopplungsanfrage abbrechen |

JSON zur Bestätigung:

```json
{
  "challenge_id": "Wert aus pair/request",
  "pairing_code": "am Gerät angezeigter Code",
  "auth_scope": "session"
}
```

`auth_scope` erlaubt `session` oder `permanent`. Ohne Angabe verwendet der Endpunkt `permanent`; die Beispiele setzen ausdrücklich `session`. Speichern Sie `data.local_secret`, `data.local_session_id`, `data.local_session_expires_at_ms`, `data.auth_scope` sowie `data.user_id` aus bootstrap. HTTP-Clients benötigen die Proxy-Felder `bridge_id` und `service_secret` nicht. Skripte und native Apps können `Origin/Referer` weglassen. Für Browser gelten weiterhin die vorhandenen Herkunftsprüfungen des Geräts.

## 4. Jede fachliche Anfrage signieren

Verketten Sie folgende Werte in dieser Reihenfolge mit `|`, ohne abschließenden Zeilenumbruch:

```text
METHOD|PATH|TIMESTAMP|NONCE|PAYLOAD_MODE|BODY_HASH|USER_ID
```

```text
signature = hex_lower(HMAC_SHA256(UTF8(local_secret), UTF8(canonical)))
```

| Header | Wert |
| --- | --- |
| `X-Local-Sig` | Signatur als 64 kleine Hexadezimalzeichen |
| `X-Local-Timestamp` | Unix-Zeit in Sekunden als Zeichenfolge; erlaubte Abweichung zur Gerätezeit: 300 Sekunden |
| `X-Local-Nonce` | Neuer Zufallswert pro Anfrage, vorzugsweise UUID; 1–128 Zeichen; kein senkrechter Strich oder Zeilenumbruch |
| `X-Local-Payload-Mode` | Modus aus der folgenden Tabelle |
| `X-Local-Body-Hash` | Hash aus der folgenden Tabelle |
| `X-Local-User-Id` | Aktuelle `user_id` aus bootstrap |
| `X-Local-Session-Id` | Pflicht, wenn die Kopplung eine nicht leere `local_session_id` geliefert hat |

`METHOD` wird großgeschrieben. `PATH` ist der URL-dekodierte Pfad einschließlich `/v1/api`, ohne Host und Query-String. Verwenden Sie die UTF-8-Bytes der ursprünglichen Zeichenfolge `local_secret` als Schlüssel; nicht hexadezimal dekodieren. Signieren Sie exakt die versendeten Body-Bytes. JSON darf nach der Signierung nicht erneut formatiert werden.

| Body | PAYLOAD_MODE | BODY_HASH |
| --- | --- | --- |
| Kein Body | `empty` | SHA-256 der leeren Bytefolge, als kleine Hexadezimalzeichen |
| JSON | `json` | SHA-256 der tatsächlich versendeten JSON-Bytes, als kleine Hexadezimalzeichen |
| Dateiformular | `multipart` | Feste Zeichenfolge `UNSIGNED-PAYLOAD` |
| zstd-Upload-Block | `zstd-stream` | SHA-256 der tatsächlichen komprimierten Bytes, als kleine Hexadezimalzeichen |
| gzip-Upload-Block | `gzip-stream` | SHA-256 der tatsächlichen komprimierten Bytes, als kleine Hexadezimalzeichen |

Hash des leeren Bodys: `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`. Komprimierte Blöcke verwenden `application/octet-stream` und sind nur für ausdrücklich unterstützende Endpunkte vorgesehen. Lassen Sie die HTTP-Bibliothek den multipart-`Content-Type` einschließlich boundary erzeugen.

## 5. Einheitliche API für Dialogschritte

| Methode | Pfad relativ zu `/v1/api` | Zweck |
| --- | --- | --- |
| POST | `/data/user/chat/turns` | Dialogschritt erstellen |
| GET | `/data/user/chat/turns/{turn_id}` | Zustand, Transkription und Antwort abfragen |
| POST | `/data/user/chat/turns/{turn_id}/input/complete` | Gerätemikrofoneingabe beenden und Generierung fortsetzen; leerer Body |
| POST | `/data/user/chat/turns/{turn_id}/cancel` | Eingabe, Generierung und Wiedergabe dieses Schritts abbrechen; leerer Body |

Lesen Sie zuerst `data.slot_number` aus `GET /data/user/slot`. Verwenden Sie für jeden neuen fachlichen Dialogschritt eine neue UUID als `request_id` und ersetzen Sie `slot` durch den aktuellen Slot:

```json
{
  "request_id": "3f743580-bd45-4c24-96c0-61a67ac5f741",
  "slot": 1,
  "input": {"type": "text", "text": "Hallo"},
  "output": {"speech": "none"}
}
```

| Eingabe | Content-Type | Inhalt |
| --- | --- | --- |
| `text` | `application/json` | `input.text` erforderlich, nach Entfernen äußerer Leerzeichen nicht leer; maximal 16000 Zeichen |
| `device_microphone` | `application/json` | Aufnahme am Gerätemikrofon; kein `text`; Eingabe mit `input/complete` beenden |
| `image_text` | `multipart/form-data` | `request` enthält die Konfiguration als JSON-Zeichenfolge, mit `input.type=image_text` und optionalem `text`; Dateifeld: `image` |
| `audio_file` | `multipart/form-data` | `request` enthält die Konfiguration als JSON-Zeichenfolge, mit `input.type=audio_file` und ohne `text`; Dateifeld: `audio` |

Bild- oder WAV-Dateien dürfen maximal 1 MiB groß sein. WAV muss PCM16, 16 kHz, mono und höchstens 30 Sekunden lang sein. Das Pflichtfeld `output.speech` erlaubt nur `none` oder `device`. Audio-Streaming-Eingaben und Audioausgaben an den Client sind noch nicht verfügbar. Unbekannte Anfragefelder werden abgewiesen.

Lesen Sie `data.turn_id` aus der Annahmeantwort und fragen Sie den Zustand beispielsweise einmal pro Sekunde ab. Endzustände sind `completed`, `failed` und `cancelled`. Lesen Sie bei Abschluss `data.assistant_text`, bei Fehlern `data.error`. `transcript` enthält die Transkription; `user_chat_ids` und `assistant_chat_ids` verknüpfen Verlaufseinträge. Zwischenzustände sind kein endgültiger Erfolg.

`202` bedeutet nur angenommen. Innerhalb der Aufbewahrungsfrist liefert dieselbe Kombination aus Zugangsdaten, `request_id` und Inhalt den ursprünglichen Dialogschritt. Abweichender Inhalt führt zu `409 REQUEST_ID_CONFLICT`. Jeder HTTP-Wiederholungsversuch benötigt trotzdem eine neue Signatur-Nonce. Ein neuer Dialogschritt stoppt die alte Sitzung über den vorhandenen Unterbrechungsablauf; eine identische Wiederholung unterbricht nicht erneut.

Endzustände bleiben 30 Minuten gespeichert, bei höchstens 256 zwischengespeicherten Dialogschritten. Ein Neustart des Gerätedienstes löscht die Turn- und Idempotenzdaten. Charakter- oder Benutzerwechsel, ungültige Zugangsdaten und das Löschen des Verlaufs können alte Schritte stoppen oder unzugänglich machen. Abbrechen lassen sich nur die angegebenen Schritte, die mit denselben Zugangsdaten erstellt wurden.

### Direkte Textausgabe als Sprache (TTS)

Lassen Sie den aktuellen Charakter einen bereits feststehenden Text einer externen Anwendung vorlesen, etwa das Ergebnis einer Home-Assistant-Aktion. Die API ruft kein LLM auf, erzeugt keine Charakterantwort und schreibt keinen Chatverlauf.

Senden Sie JSON an `POST /data/user/tts/utterances`. Alle Pfade sind relativ zu `/v1/api`; verwenden Sie die oben beschriebene HMAC-Signatur:

```json
{
  "request_id": "e14eec98-5f67-445f-b605-bcc0b23f8f75",
  "slot": 1,
  "text": "Das Licht im Wohnzimmer ist aus."
}
```

Fragen Sie `GET /data/user/tts/utterances/{utterance_id}` ab. Abbruch: `POST /data/user/tts/utterances/{utterance_id}/cancel` mit leerem Body.

202 bestätigt nur die Annahme; lesen Sie `data.utterance_id`. Zustände: `queued`, `synthesizing`, `playing`, `cancelling`; Endzustände: `completed`, `failed`, `cancelled`. completed erfordert das Ende von Synthese und Gerätewiedergabe. Fehler stehen in `data.error`. text muss nach Entfernen äußerer Leerzeichen 1–16000 Zeichen enthalten; JSON maximal 128 KiB. Verwendet werden der aktuelle Slot sowie Stimme, Sprache und Lautstärke seines Charakters. Das Gerät muss aktiv und entsperrt sein. Neue Sprachausgabe unterbricht die Vordergrundsitzung; eine neue Unterhaltung kann die Sprachausgabe unterbrechen. Es gibt keine Wiedergabewarteschlange.

Identische Wiederholungen mit denselben Zugangsdaten und derselben request_id liefern den ursprünglichen Auftrag (200); abweichender Inhalt ergibt 409. request_id-Namensraum und maximal 256 Cache-Einträge werden mit Chat geteilt. Endzustände bleiben 30 Minuten erhalten; Neustart löscht sie. Ungültige Zugangsdaten, Benutzer-/Charakterwechsel oder gelöschter Verlauf können Aufträge stoppen oder unzugänglich machen. Maximale Laufzeit: 180 Sekunden. Es werden keine Audiodateien oder Audiostreams zurückgegeben.

```sh
python examples/local_api.py http://DEVICE_IP:8081 "Das Licht im Wohnzimmer ist aus." --credentials credentials.json --tts --timeout 180
node examples/local_api.mjs http://DEVICE_IP:8081 "Das Licht im Wohnzimmer ist aus." --credentials credentials.json --tts --timeout 180
```

`playing` setzt die Wiedergabestart-Bestätigung des Geräts für den aktuellen Stream voraus. Bei aktivierten Animationen wird eine Talking-Animation zufällig ausgewählt. Ein angenommener Abbruch liefert 202, ein laufender oder bereits abgeschlossener Abbruch 200, eine bereits beendete oder fehlgeschlagene Aufgabe 409. Ein Zeitlimit beim Warten im Client bricht die Geräteaufgabe nicht automatisch ab.

## 6. Weitere Funktionen und Abschluss prüfen

Genaue Pfade, Felder und Antworten stehen in der vollständigen Offline-Referenz und in OpenAPI.

| Kategorie | HTTP-Operationen | Funktionen |
| --- | --- | --- |
| Zugriff und Kopplung | 5 | Erreichbarkeit, Kopplung, Abbruch |
| Gerät und Zustand | 5 | Systeminformationen, Berechtigungen, Geräte-/Dialogzustand, KI-Sprache |
| Charakter und proaktive Interaktion | 27 | Eigenschaften, Slots, Modell-/Outfit-Upload und Export, Laden, Zurücksetzen, proaktive Einstellungen |
| Dialog und Verlauf | 11 | Dialogschritte, Verlauf lesen/löschen, Chatbilder |
| Erinnerung und Tagebuch | 8 | Abfragen und Bearbeiten |
| Wecker und Notizen | 11 | Ressourcenverwaltung, Wecker ein-/ausschalten |
| Musik | 11 | Upload, Dateiverwaltung, Wiedergabe und Zustand |
| Stimme und Ton | 13 | Berechtigungen, direkte Textausgabe, Hörprobe mit festem Text, Steuerung, Stimmklonen |
| Beleuchtung | 12 | Projekte, Vorschau, Kompilierung, Wiedergabe, Anwenden, Hauptschalter |

Die Kopplung erweitert keine Geräteberechtigungen. Lesen Sie `capabilities` aus `/data/user/system`; die einzelnen Operationen nennen `x-capabilities`. Die Betaverteilung dieser Dokumentation bedeutet nicht, dass alle fachlichen Operationen BETA voraussetzen. Die Labor-Dokumentationsseite des Geräts behält ihre vorhandene BETA-Beschränkung.

Eine erfolgreiche Antwort kann Speicherung oder Einreihung bedeuten, ohne dass die Geräteaktion beendet ist. Prüfen Sie Dialogschritte für Unterhaltungen, Aufgaben-/Ladezustände für Modell-Upload, Anwendung und Export, den Wiedergabezustand für Musik und `/data/user/tts_audition/state` für Hörproben. Hörproben akzeptieren nur `voice_id` und optional `group_key`. Sie verwenden einen festen Text in der aktuellen KI-Sprache; beliebiger Sprechtext ist nicht zulässig.

Modelldateien: maximal 200 MiB. Dateien zum Stimmklonen: maximal 10 MiB. Musik-Sammeluploads: maximal 200 MiB und 64 Dateien je Anfrage. Gewöhnliche signierte Bodys und komprimierte Blöcke: maximal 8 MiB. Multipart-Gesamtgrößen enthalten zusätzlich den Platzbedarf der Felder. Beachten Sie die jeweiligen Endpunktgrenzen.

Optionaler Echtzeit-Zustandskanal: `/v1/ws/channel`. Verwenden Sie einen signierten GET-Handshake mit den Query-Parametern `sig`, `timestamp`, `nonce`, `payload_mode=empty`, `body_hash`, `user_id` sowie `session_id`, falls nicht leer. Signiert wird `/v1/ws/channel` ohne Query-String. Der Kanal liefert Zustandsschnappschüsse, keinen Stream des generierten Dialogtexts. Unter anderem schließen ungültige Zugangsdaten oder Benutzerwechsel die Verbindung.

## 7. Fehlerbehebung und Rückmeldung

| Symptom oder Code | Vorgehen |
| --- | --- |
| Keine Verbindung | IP, Port und LAN-Verbindung prüfen; zuerst ping aufrufen |
| `401 REQUEST_EXPIRED` | Uhren abgleichen; neuen Zeitstempel und neue Signatur erzeugen |
| `401 NONCE_REUSED` | Für jede Anfrage eine neue Nonce verwenden, auch beim Polling und Wiederholen |
| `401 LOCAL_SESSION_EXPIRED` | Mit neuem Dateinamen für Zugangsdaten erneut koppeln |
| `401 LOCAL_USER_MISMATCH` | Aktuellen Benutzer prüfen; bootstrap und Kopplung wiederholen |
| `401 LOCAL_SECRET_MISMATCH` / `INVALID_SIGNATURE` | Ursprünglichen Schlüssel, Signaturpfad, Body-Bytes und Header prüfen |
| `403` | Fehlerfelder und Geräteberechtigungen prüfen; Kopplung umgeht keine Berechtigungen |
| `409 SLOT_MISMATCH` | Aktuellen Slot erneut lesen |
| `409 DEVICE_BLOCKED` / `AMBIENT_MODE_BLOCKED` | `reason` lesen; Gerät aktivieren/entsperren und Dialogzulassung im aktuellen Modus prüfen |
| `413` / `415` | Größe, Content-Type, Ein-/Ausgabetyp und WAV-Format prüfen |
| `503` | Anhand des Fehlercodes Dienstbereitschaft oder Cache-Limit prüfen |

Fehlerstrukturen unterscheiden sich nach Funktion. Prüfen Sie zuerst den HTTP-Status, danach die dokumentierten Felder wie `code`, `message` und `reason`. Wiederholen Sie Schreiboperationen nicht unkontrolliert.

Geben Sie bei Rückmeldungen die Geräteversion, den Quellcode-Commit aus `manifest.json`, Ereigniszeit und Gerätezeitzone, Methode/Pfad, HTTP-Status, bereinigte Antwort, relevante `request_id` / `turn_id` und das beobachtete Geräteverhalten an. Senden Sie weder `local_secret` noch Zugangsdaten-Dateien oder Signatur-Header.

Administrationsendpunkte, testspezifische KI-Konfiguration, Debug-Audio, Diagnoselogs und interne Wartung gehören nicht zu diesem öffentlichen Vertrag. Dieses Paket prüft den API-Vertrag auf Quellcodeebene. Builds, Geräteaufrufe und Cloud-Ende-zu-Ende-Verhalten müssen getrennt validiert werden.
