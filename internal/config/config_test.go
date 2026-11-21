package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "conf")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestFreshConfiguration(t *testing.T) {
	s, dir := open(t)
	c := s.Get()
	if len(c.Channels) != 0 || len(c.Rules) != 4 || c.Settings.Language != "de" || len(c.Settings.Inbound.Secret) != 32 || c.Settings.Inbound.Enabled {
		t.Fatalf("fresh: %+v", c)
	}
	for _, r := range c.Rules {
		if _, err := NormalizeRule(r, nil); err != nil {
			t.Errorf("default rule %s: %v", r.ID, err)
		}
		if r.Source == SrcDigest && r.Enabled {
			t.Error("the digest starts switched off")
		}
	}
	// the file is the addon's alone
	if fi, err := os.Stat(filepath.Join(dir, File)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode: %v %v", fi.Mode(), err)
	}
	if di, _ := os.Stat(dir); di.Mode().Perm() != 0o700 {
		t.Errorf("directory mode: %v", di.Mode())
	}
	// it reads back the same, with the same secret
	s2, err := Open(dir)
	if err != nil || s2.Get().Settings.Inbound.Secret != c.Settings.Inbound.Secret {
		t.Errorf("reopen: %v", err)
	}
}

func TestSecretsAreNeverAnswered(t *testing.T) {
	s, _ := open(t)
	changed := 0
	s.OnChange = func() { changed++ }
	in := []Channel{
		{Name: "Push", Type: Pushover, Enabled: true, Token: "apptoken-secret", UserKey: "userkey-secret"},
		{Name: "Mail", Type: Email, Enabled: true, Host: "smtp.example.org", User: "me", Password: "mail-secret", Sender: "ccu@example.org", Target: "a@example.org"},
		{Name: "Chat", Type: Discord, Enabled: true, URL: "https://discord.com/api/webhooks/1/hook-secret"},
		{Name: "Hook", Type: Webhook, Enabled: true, URL: "https://example.org/hook", Headers: map[string]string{"Authorization": "Bearer header-secret"}},
	}
	for _, ch := range in {
		if _, err := s.PutChannel(ch); err != nil {
			t.Fatalf("%s: %v", ch.Name, err)
		}
	}
	if changed != 4 {
		t.Errorf("OnChange: %d", changed)
	}
	pub, _ := json.Marshal(s.Public())
	for _, secret := range []string{"apptoken-secret", "userkey-secret", "mail-secret", "hook-secret", "header-secret", s.Get().Settings.Inbound.Secret} {
		if strings.Contains(string(pub), secret) {
			t.Errorf("the public view carries %s", secret)
		}
	}
	p := s.Public()
	if !p.Channels[0].Secrets["token"] || !p.Channels[0].Secrets["user_key"] || !p.Channels[1].Secrets["password"] || !p.Channels[2].Secrets["url"] || !p.Channels[3].Secrets["headers"] {
		t.Errorf("secrets flags: %+v", p.Channels)
	}
	if p.Channels[3].URL != "https://example.org/hook" || p.Channels[1].Port != 587 || p.Channels[1].Security != "starttls" {
		t.Errorf("what is no secret stays, defaults are filled: %+v", p.Channels)
	}
	// a save of the public view keeps every secret
	for _, ch := range p.Channels {
		ch.Name += " 2"
		if _, err := s.PutChannel(ch); err != nil {
			t.Fatalf("%s: %v", ch.Name, err)
		}
	}
	c := s.Get()
	if c.Channels[0].Token != "apptoken-secret" || c.Channels[0].UserKey != "userkey-secret" || c.Channels[1].Password != "mail-secret" || !strings.HasSuffix(c.Channels[2].URL, "hook-secret") || c.Channels[3].Headers["Authorization"] != "Bearer header-secret" || c.Channels[0].Name != "Push 2" || c.Channels[0].ID != "push" {
		t.Errorf("after a save of the public view: %+v", c.Channels)
	}
	// a new value replaces, Clear removes
	mail := p.Channels[1]
	mail.Password = "new"
	if _, err := s.PutChannel(mail); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Channel("mail"); got.Password != "new" {
		t.Errorf("replace: %+v", got)
	}
	mail.Password, mail.Clear = "", []string{"password"}
	if _, err := s.PutChannel(mail); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Channel("mail"); got.Password != "" {
		t.Errorf("clear: %+v", got)
	}
}

func TestChannelValidation(t *testing.T) {
	bad := []struct {
		ch    Channel
		field string
	}{
		{Channel{Name: "x", Type: "fax"}, "type"},
		{Channel{Type: Ntfy, Target: "t"}, "name"},
		{Channel{Name: "x", Type: Pushover, Token: "t"}, "user_key"},
		{Channel{Name: "x", Type: Telegram, Token: "t"}, "target"},
		{Channel{Name: "x", Type: Email, Host: "h", Sender: "nobody", Target: "a@b"}, "sender"},
		{Channel{Name: "x", Type: Email, Host: "h", Sender: "a@b", Target: "nobody"}, "target"},
		{Channel{Name: "x", Type: Email, Host: "h", Sender: "a@b", Target: "a@b", Security: "ssl"}, "security"},
		{Channel{Name: "x", Type: Ntfy, URL: "ntfy.sh", Target: "t"}, "url"},
		{Channel{Name: "x", Type: Ntfy, Target: "a/b"}, "target"},
		{Channel{Name: "x", Type: Gotify, URL: "https://g"}, "token"},
		{Channel{Name: "x", Type: Webhook, URL: "https://h", Method: "GET"}, "method"},
		{Channel{Name: "x", Type: Webhook, URL: "https://h", Body: "{{ x "}, "body"},
		{Channel{Name: "x", Type: Webhook, URL: "https://h", Headers: map[string]string{"X Y": "v"}}, "headers"},
		{Channel{Name: "x", Type: Slack}, "url"},
		{Channel{Name: "x", Type: Signal, URL: "http://s", Sender: "+49"}, "target"},
		{Channel{Name: "x", Type: MQTT, QoS: 2}, "qos"},
		{Channel{Name: "x", Type: MQTT, Target: "{{ x "}, "target"},
		{Channel{Name: "x", Type: MQTT, Port: 70000}, "port"},
	}
	for _, tt := range bad {
		_, err := NormalizeChannel(tt.ch)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Field != tt.field {
			t.Errorf("%+v: %v, want field %s", tt.ch, err, tt.field)
		}
	}
	// defaults: ntfy.sh, the local broker, the topic
	n, err := NormalizeChannel(Channel{Name: "n", Type: Ntfy, Target: "t"})
	if err != nil || n.URL != "https://ntfy.sh" {
		t.Errorf("ntfy: %+v %v", n, err)
	}
	m, err := NormalizeChannel(Channel{Name: "m", Type: MQTT, Token: "dropped"})
	if err != nil || m.Host != "127.0.0.1" || m.Port != 1883 || m.Target != "notify/{{ kind }}" || m.Token != "" {
		t.Errorf("mqtt: %+v %v", m, err)
	}
	for _, typ := range Types {
		if Fields(typ) == nil {
			t.Errorf("%s has no fields", typ)
		}
	}
}

func TestRules(t *testing.T) {
	s, _ := open(t)
	ch, _ := s.PutChannel(Channel{Name: "n", Type: Ntfy, Target: "t", Enabled: true})
	bad := []struct {
		r     Rule
		field string
	}{
		{Rule{Source: SrcWarning, Severities: []string{"error"}}, "name"},
		{Rule{Name: "x", Source: "weather"}, "source"},
		{Rule{Name: "x", Source: SrcWarning}, "severities"},
		{Rule{Name: "x", Source: SrcWarning, Severities: []string{"fatal"}}, "severities"},
		{Rule{Name: "x", Source: SrcWarning, Severities: []string{"error"}, Channels: []string{"nope"}}, "channels"},
		{Rule{Name: "x", Source: SrcWarning, Severities: []string{"error"}, Priority: "urgent"}, "priority"},
		{Rule{Name: "x", Source: SrcWarning, Severities: []string{"error"}, Title: "{% if %}"}, "title"},
		{Rule{Name: "x", Source: SrcService, Kinds: []string{"smoke"}}, "kinds"},
		{Rule{Name: "x", Source: SrcService}, "kinds"},
		{Rule{Name: "x", Source: SrcDatapoint}, "datapoint"},
		{Rule{Name: "x", Source: SrcDatapoint, Datapoint: &Datapoint{Interface: "i", Address: "a", Key: "k", Op: "gt"}}, "datapoint.value"},
		{Rule{Name: "x", Source: SrcDatapoint, Datapoint: &Datapoint{Interface: "i", Address: "a", Key: "k", Op: "gt", Value: "warm"}}, "datapoint.value"},
		{Rule{Name: "x", Source: SrcDatapoint, Datapoint: &Datapoint{Interface: "i", Address: "a", Key: "k", Op: "like"}}, "datapoint.op"},
		{Rule{Name: "x", Source: SrcSystem}, "kinds"},
		{Rule{Name: "x", Source: SrcDigest, Time: "8"}, "time"},
	}
	for _, tt := range bad {
		_, err := s.PutRule(tt.r)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Field != tt.field {
			t.Errorf("%+v: %v, want field %s", tt.r, err, tt.field)
		}
	}
	r, err := s.PutRule(Rule{Name: "Fenster Küche offen", Enabled: true, Source: SrcDatapoint, Channels: []string{ch.ID, ch.ID}, Datapoint: &Datapoint{Interface: "HmIP-RF", Address: "A:1", Key: "STATE", Op: "true", Value: "ignored", ForS: 600}, Severities: []string{"error"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "fenster-kueche-offen" || r.Priority != "normal" || len(r.Channels) != 1 || r.Datapoint.Value != "" || r.Severities != nil {
		t.Errorf("rule: %+v", r)
	}
	// a second one of the same name gets its own id
	r2, _ := s.PutRule(Rule{Name: "Fenster Küche offen", Source: SrcInbound})
	if r2.ID != "fenster-kueche-offen-2" {
		t.Errorf("id: %s", r2.ID)
	}
	// removing the channel takes it out of the rule, and a rule that named only it is switched
	// off rather than turned into "every channel"
	if err := s.DeleteChannel(ch.ID); err != nil {
		t.Fatal(err)
	}
	for _, x := range s.Get().Rules {
		if x.ID == r.ID && (len(x.Channels) != 0 || x.Enabled) {
			t.Errorf("after the channel went: %+v", x)
		}
		if x.ID == "warnings" && !x.Enabled {
			t.Error("a rule for every channel was switched off")
		}
	}
	if err := s.DeleteRule(r.ID); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(s.DeleteRule(r.ID), ErrNotFound) || !errors.Is(s.DeleteChannel("nope"), ErrNotFound) {
		t.Error("not found")
	}
	if _, err := s.PutRule(Rule{ID: "nope", Name: "x", Source: SrcInbound}); !errors.Is(err, ErrNotFound) {
		t.Errorf("put of an unknown id: %v", err)
	}
}

func TestSettings(t *testing.T) {
	s, _ := open(t)
	secret := s.Get().Settings.Inbound.Secret
	st, err := s.PutSettings(Settings{Language: "en", BaseURL: "https://ccu.example/", Quiet: Quiet{Enabled: true, From: "22:00", To: "07:00"}, RepeatMin: 30, RatePerHour: 10, Inbound: Inbound{Enabled: true, Secret: "mine"}})
	if err != nil {
		t.Fatal(err)
	}
	if st.BaseURL != "https://ccu.example" || s.Get().Settings.Inbound.Secret != secret || !s.Get().Settings.Inbound.Enabled {
		t.Errorf("settings: %+v", s.Get().Settings)
	}
	for _, bad := range []Settings{{BaseURL: "ccu"}, {Quiet: Quiet{From: "25:00", To: "07:00"}}, {Quiet: Quiet{Enabled: true, From: "07:00", To: "07:00"}}, {RepeatMin: -1}, {RatePerHour: 99999}} {
		if _, err := s.PutSettings(bad); err == nil {
			t.Errorf("accepted: %+v", bad)
		}
	}
	n, err := s.RotateSecret()
	if err != nil || n == secret || len(n) != 32 || s.Get().Settings.Inbound.Secret != n {
		t.Errorf("rotate: %s %v", n, err)
	}
	if f, ok := Number("21,5"); !ok || f != 21.5 {
		t.Error("a comma is the decimal sign")
	}
	if _, ok := Number("5x"); ok {
		t.Error("5x is a number")
	}
}
