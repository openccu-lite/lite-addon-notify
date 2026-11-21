package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openccu-lite/lite-addon-notify/internal/config"
	"github.com/openccu-lite/lite-addon-notify/internal/message"
)

// fake is the test's clock and configuration.
type fake struct {
	t   *testing.T
	now time.Time
	c   config.Config
	e   *Engine
	out []Outgoing
}

var day = time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)

func newFake(t *testing.T, rules ...config.Rule) *fake {
	f := &fake{t: t, now: day}
	f.c = config.Config{Settings: config.DefaultSettings, Channels: []config.Channel{{ID: "push", Name: "Push", Type: config.Ntfy, Enabled: true}, {ID: "mail", Name: "Mail", Type: config.Email, Enabled: true}, {ID: "off", Name: "Off", Type: config.Ntfy}}, Rules: rules}
	f.c.Settings.Language = "en"
	f.e = New(Options{Config: func() config.Config { return f.c }, System: func() string { return "ccu" },
		Describe: func(iface, address, lang string) (string, []string) { return "Kitchen window", []string{"Kitchen"} },
		Open: func(string) Open {
			return Open{Warnings: []message.Item{{Key: "w", Line: "Backups are not encrypted"}}, Service: []message.Item{{Key: "s", Line: "Radiator: battery low"}}}
		}})
	return f
}

// run moves the clock forward, calling Tick whenever the engine wants it.
func (f *fake) run(d time.Duration) {
	f.t.Helper()
	end := f.now.Add(d)
	for i := 0; i < 1000; i++ {
		f.out = append(f.out, f.e.Tick(f.now)...)
		next := f.e.Next(f.now)
		if next.IsZero() || next.After(end) {
			break
		}
		if !next.After(f.now) {
			next = f.now.Add(time.Millisecond)
		}
		f.now = next
	}
	f.now = end
	f.out = append(f.out, f.e.Tick(f.now)...)
}

func (f *fake) take() []Outgoing {
	o := f.out
	f.out = nil
	return o
}

func (f *fake) service(key, device, text string, resolved bool) {
	f.e.Handle(Event{Source: config.SrcService, Key: key, Resolved: resolved, ServiceKind: "unreach", Address: key, Item: message.Item{Title: device, Text: text}}, f.now)
}

func serviceRule() config.Rule {
	return config.Rule{ID: "svc", Name: "Service", Enabled: true, Source: config.SrcService, Channels: []string{}, Priority: message.Normal, Resolved: true, BatchS: 60, Kinds: []string{"unreach", "lowbat"}}
}

func TestBatchAndResolved(t *testing.T) {
	f := newFake(t, serviceRule())
	f.service("A", "Radiator", "communication disturbed", false)
	f.run(30 * time.Second)
	f.service("B", "Window", "communication disturbed", false)
	if len(f.take()) != 0 {
		t.Fatal("sent before the window closed")
	}
	f.run(31 * time.Second)
	out := f.take()
	if len(out) != 1 {
		t.Fatalf("want one message, got %d", len(out))
	}
	m := out[0].Message
	if m.Title != "2 service messages" || m.Body != "Radiator: communication disturbed\nWindow: communication disturbed" || m.Count != 2 || m.Resolved {
		t.Errorf("message: %+v", m)
	}
	if strings.Join(out[0].Channels, ",") != "push,mail" {
		t.Errorf("channels: %v (a disabled channel is left out, an empty list is every enabled one)", out[0].Channels)
	}
	// resolved: one message, low key
	f.service("A", "Radiator", "communication disturbed", true)
	f.run(61 * time.Second)
	out = f.take()
	if len(out) != 1 || out[0].Message.Title != "Resolved: Radiator: communication disturbed" || !out[0].Message.Resolved {
		t.Fatalf("resolved: %+v", out)
	}
	// a resolved of something never announced says nothing
	f.service("Z", "Unknown", "x", true)
	f.run(2 * time.Minute)
	if len(f.take()) != 0 {
		t.Error("a resolved without its new was sent")
	}
}

func TestFlapIsSilence(t *testing.T) {
	r := serviceRule()
	r.HoldS = 120
	f := newFake(t, r)
	f.service("A", "Radiator", "communication disturbed", false)
	f.run(90 * time.Second)
	f.service("A", "Radiator", "communication disturbed", true)
	f.run(10 * time.Minute)
	if out := f.take(); len(out) != 0 {
		t.Fatalf("a flap within the hold time was sent: %+v", out)
	}
	// one that stays is sent after hold + batch
	f.service("A", "Radiator", "communication disturbed", false)
	f.run(179 * time.Second)
	if len(f.take()) != 0 {
		t.Fatal("sent before hold and batch were over")
	}
	f.run(2 * time.Second)
	if len(f.take()) != 1 {
		t.Fatal("not sent after hold and batch")
	}
}

func TestRepeatSuppression(t *testing.T) {
	r := serviceRule()
	r.BatchS = 0
	f := newFake(t, r)
	f.c.Settings.RepeatMin = 60
	f.service("A", "Radiator", "x", false)
	f.run(time.Second)
	f.service("A", "Radiator", "x", true)
	f.run(time.Second)
	if n := len(f.take()); n != 2 {
		t.Fatalf("new and resolved: %d", n)
	}
	// again within the hour: held; over before the hour ends: never said
	f.run(10 * time.Minute)
	f.service("A", "Radiator", "x", false)
	f.run(10 * time.Minute)
	f.service("A", "Radiator", "x", true)
	f.run(2 * time.Hour)
	if out := f.take(); len(out) != 0 {
		t.Fatalf("a repeat within the window was sent: %+v", out)
	}
	// again within the hour of the last announcement and it stays: said when the window ends
	f.service("A", "Radiator", "x", false)
	f.run(time.Second)
	if n := len(f.take()); n != 1 {
		t.Fatalf("after the window: %d", n)
	}
	f.service("A", "Radiator", "x", true)
	f.run(time.Minute)
	f.take()
	f.service("A", "Radiator", "x", false)
	f.run(30 * time.Minute)
	if len(f.take()) != 0 {
		t.Fatal("sent inside the window")
	}
	f.run(31 * time.Minute)
	if n := len(f.take()); n != 1 {
		t.Fatalf("a subject that stays is announced when its window ends: %d", n)
	}
}

func TestQuietHours(t *testing.T) {
	r := serviceRule()
	urgent := serviceRule()
	urgent.ID, urgent.IgnoreQuiet, urgent.Kinds, urgent.BatchS = "urgent", true, []string{"fault"}, 0
	f := newFake(t, r, urgent)
	f.c.Settings.Quiet = config.Quiet{Enabled: true, From: "22:00", To: "07:00"}
	f.now = time.Date(2026, 11, 21, 23, 30, 0, 0, time.Local)
	f.service("A", "Radiator", "x", false)
	f.service("B", "Window", "x", false)
	f.e.Handle(Event{Source: config.SrcService, Key: "S", ServiceKind: "fault", Item: message.Item{Title: "Siren", Text: "sabotage"}}, f.now)
	f.run(time.Hour)
	out := f.take()
	if len(out) != 1 || out[0].Rule.ID != "urgent" {
		t.Fatalf("during the quiet hours only the rule that ignores them sends: %+v", out)
	}
	// B is over before the morning: the morning's message has A alone
	f.service("B", "Window", "x", true)
	f.run(6*time.Hour + 29*time.Minute)
	if len(f.take()) != 0 {
		t.Fatal("sent before the quiet hours ended")
	}
	f.run(2 * time.Minute)
	out = f.take()
	if len(out) != 1 || out[0].Message.Title != "Radiator: x" {
		t.Fatalf("the morning's message: %+v", out)
	}
	if got := QuietEnd(config.Quiet{Enabled: true, From: "13:00", To: "15:00"}, time.Date(2026, 1, 1, 14, 0, 0, 0, time.Local)); got.Hour() != 15 {
		t.Errorf("a window within one day: %v", got)
	}
	if got := QuietEnd(f.c.Settings.Quiet, time.Date(2026, 1, 1, 3, 0, 0, 0, time.Local)); got.Hour() != 7 || got.Day() != 1 {
		t.Errorf("after midnight: %v", got)
	}
	if got := QuietEnd(f.c.Settings.Quiet, time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)); !got.IsZero() {
		t.Errorf("at noon: %v", got)
	}
}

func TestWarningFilterAndTemplates(t *testing.T) {
	r := config.Rule{ID: "w", Name: "Warnings", Enabled: true, Source: config.SrcWarning, Channels: []string{"mail"}, Priority: message.High, Resolved: false, Severities: []string{"error"}, ExcludeIDs: []string{"storage"},
		Title: "[{{ system }}] {{ headline }}", Body: "{% for i in items %}{{ i.severity }}: {{ i.title }}{% endfor %}"}
	f := newFake(t, r)
	f.c.Settings.BaseURL = "https://ccu.example"
	w := func(id, sev string, resolved bool) {
		f.e.Handle(Event{Source: config.SrcWarning, Key: id, Resolved: resolved, Severity: sev, WarningID: id, Item: message.Item{Title: "Title of " + id, Severity: sev, Link: "/backup#targets"}}, f.now)
	}
	w("journal-ram", "info", false)
	w("storage", "error", false)
	w("backup-delivery", "error", false)
	f.run(time.Second)
	out := f.take()
	if len(out) != 1 {
		t.Fatalf("filtered: %+v", out)
	}
	m := out[0].Message
	if m.Title != "[ccu] Title of backup-delivery" || m.Body != "error: Title of backup-delivery" || m.Link != "https://ccu.example/backup#targets" || m.Priority != message.High || strings.Join(out[0].Channels, ",") != "mail" {
		t.Errorf("message: %+v", m)
	}
	// the rule does not announce the end
	w("backup-delivery", "error", true)
	f.run(time.Minute)
	if len(f.take()) != 0 {
		t.Error("resolved sent although the rule does not want it")
	}
	// the title prefix applies to the default title only
	f.c.Rules[0].Title, f.c.Rules[0].Body = "", ""
	f.c.Settings.TitlePrefix = true
	w("meta", "error", false)
	f.run(time.Second)
	if out := f.take(); len(out) != 1 || out[0].Message.Title != "ccu: Title of meta" {
		t.Errorf("prefix: %+v", out)
	}
}

func TestDatapointRule(t *testing.T) {
	r := config.Rule{ID: "win", Name: "Window", Enabled: true, Source: config.SrcDatapoint, Channels: []string{}, Priority: message.Normal, Resolved: true,
		Datapoint: &config.Datapoint{Interface: "HmIP-RF", Address: "0000D3C99C6B41:1", Key: "STATE", Op: "eq", Value: "1", ForS: 600, Label: "open for 10 minutes"}}
	f := newFake(t, r)
	f.c.Settings.RepeatMin = 0
	v := func(val string) {
		f.e.Value("HmIP-RF", "0000D3C99C6B41:1", "STATE", json.RawMessage(val), f.now, f.now)
	}
	v("1")
	f.run(5 * time.Minute)
	v("0") // closed in time
	f.run(20 * time.Minute)
	if len(f.take()) != 0 {
		t.Fatal("fired although the window was closed in time")
	}
	v("1")
	f.run(9*time.Minute + 59*time.Second)
	if len(f.take()) != 0 {
		t.Fatal("fired early")
	}
	v("1") // the same value again changes nothing
	f.run(2 * time.Second)
	out := f.take()
	if len(out) != 1 {
		t.Fatalf("fire: %+v", out)
	}
	m := out[0].Message
	if m.Title != "Kitchen window: open for 10 minutes" || !strings.Contains(m.Body, "STATE = 1") || !strings.Contains(m.Body, "Kitchen") {
		t.Errorf("message: %+v", m)
	}
	if open := f.e.OpenDatapoints(); len(open) != 1 {
		t.Errorf("open: %+v", open)
	}
	// another datapoint is not the rule's
	f.e.Value("HmIP-RF", "0000D3C99C6B41:1", "LOW_BAT", json.RawMessage("true"), f.now, f.now)
	v("0")
	f.run(time.Second)
	out = f.take()
	if len(out) != 1 || out[0].Message.Title != "Resolved: Kitchen window: open for 10 minutes" {
		t.Fatalf("resolved: %+v", out)
	}
	// the memory survives a restart: a fired rule does not fire again
	v("1")
	f.run(11 * time.Minute)
	f.take()
	b, _ := f.e.Export()
	e2 := New(f.e.o)
	e2.Import(b)
	f.e = e2
	v("1")
	f.run(time.Hour)
	if len(f.take()) != 0 {
		t.Error("fired again after a restart")
	}
	// a seed that says the condition holds since long ago fires at once
	r2 := r
	r2.ID = "seed"
	f.c.Rules = append(f.c.Rules, r2)
	f.e.Value("HmIP-RF", "0000D3C99C6B41:1", "STATE", json.RawMessage("1"), f.now.Add(-time.Hour), f.now)
	f.run(time.Second)
	if out := f.take(); len(out) != 1 || out[0].Rule.ID != "seed" {
		t.Errorf("seed: %+v", out)
	}
}

// A key press is an event: every press is a message, nothing is resolved, nothing is remembered.
func TestDatapointKeyPress(t *testing.T) {
	r := config.Rule{ID: "bell", Name: "Bell", Enabled: true, Source: config.SrcDatapoint, Channels: []string{}, Priority: message.High, Resolved: true,
		Datapoint: &config.Datapoint{Interface: "HmIP-RF", Address: "A:1", Key: "PRESS_SHORT", Op: "true", Label: "the bell rang"}}
	f := newFake(t, r)
	press := func() { f.e.Value("HmIP-RF", "A:1", "PRESS_SHORT", json.RawMessage("true"), f.now, f.now) }
	press()
	f.run(time.Second)
	press()
	f.run(time.Second)
	out := f.take()
	if len(out) != 2 || out[0].Message.Title != "Kitchen window: the bell rang" || out[1].Message.Resolved {
		t.Fatalf("two presses: %+v", out)
	}
	if len(f.e.OpenDatapoints()) != 0 {
		t.Error("a key press stays open")
	}
	f.run(2 * time.Hour)
	press()
	f.run(time.Second)
	if len(f.take()) != 1 {
		t.Error("a later press")
	}
}

func TestEval(t *testing.T) {
	tests := []struct {
		op, value, raw string
		truth, ok      bool
	}{
		{"true", "", "true", true, true}, {"true", "", "false", false, true}, {"true", "", "1", true, true}, {"false", "", "0", true, true},
		{"false", "", `"x"`, false, false},
		{"gt", "21,5", "22", true, true}, {"gt", "21.5", "21.5", false, true}, {"ge", "21.5", "21.5", true, true},
		{"lt", "5", "4.9", true, true}, {"le", "5", "5", true, true}, {"lt", "5", `"x"`, false, false},
		{"eq", "2", "2", true, true}, {"ne", "2", "2", false, true}, {"eq", "true", "true", true, true}, {"eq", "false", "true", false, true},
		{"eq", "OPEN", `"OPEN"`, true, true}, {"ne", "OPEN", `"CLOSED"`, true, true}, {"eq", "OPEN", "3", false, false},
	}
	for _, tt := range tests {
		truth, ok := Eval(&config.Datapoint{Op: tt.op, Value: tt.value}, json.RawMessage(tt.raw))
		if truth != tt.truth || ok != tt.ok {
			t.Errorf("%s %s on %s: %v %v", tt.op, tt.value, tt.raw, truth, ok)
		}
	}
}

func TestDigest(t *testing.T) {
	r := config.Rule{ID: "digest", Name: "Digest", Enabled: true, Source: config.SrcDigest, Channels: []string{"mail"}, Priority: message.Low, Time: "08:00"}
	f := newFake(t, r)
	// first seen at noon: no digest today, the first one tomorrow at eight
	f.run(19 * time.Hour)
	if len(f.take()) != 0 {
		t.Fatal("a digest before its time")
	}
	f.run(2 * time.Hour)
	out := f.take()
	if len(out) != 1 {
		t.Fatalf("digest: %+v", out)
	}
	m := out[0].Message
	if m.Title != "Daily digest: 2 open" || m.Body != "Warnings:\n- Backups are not encrypted\n\nService messages:\n- Radiator: battery low" || m.Kind != message.KindDigest {
		t.Errorf("digest: %q %q", m.Title, m.Body)
	}
	// once a day
	f.run(22 * time.Hour)
	if len(f.take()) != 0 {
		t.Fatal("a second digest on the same day")
	}
	// all clear
	f.e.o.Open = func(string) Open { return Open{} }
	f.run(2 * time.Hour)
	if out := f.take(); len(out) != 1 || out[0].Message.Title != "All clear" {
		t.Fatalf("all clear: %+v", out)
	}
	f.c.Rules[0].OnlyOpen = true
	f.run(24 * time.Hour)
	if len(f.take()) != 0 {
		t.Error("all clear sent although the rule wants silence")
	}
}

func TestInboundAndSystem(t *testing.T) {
	in := config.Rule{ID: "in", Name: "Inbound", Enabled: true, Source: config.SrcInbound, Channels: []string{}, Priority: message.Normal, Resolved: true, Tag: "battery"}
	sys := config.Rule{ID: "sys", Name: "System", Enabled: true, Source: config.SrcSystem, Channels: []string{}, Priority: message.Normal, Kinds: []string{"reboot"}}
	f := newFake(t, in, sys)
	f.e.Handle(Event{Source: config.SrcInbound, Key: "k1", Tag: "Battery", Priority: message.High, Item: message.Item{Title: "Batteries due", Detail: "3 devices"}}, f.now)
	f.e.Handle(Event{Source: config.SrcInbound, Key: "k2", Tag: "other", Item: message.Item{Title: "x"}}, f.now)
	f.e.Handle(Event{Source: config.SrcSystem, Key: "reboot", SystemKind: "reboot", Item: message.Item{Title: "The system was restarted"}}, f.now)
	f.e.Handle(Event{Source: config.SrcSystem, Key: "backup", SystemKind: "backup-done", Item: message.Item{Title: "Backup made"}}, f.now)
	f.run(time.Second)
	out := f.take()
	if len(out) != 2 {
		t.Fatalf("out: %+v", out)
	}
	if m := out[0].Message; m.Title != "Batteries due" || m.Body != "3 devices" || m.Priority != message.High {
		t.Errorf("inbound: %+v", m)
	}
	if out[1].Message.Title != "The system was restarted" {
		t.Errorf("system: %+v", out[1].Message)
	}
}

func TestNoChannelNoMessage(t *testing.T) {
	f := newFake(t, serviceRule())
	f.c.Channels = nil
	f.service("A", "Radiator", "x", false)
	f.run(time.Hour)
	if len(f.take()) != 0 || f.e.PendingCount() != 0 {
		t.Error("an event without a channel waits or is sent")
	}
}

func TestSample(t *testing.T) {
	f := newFake(t)
	for _, src := range config.Sources {
		r := config.Rule{ID: "x", Name: "x", Enabled: true, Source: src, Priority: message.Normal, Time: "08:00", Datapoint: &config.Datapoint{Interface: "HmIP-RF", Address: "A:1", Key: "STATE", Op: "true"}}
		m := f.e.Sample(r, f.now)
		if m.Title == "" || m.Kind != src {
			t.Errorf("%s: %+v", src, m)
		}
	}
}
