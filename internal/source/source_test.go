package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/ccu-addon-kit/go/addonkit"
	"github.com/hobbyquaker/ccu-addon-kit/go/litesource"

	"github.com/openccu-lite/lite-addon-notify/internal/config"
	"github.com/openccu-lite/lite-addon-notify/internal/engine"
	"github.com/openccu-lite/lite-addon-notify/internal/fakesys"
	"github.com/openccu-lite/lite-addon-notify/internal/message"
)

// rig is the sources on the fake system, with a real engine whose rules send at once.
type rig struct {
	t    *testing.T
	fs   *fakesys.Server
	web  *httptest.Server
	mu   sync.Mutex
	c    config.Config
	eng  *engine.Engine
	src  *Sources
	boot string
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, fs: fakesys.New(), boot: "boot-1"}
	r.web = httptest.NewServer(r.fs.Handler())
	t.Cleanup(func() { r.web.Close(); r.fs.Close() })
	r.c = config.Config{Settings: config.DefaultSettings, Channels: []config.Channel{{ID: "a", Name: "A", Type: config.Ntfy, Enabled: true}},
		Rules: []config.Rule{
			{ID: "w", Name: "W", Enabled: true, Source: config.SrcWarning, Channels: []string{}, Priority: message.Normal, Resolved: true, BatchS: 1, Severities: []string{"error", "warning"}},
			{ID: "s", Name: "S", Enabled: true, Source: config.SrcService, Channels: []string{}, Priority: message.Normal, Resolved: true, BatchS: 1, Kinds: []string{"unreach", "lowbat"}},
			{ID: "y", Name: "Y", Enabled: true, Source: config.SrcSystem, Channels: []string{}, Priority: message.Normal, Resolved: true, Kinds: []string{"update-available", "update-installed", "backup-done", "backup-failed", "reboot"}},
		}}
	r.c.Settings.RepeatMin = 0
	r.c.Settings.Language = "en"
	r.restart()
	return r
}

func (r *rig) cfg() config.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.c
}

// restart makes new sources and a new engine that take over the old ones' memory.
func (r *rig) restart() {
	var eng, src []byte
	if r.eng != nil {
		eng, _ = r.eng.Export()
		src, _ = r.src.Export()
	}
	tok := addonkit.StaticToken("t")
	r.eng = engine.New(engine.Options{Config: r.cfg, System: func() string { return "ccu" }, Describe: func(i, a, l string) (string, []string) { return r.src.Describe(i, a, l) }})
	r.src = New(Options{Base: r.web.URL, HTTP: &http.Client{Transport: &addonkit.AuthTransport{Source: tok}}, Lite: litesource.New(r.web.URL, tok), Engine: r.eng, Config: r.cfg, BootID: func() string { return r.boot }})
	r.eng.Import(eng)
	r.src.Import(src)
}

// due answers the messages that are due once the rules' batching window (a second) is over.
func (r *rig) due() []engine.Outgoing { return r.eng.Tick(time.Now().Add(2 * time.Second)) }

// titles answers the titles of the messages that are due.
func (r *rig) titles() string {
	var out []string
	for _, o := range r.due() {
		out = append(out, o.Rule.ID+": "+o.Message.Title)
	}
	return strings.Join(out, " | ")
}

func TestWarnings(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.fs.SetWarnings(fakesys.Warning("backup-unencrypted", "plain", "warning", nil), fakesys.Warning("journal-ram", "ram", "info", nil))
	r.src.PollWarnings(ctx)
	if got := r.titles(); got != "" {
		t.Fatalf("the first read notifies: %s", got)
	}
	if st := r.src.States()["warnings"]; !st.OK || st.Open != 2 {
		t.Errorf("state: %+v", st)
	}
	// a new error, an info the rule does not take, an addon's notice with its own words
	r.fs.SetWarnings(fakesys.Warning("backup-unencrypted", "plain", "warning", nil), fakesys.Warning("journal-ram", "ram", "info", nil),
		fakesys.Warning("addon-failed", "mosquitto", "error", map[string]any{"addons": []any{map[string]any{"id": "mosquitto", "name": "Mosquitto"}}}),
		fakesys.Warning("bootloader-update", "2026-01-01", "info", nil))
	r.src.PollWarnings(ctx)
	if got := r.titles(); got != "w: Addon failed: Mosquitto" {
		t.Fatalf("new: %s", got)
	}
	r.fs.SetWarnings(fakesys.Warning("addon-notice", "battery/due", "warning", map[string]any{"addon": "battery", "title": map[string]any{"de": "Batterien bald leer", "en": "Batteries due"}, "text": map[string]any{"en": "3 devices"}}))
	r.src.PollWarnings(ctx)
	out := r.due()
	// what was there at the first read was never announced, so its end is not said either
	if len(out) != 1 || out[0].Message.Title != "2 warnings (1 new, 1 resolved)" || out[0].Message.Body != "Resolved: Addon failed: Mosquitto\nbattery: Batteries due" {
		t.Fatalf("a batch of new and resolved: %+v", out)
	}
	// the digest's view: warnings and errors, not the notices
	if o := r.src.Open("en"); len(o.Warnings) != 1 || o.Warnings[0].Line != "battery: Batteries due" {
		t.Errorf("open: %+v", o)
	}
	// after a restart nothing is said again, and the end of what was announced is still said
	r.restart()
	r.src.PollWarnings(ctx)
	if got := r.titles(); got != "" {
		t.Fatalf("after a restart: %s", got)
	}
	r.fs.SetWarnings()
	r.src.PollWarnings(ctx)
	if got := r.titles(); got != "w: Resolved: battery: Batteries due" {
		t.Fatalf("resolved after a restart: %s", got)
	}
}

func TestWarningsThatAreSystemEvents(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.src.PollWarnings(ctx)
	r.fs.SetWarnings(fakesys.Warning("system-update-available", "2.1.0", "warning", map[string]any{"version": "2.1.0", "channel": "stable"}),
		fakesys.Warning("backup-delivery", "usb:no-medium", "error", map[string]any{"name": "USB", "cause": "no-medium"}))
	r.src.PollWarnings(ctx)
	got := r.titles()
	if got != "y: Backup failed: The backup target USB does not work (no-medium) | y: System update available: 2.1.0" {
		t.Fatalf("routed to the system rule, not the warnings': %s", got)
	}
	r.fs.SetWarnings()
	r.src.PollWarnings(ctx)
	if got := r.titles(); got != "y: Resolved: Backup failed: The backup target USB does not work (no-medium) | y: Resolved: System update available: 2.1.0" {
		t.Fatalf("resolved: %s", got)
	}
}

func TestServiceMessages(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.fs.SetMessages(fakesys.Message("000A1BE9A1B2C3", "CONFIG_PENDING", "Heizung Bad", "Bad"))
	r.src.PollService(ctx)
	if got := r.titles(); got != "" {
		t.Fatalf("the first read notifies: %s", got)
	}
	r.fs.SetMessages(fakesys.Message("000A1BE9A1B2C3", "CONFIG_PENDING", "Heizung Bad", "Bad"), fakesys.Message("000A1BE9A1B2C3", "UNREACH", "Heizung Bad", "Bad"), fakesys.Message("0000D3C99C6B41", "LOW_BAT", "", ""))
	r.src.PollService(ctx)
	out := r.due()
	if len(out) != 1 || out[0].Message.Body != "0000D3C99C6B41: battery low\nHeizung Bad: communication disturbed" {
		t.Fatalf("new: %+v", out)
	}
	if d := out[0].Message.Items[0].Detail; !strings.HasPrefix(d, "HmIP-RF 0000D3C99C6B41 · since ") {
		t.Errorf("a device without a room: %q", d)
	}
	it := out[0].Message.Items[1]
	if it.Device != "Heizung Bad" || it.Address != "000A1BE9A1B2C3" || it.Datapoint != "UNREACH" || len(it.Rooms) != 1 || it.Rooms[0] != "Bad" || !strings.HasPrefix(it.Detail, "Bad · HmIP-RF 000A1BE9A1B2C3 · since ") {
		t.Errorf("item: %+v", it)
	}
	// an interface that is down says nothing about its devices
	r.fs.Down["HmIP-RF"] = true
	r.fs.SetMessages()
	r.src.PollService(ctx)
	if got := r.titles(); got != "" {
		t.Fatalf("resolved while the interface is down: %s", got)
	}
	if st := r.src.States()["service"]; st.Open != 3 {
		t.Errorf("state: %+v", st)
	}
	r.fs.Down["HmIP-RF"] = false
	r.fs.SetMessages(fakesys.Message("0000D3C99C6B41", "LOW_BAT", "", ""))
	r.src.PollService(ctx)
	if got := r.titles(); got != "s: Resolved: Heizung Bad: communication disturbed" {
		t.Fatalf("resolved: %s", got)
	}
	if o := r.src.Open("de"); len(o.Service) != 1 || o.Service[0].Line != "0000D3C99C6B41: Batterie schwach" {
		t.Errorf("open: %+v", o.Service)
	}
}

func TestServiceStream(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.src.serviceStream(ctx)
	wait := func(what string, ok func() bool) {
		t.Helper()
		for i := 0; i < 400; i++ {
			if ok() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal(what)
	}
	wait("the stream opens", func() bool { return r.src.States()["service"].Stream })
	wait("the stream's first view is the baseline", func() bool { r.src.mu.Lock(); defer r.src.mu.Unlock(); return r.src.mem.ServiceSeen })
	r.fs.SetMessages(fakesys.Message("000A1BE9A1B2C3", "UNREACH", "Heizung Bad", "Bad"))
	wait("the change arrives over the stream", func() bool { return r.eng.PendingCount() == 1 })
	if got := r.titles(); got != "s: Heizung Bad: communication disturbed" {
		t.Errorf("stream: %s", got)
	}
}

func TestSystemEvents(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.fs.SetBackup("automatic", "old.sbk", 1000, time.Now().Add(-48*time.Hour))
	r.src.boot()
	r.src.PollSystem(ctx)
	if got := r.titles(); got != "" {
		t.Fatalf("the first read notifies: %s", got)
	}
	r.fs.SetVersion("2.1.0")
	r.fs.SetBackup("run", "ccu-2026-11-21.sbk", 55869440, time.Now())
	r.src.PollSystem(ctx)
	out := r.due()
	if len(out) != 2 || out[0].Message.Title != "System update installed: 2.1.0" || out[0].Message.Body != "Before: 2.0.0" || out[1].Message.Title != "Backup made" || out[1].Message.Body != "ccu-2026-11-21.sbk · 55.9 MB · Back up now" {
		t.Fatalf("events: %+v", out)
	}
	r.src.PollSystem(ctx)
	if got := r.titles(); got != "" {
		t.Fatalf("said twice: %s", got)
	}
	// a restart of the addon alone is no reboot; another boot id is
	r.restart()
	r.src.boot()
	if got := r.titles(); got != "" {
		t.Fatalf("a restart of the addon: %s", got)
	}
	r.boot = "boot-2"
	r.restart()
	r.src.boot()
	if got := r.titles(); got != "y: The system was restarted" {
		t.Fatalf("reboot: %s", got)
	}
	// a system that does not answer is a state, not an event
	r.web.Close()
	r.src.PollSystem(ctx)
	r.src.PollWarnings(ctx)
	if st := r.src.States(); st["system"].OK || st["system"].Error == "" || st["warnings"].OK {
		t.Errorf("states: %+v", st)
	}
	if got := r.titles(); got != "" {
		t.Fatalf("an unreachable system notifies: %s", got)
	}
}

func TestDatapoints(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.src.datapointLoop(ctx)
	wait := func(what string, ok func() bool) {
		t.Helper()
		for i := 0; i < 600; i++ {
			if ok() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal(what)
	}
	// no rule, no stream
	time.Sleep(50 * time.Millisecond)
	if n := r.fs.Lite.Streams(); n != 0 {
		t.Fatalf("a stream without a datapoint rule: %d", n)
	}
	r.mu.Lock()
	r.c.Rules = append(r.c.Rules, config.Rule{ID: "win", Name: "Window", Enabled: true, Source: config.SrcDatapoint, Channels: []string{}, Priority: message.Normal, Resolved: true,
		Datapoint: &config.Datapoint{Interface: "HmIP-RF", Address: "0000D3C99C6B41:1", Key: "STATE", Op: "eq", Value: "1", Label: "open"}})
	r.mu.Unlock()
	r.src.Kick()
	wait("the stream opens", func() bool { return r.fs.Lite.Streams() == 1 && r.src.States()["datapoints"].Stream })
	if q := r.fs.Lite.Queries(); len(q) == 0 || !strings.Contains(q[len(q)-1], "0000D3C99C6B41") || !strings.Contains(q[len(q)-1], "STATE") {
		t.Errorf("the stream is not filtered to the rule: %v", q)
	}
	if got := r.titles(); got != "" {
		t.Fatalf("the seed (closed) notifies: %s", got)
	}
	r.fs.Lite.PushEvent("HmIP-RF", "0000D3C99C6B41:1", "STATE", 1)
	r.fs.Lite.PushEvent("HmIP-RF", "0000D3C99C6B41:1", "LOW_BAT", true)
	var got string
	wait("the rule fires", func() bool { got += r.titles(); return got != "" })
	if got != "win: Fenster Küche: open" {
		t.Errorf("fired: %s", got)
	}
	r.fs.Lite.PushEvent("HmIP-RF", "0000D3C99C6B41:1", "STATE", 0)
	got = ""
	wait("the rule resolves", func() bool { got += r.titles(); return got != "" })
	if got != "win: Resolved: Fenster Küche: open" {
		t.Errorf("resolved: %s", got)
	}
	// a key press the state store remembers is not a press: a rule on it fires on the event only
	r.fs.Lite.State = append(r.fs.Lite.State, map[string]any{"interface": "HmIP-RF", "address": "HmIP-RCV-1:1", "datapoint": "PRESS_SHORT", "value": true, "ts": time.Now().UTC().Format(time.RFC3339), "confirmed": true, "source": "event"})
	r.mu.Lock()
	r.c.Rules = append(r.c.Rules, config.Rule{ID: "key", Name: "Key", Enabled: true, Source: config.SrcDatapoint, Channels: []string{}, Priority: message.Normal,
		Datapoint: &config.Datapoint{Interface: "HmIP-RF", Address: "HmIP-RCV-1:1", Key: "PRESS_SHORT", Op: "true", Label: "pressed"}})
	r.mu.Unlock()
	r.src.Kick()
	wait("the stream follows the rules", func() bool {
		q := r.fs.Lite.Queries()
		return r.fs.Lite.Streams() == 1 && len(q) > 0 && strings.Contains(q[len(q)-1], "PRESS_SHORT") && r.src.States()["datapoints"].Stream
	})
	time.Sleep(100 * time.Millisecond)
	if got := r.titles(); got != "" {
		t.Fatalf("the remembered press fired: %s", got)
	}
	r.fs.Lite.PushEvent("HmIP-RF", "HmIP-RCV-1:1", "PRESS_SHORT", true)
	got = ""
	wait("the press fires", func() bool { got += r.titles(); return got != "" })
	if got != "key: HmIP-RCV-1:1: pressed" {
		t.Errorf("press: %s", got)
	}
	// the rule goes: the stream closes
	r.mu.Lock()
	r.c.Rules = r.c.Rules[:3]
	r.mu.Unlock()
	r.src.Kick()
	wait("the stream closes", func() bool { return r.fs.Lite.Streams() == 0 })
}

func TestDevices(t *testing.T) {
	r := newRig(t)
	l, err := r.src.Devices(context.Background(), "en")
	if err != nil || len(l) != 3 {
		t.Fatalf("devices: %+v %v", l, err)
	}
	if l[0].Name != "Fenster Küche" || l[0].Address != "0000D3C99C6B41:1" || l[0].Rooms[0] != "Kitchen" || l[0].Keys[0].Key != "STATE" {
		t.Errorf("first: %+v", l[0])
	}
	// a channel without a name of its own takes the device's
	if n, rooms := r.src.Describe("HmIP-RF", "0000D3C99C6B41:0", "de"); n != "Fenster Küche:0" || len(rooms) != 1 || rooms[0] != "Küche" {
		t.Errorf("describe: %q %v", n, rooms)
	}
}
