// Package texts holds the words of the messages, in German and English: the service messages'
// names, a short title per Status-page warning, the system events, the headlines of a batch.
// User-facing text says "the system", never "the box".
package texts

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Lang normalises a language to de or en (de for anything else: the addon is German first).
func Lang(l string) string {
	if l == "en" {
		return "en"
	}
	return "de"
}

func pick(lang, de, en string) string {
	if Lang(lang) == "en" {
		return en
	}
	return de
}

// ServiceKind maps a maintenance datapoint to its kind, as the Status page groups them:
// unreach, sticky, lowbat, fault, config, update, other.
func ServiceKind(key string) string {
	switch key {
	case "UNREACH":
		return "unreach"
	case "STICKY_UNREACH":
		return "sticky"
	case "LOWBAT", "LOW_BAT":
		return "lowbat"
	case "CONFIG_PENDING":
		return "config"
	case "UPDATE_PENDING", "DEVICE_IN_BOOTLOADER":
		return "update"
	}
	if strings.Contains(key, "SABOTAGE") || key == "FAULT_REPORTING" || strings.HasPrefix(key, "ERROR") || strings.HasPrefix(key, "DUTY_CYCLE") || strings.HasPrefix(key, "DUTYCYCLE") {
		return "fault"
	}
	return "other"
}

// ServiceKinds lists the kinds in the order of their weight.
var ServiceKinds = []string{"unreach", "fault", "lowbat", "other", "config", "update", "sticky"}

var serviceText = map[string][2]string{
	"UNREACH":                               {"Kommunikation gestört", "communication disturbed"},
	"STICKY_UNREACH":                        {"Kommunikation war gestört", "communication was disturbed"},
	"LOWBAT":                                {"Batterie schwach", "battery low"},
	"LOW_BAT":                               {"Batterie schwach", "battery low"},
	"CONFIG_PENDING":                        {"Konfigurationsdaten stehen zur Übertragung an", "configuration data waits to be transferred"},
	"UPDATE_PENDING":                        {"Firmware-Update steht an", "firmware update pending"},
	"DEVICE_IN_BOOTLOADER":                  {"Gerät im Bootloader", "device in bootloader"},
	"SABOTAGE":                              {"Sabotage", "sabotage"},
	"ERROR_SABOTAGE":                        {"Sabotage", "sabotage"},
	"STICKY_SABOTAGE":                       {"Sabotage war ausgelöst", "sabotage was triggered"},
	"FAULT_REPORTING":                       {"Störung", "fault"},
	"DUTY_CYCLE":                            {"Duty Cycle erreicht", "duty cycle reached"},
	"DUTYCYCLE":                             {"Duty Cycle erreicht", "duty cycle reached"},
	"ERROR_OVERHEAT":                        {"Überhitzung", "overheated"},
	"ERROR_OVERLOAD":                        {"Überlast", "overload"},
	"ERROR_REDUCED":                         {"Leistung reduziert", "power reduced"},
	"ERROR_NON_FLAT_POSITIONING":            {"Gerät liegt nicht plan", "device is not mounted flat"},
	"ERROR_CODE":                            {"Fehler", "error"},
	"ERROR_BAD_RECHARGEABLE_BATTERY_HEALTH": {"Akku verbraucht", "rechargeable battery worn out"},
	"ERROR_DEGRADED_CHAMBER":                {"Rauchkammer verschmutzt", "smoke chamber degraded"},
	"ERROR_UNDERVOLTAGE":                    {"Unterspannung", "undervoltage"},
	"ERROR_BUS_CONFIG_MISMATCH":             {"Bus-Konfiguration passt nicht", "bus configuration mismatch"},
	"ERROR_POWER_FAILURE":                   {"Netzausfall", "power failure"},
	"ERROR_COMMUNICATION_PARTICULATE_MATTER_SENSOR": {"Feinstaubsensor antwortet nicht", "particulate matter sensor does not answer"},
}

// Service answers what a service message says: its name, and the value where it is a code.
func Service(lang, key string, value any) string {
	t, ok := serviceText[key]
	text := key
	if ok {
		text = pick(lang, t[0], t[1])
	}
	switch v := value.(type) {
	case float64:
		if v != 0 && v != 1 {
			text += " (" + strconv.FormatFloat(v, 'f', -1, 64) + ")"
		}
	case string:
		if v != "" {
			text += " (" + v + ")"
		}
	}
	return text
}

// ServiceKindName names a kind for the page and the digest.
func ServiceKindName(lang, kind string) string {
	m := map[string][2]string{
		"unreach": {"Kommunikation gestört", "communication disturbed"},
		"sticky":  {"Kommunikation war gestört", "communication was disturbed"},
		"lowbat":  {"Batterie schwach", "battery low"},
		"fault":   {"Störung, Sabotage, Fehler", "fault, sabotage, error"},
		"config":  {"Konfiguration steht an", "configuration pending"},
		"update":  {"Firmware-Update steht an", "firmware update pending"},
		"other":   {"Sonstige Meldungen", "other messages"},
	}
	if t, ok := m[kind]; ok {
		return pick(lang, t[0], t[1])
	}
	return kind
}

// warningTitle is a short title per Status-page warning id. {list} is filled with the names of
// the addons, units, devices or disks the warning's params carry, any other {name} with the
// param of that name.
var warningTitle = map[string][2]string{
	"rega":                    {"Addons brauchen die ReGa und laufen hier nicht: {list}", "Addons need the ReGa and do not run here: {list}"},
	"arch":                    {"Addons sind für eine andere Architektur gebaut: {list}", "Addons are built for another architecture: {list}"},
	"addon-payload":           {"Addons ohne Programmdateien nach einer Wiederherstellung: {list}", "Addons without their program files after a restore: {list}"},
	"addon-ended":             {"Addon-Programm hat sich beendet: {list}", "Addon program has ended: {list}"},
	"addon-failed":            {"Addon fehlgeschlagen: {list}", "Addon failed: {list}"},
	"crash-loop":              {"Dienst scheitert und startet immer wieder neu: {list}", "Service keeps failing and restarting: {list}"},
	"occulited-crash-loop":    {"Der Systemdienst occulited ist wiederholt gescheitert", "The system service occulited kept failing"},
	"meta":                    {"Der Metadatenspeicher wurde aus seiner Sicherung wiederhergestellt", "The metadata store was recovered from its backup"},
	"trust-ca":                {"Ein Server zeigt ein Zertifikat einer unbekannten Zertifizierungsstelle", "A server presents a certificate of an unknown authority"},
	"trust-pin":               {"Ein Server zeigt ein anderes Zertifikat als das hinterlegte", "A server presents another certificate than the one on file"},
	"app-public":              {"Die Bedienung ist öffentlich: ohne Anmeldung, als {account}", "Control is public: without a login, as {account}"},
	"ssh-addon-key":           {"Ein Addon hält einen SSH-Schlüssel für root", "An addon holds an SSH key for root"},
	"unclean":                 {"Das System wurde nicht sauber heruntergefahren (Stromausfall oder harter Reset)", "The system was not shut down cleanly (power loss or hard reset)"},
	"backup-target":           {"Kein Sicherungsziel: {path} ist nicht lesbar", "No backup target: {path} cannot be read"},
	"backup-userfs":           {"Die nächtliche Sicherung schreibt auf das System selbst ({path})", "The nightly backup writes onto the system itself ({path})"},
	"backup-unencrypted":      {"Die Sicherung ist nicht verschlüsselt", "Backups are not encrypted"},
	"backup-addon":            {"Addon-Schnappschuss für die Sicherung fehlgeschlagen: {list}", "Addon snapshot for the backup failed: {list}"},
	"backup-age":              {"Seit {days} Tagen keine Sicherung", "No backup for {days} days"},
	"backup-delivery":         {"Das Sicherungsziel {name} funktioniert nicht ({cause})", "The backup target {name} does not work ({cause})"},
	"certificate":             {"Das Zertifikat des Systems braucht Aufmerksamkeit ({variant})", "The system's certificate needs attention ({variant})"},
	"storage":                 {"Der Speicher des Systems altert ({variant})", "The system's storage is wearing ({variant})"},
	"soc-hot":                 {"Der Prozessor ist heiß", "The processor is hot"},
	"disk-hot":                {"Ein Datenträger ist heiß: {list}", "A disk runs hot: {list}"},
	"addon-ownership":         {"Dateien eines Addons gehören root: {list}", "Files of an addon belong to root: {list}"},
	"addon-adopted-root":      {"Addon läuft als root, obwohl es das nicht braucht: {list}", "Addon runs as root although it need not: {list}"},
	"addon-parents":           {"Ein Verzeichnis über den Addons ist nicht geschützt", "A directory above the addons is not protected"},
	"security-key":            {"BidCos-RF verwendet den Standard-Sicherheitsschlüssel", "BidCos-RF uses the default security key"},
	"hmip-adapter":            {"HmIP-RF ist ausgefallen: das Funkmodul fehlt oder wurde abgelehnt", "HmIP-RF is down: the radio module is missing or was rejected"},
	"hmip-local-swap":         {"HmIP-RF läuft ohne den Schlüssel des HmIP-Netzes", "HmIP-RF runs without the HmIP network's key"},
	"hmip-module-missing":     {"HmIP-RF ist ausgefallen: das Funkmodul des HmIP-Netzes fehlt", "HmIP-RF is down: the radio module that holds the HmIP network is missing"},
	"hmip-security-counter":   {"Der HmIP-Sicherheitszähler ist zu niedrig: Geräte verweigern das System", "The HmIP security counter is too low: devices refuse the system"},
	"hmip-clock-hold":         {"HmIP-RF wartet auf eine vertrauenswürdige Uhrzeit", "HmIP-RF is waiting for a trusted clock"},
	"hmip-local-key":          {"HmIP: der eingegebene lokale Schlüssel passt vermutlich nicht", "HmIP: the local key entered is probably not the network's"},
	"hmip-key-declined":       {"HmIP: ein Geräteschlüssel wurde beim Anlernen abgelehnt", "HmIP: a device key was declined at pairing"},
	"hmip-pairing-failed":     {"HmIP: ein Anlernen ist fehlgeschlagen", "HmIP: a pairing failed"},
	"hmip-port-open":          {"Der HmIP-Port {port} ist aus dem Netz erreichbar", "The HmIP port {port} is reachable from the network"},
	"rpc-stalled":             {"Ein Schnittstellenprozess hängt: {interface}", "An interface process is stalled: {interface}"},
	"catalog-check":           {"Der Addon-Katalog konnte wiederholt nicht geladen werden", "The addon catalogue could not be loaded repeatedly"},
	"addon-update":            {"Addon-Update verfügbar: {list}", "Addon update available: {list}"},
	"device-update-ready":     {"Geräte-Firmware bereit zur Installation: {list}", "Device firmware ready to install: {list}"},
	"system-update-available": {"Systemupdate verfügbar: {version}", "System update available: {version}"},
	"radio-firmware":          {"Neuere Firmware für das Funkmodul {device} liegt bereit ({newest})", "Newer firmware for the radio module {device} is on the system ({newest})"},
	"radio-module-unusable":   {"Das Funkmodul {device} ist nicht nutzbar", "The radio module {device} is not usable"},
	"radio-load":              {"Die Funklast ist hoch ({variant})", "The radio load is high ({variant})"},
	"radio-writes":            {"Ein Programm sendet sehr viele Funkbefehle: {client}", "A program sends very many radio commands: {client}"},
	"radio-writes-duty-cycle": {"Ein Programm treibt den Duty Cycle hoch: {client}", "A program drives the duty cycle up: {client}"},
	"hb-rf-eth":               {"Das HB-RF-ETH unter {address} hat die Verbindung verloren", "The HB-RF-ETH at {address} lost its connection"},
	"firewall":                {"Die Firewall-Regeln sind nicht geladen ({variant})", "The firewall rules are not loaded ({variant})"},
	"classic-rpc-open":        {"Klassisches RPC ist ohne Anmeldung offen", "Classic RPC is open without authentication"},
	"devices-import":          {"Importierte HmIP-Geräte warten auf ein Funkmodul", "Imported HmIP devices are waiting for a radio module"},
	"journal-target":          {"Der Speicherort des Journals fehlt: es liegt im RAM", "The journal's location is missing: it is kept in RAM"},
	"journal-ram":             {"Das Journal liegt nur im RAM", "The journal is kept in RAM only"},
	"journal-sync":            {"Die letzte Kopie des Journals ist fehlgeschlagen", "The last copy of the journal failed"},
	"store-target":            {"Der USB-Stick für die Datenbank fehlt oder die Kopie ist fehlgeschlagen", "The USB stick for the database is missing or the copy failed"},
	"legacy-session":          {"Addon bekommt die Sitzung in der URL: {list}", "Addon receives the session in its URL: {list}"},
	"console-reset":           {"Ein Zugang wurde an der Konsole zurückgesetzt", "Access was reset on the console"},
	"bootloader-update":       {"Ein Bootloader-Update liegt bereit", "A bootloader update is available"},
}

// SeverityName names a warning's severity.
func SeverityName(lang, sev string) string {
	switch sev {
	case "error":
		return pick(lang, "Fehler", "Error")
	case "warning":
		return pick(lang, "Warnung", "Warning")
	case "info":
		return pick(lang, "Hinweis", "Notice")
	}
	return sev
}

func text(v any, lang string) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		for _, l := range []string{Lang(lang), "en", "de"} {
			if s, ok := x[l].(string); ok && s != "" {
				return s
			}
		}
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

// listOf joins the names of the first list of things the params carry.
func listOf(params map[string]any) string {
	for _, k := range []string{"addons", "units", "updates", "disks", "devices"} {
		l, ok := params[k].([]any)
		if !ok || len(l) == 0 {
			continue
		}
		var names []string
		for _, e := range l {
			m, ok := e.(map[string]any)
			if !ok {
				if s, ok := e.(string); ok {
					names = append(names, s)
				}
				continue
			}
			for _, f := range []string{"name", "unit", "model", "id", "address", "device"} {
				if s, ok := m[f].(string); ok && s != "" {
					names = append(names, s)
					break
				}
			}
		}
		if len(names) > 0 {
			return strings.Join(names, ", ")
		}
	}
	return ""
}

// Warning answers the title and, where the warning brings one, the text of a Status-page
// warning. An addon's notice carries its own words; a warning this version does not know is
// named by its id.
func Warning(lang, id, variant string, params map[string]any) (title, detail string) {
	if id == "addon-notice" {
		title = text(params["title"], lang)
		if a, _ := params["addon"].(string); a != "" && title != "" {
			if n, _ := params["addon_name"].(string); n != "" {
				a = n
			}
			title = a + ": " + title
		}
		if title == "" {
			title = variant
		}
		return title, text(params["text"], lang)
	}
	t, ok := warningTitle[id]
	if !ok {
		s := id
		if variant != "" && variant != "plain" {
			s += " (" + variant + ")"
		}
		return s, ""
	}
	s := pick(lang, t[0], t[1])
	fill := func(name string) string {
		switch name {
		case "list":
			if l := listOf(params); l != "" {
				return l
			}
			return variant
		case "variant":
			return variant
		}
		return text(params[name], lang)
	}
	for {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			break
		}
		s = s[:i] + "\x00" + fill(s[i+1:i+j]) + "\x01" + s[i+j+1:]
	}
	// a placeholder that stayed empty takes its brackets and separators with it
	for _, empty := range []string{" (\x00\x01)", ": \x00\x01", " \x00\x01", "\x00\x01"} {
		s = strings.ReplaceAll(s, empty, "")
	}
	s = strings.NewReplacer("\x00", "", "\x01", "").Replace(s)
	return strings.TrimSpace(s), ""
}

// KnownWarnings lists the ids with a title of their own, sorted (the page's filter offers them).
func KnownWarnings() []string {
	out := make([]string, 0, len(warningTitle))
	for id := range warningTitle {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// The system events.
const (
	SysUpdateAvailable = "update-available"
	SysUpdateInstalled = "update-installed"
	SysBackupDone      = "backup-done"
	SysBackupFailed    = "backup-failed"
	SysReboot          = "reboot"
)

// SystemKinds lists the system events.
var SystemKinds = []string{SysUpdateAvailable, SysUpdateInstalled, SysBackupDone, SysBackupFailed, SysReboot}

// SystemKindName names a system event.
func SystemKindName(lang, kind string) string {
	m := map[string][2]string{
		SysUpdateAvailable: {"Systemupdate verfügbar", "System update available"},
		SysUpdateInstalled: {"Systemupdate installiert", "System update installed"},
		SysBackupDone:      {"Sicherung erstellt", "Backup made"},
		SysBackupFailed:    {"Sicherung fehlgeschlagen", "Backup failed"},
		SysReboot:          {"System neu gestartet", "System restarted"},
	}
	if t, ok := m[kind]; ok {
		return pick(lang, t[0], t[1])
	}
	return kind
}

// Resolved marks a line as over.
func Resolved(lang, line string) string {
	return pick(lang, "Behoben: ", "Resolved: ") + line
}

// Headline is the title of a message with several items.
func Headline(lang, kind string, fresh, resolved int) string {
	n := fresh + resolved
	var de, en string
	switch kind {
	case "warning":
		de, en = "Warnungen", "warnings"
	case "service":
		de, en = "Servicemeldungen", "service messages"
	case "datapoint":
		de, en = "Meldungen", "alerts"
	case "system":
		de, en = "Systemereignisse", "system events"
	default:
		de, en = "Meldungen", "messages"
	}
	switch {
	case resolved == 0:
		return fmt.Sprintf("%d %s", n, pick(lang, de, en))
	case fresh == 0:
		return fmt.Sprintf("%d %s %s", n, pick(lang, de, en), pick(lang, "behoben", "resolved"))
	}
	return fmt.Sprintf("%d %s (%d %s, %d %s)", n, pick(lang, de, en), fresh, pick(lang, "neu", "new"), resolved, pick(lang, "behoben", "resolved"))
}

// Since says since when, in the system's time zone.
func Since(lang string, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return pick(lang, "seit ", "since ") + Clock(lang, t)
}

// Clock formats a time of day with its date.
func Clock(lang string, t time.Time) string {
	if Lang(lang) == "en" {
		return t.Local().Format("2 Jan 15:04")
	}
	return t.Local().Format("02.01. 15:04")
}

// T answers one of the fixed sentences.
func T(lang, key string, args ...any) string {
	t, ok := sentences[key]
	if !ok {
		return key
	}
	s := pick(lang, t[0], t[1])
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

var sentences = map[string][2]string{
	"status-page":          {"Details auf der Status-Seite des Systems.", "Details on the system's Status page."},
	"room":                 {"Raum: %s", "Room: %s"},
	"test-title":           {"Testnachricht", "Test message"},
	"test-body":            {"Dieser Kanal funktioniert: lite-addon-notify auf %s kann hierhin senden.", "This channel works: lite-addon-notify on %s can send here."},
	"digest-clear":         {"Alles in Ordnung", "All clear"},
	"digest-clear-body":    {"Keine offenen Warnungen, keine Servicemeldungen.", "No open warnings, no service messages."},
	"digest-open":          {"Tagesbericht: %d offen", "Daily digest: %d open"},
	"digest-warnings":      {"Warnungen:", "Warnings:"},
	"digest-service":       {"Servicemeldungen:", "Service messages:"},
	"digest-datapoints":    {"Datenpunkt-Regeln:", "Datapoint rules:"},
	"held-back":            {"(%d weitere Nachrichten wurden vom Mengenlimit zurückgehalten.)", "(%d more messages were held back by the rate limit.)"},
	"dp-true":              {"%s: %s", "%s: %s"},
	"dp-for":               {"seit %s", "for %s"},
	"sys-update-available": {"Systemupdate verfügbar: %s", "System update available: %s"},
	"sys-update-installed": {"Systemupdate installiert: %s", "System update installed: %s"},
	"sys-update-from":      {"Vorher: %s", "Before: %s"},
	"sys-backup-done":      {"Sicherung erstellt", "Backup made"},
	"sys-backup-failed":    {"Sicherung fehlgeschlagen", "Backup failed"},
	"sys-reboot":           {"Das System wurde neu gestartet", "The system was restarted"},
	"sys-running":          {"Läuft mit %s.", "Running %s."},
	"channel-failing":      {"Kanal %s: Zustellung fehlgeschlagen", "Channel %s: delivery failed"},
	"default-warnings":     {"Warnungen der Status-Seite", "Status page warnings"},
	"default-service":      {"Servicemeldungen der Geräte", "Device service messages"},
	"default-system":       {"Systemereignisse", "System events"},
	"default-digest":       {"Tagesbericht", "Daily digest"},
	"default-inbound":      {"Eingehende Nachrichten (Webhook)", "Inbound messages (webhook)"},
}

// Duration says a number of seconds in words.
func Duration(lang string, s int) string {
	switch {
	case s < 120:
		return fmt.Sprintf("%d s", s)
	case s < 7200:
		return fmt.Sprintf("%d min", s/60)
	}
	return fmt.Sprintf("%d h", s/3600)
}
