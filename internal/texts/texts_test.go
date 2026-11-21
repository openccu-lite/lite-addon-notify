package texts

import (
	"regexp"
	"strings"
	"testing"
)

func TestWarningTitles(t *testing.T) {
	tests := []struct {
		id, variant string
		params      map[string]any
		de, en      string
	}{
		{"addon-failed", "mosquitto,redmatic", map[string]any{"addons": []any{map[string]any{"id": "mosquitto", "name": "Mosquitto"}, map[string]any{"id": "redmatic"}}}, "Addon fehlgeschlagen: Mosquitto, redmatic", "Addon failed: Mosquitto, redmatic"},
		{"crash-loop", "rfd", map[string]any{"units": []any{map[string]any{"unit": "rfd", "restarts": 3.0}}}, "Dienst scheitert und startet immer wieder neu: rfd", "Service keeps failing and restarting: rfd"},
		{"crash-loop", "rfd,hs485d", nil, "Dienst scheitert und startet immer wieder neu: rfd,hs485d", "Service keeps failing and restarting: rfd,hs485d"},
		{"backup-delivery", "usb:no-medium", map[string]any{"name": "USB", "cause": "no-medium"}, "Das Sicherungsziel USB funktioniert nicht (no-medium)", "The backup target USB does not work (no-medium)"},
		{"backup-age", "warning", map[string]any{"days": 4.0}, "Seit 4 Tagen keine Sicherung", "No backup for 4 days"},
		{"backup-unencrypted", "plain", nil, "Die Sicherung ist nicht verschlüsselt", "Backups are not encrypted"},
		{"system-update-available", "2.0.0", map[string]any{"version": "2.0.0"}, "Systemupdate verfügbar: 2.0.0", "System update available: 2.0.0"},
		{"hmip-port-open", "39292", nil, "Der HmIP-Port ist aus dem Netz erreichbar", "The HmIP port is reachable from the network"},
		{"app-public", "gast", nil, "Die Bedienung ist öffentlich: ohne Anmeldung, als", "Control is public: without a login, as"},
		{"something-new", "x", nil, "something-new (x)", "something-new (x)"},
		{"something-new", "plain", nil, "something-new", "something-new"},
	}
	for _, tt := range tests {
		if got, _ := Warning("de", tt.id, tt.variant, tt.params); got != tt.de {
			t.Errorf("%s de: %q", tt.id, got)
		}
		if got, _ := Warning("en", tt.id, tt.variant, tt.params); got != tt.en {
			t.Errorf("%s en: %q", tt.id, got)
		}
	}
	// an addon's notice brings its own words
	title, detail := Warning("de", "addon-notice", "battery/due", map[string]any{"addon": "battery", "title": map[string]any{"de": "Batterien bald leer", "en": "Batteries due"}, "text": map[string]any{"de": "3 Geräte", "en": "3 devices"}})
	if title != "battery: Batterien bald leer" || detail != "3 Geräte" {
		t.Errorf("notice: %q %q", title, detail)
	}
	if title, _ := Warning("en", "addon-notice", "battery/due", nil); title != "battery/due" {
		t.Errorf("a notice without words: %q", title)
	}
}

// Every title in both languages, no placeholder left behind for a warning without params, and
// never "the box".
func TestEveryText(t *testing.T) {
	left := regexp.MustCompile(`[{}\x00\x01]`)
	box := regexp.MustCompile(`(?i)\bbox\b`)
	for id, tt := range warningTitle {
		if tt[0] == "" || tt[1] == "" {
			t.Errorf("%s lacks a language", id)
		}
		for _, lang := range []string{"de", "en"} {
			got, _ := Warning(lang, id, "v", nil)
			if left.MatchString(got) || strings.TrimSpace(got) == "" || strings.HasSuffix(got, ":") || strings.Contains(got, "  ") {
				t.Errorf("%s %s without params: %q", id, lang, got)
			}
			if box.MatchString(got) {
				t.Errorf("%s says box", id)
			}
		}
	}
	for k, tt := range sentences {
		if tt[0] == "" || tt[1] == "" || box.MatchString(tt[0]+tt[1]) {
			t.Errorf("sentence %s", k)
		}
	}
	for k, tt := range serviceText {
		if tt[0] == "" || tt[1] == "" {
			t.Errorf("service %s", k)
		}
	}
	for _, k := range ServiceKinds {
		if ServiceKindName("de", k) == k || ServiceKindName("en", k) == k {
			t.Errorf("kind %s has no name", k)
		}
	}
	for _, k := range SystemKinds {
		if SystemKindName("de", k) == k || SystemKindName("en", k) == k {
			t.Errorf("system event %s has no name", k)
		}
	}
}

func TestService(t *testing.T) {
	kinds := map[string]string{"UNREACH": "unreach", "STICKY_UNREACH": "sticky", "LOWBAT": "lowbat", "LOW_BAT": "lowbat", "CONFIG_PENDING": "config", "UPDATE_PENDING": "update", "SABOTAGE": "fault", "STICKY_SABOTAGE": "fault", "ERROR_OVERHEAT": "fault", "FAULT_REPORTING": "fault", "DUTY_CYCLE": "fault", "INHIBIT": "other"}
	for k, want := range kinds {
		if got := ServiceKind(k); got != want {
			t.Errorf("%s: %s", k, got)
		}
	}
	if got := Service("de", "UNREACH", true); got != "Kommunikation gestört" {
		t.Error(got)
	}
	if got := Service("en", "ERROR_CODE", 7.0); got != "error (7)" {
		t.Error(got)
	}
	if got := Service("en", "SOMETHING_NEW", true); got != "SOMETHING_NEW" {
		t.Error(got)
	}
	if got := Headline("de", "service", 2, 1); got != "3 Servicemeldungen (2 neu, 1 behoben)" {
		t.Error(got)
	}
	if got := Headline("en", "warning", 0, 2); got != "2 warnings resolved" {
		t.Error(got)
	}
	if Lang("fr") != "de" || Lang("en") != "en" {
		t.Error("Lang")
	}
}
