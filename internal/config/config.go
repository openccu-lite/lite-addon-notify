// Package config is the addon's configuration: the settings, the channels with their
// credentials and the rules, in one JSON file (notify.json, 0600) in the configuration
// directory, written atomically. The API never answers a secret: Public blanks them and says
// which are set; a save with an empty secret keeps the stored one.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/openccu-lite/lite-addon-notify/internal/message"
	"github.com/openccu-lite/lite-addon-notify/internal/texts"
	"github.com/openccu-lite/lite-addon-notify/internal/tmpl"
)

// The channel types.
const (
	Pushover = "pushover"
	Telegram = "telegram"
	Email    = "email"
	Ntfy     = "ntfy"
	Gotify   = "gotify"
	Webhook  = "webhook"
	Discord  = "discord"
	Slack    = "slack"
	Matrix   = "matrix"
	Signal   = "signal"
	MQTT     = "mqtt"
)

// Types lists the channel types in the order the page offers them.
var Types = []string{Pushover, Telegram, Email, Ntfy, Gotify, Signal, Discord, Slack, Matrix, Webhook, MQTT}

// The rule sources.
const (
	SrcWarning   = message.KindWarning
	SrcService   = message.KindService
	SrcDatapoint = message.KindDatapoint
	SrcSystem    = message.KindSystem
	SrcInbound   = message.KindInbound
	SrcDigest    = message.KindDigest
)

// Sources lists the rule sources.
var Sources = []string{SrcWarning, SrcService, SrcDatapoint, SrcSystem, SrcInbound, SrcDigest}

// Channel is one target. The fields are a union; Fields says which a type reads.
type Channel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
	// URL: the server (ntfy, Gotify, Signal), the webhook URL (webhook, Discord, Slack, Matrix:
	// a secret there), or another API address for Pushover and Telegram (empty: theirs).
	URL string `json:"url,omitempty"`
	// Token: Pushover's application token, Telegram's bot token, ntfy's access token, Gotify's
	// application token. Secret.
	Token string `json:"token,omitempty"`
	// UserKey: Pushover's user or group key. Secret.
	UserKey string `json:"user_key,omitempty"`
	// User and Password: SMTP, ntfy, the Signal server, MQTT. The password is secret.
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	// Target: Telegram's chat id, ntfy's topic, the recipients of an email or a Signal message
	// (comma-separated), MQTT's topic template, Pushover's device.
	Target string `json:"target,omitempty"`
	// Sender: the email's From, Signal's own number.
	Sender string `json:"sender,omitempty"`
	// Host, Port and Security: SMTP (starttls, tls, none) and MQTT (none, tls).
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Security string `json:"security,omitempty"`
	// Insecure accepts any server certificate (a self-signed server).
	Insecure bool `json:"insecure,omitempty"`
	// Method, Headers, ContentType and Body: the webhook. Body is a template; empty sends the
	// message as JSON. Header values are secret. MQTT reads Body as its payload template.
	Method      string            `json:"method,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	ContentType string            `json:"content_type,omitempty"`
	Body        string            `json:"body,omitempty"`
	// Thread: Telegram's topic id. Sound: Pushover's sound. QoS and Retain: MQTT.
	Thread string `json:"thread,omitempty"`
	Sound  string `json:"sound,omitempty"`
	QoS    int    `json:"qos,omitempty"`
	Retain bool   `json:"retain,omitempty"`

	// Secrets says which secrets are set, in the public view only (never stored).
	Secrets map[string]bool `json:"secrets,omitempty"`
	// Clear names secrets to remove, on a save only (never stored).
	Clear []string `json:"clear,omitempty"`
}

// urlSecret says whether the type's URL is itself the credential.
func urlSecret(typ string) bool { return typ == Discord || typ == Slack || typ == Matrix }

// Datapoint is a datapoint rule's condition.
type Datapoint struct {
	Interface string `json:"interface"`
	Address   string `json:"address"`
	Key       string `json:"key"`
	// Op is true, false, eq, ne, gt, ge, lt or le; Value what eq to le compare with.
	Op    string `json:"op"`
	Value string `json:"value,omitempty"`
	// ForS: the condition must hold that many seconds before the rule fires.
	ForS int `json:"for_s,omitempty"`
	// Label is what the message says is the matter ("Fenster offen"); empty: the condition.
	Label string `json:"label,omitempty"`
}

// Rule sends the events of one source, filtered, to channels.
type Rule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
	// Channels are channel ids; empty means every enabled channel.
	Channels []string `json:"channels"`
	Priority string   `json:"priority"`
	// Resolved: also say when it is over.
	Resolved bool `json:"resolved"`
	// BatchS is the batching window in seconds; 0 sends at once.
	BatchS int `json:"batch_s"`
	// IgnoreQuiet sends during the quiet hours too.
	IgnoreQuiet bool `json:"ignore_quiet"`

	// Severities (warning): error, warning, info. ExcludeIDs: warning ids to leave out.
	Severities []string `json:"severities,omitempty"`
	ExcludeIDs []string `json:"exclude_ids,omitempty"`
	// Kinds: the service kinds (service) or the system events (system).
	Kinds []string `json:"kinds,omitempty"`
	// Exclude (service): device addresses to leave out. HoldS: a message must stand that long.
	Exclude []string `json:"exclude,omitempty"`
	HoldS   int      `json:"hold_s,omitempty"`
	// Datapoint (datapoint): the condition.
	Datapoint *Datapoint `json:"datapoint,omitempty"`
	// Tag (inbound): the sender's source to match; empty matches all.
	Tag string `json:"tag,omitempty"`
	// Time (digest): the time of day, HH:MM. OnlyOpen: nothing when all is clear.
	Time     string `json:"time,omitempty"`
	OnlyOpen bool   `json:"only_open,omitempty"`
	// Title and Body replace the default templates.
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

// Quiet is the quiet-hours window, From and To as HH:MM in the system's time zone.
type Quiet struct {
	Enabled bool   `json:"enabled"`
	From    string `json:"from"`
	To      string `json:"to"`
}

// Inbound is the inbound webhook.
type Inbound struct {
	Enabled bool `json:"enabled"`
	// Secret is generated here; the public view blanks it (GET /inbound/secret shows it).
	Secret string `json:"secret,omitempty"`
}

// Settings are the addon's general settings.
type Settings struct {
	// Language of the messages: de or en.
	Language string `json:"language"`
	// SystemName names the system in the messages; empty: the host name.
	SystemName string `json:"system_name"`
	// BaseURL is the system's address as the receiver reaches it (https://ccu.example); with it
	// a message links to the system's page.
	BaseURL string `json:"base_url"`
	// TitlePrefix puts the system's name in front of every title.
	TitlePrefix bool  `json:"title_prefix"`
	Quiet       Quiet `json:"quiet"`
	// RepeatMin: the same subject is not announced again within that many minutes.
	RepeatMin int `json:"repeat_min"`
	// RatePerHour: at most that many messages per channel in an hour; 0: no limit.
	RatePerHour int     `json:"rate_per_hour"`
	Inbound     Inbound `json:"inbound"`
}

// Config is the whole file.
type Config struct {
	Settings Settings  `json:"settings"`
	Channels []Channel `json:"channels"`
	Rules    []Rule    `json:"rules"`
}

// DefaultSettings are a fresh system's.
var DefaultSettings = Settings{Language: "de", Quiet: Quiet{From: "22:00", To: "07:00"}, RepeatMin: 60, RatePerHour: 30}

// DefaultRules are the rules a fresh configuration starts with: they send to every channel, so
// the first channel a user adds receives the warnings, the service messages and the system
// events at once. The digest is there, switched off.
func DefaultRules(lang string) []Rule {
	return []Rule{
		{ID: "warnings", Name: texts.T(lang, "default-warnings"), Enabled: true, Source: SrcWarning, Channels: []string{}, Priority: message.Normal, Resolved: true, BatchS: 60, Severities: []string{"error", "warning"}},
		{ID: "service", Name: texts.T(lang, "default-service"), Enabled: true, Source: SrcService, Channels: []string{}, Priority: message.Normal, Resolved: true, BatchS: 60, HoldS: 120, Kinds: []string{"unreach", "fault", "lowbat", "other"}},
		{ID: "system", Name: texts.T(lang, "default-system"), Enabled: true, Source: SrcSystem, Channels: []string{}, Priority: message.Normal, Resolved: true, Kinds: []string{texts.SysUpdateAvailable, texts.SysUpdateInstalled, texts.SysBackupFailed}},
		{ID: "digest", Name: texts.T(lang, "default-digest"), Enabled: false, Source: SrcDigest, Channels: []string{}, Priority: message.Low, Time: "08:00"},
	}
}

// ErrNotFound is answered for an id the configuration does not have.
var ErrNotFound = errors.New("not found")

// ValidationError names what is wrong with a channel, a rule or the settings.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Msg }

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// Store holds the configuration and writes it.
type Store struct {
	mu   sync.Mutex
	path string
	c    Config
	// OnChange is called after every change that was written (outside the lock).
	OnChange func()
}

// File is the configuration file's name in the configuration directory.
const File = "notify.json"

// Open reads the configuration, or starts a fresh one with the default rules.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, File)}
	b, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.c = Config{Settings: DefaultSettings, Channels: []Channel{}, Rules: DefaultRules(DefaultSettings.Language)}
		s.c.Settings.Inbound.Secret = NewSecret()
		return s, s.write()
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(b, &s.c); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path, err)
	}
	if s.c.Channels == nil {
		s.c.Channels = []Channel{}
	}
	if s.c.Rules == nil {
		s.c.Rules = []Rule{}
	}
	if s.c.Settings.Inbound.Secret == "" {
		s.c.Settings.Inbound.Secret = NewSecret()
	}
	return s, nil
}

// NewSecret makes a random secret of 32 hex characters.
func NewSecret() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (s *Store) write() error {
	b, err := json.MarshalIndent(s.c, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func clone(c Config) Config {
	b, _ := json.Marshal(c)
	var out Config
	_ = json.Unmarshal(b, &out)
	if out.Channels == nil {
		out.Channels = []Channel{}
	}
	if out.Rules == nil {
		out.Rules = []Rule{}
	}
	for i := range out.Rules {
		if out.Rules[i].Channels == nil {
			out.Rules[i].Channels = []string{}
		}
	}
	return out
}

// Get answers a copy of the configuration, secrets included (for the daemon).
func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.c)
}

// Public answers the configuration without its secrets.
func (s *Store) Public() Config {
	c := s.Get()
	for i := range c.Channels {
		c.Channels[i] = Redact(c.Channels[i])
	}
	c.Settings.Inbound.Secret = ""
	return c
}

// Redact blanks a channel's secrets and says which are set.
func Redact(ch Channel) Channel {
	ch.Secrets = map[string]bool{}
	if ch.Token != "" {
		ch.Secrets["token"] = true
		ch.Token = ""
	}
	if ch.UserKey != "" {
		ch.Secrets["user_key"] = true
		ch.UserKey = ""
	}
	if ch.Password != "" {
		ch.Secrets["password"] = true
		ch.Password = ""
	}
	if urlSecret(ch.Type) && ch.URL != "" {
		ch.Secrets["url"] = true
		ch.URL = ""
	}
	if len(ch.Headers) > 0 {
		h := map[string]string{}
		for k := range ch.Headers {
			h[k] = ""
		}
		ch.Headers = h
		ch.Secrets["headers"] = true
	}
	return ch
}

// Merge fills the secrets a save left empty from the stored channel, and removes the ones
// the save names in Clear.
func Merge(in Channel, old Channel) Channel {
	cleared := map[string]bool{}
	for _, c := range in.Clear {
		cleared[c] = true
	}
	if in.Token == "" && !cleared["token"] {
		in.Token = old.Token
	}
	if in.UserKey == "" && !cleared["user_key"] {
		in.UserKey = old.UserKey
	}
	if in.Password == "" && !cleared["password"] {
		in.Password = old.Password
	}
	if urlSecret(in.Type) && in.URL == "" && in.Type == old.Type {
		in.URL = old.URL
	}
	for k, v := range in.Headers {
		if v == "" {
			if ov, ok := old.Headers[k]; ok {
				in.Headers[k] = ov
			}
		}
	}
	return in
}

// Channel answers the stored channel of an id.
func (s *Store) Channel(id string) (Channel, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.c.Channels {
		if ch.ID == id {
			return ch, true
		}
	}
	return Channel{}, false
}

var idRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	r := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss")
	s = strings.Trim(idRe.ReplaceAllString(r.Replace(strings.ToLower(s)), "-"), "-")
	if len(s) > 32 {
		s = strings.Trim(s[:32], "-")
	}
	return s
}

func uniqueID(base string, taken func(string) bool) string {
	if base == "" {
		base = "x"
	}
	id := base
	for n := 2; taken(id); n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

func (s *Store) commit(c Config) (Config, error) {
	old := s.c
	s.c = c
	if err := s.write(); err != nil {
		s.c = old
		return Config{}, err
	}
	return clone(s.c), nil
}

func (s *Store) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

// PutChannel adds a channel (an empty id) or replaces one; secrets left empty are kept.
func (s *Store) PutChannel(in Channel) (Channel, error) {
	s.mu.Lock()
	c := clone(s.c)
	idx := -1
	for i, ch := range c.Channels {
		if ch.ID == in.ID {
			idx = i
		}
	}
	if in.ID != "" && idx < 0 {
		s.mu.Unlock()
		return Channel{}, ErrNotFound
	}
	if idx >= 0 {
		in = Merge(in, c.Channels[idx])
	}
	in, err := NormalizeChannel(in)
	if err != nil {
		s.mu.Unlock()
		return Channel{}, err
	}
	if idx >= 0 {
		c.Channels[idx] = in
	} else {
		in.ID = uniqueID(slug(in.Name), func(id string) bool {
			for _, ch := range c.Channels {
				if ch.ID == id {
					return true
				}
			}
			return false
		})
		c.Channels = append(c.Channels, in)
	}
	_, err = s.commit(c)
	s.mu.Unlock()
	if err != nil {
		return Channel{}, err
	}
	s.changed()
	return in, nil
}

// DeleteChannel removes a channel and takes it out of every rule.
func (s *Store) DeleteChannel(id string) error {
	s.mu.Lock()
	c := clone(s.c)
	found := false
	out := c.Channels[:0]
	for _, ch := range c.Channels {
		if ch.ID == id {
			found = true
			continue
		}
		out = append(out, ch)
	}
	if !found {
		s.mu.Unlock()
		return ErrNotFound
	}
	c.Channels = out
	for i := range c.Rules {
		had := len(c.Rules[i].Channels) > 0
		keep := c.Rules[i].Channels[:0]
		for _, cid := range c.Rules[i].Channels {
			if cid != id {
				keep = append(keep, cid)
			}
		}
		c.Rules[i].Channels = keep
		// a rule that named only this channel must not turn into "every channel"
		if had && len(keep) == 0 {
			c.Rules[i].Enabled = false
		}
	}
	_, err := s.commit(c)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.changed()
	return nil
}

// PutRule adds a rule (an empty id) or replaces one.
func (s *Store) PutRule(in Rule) (Rule, error) {
	s.mu.Lock()
	c := clone(s.c)
	idx := -1
	for i, r := range c.Rules {
		if r.ID == in.ID {
			idx = i
		}
	}
	if in.ID != "" && idx < 0 {
		s.mu.Unlock()
		return Rule{}, ErrNotFound
	}
	in, err := NormalizeRule(in, c.Channels)
	if err != nil {
		s.mu.Unlock()
		return Rule{}, err
	}
	if idx >= 0 {
		c.Rules[idx] = in
	} else {
		in.ID = uniqueID(slug(in.Name), func(id string) bool {
			for _, r := range c.Rules {
				if r.ID == id {
					return true
				}
			}
			return false
		})
		c.Rules = append(c.Rules, in)
	}
	_, err = s.commit(c)
	s.mu.Unlock()
	if err != nil {
		return Rule{}, err
	}
	s.changed()
	return in, nil
}

// DeleteRule removes a rule.
func (s *Store) DeleteRule(id string) error {
	s.mu.Lock()
	c := clone(s.c)
	found := false
	out := c.Rules[:0]
	for _, r := range c.Rules {
		if r.ID == id {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		s.mu.Unlock()
		return ErrNotFound
	}
	c.Rules = out
	_, err := s.commit(c)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.changed()
	return nil
}

// PutSettings replaces the settings; the inbound secret is kept.
func (s *Store) PutSettings(in Settings) (Settings, error) {
	in, err := NormalizeSettings(in)
	if err != nil {
		return Settings{}, err
	}
	s.mu.Lock()
	c := clone(s.c)
	in.Inbound.Secret = c.Settings.Inbound.Secret
	c.Settings = in
	_, err = s.commit(c)
	s.mu.Unlock()
	if err != nil {
		return Settings{}, err
	}
	s.changed()
	return in, nil
}

// RotateSecret replaces the inbound webhook's secret and answers the new one.
func (s *Store) RotateSecret() (string, error) {
	s.mu.Lock()
	c := clone(s.c)
	c.Settings.Inbound.Secret = NewSecret()
	_, err := s.commit(c)
	secret := c.Settings.Inbound.Secret
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	s.changed()
	return secret, nil
}

var clockRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// NormalizeSettings validates the settings and fills what is empty.
func NormalizeSettings(in Settings) (Settings, error) {
	in.Language = texts.Lang(in.Language)
	in.SystemName = strings.TrimSpace(in.SystemName)
	if len(in.SystemName) > 64 {
		return in, invalid("system_name", "at most 64 characters")
	}
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if in.BaseURL != "" {
		if err := checkURL("base_url", in.BaseURL); err != nil {
			return in, err
		}
	}
	if in.Quiet.From == "" {
		in.Quiet.From = DefaultSettings.Quiet.From
	}
	if in.Quiet.To == "" {
		in.Quiet.To = DefaultSettings.Quiet.To
	}
	if !clockRe.MatchString(in.Quiet.From) || !clockRe.MatchString(in.Quiet.To) {
		return in, invalid("quiet", "from and to are times of day, HH:MM")
	}
	if in.Quiet.Enabled && in.Quiet.From == in.Quiet.To {
		return in, invalid("quiet", "from and to are the same time")
	}
	if in.RepeatMin < 0 || in.RepeatMin > 7*24*60 {
		return in, invalid("repeat_min", "0 to 10080 minutes")
	}
	if in.RatePerHour < 0 || in.RatePerHour > 3600 {
		return in, invalid("rate_per_hour", "0 to 3600")
	}
	return in, nil
}

func checkURL(field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid(field, "an http:// or https:// address is needed")
	}
	return nil
}

// Fields answers the fields a channel type reads beside id, name, type and enabled; the page
// builds its form from the same list in its own model.
func Fields(typ string) []string {
	switch typ {
	case Pushover:
		return []string{"token", "user_key", "target", "sound", "url"}
	case Telegram:
		return []string{"token", "target", "thread", "url"}
	case Email:
		return []string{"host", "port", "security", "insecure", "user", "password", "sender", "target"}
	case Ntfy:
		return []string{"url", "target", "token", "user", "password", "insecure"}
	case Gotify:
		return []string{"url", "token", "insecure"}
	case Webhook:
		return []string{"url", "method", "headers", "content_type", "body", "insecure"}
	case Discord, Slack, Matrix:
		return []string{"url"}
	case Signal:
		return []string{"url", "sender", "target", "user", "password", "insecure"}
	case MQTT:
		return []string{"host", "port", "security", "insecure", "user", "password", "target", "body", "qos", "retain"}
	}
	return nil
}

// NormalizeChannel validates a channel (with its secrets filled) and drops the fields its type
// does not read.
func NormalizeChannel(in Channel) (Channel, error) {
	in.Secrets, in.Clear = nil, nil
	in.Name = strings.TrimSpace(in.Name)
	fields := Fields(in.Type)
	if fields == nil {
		return in, invalid("type", "unknown channel type %q", in.Type)
	}
	if in.Name == "" {
		return in, invalid("name", "a name is needed")
	}
	if len(in.Name) > 64 {
		return in, invalid("name", "at most 64 characters")
	}
	has := map[string]bool{}
	for _, f := range fields {
		has[f] = true
	}
	out := Channel{ID: in.ID, Name: in.Name, Type: in.Type, Enabled: in.Enabled}
	trim := strings.TrimSpace
	if has["url"] {
		out.URL = strings.TrimRight(trim(in.URL), "/")
	}
	if has["token"] {
		out.Token = trim(in.Token)
	}
	if has["user_key"] {
		out.UserKey = trim(in.UserKey)
	}
	if has["user"] {
		out.User = trim(in.User)
	}
	if has["password"] {
		out.Password = in.Password
	}
	if has["target"] {
		out.Target = trim(in.Target)
	}
	if has["sender"] {
		out.Sender = trim(in.Sender)
	}
	if has["host"] {
		out.Host = trim(in.Host)
		out.Port = in.Port
		out.Security = in.Security
	}
	if has["insecure"] {
		out.Insecure = in.Insecure
	}
	if has["method"] {
		out.Method = strings.ToUpper(trim(in.Method))
		out.ContentType = trim(in.ContentType)
	}
	if has["headers"] && len(in.Headers) > 0 {
		out.Headers = map[string]string{}
		for k, v := range in.Headers {
			k = trim(k)
			if k == "" {
				continue
			}
			if !headerRe.MatchString(k) || strings.ContainsAny(v, "\r\n") {
				return in, invalid("headers", "%q is not a header", k)
			}
			out.Headers[k] = v
		}
	}
	if has["body"] {
		out.Body = in.Body
	}
	if has["thread"] {
		out.Thread = trim(in.Thread)
	}
	if has["sound"] {
		out.Sound = trim(in.Sound)
	}
	if has["qos"] {
		out.QoS = in.QoS
		out.Retain = in.Retain
	}

	need := func(field, v string) error {
		if v == "" {
			return invalid(field, "is needed")
		}
		return nil
	}
	var err error
	switch out.Type {
	case Pushover:
		err = errors.Join(need("token", out.Token), need("user_key", out.UserKey))
		if out.URL != "" && err == nil {
			err = checkURL("url", out.URL)
		}
	case Telegram:
		err = errors.Join(need("token", out.Token), need("target", out.Target))
		if out.URL != "" && err == nil {
			err = checkURL("url", out.URL)
		}
	case Email:
		if out.Security == "" {
			out.Security = "starttls"
		}
		if out.Security != "starttls" && out.Security != "tls" && out.Security != "none" {
			return in, invalid("security", "starttls, tls or none")
		}
		if out.Port == 0 {
			out.Port = map[string]int{"starttls": 587, "tls": 465, "none": 25}[out.Security]
		}
		err = errors.Join(need("host", out.Host), need("sender", out.Sender), need("target", out.Target))
		if err == nil && !strings.Contains(addrOf(out.Sender), "@") {
			err = invalid("sender", "an email address is needed")
		}
		if err == nil {
			for _, to := range SplitList(out.Target) {
				if !strings.Contains(to, "@") {
					err = invalid("target", "%q is not an email address", to)
				}
			}
		}
	case Ntfy:
		if out.URL == "" {
			out.URL = "https://ntfy.sh"
		}
		err = errors.Join(checkURL("url", out.URL), need("target", out.Target))
		if err == nil && strings.ContainsAny(out.Target, "/ ?#") {
			err = invalid("target", "a topic has no slash and no space")
		}
	case Gotify:
		err = need("url", out.URL)
		if err == nil {
			err = errors.Join(checkURL("url", out.URL), need("token", out.Token))
		}
	case Webhook:
		if out.Method == "" {
			out.Method = "POST"
		}
		if out.Method != "POST" && out.Method != "PUT" {
			return in, invalid("method", "POST or PUT")
		}
		err = need("url", out.URL)
		if err == nil {
			err = checkURL("url", out.URL)
		}
		if err == nil && out.Body != "" {
			if _, terr := tmpl.Compile(out.Body); terr != nil {
				err = invalid("body", "%v", terr)
			}
		}
	case Discord, Slack, Matrix:
		err = need("url", out.URL)
		if err == nil {
			err = checkURL("url", out.URL)
		}
	case Signal:
		err = need("url", out.URL)
		if err == nil {
			err = errors.Join(checkURL("url", out.URL), need("sender", out.Sender), need("target", out.Target))
		}
	case MQTT:
		if out.Host == "" {
			out.Host = "127.0.0.1"
		}
		if out.Security == "" {
			out.Security = "none"
		}
		if out.Security != "none" && out.Security != "tls" {
			return in, invalid("security", "none or tls")
		}
		if out.Port == 0 {
			out.Port = map[string]int{"none": 1883, "tls": 8883}[out.Security]
		}
		if out.Target == "" {
			out.Target = "notify/{{ kind }}"
		}
		if out.QoS != 0 && out.QoS != 1 {
			return in, invalid("qos", "0 or 1")
		}
		if _, terr := tmpl.Compile(out.Target); terr != nil {
			err = invalid("target", "%v", terr)
		}
		if err == nil && out.Body != "" {
			if _, terr := tmpl.Compile(out.Body); terr != nil {
				err = invalid("body", "%v", terr)
			}
		}
	}
	if err != nil {
		return in, firstValidation(err)
	}
	if has["host"] && (out.Port < 1 || out.Port > 65535) {
		return in, invalid("port", "1 to 65535")
	}
	return out, nil
}

var headerRe = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]+$`)

func firstValidation(err error) error {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve
	}
	return err
}

func addrOf(s string) string {
	if i := strings.LastIndex(s, "<"); i >= 0 {
		return strings.TrimSuffix(strings.TrimSpace(s[i+1:]), ">")
	}
	return s
}

// SplitList splits a comma- or space-separated list.
func SplitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func subset(field string, got []string, allowed ...string) ([]string, error) {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	seen := map[string]bool{}
	out := []string{}
	for _, g := range got {
		if !ok[g] {
			return nil, invalid(field, "unknown value %q", g)
		}
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	return out, nil
}

// Ops are a datapoint condition's operators.
var Ops = []string{"true", "false", "eq", "ne", "gt", "ge", "lt", "le"}

// NormalizeRule validates a rule against the channels and drops what its source does not read.
func NormalizeRule(in Rule, channels []Channel) (Rule, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return in, invalid("name", "a name is needed")
	}
	if len(in.Name) > 64 {
		return in, invalid("name", "at most 64 characters")
	}
	out := Rule{ID: in.ID, Name: in.Name, Enabled: in.Enabled, Source: in.Source, Priority: in.Priority, Resolved: in.Resolved, BatchS: in.BatchS, IgnoreQuiet: in.IgnoreQuiet, Title: in.Title, Body: in.Body, Channels: []string{}}
	known := map[string]bool{}
	for _, ch := range channels {
		known[ch.ID] = true
	}
	seen := map[string]bool{}
	for _, id := range in.Channels {
		if !known[id] {
			return in, invalid("channels", "unknown channel %q", id)
		}
		if !seen[id] {
			seen[id] = true
			out.Channels = append(out.Channels, id)
		}
	}
	if out.Priority == "" {
		out.Priority = message.Normal
	}
	if out.Priority != message.Low && out.Priority != message.Normal && out.Priority != message.High {
		return in, invalid("priority", "low, normal or high")
	}
	if out.BatchS < 0 || out.BatchS > 3600 {
		return in, invalid("batch_s", "0 to 3600 seconds")
	}
	for field, src := range map[string]string{"title": out.Title, "body": out.Body} {
		if src == "" {
			continue
		}
		if _, err := tmpl.Compile(src); err != nil {
			return in, invalid(field, "%v", err)
		}
	}
	var err error
	switch in.Source {
	case SrcWarning:
		if out.Severities, err = subset("severities", in.Severities, "error", "warning", "info"); err != nil {
			return in, err
		}
		if len(out.Severities) == 0 {
			return in, invalid("severities", "at least one severity is needed")
		}
		for _, id := range in.ExcludeIDs {
			if id = strings.TrimSpace(id); id != "" {
				out.ExcludeIDs = append(out.ExcludeIDs, id)
			}
		}
		sort.Strings(out.ExcludeIDs)
	case SrcService:
		if out.Kinds, err = subset("kinds", in.Kinds, texts.ServiceKinds...); err != nil {
			return in, err
		}
		if len(out.Kinds) == 0 {
			return in, invalid("kinds", "at least one kind is needed")
		}
		for _, a := range in.Exclude {
			if a = strings.TrimSpace(a); a != "" {
				out.Exclude = append(out.Exclude, a)
			}
		}
		if in.HoldS < 0 || in.HoldS > 86400 {
			return in, invalid("hold_s", "0 to 86400 seconds")
		}
		out.HoldS = in.HoldS
	case SrcDatapoint:
		d := in.Datapoint
		if d == nil {
			return in, invalid("datapoint", "a datapoint is needed")
		}
		dp := Datapoint{Interface: strings.TrimSpace(d.Interface), Address: strings.TrimSpace(d.Address), Key: strings.TrimSpace(d.Key), Op: d.Op, Value: strings.TrimSpace(d.Value), ForS: d.ForS, Label: strings.TrimSpace(d.Label)}
		if dp.Interface == "" || dp.Address == "" || dp.Key == "" {
			return in, invalid("datapoint", "the interface, the address and the datapoint are needed")
		}
		if _, err := subset("datapoint.op", []string{dp.Op}, Ops...); err != nil {
			return in, err
		}
		if dp.Op == "true" || dp.Op == "false" {
			dp.Value = ""
		} else if dp.Value == "" {
			return in, invalid("datapoint.value", "a value to compare with is needed")
		} else if dp.Op != "eq" && dp.Op != "ne" {
			if _, ok := Number(dp.Value); !ok {
				return in, invalid("datapoint.value", "%q is not a number", dp.Value)
			}
		}
		if dp.ForS < 0 || dp.ForS > 7*86400 {
			return in, invalid("datapoint.for_s", "0 to 604800 seconds")
		}
		out.Datapoint = &dp
	case SrcSystem:
		if out.Kinds, err = subset("kinds", in.Kinds, texts.SystemKinds...); err != nil {
			return in, err
		}
		if len(out.Kinds) == 0 {
			return in, invalid("kinds", "at least one event is needed")
		}
	case SrcInbound:
		out.Tag = strings.TrimSpace(in.Tag)
	case SrcDigest:
		out.Time = in.Time
		if !clockRe.MatchString(out.Time) {
			return in, invalid("time", "a time of day, HH:MM")
		}
		out.OnlyOpen = in.OnlyOpen
		out.Resolved, out.BatchS = false, 0
	default:
		return in, invalid("source", "unknown source %q", in.Source)
	}
	return out, nil
}

// Number parses a condition's value as a number; a comma is taken as the decimal sign.
func Number(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.Replace(strings.TrimSpace(s), ",", ".", 1), 64)
	return f, err == nil
}
