// Package channel sends a message to one target: Pushover, Telegram, email, ntfy, Gotify, a
// generic webhook, Discord, Slack, Matrix (through a webhook bridge), Signal (through a
// signal-cli REST server) and MQTT. Every send is one attempt; the queue, the retries and the
// rate limit are the deliver package's. An error names the step that failed and carries the
// target's own answer, so the Test button can say what is wrong.
package channel

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openccu-lite/lite-addon-notify/internal/config"
	"github.com/openccu-lite/lite-addon-notify/internal/message"
	"github.com/openccu-lite/lite-addon-notify/internal/tmpl"
)

// The steps an Error names.
const (
	StepConfig    = "config"
	StepTemplate  = "template"
	StepResolve   = "resolve"
	StepConnect   = "connect"
	StepTLS       = "tls"
	StepLogin     = "login"
	StepSender    = "sender"
	StepRecipient = "recipient"
	StepSend      = "send"
	StepAnswer    = "answer"
)

// Error is a failed send: the step and what the target (or the network) said.
type Error struct {
	Step string
	Msg  string
	// Permanent says that a retry cannot help (a refused login, an unknown recipient, a 4xx).
	Permanent bool
}

func (e *Error) Error() string { return e.Step + ": " + e.Msg }

func fail(step, format string, args ...any) *Error {
	return &Error{Step: step, Msg: fmt.Sprintf(format, args...)}
}

func permanent(step, format string, args ...any) *Error {
	return &Error{Step: step, Msg: fmt.Sprintf(format, args...), Permanent: true}
}

// The default API addresses; a channel's URL replaces them (a test, a proxy).
const (
	PushoverAPI = "https://api.pushover.net"
	TelegramAPI = "https://api.telegram.org"
)

// Timeout bounds one send.
var Timeout = 30 * time.Second

// UserAgent is what the HTTP channels send.
var UserAgent = "lite-addon-notify"

// Send sends the message to the channel, once.
func Send(ctx context.Context, ch config.Channel, m message.Message) error {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	switch ch.Type {
	case config.Pushover:
		return sendPushover(ctx, ch, m)
	case config.Telegram:
		return sendTelegram(ctx, ch, m)
	case config.Email:
		return sendEmail(ctx, ch, m)
	case config.Ntfy:
		return sendNtfy(ctx, ch, m)
	case config.Gotify:
		return sendGotify(ctx, ch, m)
	case config.Webhook:
		return sendWebhook(ctx, ch, m)
	case config.Discord:
		return sendDiscord(ctx, ch, m)
	case config.Slack:
		return sendSlack(ctx, ch, m)
	case config.Matrix:
		return sendMatrix(ctx, ch, m)
	case config.Signal:
		return sendSignal(ctx, ch, m)
	case config.MQTT:
		return sendMQTT(ctx, ch, m)
	}
	return permanent(StepConfig, "unknown channel type %q", ch.Type)
}

// clip shortens a text to at most n characters, with an ellipsis.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// bodyWithLink is the body, with the link below it for a channel that has no link of its own.
func bodyWithLink(m message.Message) string {
	b := m.Body
	if m.Link != "" {
		if b != "" {
			b += "\n\n"
		}
		b += m.Link
	}
	return b
}

// bodyOrTitle is the body, or the title for a message without one (a channel that needs text).
func bodyOrTitle(m message.Message) string {
	if strings.TrimSpace(m.Body) == "" {
		return m.Title
	}
	return m.Body
}

func httpClient(ch config.Channel) *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		DisableKeepAlives:     true,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: ch.Insecure}, //nolint:gosec // the user's choice for a self-signed server
	}
	return &http.Client{Transport: tr, Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// netError puts a network error into words: which step, and the reason without Go's wrapping.
func netError(err error) *Error {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return fail(StepResolve, "the name %s cannot be resolved", dns.Name)
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return permanent(StepTLS, "the server's certificate is not trusted: %v", certErr.Err)
	}
	var recErr tls.RecordHeaderError
	if errors.As(err, &recErr) {
		return permanent(StepTLS, "the server does not speak TLS on this port")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fail(StepConnect, "no answer in time")
	}
	var op *net.OpError
	if errors.As(err, &op) {
		if op.Op == "dial" {
			return fail(StepConnect, "%s: %v", op.Addr, unwrapSys(op.Err))
		}
		return fail(StepSend, "%v", unwrapSys(op.Err))
	}
	msg := err.Error()
	if strings.Contains(msg, "server gave HTTP response to HTTPS client") {
		return permanent(StepTLS, "the server speaks plain HTTP on this port: the address begins with http://, not https://")
	}
	for _, mark := range []string{"tls: ", "x509: "} {
		if i := strings.Index(msg, mark); i >= 0 {
			return permanent(StepTLS, "%s", msg[i:])
		}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return fail(StepConnect, "%v", ue.Err)
	}
	return fail(StepSend, "%v", err)
}

func unwrapSys(err error) error {
	for {
		u := errors.Unwrap(err)
		if u == nil {
			return err
		}
		err = u
	}
}

// do sends the request and answers the body of a 2xx; anything else is an Error with the
// target's answer, picked out of its JSON by the given keys when it is JSON.
func do(ch config.Channel, req *http.Request, name string, errorKeys ...string) ([]byte, error) {
	req.Header.Set("User-Agent", UserAgent)
	res, err := httpClient(ch).Do(req)
	if err != nil {
		return nil, netError(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode >= 200 && res.StatusCode <= 299 {
		return body, nil
	}
	said := answerText(body, errorKeys...)
	e := fail(StepAnswer, "%s answered %s%s", name, res.Status, said)
	// a 4xx is the request's fault (a wrong token, an unknown chat) and will not get better;
	// 408 and 429 are the exceptions
	if res.StatusCode >= 400 && res.StatusCode < 500 && res.StatusCode != 408 && res.StatusCode != 429 {
		e.Permanent = true
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		e.Permanent = true
		e.Msg += " (a redirect to " + res.Header.Get("Location") + ": use the final address)"
	}
	return nil, e
}

func answerText(body []byte, keys ...string) string {
	var j map[string]any
	if json.Unmarshal(body, &j) == nil {
		for _, k := range keys {
			switch v := j[k].(type) {
			case string:
				if v != "" {
					return ": " + clip(v, 300)
				}
			case []any:
				var parts []string
				for _, p := range v {
					parts = append(parts, fmt.Sprint(p))
				}
				if len(parts) > 0 {
					return ": " + clip(strings.Join(parts, "; "), 300)
				}
			}
		}
	}
	s := strings.TrimSpace(string(body))
	if s == "" || strings.HasPrefix(s, "<") {
		return ""
	}
	return ": " + clip(s, 300)
}

func postJSON(ctx context.Context, ch config.Channel, rawURL string, v any, name string, hdr map[string]string, errorKeys ...string) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, permanent(StepConfig, "%v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(b))
	if err != nil {
		return nil, permanent(StepConfig, "the address is not a URL")
	}
	req.Header.Set("Content-Type", "application/json")
	for k, val := range hdr {
		req.Header.Set(k, val)
	}
	return do(ch, req, name, errorKeys...)
}

func sendPushover(ctx context.Context, ch config.Channel, m message.Message) error {
	api := ch.URL
	if api == "" {
		api = PushoverAPI
	}
	prio := map[string]string{message.Low: "-1", message.Normal: "0", message.High: "1"}[m.Priority]
	if prio == "" {
		prio = "0"
	}
	form := url.Values{"token": {ch.Token}, "user": {ch.UserKey}, "title": {clip(m.Title, 250)}, "message": {clip(bodyOrTitle(m), 1024)}, "priority": {prio}, "timestamp": {strconv.FormatInt(m.Time.Unix(), 10)}}
	if ch.Target != "" {
		form.Set("device", ch.Target)
	}
	if ch.Sound != "" {
		form.Set("sound", ch.Sound)
	}
	if m.Link != "" {
		form.Set("url", m.Link)
		form.Set("url_title", m.System)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api+"/1/messages.json", strings.NewReader(form.Encode()))
	if err != nil {
		return permanent(StepConfig, "the address is not a URL")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, err = do(ch, req, "Pushover", "errors")
	return err
}

func sendTelegram(ctx context.Context, ch config.Channel, m message.Message) error {
	api := ch.URL
	if api == "" {
		api = TelegramAPI
	}
	text := "<b>" + html.EscapeString(m.Title) + "</b>"
	if b := bodyWithLink(m); strings.TrimSpace(b) != "" && b != m.Title {
		text += "\n" + html.EscapeString(b)
	}
	body := map[string]any{"chat_id": ch.Target, "text": clip(text, 4096), "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}}
	if m.Priority == message.Low {
		body["disable_notification"] = true
	}
	if ch.Thread != "" {
		n, err := strconv.Atoi(ch.Thread)
		if err != nil {
			return permanent(StepConfig, "the topic id %q is not a number", ch.Thread)
		}
		body["message_thread_id"] = n
	}
	_, err := postJSON(ctx, ch, api+"/bot"+ch.Token+"/sendMessage", body, "Telegram", nil, "description")
	var e *Error
	if errors.As(err, &e) {
		// the bot token is part of the URL: never let it into an error text
		e.Msg = strings.ReplaceAll(e.Msg, ch.Token, "<token>")
	}
	return err
}

func sendNtfy(ctx context.Context, ch config.Channel, m message.Message) error {
	prio := map[string]int{message.Low: 2, message.Normal: 3, message.High: 4}[m.Priority]
	if prio == 0 {
		prio = 3
	}
	tag := map[string]string{message.KindWarning: "warning", message.KindService: "wrench", message.KindDatapoint: "bell", message.KindSystem: "computer", message.KindDigest: "clipboard", message.KindInbound: "incoming_envelope", message.KindTest: "white_check_mark"}[m.Kind]
	if m.Resolved {
		tag = "white_check_mark"
	}
	body := map[string]any{"topic": ch.Target, "title": clip(m.Title, 250), "message": clip(bodyOrTitle(m), 3800), "priority": prio}
	if tag != "" {
		body["tags"] = []string{tag}
	}
	if m.Link != "" {
		body["click"] = m.Link
	}
	hdr := map[string]string{}
	if ch.Token != "" {
		hdr["Authorization"] = "Bearer " + ch.Token
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.URL, bytes.NewReader(b))
	if err != nil {
		return permanent(StepConfig, "the address is not a URL")
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if ch.Token == "" && ch.User != "" {
		req.SetBasicAuth(ch.User, ch.Password)
	}
	_, err = do(ch, req, "ntfy", "error")
	return err
}

func sendGotify(ctx context.Context, ch config.Channel, m message.Message) error {
	prio := map[string]int{message.Low: 2, message.Normal: 5, message.High: 8}[m.Priority]
	if prio == 0 {
		prio = 5
	}
	body := map[string]any{"title": clip(m.Title, 250), "message": bodyOrTitle(m), "priority": prio}
	if m.Link != "" {
		body["extras"] = map[string]any{"client::notification": map[string]any{"click": map[string]any{"url": m.Link}}}
	}
	_, err := postJSON(ctx, ch, ch.URL+"/message", body, "Gotify", map[string]string{"X-Gotify-Key": ch.Token}, "errorDescription", "error")
	return err
}

func sendWebhook(ctx context.Context, ch config.Channel, m message.Message) error {
	var body []byte
	ctype := ch.ContentType
	if ch.Body == "" {
		body, _ = json.Marshal(m)
		if ctype == "" {
			ctype = "application/json"
		}
	} else {
		s, err := tmpl.Render(ch.Body, m.Context())
		if err != nil {
			return permanent(StepTemplate, "%v", err)
		}
		body = []byte(s)
		if ctype == "" {
			ctype = "text/plain; charset=utf-8"
			if json.Valid(body) {
				ctype = "application/json"
			}
		}
	}
	method := ch.Method
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, ch.URL, bytes.NewReader(body))
	if err != nil {
		return permanent(StepConfig, "the address is not a URL")
	}
	req.Header.Set("Content-Type", ctype)
	for k, v := range ch.Headers {
		req.Header.Set(k, v)
	}
	_, err = do(ch, req, "the webhook", "error", "message")
	return err
}

func sendDiscord(ctx context.Context, ch config.Channel, m message.Message) error {
	color := 0xf39c12
	switch {
	case m.Resolved || m.Kind == message.KindTest:
		color = 0x2ecc71
	case m.Priority == message.High:
		color = 0xe74c3c
	case m.Priority == message.Low:
		color = 0x95a5a6
	}
	embed := map[string]any{"title": clip(m.Title, 256), "color": color, "timestamp": m.Time.UTC().Format(time.RFC3339)}
	if b := m.Body; strings.TrimSpace(b) != "" && b != m.Title {
		embed["description"] = clip(b, 4000)
	}
	if m.Link != "" {
		embed["url"] = m.Link
	}
	if m.System != "" {
		embed["footer"] = map[string]any{"text": clip(m.System, 200)}
	}
	_, err := postJSON(ctx, ch, ch.URL, map[string]any{"embeds": []any{embed}}, "Discord", nil, "message")
	return hideURL(err, ch.URL)
}

func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func sendSlack(ctx context.Context, ch config.Channel, m message.Message) error {
	text := "*" + slackEscape(m.Title) + "*"
	if b := m.Body; strings.TrimSpace(b) != "" && b != m.Title {
		text += "\n" + slackEscape(b)
	}
	if m.Link != "" {
		text += "\n<" + m.Link + ">"
	}
	_, err := postJSON(ctx, ch, ch.URL, map[string]any{"text": clip(text, 30000)}, "Slack", nil, "error")
	return hideURL(err, ch.URL)
}

func sendMatrix(ctx context.Context, ch config.Channel, m message.Message) error {
	h := "<b>" + html.EscapeString(m.Title) + "</b>"
	if b := m.Body; strings.TrimSpace(b) != "" && b != m.Title {
		h += "<br>" + strings.ReplaceAll(html.EscapeString(b), "\n", "<br>")
	}
	if m.Link != "" {
		h += `<br><a href="` + html.EscapeString(m.Link) + `">` + html.EscapeString(m.Link) + "</a>"
	}
	text := m.Text()
	if m.Link != "" {
		text += "\n" + m.Link
	}
	_, err := postJSON(ctx, ch, ch.URL, map[string]any{"text": text, "html": h, "username": m.System}, "the Matrix webhook", nil, "error", "message")
	return hideURL(err, ch.URL)
}

// hideURL keeps a webhook URL, which is the credential, out of an error text.
func hideURL(err error, raw string) error {
	var e *Error
	if errors.As(err, &e) && raw != "" {
		e.Msg = strings.ReplaceAll(e.Msg, raw, "<webhook URL>")
		if u, perr := url.Parse(raw); perr == nil && u.Path != "" && len(u.Path) > 8 {
			e.Msg = strings.ReplaceAll(e.Msg, u.Path, "/…")
		}
	}
	return err
}

func sendSignal(ctx context.Context, ch config.Channel, m message.Message) error {
	text := m.Text()
	if m.Link != "" {
		text += "\n\n" + m.Link
	}
	b, _ := json.Marshal(map[string]any{"message": text, "number": ch.Sender, "recipients": config.SplitList(ch.Target)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.URL+"/v2/send", bytes.NewReader(b))
	if err != nil {
		return permanent(StepConfig, "the address is not a URL")
	}
	req.Header.Set("Content-Type", "application/json")
	if ch.User != "" {
		req.SetBasicAuth(ch.User, ch.Password)
	}
	_, err = do(ch, req, "the Signal server", "error")
	return err
}
