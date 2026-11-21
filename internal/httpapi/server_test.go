package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/ccu-addon-kit/go/addonkit"
	"github.com/hobbyquaker/ccu-addon-kit/go/litesource"

	"github.com/openccu-lite/lite-addon-notify/internal/config"
	"github.com/openccu-lite/lite-addon-notify/internal/deliver"
	"github.com/openccu-lite/lite-addon-notify/internal/engine"
	"github.com/openccu-lite/lite-addon-notify/internal/fakesys"
	"github.com/openccu-lite/lite-addon-notify/internal/message"
	"github.com/openccu-lite/lite-addon-notify/internal/source"
)

// rig is the API on a real store, engine, sources and dispatcher, the system a fake one; the
// messages go to the fake's sink through the real channels.
type rig struct {
	t     *testing.T
	fs    *fakesys.Server
	sys   *httptest.Server
	web   *httptest.Server
	store *config.Store
	eng   *engine.Engine
	disp  *deliver.Dispatcher
	mu    sync.Mutex
	kicks int
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, fs: fakesys.New()}
	r.sys = httptest.NewServer(r.fs.Handler())
	dir := t.TempDir()
	www := filepath.Join(dir, "www")
	_ = os.MkdirAll(www, 0o755)
	_ = os.WriteFile(filepath.Join(www, "index.html"), []byte("<title>page</title>"), 0o644)
	store, err := config.Open(filepath.Join(dir, "conf"))
	if err != nil {
		t.Fatal(err)
	}
	r.store = store
	tok := addonkit.StaticToken("t")
	var src *source.Sources
	r.eng = engine.New(engine.Options{Config: store.Get, System: func() string { return "ccu" }, Open: func(l string) engine.Open { return src.Open(l) }})
	src = source.New(source.Options{Base: r.sys.URL, HTTP: &http.Client{Transport: &addonkit.AuthTransport{Source: tok}}, Lite: litesource.New(r.sys.URL, tok), Engine: r.eng, Config: store.Get})
	ctx, cancel := context.WithCancel(context.Background())
	r.disp = deliver.New(deliver.Options{Context: ctx, Channel: store.Channel, Settings: func() config.Settings { return store.Get().Settings }})
	h := New(Options{BasePath: "/addons/notify", WWW: www, Store: store, Engine: r.eng, Sources: src, Deliver: r.disp, Version: "1.2.3", Started: time.Now(), System: func() string { return "ccu" },
		InboundURL: "http://127.0.0.1:8300/inbound", Kick: func() { r.mu.Lock(); r.kicks++; r.mu.Unlock() }})
	r.web = httptest.NewServer(h)
	t.Cleanup(func() { cancel(); r.web.Close(); r.sys.Close(); r.fs.Close() })
	return r
}

// call sends a request to the API as the page does and answers the status and the JSON.
func (r *rig) call(method, path string, body any) (int, map[string]any) {
	r.t.Helper()
	status, raw := r.raw(method, "/addons/notify/api/v1/"+path, body, map[string]string{"X-CCU-Addon-Request": "1"})
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return status, out
}

func (r *rig) raw(method, path string, body any, hdr map[string]string) (int, []byte) {
	r.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, r.web.URL+path, rd)
	if _, isText := body.(string); body != nil && !isText {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw
}

func (r *rig) sunk(n int) []fakesys.Request {
	r.t.Helper()
	for i := 0; i < 400; i++ {
		if l := r.fs.Sunk(); len(l) >= n {
			return l
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("the sink has %d requests, want %d", len(r.fs.Sunk()), n)
	return nil
}

func TestPageAndGuard(t *testing.T) {
	r := newRig(t)
	if s, b := r.raw("GET", "/addons/notify/", nil, nil); s != 200 || !strings.Contains(string(b), "page") {
		t.Errorf("the page: %d %s", s, b)
	}
	if s, _ := r.call("GET", "nope", nil); s != 404 {
		t.Errorf("an unknown route: %d", s)
	}
	// a write without the kit's header is refused
	if s, _ := r.raw("PUT", "/addons/notify/api/v1/settings", map[string]any{}, nil); s != 403 {
		t.Errorf("a write without the CSRF header: %d", s)
	}
	s, h := r.call("GET", "health", nil)
	if s != 200 || h["version"] != "1.2.3" {
		t.Errorf("health: %d %v", s, h)
	}
}

func TestChannelsAndTheTestButton(t *testing.T) {
	r := newRig(t)
	// the Test button works on an unsaved channel
	hook := map[string]any{"name": "Hook", "type": "webhook", "enabled": true, "url": r.sys.URL + "/sink/hook", "headers": map[string]string{"X-Key": "header-secret"}}
	if s, out := r.call("POST", "channels/test", hook); s != 200 || out["ok"] != true {
		t.Fatalf("test: %d %v", s, out)
	}
	got := r.sunk(1)[0]
	var m map[string]any
	_ = json.Unmarshal([]byte(got.Body), &m)
	if got.Header["X-Key"] != "header-secret" || m["kind"] != "test" || m["title"] != "Testnachricht" || !strings.Contains(m["body"].(string), "ccu") {
		t.Errorf("the test message: %+v", got)
	}
	if len(r.disp.History()) != 0 {
		t.Error("a test of an unsaved channel is in the history")
	}
	// a target that refuses: the step and its own words
	r.fs.SinkStatus["hook"] = 401
	s, out := r.call("POST", "channels/test", hook)
	if s != 502 || out["error"] != "send" || out["step"] != "answer" || !strings.Contains(out["message"].(string), "401") || !strings.Contains(out["message"].(string), "told to refuse") {
		t.Errorf("a refusing target: %d %v", s, out)
	}
	delete(r.fs.SinkStatus, "hook")
	// an invalid one names the field
	if s, out := r.call("POST", "channels/test", map[string]any{"name": "x", "type": "gotify", "url": "https://g"}); s != 422 || out["field"] != "token" {
		t.Errorf("invalid: %d %v", s, out)
	}
	// save: the answer is the configuration without the secret
	s, out = r.call("POST", "channels", hook)
	raw, _ := json.Marshal(out)
	if s != 200 || strings.Contains(string(raw), "header-secret") || len(out["channels"].([]any)) != 1 {
		t.Fatalf("save: %d %s", s, raw)
	}
	ch := out["channels"].([]any)[0].(map[string]any)
	if ch["id"] != "hook" || ch["secrets"].(map[string]any)["headers"] != true {
		t.Errorf("channel: %v", ch)
	}
	// the Test button on the saved channel, as the page sends it (no secret): the stored one is used
	ch["url"] = r.sys.URL + "/sink/hook2"
	if s, out := r.call("POST", "channels/test", ch); s != 200 {
		t.Fatalf("test of the saved channel: %d %v", s, out)
	}
	if got := r.sunk(3)[2]; got.Header["X-Key"] != "header-secret" || got.Path != "/sink/hook2" {
		t.Errorf("stored secret on a test: %+v", got)
	}
	if h := r.disp.History(); len(h) != 1 || h[0].Result != deliver.Sent || h[0].Kind != "test" {
		t.Errorf("history: %+v", h)
	}
	if s, _ := r.call("PUT", "channels/nope", hook); s != 404 {
		t.Errorf("put of an unknown channel: %d", s)
	}
	if s, _ := r.call("POST", "channels/test", map[string]any{"id": "nope", "type": "webhook"}); s != 404 {
		t.Errorf("test of an unknown channel: %d", s)
	}
	if s, out := r.call("DELETE", "channels/hook", nil); s != 200 || len(out["channels"].([]any)) != 0 {
		t.Errorf("delete: %d %v", s, out)
	}
}

func TestRulesPreviewAndTest(t *testing.T) {
	r := newRig(t)
	r.call("POST", "channels", map[string]any{"name": "Hook", "type": "webhook", "enabled": true, "url": r.sys.URL + "/sink/hook"})
	_, cfg := r.call("GET", "config", nil)
	if len(cfg["rules"].([]any)) != 4 || cfg["system"] != "ccu" || len(cfg["types"].([]any)) != len(config.Types) || len(cfg["warning_ids"].([]any)) < 40 {
		t.Fatalf("config: %v", cfg)
	}
	// the preview renders the rule's templates on sample events
	rule := map[string]any{"name": "Service", "source": "service", "enabled": true, "channels": []string{}, "kinds": []string{"unreach"}, "priority": "high", "title": "{{ count }} x {{ system }}", "body": "{{ lines | join(' / ') }}"}
	s, out := r.call("POST", "rules/preview", rule)
	if s != 200 || out["title"] != "2 x ccu" || out["body"] != "Heizung Bad: Kommunikation gestört / Fenster Küche: Batterie schwach" || out["priority"] != "high" {
		t.Errorf("preview: %d %v", s, out)
	}
	rule["body"] = "{{ lines | nosuch }}"
	if s, out := r.call("POST", "rules/preview", rule); s != 422 || out["field"] != "body" {
		t.Errorf("a broken template: %d %v", s, out)
	}
	rule["body"] = ""
	if s, out := r.call("POST", "rules", rule); s != 200 || len(out["rules"].([]any)) != 5 {
		t.Fatalf("save: %d %v", s, out)
	}
	// the rule's test sends its sample through the queue to its channels
	if s, out := r.call("POST", "rules/service/test", nil); s != 200 || out["channels"].([]any)[0] != "hook" {
		t.Fatalf("rule test: %d %v", s, out)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(r.sunk(1)[0].Body), &m)
	if m["title"] != "2 Servicemeldungen" || m["rule"] != "service" || m["priority"] != "normal" {
		t.Errorf("the default rule's sample: %v", m)
	}
	if s, _ := r.call("POST", "rules/nope/test", nil); s != 404 {
		t.Errorf("test of an unknown rule: %d", s)
	}
	r.call("DELETE", "channels/hook", nil)
	if s, out := r.call("POST", "rules/warnings/test", nil); s != 409 || out["error"] != "no-channel" {
		t.Errorf("a rule without a channel: %d %v", s, out)
	}
	if s, out := r.call("DELETE", "rules/service-2", nil); s != 200 || len(out["rules"].([]any)) != 4 {
		t.Errorf("delete: %d %v", s, out)
	}
	if s, _ := r.call("PUT", "rules/nope", rule); s != 404 {
		t.Errorf("put of an unknown rule: %d", s)
	}
}

func TestStatusHistoryDevices(t *testing.T) {
	r := newRig(t)
	s, st := r.call("GET", "status", nil)
	if s != 200 || st["pending"] != 0.0 || st["sources"].(map[string]any)["warnings"] == nil || st["open"].(map[string]any)["warnings"] == nil {
		t.Errorf("status: %d %v", s, st)
	}
	if _, quiet := st["quiet_until"]; quiet {
		t.Error("quiet hours are off")
	}
	if s, raw := r.raw("GET", "/addons/notify/api/v1/history", nil, nil); s != 200 || strings.TrimSpace(string(raw)) != "[]" {
		t.Errorf("history: %d %s", s, raw)
	}
	if s, _ := r.call("DELETE", "history", nil); s != 200 {
		t.Errorf("clear: %d", s)
	}
	s, raw := r.raw("GET", "/addons/notify/api/v1/devices", nil, nil)
	var devs []map[string]any
	if json.Unmarshal(raw, &devs) != nil || s != 200 || len(devs) != 3 || devs[0]["name"] != "Fenster Küche" {
		t.Errorf("devices: %d %s", s, raw)
	}
	// settings
	if s, out := r.call("PUT", "settings", map[string]any{"language": "en", "quiet": map[string]any{"enabled": true, "from": "00:00", "to": "23:59"}, "repeat_min": 5}); s != 200 || out["settings"].(map[string]any)["language"] != "en" {
		t.Errorf("settings: %d %v", s, out)
	}
	if _, st := r.call("GET", "status", nil); st["quiet_until"] == nil {
		t.Error("quiet hours are on")
	}
	if s, out := r.call("PUT", "settings", map[string]any{"base_url": "ccu"}); s != 422 || out["field"] != "base_url" {
		t.Errorf("invalid settings: %d %v", s, out)
	}
}

func TestInbound(t *testing.T) {
	r := newRig(t)
	r.call("POST", "channels", map[string]any{"name": "Hook", "type": "webhook", "enabled": true, "url": r.sys.URL + "/sink/hook"})
	r.call("POST", "rules", map[string]any{"name": "In", "source": "inbound", "enabled": true, "channels": []string{}, "resolved": true})
	post := func(path string, body any, hdr map[string]string) (int, string) {
		s, raw := r.raw("POST", path, body, hdr)
		return s, string(raw)
	}
	// off until switched on
	if s, _ := post("/inbound", `{"title":"x"}`, nil); s != 404 {
		t.Fatalf("switched off: %d", s)
	}
	r.call("PUT", "settings", map[string]any{"inbound": map[string]any{"enabled": true}})
	_, info := r.call("GET", "inbound", nil)
	secret := info["secret"].(string)
	if len(secret) != 32 || info["url"] != "http://127.0.0.1:8300/inbound" || info["enabled"] != true {
		t.Fatalf("info: %v", info)
	}
	if s, _ := post("/inbound", `{"title":"x"}`, nil); s != 401 {
		t.Errorf("without the secret: %d", s)
	}
	if s, _ := post("/inbound?secret=wrong", `{"title":"x"}`, nil); s != 401 {
		t.Errorf("a wrong secret: %d", s)
	}
	if s, _ := r.raw("GET", "/inbound?secret="+secret, nil, nil); s != 405 {
		t.Errorf("GET: %d", s)
	}
	auth := map[string]string{"Authorization": "Bearer " + secret}
	if s, b := post("/inbound", `{"title":`, auth); s != 400 {
		t.Errorf("broken JSON: %d %s", s, b)
	}
	if s, b := post("/inbound", `{}`, auth); s != 400 {
		t.Errorf("nothing to say: %d %s", s, b)
	}
	// battery's webhook body as it is, with the source in the URL
	s, b := post("/inbound?source=battery", map[string]any{"kind": "low", "key": "dev1", "title": "Batterie schwach", "text": "Fenster Küche (HmIP-SWDO)", "link": "/addons/battery/#dev1", "at": 1}, auth)
	if s != 202 {
		t.Fatalf("accepted: %d %s", s, b)
	}
	out := r.eng.Tick(time.Now())
	if len(out) != 1 || out[0].Message.Title != "Batterie schwach" || out[0].Message.Body != "Fenster Küche (HmIP-SWDO)" || out[0].Message.Kind != message.KindInbound {
		t.Fatalf("message: %+v", out)
	}
	// its end, by the same key
	post("/inbound?source=battery", map[string]any{"key": "dev1", "title": "Batterie schwach", "resolved": true}, auth)
	if out := r.eng.Tick(time.Now()); len(out) != 1 || out[0].Message.Title != "Behoben: Batterie schwach" {
		t.Fatalf("resolved: %+v", out)
	}
	// plain text with the secret in a header of its own; a priority; a link out of the system goes into the text
	post("/inbound?title=Waschmaschine", "fertig", map[string]string{"X-Notify-Secret": secret, "Content-Type": "text/plain"})
	post("/inbound", "Erste Zeile\nzweite Zeile", map[string]string{"X-Notify-Secret": secret})
	post("/inbound", map[string]any{"message": "nur Text", "priority": 5, "url": "https://example.org/x"}, auth)
	out = r.eng.Tick(time.Now())
	if len(out) != 3 {
		t.Fatalf("three messages: %+v", out)
	}
	got := map[string]message.Message{}
	for _, o := range out {
		got[o.Message.Title] = o.Message
	}
	if got["Waschmaschine"].Body != "fertig" || got["Erste Zeile"].Body != "zweite Zeile" || got["nur Text"].Priority != message.High || got["nur Text"].Body != "https://example.org/x" {
		t.Errorf("messages: %+v", got)
	}
	r.mu.Lock()
	if r.kicks < 5 {
		t.Errorf("the loop was not woken: %d", r.kicks)
	}
	r.mu.Unlock()
	// a new secret ends the old one
	_, rot := r.call("POST", "inbound/rotate", nil)
	if rot["secret"] == secret || len(rot["secret"].(string)) != 32 {
		t.Errorf("rotate: %v", rot)
	}
	if s, _ := post("/inbound", `{"title":"x"}`, auth); s != 401 {
		t.Errorf("the old secret: %d", s)
	}
	// the rate limit
	auth["Authorization"] = "Bearer " + rot["secret"].(string)
	limited := 0
	for i := 0; i < InboundPerMinute+5; i++ {
		if s, _ := post("/inbound", `{"title":"x"}`, auth); s == 429 {
			limited++
		}
	}
	if limited == 0 {
		t.Error("no rate limit")
	}
	// the public configuration never carries the secret
	_, raw := r.raw("GET", "/addons/notify/api/v1/config", nil, nil)
	if strings.Contains(string(raw), rot["secret"].(string)) {
		t.Error("the configuration answers the inbound secret")
	}
}

// docs/api.md names every route, and no route that is not served.
func TestRoutesDocumented(t *testing.T) {
	newRig(t)
	doc, err := os.ReadFile("../../docs/api.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range Routes {
		method, p, _ := strings.Cut(route, " ")
		want := "`" + method + " " + strings.TrimPrefix(p, "/api/v1/") + "`"
		if !strings.Contains(string(doc), want) {
			t.Errorf("docs/api.md lacks %s", want)
		}
	}
	if !strings.Contains(string(doc), "`POST /inbound`") {
		t.Error("docs/api.md lacks the inbound webhook")
	}
}
