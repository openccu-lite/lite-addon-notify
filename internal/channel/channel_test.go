package channel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openccu-lite/lite-addon-notify/internal/config"
	"github.com/openccu-lite/lite-addon-notify/internal/message"
)

// mock is the HTTP target every HTTP channel is tested against: it records the request and
// answers what the test says.
type mock struct {
	*httptest.Server
	mu     sync.Mutex
	req    *http.Request
	body   []byte
	status int
	answer string
}

func newMock(t *testing.T) *mock {
	m := &mock{status: 200, answer: `{"ok":true,"status":1}`}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.req, m.body = r, b
		status, answer := m.status, m.answer
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *mock) json(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(m.body, &v); err != nil {
		t.Fatalf("the body is not JSON: %v: %s", err, m.body)
	}
	return v
}

func msg() message.Message {
	return message.Message{ID: "m1", Kind: message.KindService, Rule: "service", RuleName: "Service", Title: "Heizung Bad: Kommunikation gestört", Body: "Bad · HmIP-RF 000A1BE9A1B2C3", Priority: message.High,
		Link: "https://ccu.example/", System: "ccu", Time: time.Date(2026, 11, 21, 14, 3, 0, 0, time.UTC), Count: 1,
		Items: []message.Item{{Key: "k", Title: "Heizung Bad", Text: "Kommunikation gestört", Line: "Heizung Bad: Kommunikation gestört", Device: "Heizung Bad", Address: "000A1BE9A1B2C3", Rooms: []string{"Bad"}}}}
}

func send(t *testing.T, ch config.Channel) error {
	t.Helper()
	return Send(context.Background(), ch, msg())
}

func wantErr(t *testing.T, err error, step string, permanent bool, contains string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want an Error, got %v", err)
	}
	if e.Step != step || e.Permanent != permanent || !strings.Contains(e.Msg, contains) {
		t.Errorf("error: step %q permanent %v %q; want %q %v containing %q", e.Step, e.Permanent, e.Msg, step, permanent, contains)
	}
}

func TestPushover(t *testing.T) {
	m := newMock(t)
	ch := config.Channel{Type: config.Pushover, Token: "apptoken", UserKey: "userkey", Target: "phone", Sound: "siren", URL: m.URL}
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	form, _ := url.ParseQuery(string(m.body))
	if m.req.URL.Path != "/1/messages.json" || form.Get("token") != "apptoken" || form.Get("user") != "userkey" || form.Get("title") != msg().Title || form.Get("message") != msg().Body ||
		form.Get("priority") != "1" || form.Get("device") != "phone" || form.Get("sound") != "siren" || form.Get("url") != "https://ccu.example/" {
		t.Errorf("form: %v", form)
	}
	m.status, m.answer = 400, `{"user":"invalid","errors":["user identifier is not a valid user, group, or subscribed user key"],"status":0}`
	wantErr(t, send(t, ch), StepAnswer, true, "not a valid user")
	m.status, m.answer = 500, ``
	wantErr(t, send(t, ch), StepAnswer, false, "500")
	m.status = 429
	wantErr(t, send(t, ch), StepAnswer, false, "429")
}

func TestTelegram(t *testing.T) {
	m := newMock(t)
	ch := config.Channel{Type: config.Telegram, Token: "123:SECRET", Target: "-1001", Thread: "7", URL: m.URL}
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	b := m.json(t)
	if m.req.URL.Path != "/bot123:SECRET/sendMessage" || b["chat_id"] != "-1001" || b["parse_mode"] != "HTML" || b["message_thread_id"] != 7.0 ||
		b["text"] != "<b>Heizung Bad: Kommunikation gestört</b>\nBad · HmIP-RF 000A1BE9A1B2C3\n\nhttps://ccu.example/" {
		t.Errorf("body: %v", b)
	}
	if _, silent := b["disable_notification"]; silent {
		t.Error("a high priority message is sent silently")
	}
	m.status, m.answer = 400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`
	wantErr(t, send(t, ch), StepAnswer, true, "chat not found")
	// the token never shows in an error
	m.Close()
	err := send(t, ch)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error: %v", err)
	}
	ch.Thread = "x"
	wantErr(t, send(t, ch), StepConfig, true, "not a number")
}

func TestTelegramEscapes(t *testing.T) {
	m := newMock(t)
	mm := msg()
	mm.Title, mm.Body, mm.Link, mm.Priority = "a <b> & c", "x < y", "", message.Low
	if err := Send(context.Background(), config.Channel{Type: config.Telegram, Token: "t", Target: "1", URL: m.URL}, mm); err != nil {
		t.Fatal(err)
	}
	b := m.json(t)
	if b["text"] != "<b>a &lt;b&gt; &amp; c</b>\nx &lt; y" || b["disable_notification"] != true {
		t.Errorf("body: %v", b)
	}
}

func TestNtfy(t *testing.T) {
	m := newMock(t)
	ch := config.Channel{Type: config.Ntfy, URL: m.URL, Target: "ccu", Token: "tk_x"}
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	b := m.json(t)
	if b["topic"] != "ccu" || b["title"] != msg().Title || b["message"] != msg().Body || b["priority"] != 4.0 || b["click"] != "https://ccu.example/" || m.req.Header.Get("Authorization") != "Bearer tk_x" {
		t.Errorf("body: %v %v", b, m.req.Header)
	}
	ch.Token, ch.User, ch.Password = "", "u", "p"
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	if u, p, ok := m.req.BasicAuth(); !ok || u != "u" || p != "p" {
		t.Error("basic auth")
	}
	m.status, m.answer = 403, `{"code":40301,"http":403,"error":"forbidden","link":"https://ntfy.sh/docs/publish/#authentication"}`
	wantErr(t, send(t, ch), StepAnswer, true, "forbidden")
}

func TestGotify(t *testing.T) {
	m := newMock(t)
	ch := config.Channel{Type: config.Gotify, URL: m.URL, Token: "AppTok"}
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	b := m.json(t)
	if m.req.URL.Path != "/message" || m.req.Header.Get("X-Gotify-Key") != "AppTok" || b["title"] != msg().Title || b["priority"] != 8.0 {
		t.Errorf("body: %v", b)
	}
	m.status, m.answer = 401, `{"error":"Unauthorized","errorCode":401,"errorDescription":"you need to provide a valid access token or user credentials to access this api"}`
	wantErr(t, send(t, ch), StepAnswer, true, "valid access token")
}

func TestWebhook(t *testing.T) {
	m := newMock(t)
	ch := config.Channel{Type: config.Webhook, URL: m.URL + "/hook", Method: "PUT", Headers: map[string]string{"X-Key": "s3cret"}}
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	b := m.json(t)
	if m.req.Method != "PUT" || m.req.Header.Get("X-Key") != "s3cret" || m.req.Header.Get("Content-Type") != "application/json" || b["title"] != msg().Title || b["kind"] != "service" || b["count"] != 1.0 ||
		b["items"].([]any)[0].(map[string]any)["device"] != "Heizung Bad" {
		t.Errorf("request: %v %v", m.req.Header, b)
	}
	// a template body
	ch.Method, ch.Body = "POST", `{"text": {{ headline | tojson }}, "rooms": "{{ items[0].rooms | join(",") }}", "n": {{ count }}}`
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	b = m.json(t)
	if b["text"] != msg().Title || b["rooms"] != "Bad" || b["n"] != 1.0 || m.req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("template: %s", m.body)
	}
	ch.Body = "{{ headline }} ({{ system }})"
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	if string(m.body) != msg().Title+" (ccu)" || !strings.HasPrefix(m.req.Header.Get("Content-Type"), "text/plain") {
		t.Errorf("text: %s %s", m.body, m.req.Header.Get("Content-Type"))
	}
	ch.Body = "{{ headline | nosuch }}"
	wantErr(t, send(t, ch), StepTemplate, true, "nosuch")
	// a redirect is not followed: the user gets the final address to enter
	ch.Body = ""
	r := httptest.NewServer(http.RedirectHandler(m.URL, http.StatusFound))
	defer r.Close()
	ch.URL = r.URL
	wantErr(t, send(t, ch), StepAnswer, true, "redirect")
}

func TestDiscordSlackMatrix(t *testing.T) {
	m := newMock(t)
	if err := send(t, config.Channel{Type: config.Discord, URL: m.URL + "/api/webhooks/1/tokentoken"}); err != nil {
		t.Fatal(err)
	}
	e := m.json(t)["embeds"].([]any)[0].(map[string]any)
	if e["title"] != msg().Title || e["description"] != msg().Body || e["color"] != float64(0xe74c3c) || e["url"] != "https://ccu.example/" {
		t.Errorf("discord: %v", e)
	}
	if err := send(t, config.Channel{Type: config.Slack, URL: m.URL}); err != nil {
		t.Fatal(err)
	}
	if b := m.json(t); b["text"] != "*Heizung Bad: Kommunikation gestört*\nBad · HmIP-RF 000A1BE9A1B2C3\n<https://ccu.example/>" {
		t.Errorf("slack: %v", b)
	}
	if err := send(t, config.Channel{Type: config.Matrix, URL: m.URL}); err != nil {
		t.Fatal(err)
	}
	if b := m.json(t); !strings.HasPrefix(b["text"].(string), msg().Title+"\n\n") || !strings.HasPrefix(b["html"].(string), "<b>Heizung Bad: Kommunikation gestört</b><br>") {
		t.Errorf("matrix: %v", b)
	}
	// the webhook URL is the credential: an error does not repeat it
	m.status, m.answer = 404, `{"message":"Unknown Webhook","code":10015}`
	err := send(t, config.Channel{Type: config.Discord, URL: m.URL + "/api/webhooks/1/tokentoken"})
	wantErr(t, err, StepAnswer, true, "Unknown Webhook")
	if strings.Contains(err.Error(), "tokentoken") {
		t.Errorf("the URL leaks: %v", err)
	}
}

func TestSignal(t *testing.T) {
	m := newMock(t)
	m.status = 201
	ch := config.Channel{Type: config.Signal, URL: m.URL, Sender: "+491700000000", Target: "+491711111111, group.abc", User: "api", Password: "pw"}
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	b := m.json(t)
	if m.req.URL.Path != "/v2/send" || b["number"] != "+491700000000" || len(b["recipients"].([]any)) != 2 || !strings.HasPrefix(b["message"].(string), msg().Title+"\n\n"+msg().Body) {
		t.Errorf("signal: %v", b)
	}
	if u, _, ok := m.req.BasicAuth(); !ok || u != "api" {
		t.Error("basic auth")
	}
	m.status, m.answer = 400, `{"error":"Unregistered user"}`
	wantErr(t, send(t, ch), StepAnswer, true, "Unregistered user")
}

func TestNetworkErrors(t *testing.T) {
	// nothing listens
	l := httptest.NewServer(nil)
	addr := l.URL
	l.Close()
	wantErr(t, send(t, config.Channel{Type: config.Gotify, URL: addr, Token: "x"}), StepConnect, false, "refused")
	// a name that does not resolve
	wantErr(t, send(t, config.Channel{Type: config.Gotify, URL: "http://no-such-host.invalid", Token: "x"}), StepResolve, false, "no-such-host.invalid")
	// a self-signed certificate: refused unless the channel says insecure
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "{}") }))
	defer s.Close()
	wantErr(t, send(t, config.Channel{Type: config.Gotify, URL: s.URL, Token: "x"}), StepTLS, true, "certificate")
	if err := send(t, config.Channel{Type: config.Gotify, URL: s.URL, Token: "x", Insecure: true}); err != nil {
		t.Errorf("insecure: %v", err)
	}
	// plain HTTP on a TLS URL
	p := httptest.NewServer(http.NotFoundHandler())
	defer p.Close()
	wantErr(t, send(t, config.Channel{Type: config.Gotify, URL: strings.Replace(p.URL, "http://", "https://", 1), Token: "x"}), StepTLS, true, "")
	if err := Send(context.Background(), config.Channel{Type: "fax"}, msg()); err == nil {
		t.Error("an unknown type sent")
	}
}

func TestClip(t *testing.T) {
	if got := clip("äöüäöü", 4); got != "äöü…" {
		t.Errorf("clip: %q", got)
	}
	if got := clip("abc", 3); got != "abc" {
		t.Errorf("clip: %q", got)
	}
}

func decodeMail(t *testing.T, data string) (hdr map[string]string, body string) {
	t.Helper()
	head, b64, ok := strings.Cut(data, "\r\n\r\n")
	if !ok {
		t.Fatalf("no header end: %q", data)
	}
	hdr = map[string]string{}
	for _, l := range strings.Split(head, "\r\n") {
		k, v, _ := strings.Cut(l, ": ")
		hdr[k] = v
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSuffix(strings.TrimSpace(b64), "."), "\r\n", ""))
	if err != nil {
		t.Fatalf("body: %v", err)
	}
	return hdr, string(raw)
}
