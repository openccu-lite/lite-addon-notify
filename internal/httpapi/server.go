// Package httpapi is the addon's own HTTP server: the Svelte page's files and its JSON API
// under one base path (/addons/notify/), which lighttpd proxies to the loopback port, and the
// inbound webhook at /inbound, outside that path - so the system's web server never reaches
// it and only a program on the system itself can. Every request under the base path carries
// the shell's session, confirmed through the kit (administrators only); writes carry the kit's
// CSRF header. docs/api.md documents the routes.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/ccu-addon-kit/go/addonkit"

	"github.com/openccu-lite/lite-addon-notify/internal/channel"
	"github.com/openccu-lite/lite-addon-notify/internal/config"
	"github.com/openccu-lite/lite-addon-notify/internal/deliver"
	"github.com/openccu-lite/lite-addon-notify/internal/engine"
	"github.com/openccu-lite/lite-addon-notify/internal/message"
	"github.com/openccu-lite/lite-addon-notify/internal/source"
	"github.com/openccu-lite/lite-addon-notify/internal/texts"
	"github.com/openccu-lite/lite-addon-notify/internal/tmpl"
)

// Options configure the server.
type Options struct {
	// BasePath is where the page is served, /addons/notify (no trailing slash).
	BasePath string
	// WWW is the directory with the built page.
	WWW     string
	Store   *config.Store
	Engine  *engine.Engine
	Sources *source.Sources
	Deliver *deliver.Dispatcher
	// Wrap is the session check; nil: none (development only).
	Wrap    func(http.Handler) http.Handler
	Version string
	Started time.Time
	Now     func() time.Time
	// Kick lets the daemon's loop run now (an inbound message, a rule's test).
	Kick func()
	// System names the system; InboundURL is the inbound webhook's address as a program on the
	// system reaches it.
	System     func() string
	InboundURL string
	Log        func(format string, args ...any)
}

// Routes lists every "METHOD /path" the API serves; the API document is held to it by a test.
var Routes []string

type server struct {
	o  Options
	mu sync.Mutex
	// inbound: the times of the last minute's requests, for its rate limit
	inbound []time.Time
}

func handle(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	Routes = append(Routes, pattern)
	mux.HandleFunc(pattern, h)
}

// InboundPath is the inbound webhook's path on the daemon's port.
const InboundPath = "/inbound"

// InboundPerMinute bounds the inbound webhook.
const InboundPerMinute = 60

// New builds the handler.
func New(o Options) http.Handler {
	o.BasePath = strings.TrimRight(o.BasePath, "/")
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Kick == nil {
		o.Kick = func() {}
	}
	if o.System == nil {
		o.System = func() string { return "" }
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	s := &server{o: o}
	Routes = nil
	api := http.NewServeMux()
	handle(api, "GET /api/v1/health", s.health)
	handle(api, "GET /api/v1/config", s.config)
	handle(api, "POST /api/v1/channels", s.putChannel)
	handle(api, "PUT /api/v1/channels/{id}", s.putChannel)
	handle(api, "DELETE /api/v1/channels/{id}", s.deleteChannel)
	handle(api, "POST /api/v1/channels/test", s.testChannel)
	handle(api, "POST /api/v1/rules", s.putRule)
	handle(api, "PUT /api/v1/rules/{id}", s.putRule)
	handle(api, "DELETE /api/v1/rules/{id}", s.deleteRule)
	handle(api, "POST /api/v1/rules/preview", s.previewRule)
	handle(api, "POST /api/v1/rules/{id}/test", s.testRule)
	handle(api, "PUT /api/v1/settings", s.putSettings)
	handle(api, "GET /api/v1/inbound", s.inboundInfo)
	handle(api, "POST /api/v1/inbound/rotate", s.inboundRotate)
	handle(api, "GET /api/v1/status", s.status)
	handle(api, "GET /api/v1/history", s.history)
	handle(api, "DELETE /api/v1/history", s.clearHistory)
	handle(api, "GET /api/v1/devices", s.devices)
	api.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		addonkit.WriteError(w, http.StatusNotFound, "not-found", r.URL.Path)
	})

	page := http.NewServeMux()
	page.Handle("/api/", addonkit.CSRF(api))
	page.Handle("/", addonkit.AppFiles(o.WWW))

	var h http.Handler = http.StripPrefix(o.BasePath, page)
	if o.Wrap != nil {
		h = o.Wrap(h)
	}
	root := http.NewServeMux()
	root.HandleFunc(InboundPath, s.inboundHook)
	root.Handle(o.BasePath+"/", guard(h))
	return root
}

func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; p != "/" && (path.Clean(p) != strings.TrimSuffix(p, "/") && path.Clean(p) != p) {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		addonkit.WriteError(w, http.StatusBadRequest, "invalid", err.Error())
		return false
	}
	return true
}

func writeErr(w http.ResponseWriter, err error) {
	var ve *config.ValidationError
	switch {
	case errors.Is(err, config.ErrNotFound):
		addonkit.WriteError(w, http.StatusNotFound, "not-found", err.Error())
	case errors.As(err, &ve):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "invalid", "field": ve.Field, "message": ve.Error()})
	default:
		addonkit.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// GET /api/v1/health: {ok, version, uptime_s}.
func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "version": s.o.Version, "uptime_s": int(s.o.Now().Sub(s.o.Started).Seconds())})
}

// configAnswer is GET /config: the configuration without its secrets, and what the page needs
// to build its forms.
type configAnswer struct {
	config.Config
	Defaults struct {
		Settings config.Settings `json:"settings"`
		Rules    []config.Rule   `json:"rules"`
	} `json:"defaults"`
	Version string `json:"version"`
	// System is the system's name as the messages say it.
	System string `json:"system"`
	// Types are the channel types, WarningIDs the warning ids with a title of their own,
	// Filters the template helpers.
	Types      []string `json:"types"`
	WarningIDs []string `json:"warning_ids"`
	Filters    any      `json:"filters"`
}

func (s *server) answer(w http.ResponseWriter) {
	c := s.o.Store.Public()
	a := configAnswer{Config: c, Version: s.o.Version, Types: config.Types, WarningIDs: texts.KnownWarnings(), Filters: tmpl.Filters}
	a.Defaults.Settings = config.DefaultSettings
	a.Defaults.Rules = config.DefaultRules(c.Settings.Language)
	a.System = c.Settings.SystemName
	if a.System == "" {
		a.System = s.o.System()
	}
	writeJSON(w, 200, a)
}

func (s *server) config(w http.ResponseWriter, r *http.Request) { s.answer(w) }

// POST /channels, PUT /channels/{id}: a channel; a secret left empty on an update is kept.
func (s *server) putChannel(w http.ResponseWriter, r *http.Request) {
	var ch config.Channel
	if !decode(w, r, &ch) {
		return
	}
	ch.ID = r.PathValue("id")
	if r.Method == http.MethodPut && ch.ID == "" {
		writeErr(w, config.ErrNotFound)
		return
	}
	if _, err := s.o.Store.PutChannel(ch); err != nil {
		writeErr(w, err)
		return
	}
	s.answer(w)
}

func (s *server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	if err := s.o.Store.DeleteChannel(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	s.answer(w)
}

// POST /channels/test: sends a test message with the given settings (an id with empty secrets
// uses the stored ones). 200 {ok}, or 502 {error: "send", step, message} with the target's answer.
func (s *server) testChannel(w http.ResponseWriter, r *http.Request) {
	var ch config.Channel
	if !decode(w, r, &ch) {
		return
	}
	if ch.ID != "" {
		old, ok := s.o.Store.Channel(ch.ID)
		if !ok {
			writeErr(w, config.ErrNotFound)
			return
		}
		ch = config.Merge(ch, old)
	}
	id := ch.ID
	ch, err := config.NormalizeChannel(ch)
	if err != nil {
		writeErr(w, err)
		return
	}
	ch.ID = id
	c := s.o.Store.Get()
	lang := c.Settings.Language
	system := c.Settings.SystemName
	if system == "" {
		system = s.o.System()
	}
	m := message.Message{ID: "test", Kind: message.KindTest, Title: texts.T(lang, "test-title"), Body: texts.T(lang, "test-body", system), Priority: message.Normal, System: system, Time: s.o.Now(), Items: []message.Item{}}
	if c.Settings.BaseURL != "" {
		m.Link = c.Settings.BaseURL + "/"
	}
	ctx, cancel := context.WithTimeout(r.Context(), channel.Timeout)
	defer cancel()
	if err := s.o.Deliver.Test(ctx, ch, m); err != nil {
		var ce *channel.Error
		if errors.As(err, &ce) {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": "send", "step": ce.Step, "message": ce.Msg})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "send", "step": channel.StepSend, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *server) putRule(w http.ResponseWriter, r *http.Request) {
	var rule config.Rule
	if !decode(w, r, &rule) {
		return
	}
	rule.ID = r.PathValue("id")
	if r.Method == http.MethodPut && rule.ID == "" {
		writeErr(w, config.ErrNotFound)
		return
	}
	if _, err := s.o.Store.PutRule(rule); err != nil {
		writeErr(w, err)
		return
	}
	s.answer(w)
}

func (s *server) deleteRule(w http.ResponseWriter, r *http.Request) {
	if err := s.o.Store.DeleteRule(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	s.answer(w)
}

// POST /rules/preview: a rule as the form holds it -> the message it would send, made of
// sample events: {title, body, priority}. 422 names a broken template.
func (s *server) previewRule(w http.ResponseWriter, r *http.Request) {
	var rule config.Rule
	if !decode(w, r, &rule) {
		return
	}
	if rule.Name == "" {
		rule.Name = "preview"
	}
	rule, err := config.NormalizeRule(rule, s.o.Store.Get().Channels)
	if err != nil {
		writeErr(w, err)
		return
	}
	m := s.o.Engine.Sample(rule, s.o.Now())
	writeJSON(w, 200, map[string]any{"title": m.Title, "body": m.Body, "priority": m.Priority})
}

// POST /rules/{id}/test: sends the rule's sample message to its channels (through the queues).
func (s *server) testRule(w http.ResponseWriter, r *http.Request) {
	c := s.o.Store.Get()
	for _, rule := range c.Rules {
		if rule.ID != r.PathValue("id") {
			continue
		}
		var ids []string
		for _, ch := range c.Channels {
			if ch.Enabled && (len(rule.Channels) == 0 || contains(rule.Channels, ch.ID)) {
				ids = append(ids, ch.ID)
			}
		}
		if len(ids) == 0 {
			addonkit.WriteError(w, http.StatusConflict, "no-channel", "the rule has no enabled channel")
			return
		}
		m := s.o.Engine.Sample(rule, s.o.Now())
		s.o.Deliver.Enqueue(ids, m)
		writeJSON(w, 200, map[string]any{"ok": true, "channels": ids, "title": m.Title})
		return
	}
	writeErr(w, config.ErrNotFound)
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (s *server) putSettings(w http.ResponseWriter, r *http.Request) {
	var st config.Settings
	if !decode(w, r, &st) {
		return
	}
	if _, err := s.o.Store.PutSettings(st); err != nil {
		writeErr(w, err)
		return
	}
	s.answer(w)
}

// GET /inbound: the inbound webhook's address and secret, for the page to show.
func (s *server) inboundInfo(w http.ResponseWriter, r *http.Request) {
	in := s.o.Store.Get().Settings.Inbound
	writeJSON(w, 200, map[string]any{"enabled": in.Enabled, "url": s.o.InboundURL, "secret": in.Secret, "per_minute": InboundPerMinute})
}

// POST /inbound/rotate: a new secret; the old one stops working at once.
func (s *server) inboundRotate(w http.ResponseWriter, r *http.Request) {
	secret, err := s.o.Store.RotateSecret()
	if err != nil {
		writeErr(w, err)
		return
	}
	in := s.o.Store.Get().Settings.Inbound
	writeJSON(w, 200, map[string]any{"enabled": in.Enabled, "url": s.o.InboundURL, "secret": secret, "per_minute": InboundPerMinute})
}

// GET /status: the sources, what is open, the channels' delivery state.
func (s *server) status(w http.ResponseWriter, r *http.Request) {
	c := s.o.Store.Get()
	now := s.o.Now()
	open := s.o.Sources.Open(c.Settings.Language)
	open.Datapoints = s.o.Engine.OpenDatapoints()
	list := func(l []message.Item) []message.Item {
		if l == nil {
			return []message.Item{}
		}
		return l
	}
	all := s.o.Deliver.Status()
	channels := map[string]deliver.Status{}
	for _, ch := range c.Channels {
		channels[ch.ID] = all[ch.ID]
	}
	out := map[string]any{
		"now":      now.UnixMilli(),
		"sources":  s.o.Sources.States(),
		"open":     map[string]any{"warnings": list(open.Warnings), "service": list(open.Service), "datapoints": list(open.Datapoints)},
		"channels": channels,
		"pending":  s.o.Engine.PendingCount(),
	}
	if end := engine.QuietEnd(c.Settings.Quiet, now); !end.IsZero() {
		out["quiet_until"] = end.UnixMilli()
	}
	writeJSON(w, 200, out)
}

func (s *server) history(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.o.Deliver.History())
}

func (s *server) clearHistory(w http.ResponseWriter, r *http.Request) {
	s.o.Deliver.Clear()
	writeJSON(w, 200, map[string]any{"ok": true})
}

// GET /devices: the devices and channels with the datapoints the system holds values of, for
// the datapoint rule's picker.
func (s *server) devices(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.o.Sources.Devices(ctx, s.o.Store.Get().Settings.Language)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "system", "message": err.Error()})
		return
	}
	writeJSON(w, 200, l)
}

func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *server) inboundAllowed(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := s.inbound[:0]
	for _, t := range s.inbound {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	s.inbound = keep
	if len(s.inbound) >= InboundPerMinute {
		return false
	}
	s.inbound = append(s.inbound, now)
	return true
}

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return ""
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// inboundPriority reads low, normal or high, or a number (ntfy's 1 to 5, Pushover's -2 to 2).
func inboundPriority(m map[string]any) string {
	switch v := m["priority"].(type) {
	case string:
		switch strings.ToLower(v) {
		case "low", "min":
			return message.Low
		case "high", "urgent", "max":
			return message.High
		case "normal", "default":
			return message.Normal
		}
	case float64:
		switch {
		case v >= 4:
			return message.High
		case v <= -1 || v == 1 || v == 2:
			return message.Low
		}
		return message.Normal
	}
	return ""
}

// POST /inbound: the inbound webhook. The secret as Authorization: Bearer, X-Notify-Secret or
// ?secret=; a JSON body {title, text|message|body, priority?, link|url?, source|kind?, key?,
// resolved?} or plain text with ?title=. 202 {ok}.
func (s *server) inboundHook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		addonkit.WriteError(w, http.StatusMethodNotAllowed, "method", "POST only")
		return
	}
	in := s.o.Store.Get().Settings.Inbound
	// the listener is on the loopback; the check holds if that ever changes
	if !in.Enabled || !loopback(r.RemoteAddr) {
		addonkit.WriteError(w, http.StatusNotFound, "disabled", "the inbound webhook is switched off")
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == "" {
		got = r.Header.Get("X-Notify-Secret")
	}
	if got == "" {
		got = r.URL.Query().Get("secret")
	}
	if in.Secret == "" || subtle.ConstantTimeCompare([]byte(got), []byte(in.Secret)) != 1 {
		addonkit.WriteError(w, http.StatusUnauthorized, "secret", "the secret is missing or wrong")
		return
	}
	now := s.o.Now()
	if !s.inboundAllowed(now) {
		w.Header().Set("Retry-After", "60")
		addonkit.WriteError(w, http.StatusTooManyRequests, "rate", "more than "+strconv.Itoa(InboundPerMinute)+" messages a minute")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		addonkit.WriteError(w, http.StatusRequestEntityTooLarge, "too-large", "at most 1 MiB")
		return
	}
	q := r.URL.Query()
	ev := engine.Event{Source: config.SrcInbound, At: now, Tag: q.Get("source")}
	title, text := q.Get("title"), ""
	ctype, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	trimmed := strings.TrimSpace(string(body))
	if ctype == "application/json" || (ctype != "text/plain" && strings.HasPrefix(trimmed, "{")) {
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			addonkit.WriteError(w, http.StatusBadRequest, "invalid", "the body is not a JSON object: "+err.Error())
			return
		}
		if t := str(m, "title", "subject"); t != "" {
			title = t
		}
		text = str(m, "text", "message", "body")
		if t := str(m, "source", "kind", "tag"); t != "" && ev.Tag == "" {
			ev.Tag = t
		}
		ev.Key = str(m, "key")
		ev.Priority = inboundPriority(m)
		ev.Resolved, _ = m["resolved"].(bool)
		ev.Item.Link = str(m, "link", "url", "click")
		ev.Item.Device = str(m, "name", "device")
	} else {
		text = trimmed
	}
	if title == "" {
		first, rest, _ := strings.Cut(text, "\n")
		title, text = clip(strings.TrimSpace(first), 120), strings.TrimSpace(rest)
	}
	if title == "" {
		addonkit.WriteError(w, http.StatusBadRequest, "invalid", "a title or a text is needed")
		return
	}
	// a link into the system's page is a path; anything else stays out of the message's link
	if !strings.HasPrefix(ev.Item.Link, "/") {
		if ev.Item.Link != "" && text != "" {
			text += "\n" + ev.Item.Link
		} else if ev.Item.Link != "" {
			text = ev.Item.Link
		}
		ev.Item.Link = ""
	}
	ev.Item.Title, ev.Item.Detail = clip(title, 250), clip(text, 4000)
	if ev.Key == "" {
		ev.Key = "in-" + strconv.FormatInt(now.UnixNano(), 36)
		ev.Once = true
	} else {
		ev.Key = clip(ev.Tag+"/"+ev.Key, 200)
	}
	s.o.Engine.Handle(ev, now)
	s.o.Kick()
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}
