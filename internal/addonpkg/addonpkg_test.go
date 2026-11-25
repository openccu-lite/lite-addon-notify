// Package addonpkg tests the package files the addon ships.
package addonpkg

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/hobbyquaker/ccu-addon-kit/go/addonkit/fragment"
	"github.com/hobbyquaker/ccu-addon-kit/go/addonkit/manifest"
)

// The manifest as the system validates it at install, through the kit's strict load.
func TestManifest(t *testing.T) {
	m, err := manifest.Load("../../addon/openccu-lite.json")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "notify" || m.Undeclared() || m.Runtime == nil || !m.Runtime.Daemon {
		t.Fatalf("manifest: %+v", m)
	}
	if len(m.Runtime.Capabilities) != 0 || len(m.Runtime.Ports) != 0 {
		t.Errorf("the addon needs no capability and no port: %+v", m.Runtime)
	}
	// read-only scopes, and exactly the three the addon uses
	if strings.Join(m.Runtime.APIScopes, ",") != "system:read,rpc:read,meta:read" {
		t.Errorf("scopes %v", m.Runtime.APIScopes)
	}
	for _, s := range m.Runtime.APIScopes {
		if !strings.HasSuffix(s, ":read") {
			t.Errorf("%s is not a read scope", s)
		}
	}
	if m.Backup != nil {
		t.Error("no backup block: the configuration is one file in the system's backup, the state is not worth one")
	}
	if m.Description["de"] == "" || m.Description["en"] == "" || m.Runtime.Technical["de"] == "" || m.Runtime.Technical["en"] == "" ||
		m.Runtime.Note["de"] == "" || m.Runtime.Note["en"] == "" {
		t.Error("the description, the note and the technical text in both languages")
	}
	for _, scope := range m.Runtime.APIScopes {
		if !strings.Contains(m.Runtime.Technical["en"], scope) || !strings.Contains(m.Runtime.Technical["de"], scope) {
			t.Errorf("the technical text does not explain %s", scope)
		}
	}
	for _, text := range []string{m.Description["de"], m.Description["en"], m.Runtime.Technical["de"], m.Runtime.Note["de"], m.Runtime.Technical["en"], m.Runtime.Note["en"]} {
		if strings.Contains(strings.ToLower(text), " box") {
			t.Error("user-facing text says \"the system\"")
		}
	}
}

// The manifest's version is the changelog's newest.
func TestVersionInChangelog(t *testing.T) {
	var m struct {
		Version string `json:"version"`
	}
	b, _ := os.ReadFile("../../addon/openccu-lite.json")
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	cl, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cl), "## ["+m.Version+"]") {
		t.Errorf("CHANGELOG.md has no entry for %s", m.Version)
	}
}

// The manifest's images are tracked at the path the catalogue reads them from.
func TestImagesTracked(t *testing.T) {
	for _, p := range []string{"addon/notify/www/icon.svg", "addon/notify/www/icon-dark.svg", "addon/notify/etc/lighttpd.conf", "addon/notify/www/lighttpd-error-503.html"} {
		if out, err := exec.Command("git", "-C", "../..", "ls-files", "--error-unmatch", "--", p).CombinedOutput(); err != nil {
			t.Errorf("%s not tracked: %v %s", p, err, out)
		}
	}
}

// The proxy rule is what the kit's renderer writes (apart from the comment lines).
func TestFragment(t *testing.T) {
	want, err := fragment.Render(fragment.Fragment{ID: "notify", Port: 8300, ErrorfilePrefix: "/usr/local/addons/notify/www/lighttpd-error-"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile("../../addon/notify/etc/lighttpd.conf")
	strip := func(s string) string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if !strings.HasPrefix(l, "#") && strings.TrimSpace(l) != "" {
				out = append(out, l)
			}
		}
		return strings.Join(out, "\n")
	}
	if strip(string(got)) != strip(want) {
		t.Errorf("etc/lighttpd.conf:\n%s\nwant:\n%s", got, want)
	}
}

// The 503 page names the addon, in the tracked copy and the one the build copies.
func TestErrorPage(t *testing.T) {
	for _, p := range []string{"../../addon/static/lighttpd-error-503.html", "../../addon/notify/www/lighttpd-error-503.html"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "<title>Benachrichtigungen / Notifications</title>") {
			t.Errorf("%s does not name the addon", p)
		}
	}
}

// The rc.d script and the update_script name no other addon and the daemon's flags are right.
func TestScripts(t *testing.T) {
	for _, p := range []string{"../../addon/notify/rc.d", "../../addon/update_script"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "nut") || strings.Contains(string(b), "UPS") || strings.Contains(string(b), "forwarder") {
			t.Errorf("%s names another addon", p)
		}
		if !strings.HasPrefix(string(b), "#!/bin/sh\n") {
			t.Errorf("%s is not a sh script", p)
		}
	}
	rc, _ := os.ReadFile("../../addon/notify/rc.d")
	if !strings.Contains(string(rc), "ADDON=notify\n") || !strings.Contains(string(rc), "Config-Url: /addons/$ADDON/") {
		t.Error("rc.d: the addon id or the Config-Url")
	}
}
