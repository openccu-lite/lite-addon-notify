// Package engine turns events into messages: every event runs through the rules of its
// source; what a rule takes waits in the rule's batch (the batching window, a service
// message's hold time, the repeat suppression, the quiet hours) and leaves as one message for
// the rule's channels. The engine has no clock and no goroutine of its own: every call takes
// the time, and Next says when Tick wants to be called again - the daemon drives it with a
// timer, the tests with a fake clock.
package engine

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openccu-lite/lite-addon-notify/internal/config"
	"github.com/openccu-lite/lite-addon-notify/internal/message"
	"github.com/openccu-lite/lite-addon-notify/internal/texts"
	"github.com/openccu-lite/lite-addon-notify/internal/tmpl"
)

// Event is one change a source saw: something new, or something that is over.
type Event struct {
	Source   string    `json:"source"`
	Key      string    `json:"key"`
	Resolved bool      `json:"resolved"`
	At       time.Time `json:"at"`
	// What the rules filter by, per source.
	Severity    string `json:"severity,omitempty"`
	WarningID   string `json:"warning_id,omitempty"`
	ServiceKind string `json:"service_kind,omitempty"`
	Address     string `json:"address,omitempty"`
	SystemKind  string `json:"system_kind,omitempty"`
	Tag         string `json:"tag,omitempty"`
	// Once marks an event that has no end (a reboot, a backup made): nothing waits for its
	// "resolved".
	Once bool `json:"once,omitempty"`
	// RuleID addresses one rule (a datapoint rule's own event).
	RuleID string `json:"rule_id,omitempty"`
	// Priority replaces the rule's (an inbound message that names one).
	Priority string       `json:"priority,omitempty"`
	Item     message.Item `json:"item"`
}

// Outgoing is one message with the channels it goes to.
type Outgoing struct {
	Rule     config.Rule
	Channels []string
	Message  message.Message
}

// Open is what is open right now, for the digest.
type Open struct {
	Warnings   []message.Item
	Service    []message.Item
	Datapoints []message.Item
}

// Count is the number of open items.
func (o Open) Count() int { return len(o.Warnings) + len(o.Service) + len(o.Datapoints) }

type entry struct {
	Event Event `json:"event"`
	// Ready is when the entry may leave: at once, after a service message's hold time, or when
	// the repeat suppression of its subject ends.
	Ready time.Time `json:"ready"`
}

// dpState is a datapoint rule's memory.
type dpState struct {
	// Sig is the condition it belongs to; a changed condition starts afresh.
	Sig string `json:"sig"`
	// Since is when the condition became true (zero: it is false), Fired whether the rule fired.
	Since time.Time       `json:"since,omitzero"`
	Fired bool            `json:"fired,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

// state is what the engine keeps across restarts.
type state struct {
	// Announced: rule|key of every subject whose "new" went out and that is not over.
	Announced map[string]time.Time `json:"announced"`
	// LastNew: rule|key and when it was last announced, for the repeat suppression.
	LastNew map[string]time.Time `json:"last_new"`
	// Digest: rule and the day (2006-01-02) its digest last went out.
	Digest     map[string]string   `json:"digest"`
	Datapoints map[string]*dpState `json:"datapoints"`
	Pending    map[string][]*entry `json:"pending"`
}

func newState() state {
	return state{Announced: map[string]time.Time{}, LastNew: map[string]time.Time{}, Digest: map[string]string{}, Datapoints: map[string]*dpState{}, Pending: map[string][]*entry{}}
}

// Options are what the engine asks its surroundings.
type Options struct {
	// Config answers the configuration in force.
	Config func() config.Config
	// System names the system (the settings' name, else the host name).
	System func() string
	// Open answers what is open, for the digest; nil: nothing.
	Open func(lang string) Open
	// Describe answers a channel's or device's name and rooms; nil: the address.
	Describe func(iface, address, lang string) (name string, rooms []string)
	Log      func(format string, args ...any)
}

// Engine is the rule engine.
type Engine struct {
	mu    sync.Mutex
	o     Options
	st    state
	dirty bool
}

// New makes an engine.
func New(o Options) *Engine {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.System == nil {
		o.System = func() string { return "" }
	}
	return &Engine{o: o, st: newState()}
}

// Export answers the engine's memory as JSON, and whether it changed since the last Export.
func (e *Engine) Export() (json.RawMessage, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, _ := json.Marshal(e.st)
	d := e.dirty
	e.dirty = false
	return b, d
}

// Import restores the memory Export answered.
func (e *Engine) Import(b json.RawMessage) {
	if len(b) == 0 {
		return
	}
	st := newState()
	if json.Unmarshal(b, &st) != nil {
		return
	}
	if st.Announced == nil {
		st.Announced = map[string]time.Time{}
	}
	if st.LastNew == nil {
		st.LastNew = map[string]time.Time{}
	}
	if st.Digest == nil {
		st.Digest = map[string]string{}
	}
	if st.Datapoints == nil {
		st.Datapoints = map[string]*dpState{}
	}
	if st.Pending == nil {
		st.Pending = map[string][]*entry{}
	}
	e.mu.Lock()
	e.st = st
	e.mu.Unlock()
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// targets answers the enabled channels a rule sends to.
func targets(r config.Rule, c config.Config) []string {
	var out []string
	for _, ch := range c.Channels {
		if !ch.Enabled {
			continue
		}
		if len(r.Channels) == 0 || contains(r.Channels, ch.ID) {
			out = append(out, ch.ID)
		}
	}
	return out
}

// matches says whether a rule takes an event.
func matches(r config.Rule, ev Event) bool {
	if !r.Enabled || r.Source != ev.Source {
		return false
	}
	if ev.RuleID != "" && ev.RuleID != r.ID {
		return false
	}
	switch r.Source {
	case config.SrcWarning:
		return contains(r.Severities, ev.Severity) && !contains(r.ExcludeIDs, ev.WarningID)
	case config.SrcService:
		if !contains(r.Kinds, ev.ServiceKind) {
			return false
		}
		for _, x := range r.Exclude {
			if x == ev.Address || strings.HasPrefix(ev.Address, x+":") {
				return false
			}
		}
		return true
	case config.SrcSystem:
		return contains(r.Kinds, ev.SystemKind)
	case config.SrcInbound:
		return r.Tag == "" || strings.EqualFold(r.Tag, ev.Tag)
	case config.SrcDatapoint:
		return ev.RuleID == r.ID
	}
	return false
}

// Handle runs an event through the rules.
func (e *Engine) Handle(ev Event, now time.Time) {
	c := e.o.Config()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handle(ev, now, c)
}

func (e *Engine) handle(ev Event, now time.Time, c config.Config) {
	if ev.At.IsZero() {
		ev.At = now
	}
	repeat := time.Duration(c.Settings.RepeatMin) * time.Minute
	for _, r := range c.Rules {
		if !matches(r, ev) {
			continue
		}
		k := r.ID + "|" + ev.Key
		if ev.Resolved {
			// a "new" still waiting meets its own "resolved": a flap is silence
			if e.removePending(r.ID, ev.Key, false) {
				e.dirty = true
				continue
			}
			if _, ok := e.st.Announced[k]; !ok {
				continue
			}
			if !r.Resolved {
				delete(e.st.Announced, k)
				e.dirty = true
				continue
			}
			e.st.Pending[r.ID] = append(e.st.Pending[r.ID], &entry{Event: ev, Ready: now})
			e.dirty = true
			continue
		}
		if len(targets(r, c)) == 0 {
			continue
		}
		// back again before its "resolved" left: nothing changed for the receiver
		if e.removePending(r.ID, ev.Key, true) {
			e.dirty = true
			continue
		}
		if p := e.findPending(r.ID, ev.Key); p != nil {
			p.Event.Item = ev.Item
			continue
		}
		if _, ok := e.st.Announced[k]; ok {
			continue
		}
		ready := now
		if r.Source == config.SrcService && r.HoldS > 0 {
			ready = now.Add(time.Duration(r.HoldS) * time.Second)
		}
		if last, ok := e.st.LastNew[k]; ok && repeat > 0 && last.Add(repeat).After(ready) {
			ready = last.Add(repeat)
		}
		e.st.Pending[r.ID] = append(e.st.Pending[r.ID], &entry{Event: ev, Ready: ready})
		e.dirty = true
	}
}

func (e *Engine) findPending(rule, key string) *entry {
	for _, p := range e.st.Pending[rule] {
		if p.Event.Key == key && !p.Event.Resolved {
			return p
		}
	}
	return nil
}

// removePending removes the rule's waiting entry of a key (the new one, or the resolved one).
func (e *Engine) removePending(rule, key string, resolved bool) bool {
	l := e.st.Pending[rule]
	for i, p := range l {
		if p.Event.Key == key && p.Event.Resolved == resolved {
			e.st.Pending[rule] = append(l[:i:i], l[i+1:]...)
			if len(e.st.Pending[rule]) == 0 {
				delete(e.st.Pending, rule)
			}
			return true
		}
	}
	return false
}

func clock(s string, day time.Time) time.Time {
	h, _ := strconv.Atoi(s[:2])
	m, _ := strconv.Atoi(s[3:])
	return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, day.Location())
}

// QuietEnd answers when the quiet hours that hold at now end; the zero time outside them.
func QuietEnd(q config.Quiet, now time.Time) time.Time {
	if !q.Enabled || len(q.From) != 5 || len(q.To) != 5 || q.From == q.To {
		return time.Time{}
	}
	from, to := clock(q.From, now), clock(q.To, now)
	if from.Before(to) {
		if !now.Before(from) && now.Before(to) {
			return to
		}
		return time.Time{}
	}
	// over midnight
	if !now.Before(from) {
		return clock(q.To, now.AddDate(0, 0, 1))
	}
	if now.Before(to) {
		return to
	}
	return time.Time{}
}

// flushTime answers when a rule's batch leaves: the zero time when nothing is ready.
func flushTime(r config.Rule, pending []*entry, q config.Quiet, now time.Time) (flush time.Time, next time.Time) {
	var first time.Time
	for _, p := range pending {
		if p.Ready.After(now) {
			if next.IsZero() || p.Ready.Before(next) {
				next = p.Ready
			}
			continue
		}
		if first.IsZero() || p.Ready.Before(first) {
			first = p.Ready
		}
	}
	if first.IsZero() {
		return time.Time{}, next
	}
	flush = first.Add(time.Duration(r.BatchS) * time.Second)
	if !r.IgnoreQuiet {
		at := flush
		if at.Before(now) {
			at = now
		}
		if end := QuietEnd(q, at); !end.IsZero() {
			flush = end
		}
	}
	return flush, next
}

// Tick answers the messages that are due at now.
func (e *Engine) Tick(now time.Time) []Outgoing {
	c := e.o.Config()
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []Outgoing
	rules := map[string]config.Rule{}
	for _, r := range c.Rules {
		rules[r.ID] = r
	}
	// datapoint rules whose condition has held long enough
	for id, st := range e.st.Datapoints {
		r, ok := rules[id]
		if !ok || r.Source != config.SrcDatapoint || r.Datapoint == nil || sig(r.Datapoint) != st.Sig {
			delete(e.st.Datapoints, id)
			e.dirty = true
			continue
		}
		if r.Enabled && !st.Since.IsZero() && !st.Fired && !now.Before(st.Since.Add(time.Duration(r.Datapoint.ForS)*time.Second)) {
			st.Fired = true
			e.dirty = true
			e.handle(e.dpEvent(r, st, false, now, c), now, c)
		}
	}
	ids := make([]string, 0, len(e.st.Pending))
	for id := range e.st.Pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r, ok := rules[id]
		if !ok || !r.Enabled {
			delete(e.st.Pending, id)
			e.dirty = true
			continue
		}
		flush, _ := flushTime(r, e.st.Pending[id], c.Settings.Quiet, now)
		if flush.IsZero() || flush.After(now) {
			continue
		}
		var ready []*entry
		var rest []*entry
		for _, p := range e.st.Pending[id] {
			if p.Ready.After(now) {
				rest = append(rest, p)
			} else {
				ready = append(ready, p)
			}
		}
		if len(rest) == 0 {
			delete(e.st.Pending, id)
		} else {
			e.st.Pending[id] = rest
		}
		e.dirty = true
		sort.SliceStable(ready, func(i, j int) bool { return ready[i].Event.At.Before(ready[j].Event.At) })
		events := make([]Event, 0, len(ready))
		for _, p := range ready {
			k := id + "|" + p.Event.Key
			if p.Event.Resolved {
				delete(e.st.Announced, k)
			} else {
				if !p.Event.Once {
					e.st.Announced[k] = now
				}
				e.st.LastNew[k] = now
			}
			events = append(events, p.Event)
		}
		ch := targets(r, c)
		if len(ch) == 0 {
			continue
		}
		// without a batching window every event is its own message
		if r.BatchS == 0 {
			for _, ev := range events {
				out = append(out, Outgoing{Rule: r, Channels: ch, Message: e.build(r, []Event{ev}, c, now)})
			}
			continue
		}
		out = append(out, Outgoing{Rule: r, Channels: ch, Message: e.build(r, events, c, now)})
	}
	// the digests
	for _, r := range c.Rules {
		if r.Source != config.SrcDigest || !r.Enabled || len(r.Time) != 5 {
			continue
		}
		day := now.Format("2006-01-02")
		due := clock(r.Time, now)
		last, seen := e.st.Digest[r.ID]
		if now.Before(due) {
			if !seen {
				// a rule first seen before its time today: its first digest is today's
				e.st.Digest[r.ID] = now.AddDate(0, 0, -1).Format("2006-01-02")
				e.dirty = true
			}
			continue
		}
		if last == day {
			continue
		}
		e.st.Digest[r.ID] = day
		e.dirty = true
		// a rule first seen after its time, or a day whose time passed long ago (the system was
		// off): no late digest
		if !seen || now.Sub(due) > 6*time.Hour {
			continue
		}
		ch := targets(r, c)
		if len(ch) == 0 {
			continue
		}
		if m, ok := e.digest(r, c, now); ok {
			out = append(out, Outgoing{Rule: r, Channels: ch, Message: m})
		}
	}
	// forget announcements the repeat suppression no longer needs
	keep := time.Duration(c.Settings.RepeatMin)*time.Minute + time.Hour
	for k, t := range e.st.LastNew {
		if now.Sub(t) > keep {
			delete(e.st.LastNew, k)
			e.dirty = true
		}
	}
	for k, t := range e.st.Announced {
		// a subject nobody ever closed (an inbound message with a key) is forgotten after 90 days
		if _, ok := rules[k[:strings.IndexByte(k, '|')]]; !ok || now.Sub(t) > 90*24*time.Hour {
			delete(e.st.Announced, k)
			e.dirty = true
		}
	}
	for id := range e.st.Digest {
		if _, ok := rules[id]; !ok {
			delete(e.st.Digest, id)
			e.dirty = true
		}
	}
	return out
}

// Next answers when Tick is due again; the zero time when nothing waits.
func (e *Engine) Next(now time.Time) time.Time {
	c := e.o.Config()
	e.mu.Lock()
	defer e.mu.Unlock()
	var next time.Time
	take := func(t time.Time) {
		if !t.IsZero() && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	for _, r := range c.Rules {
		if !r.Enabled {
			continue
		}
		if p := e.st.Pending[r.ID]; len(p) > 0 {
			flush, later := flushTime(r, p, c.Settings.Quiet, now)
			take(flush)
			take(later)
		}
		switch r.Source {
		case config.SrcDigest:
			if len(r.Time) == 5 {
				due := clock(r.Time, now)
				if !now.Before(due) && e.st.Digest[r.ID] == now.Format("2006-01-02") {
					due = clock(r.Time, now.AddDate(0, 0, 1))
				}
				take(due)
			}
		case config.SrcDatapoint:
			if st := e.st.Datapoints[r.ID]; st != nil && r.Datapoint != nil && !st.Since.IsZero() && !st.Fired {
				take(st.Since.Add(time.Duration(r.Datapoint.ForS) * time.Second))
			}
		}
	}
	return next
}

// PendingCount answers how many events wait in the batches.
func (e *Engine) PendingCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, p := range e.st.Pending {
		n += len(p)
	}
	return n
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (e *Engine) system(c config.Config) string {
	if c.Settings.SystemName != "" {
		return c.Settings.SystemName
	}
	return e.o.System()
}

func link(c config.Config, path string) string {
	if c.Settings.BaseURL == "" {
		return ""
	}
	if path == "" || strings.HasPrefix(path, "#") {
		path = "/" + path
	}
	return c.Settings.BaseURL + path
}

// build makes a rule's message from its events.
func (e *Engine) build(r config.Rule, events []Event, c config.Config, now time.Time) message.Message {
	lang := c.Settings.Language
	m := message.Message{ID: newID(), Kind: r.Source, Rule: r.ID, RuleName: r.Name, Priority: r.Priority, System: e.system(c), Time: now, Resolved: true}
	fresh, resolved := 0, 0
	for _, ev := range events {
		it := ev.Item
		it.Key = ev.Key
		it.Resolved = ev.Resolved
		if it.Line == "" {
			it.Line = it.Title
			if it.Text != "" {
				it.Line += ": " + it.Text
			}
		}
		if ev.Resolved {
			it.Line = texts.Resolved(lang, it.Line)
			resolved++
		} else {
			m.Resolved = false
			fresh++
		}
		if ev.Priority != "" && len(events) == 1 {
			m.Priority = ev.Priority
		}
		m.Items = append(m.Items, it)
	}
	m.Count = len(m.Items)
	if m.Resolved && m.Priority == message.High {
		m.Priority = message.Normal
	}
	var lines []string
	for _, it := range m.Items {
		lines = append(lines, it.Line)
	}
	itemLink := ""
	if len(m.Items) == 1 {
		m.Title = m.Items[0].Line
		m.Body = m.Items[0].Detail
		itemLink = m.Items[0].Link
	} else {
		m.Title = texts.Headline(lang, r.Source, fresh, resolved)
		m.Body = strings.Join(lines, "\n")
	}
	m.Link = link(c, itemLink)
	e.finish(&m, r, c)
	return m
}

// finish applies the title prefix and the rule's own templates.
func (e *Engine) finish(m *message.Message, r config.Rule, c config.Config) {
	ctx := m.Context()
	if r.Title != "" {
		if s, err := tmpl.Render(r.Title, ctx); err != nil {
			e.o.Log("rule %s: the title template failed (%v): the default title is sent", r.ID, err)
		} else if s != "" {
			m.Title = s
		}
	} else if c.Settings.TitlePrefix && m.System != "" {
		m.Title = m.System + ": " + m.Title
	}
	if r.Body != "" {
		if s, err := tmpl.Render(r.Body, ctx); err != nil {
			e.o.Log("rule %s: the body template failed (%v): the default body is sent", r.ID, err)
		} else {
			m.Body = s
		}
	}
}

// digest makes a digest rule's message; false when all is clear and the rule wants silence.
func (e *Engine) digest(r config.Rule, c config.Config, now time.Time) (message.Message, bool) {
	lang := c.Settings.Language
	var open Open
	if e.o.Open != nil {
		// the sources answer under their own locks; the engine's is free of them
		e.mu.Unlock()
		open = e.o.Open(lang)
		e.mu.Lock()
	}
	open.Datapoints = e.openDatapoints(c, lang)
	m := message.Message{ID: newID(), Kind: message.KindDigest, Rule: r.ID, RuleName: r.Name, Priority: r.Priority, System: e.system(c), Time: now}
	n := open.Count()
	if n == 0 {
		if r.OnlyOpen {
			return m, false
		}
		m.Title = texts.T(lang, "digest-clear")
		m.Body = texts.T(lang, "digest-clear-body")
		m.Resolved = true
	} else {
		m.Title = texts.T(lang, "digest-open", n)
		var b []string
		section := func(head string, items []message.Item) {
			if len(items) == 0 {
				return
			}
			if len(b) > 0 {
				b = append(b, "")
			}
			b = append(b, texts.T(lang, head))
			for _, it := range items {
				b = append(b, "- "+it.Line)
				m.Items = append(m.Items, it)
			}
		}
		section("digest-warnings", open.Warnings)
		section("digest-service", open.Service)
		section("digest-datapoints", open.Datapoints)
		m.Body = strings.Join(b, "\n")
	}
	m.Count = len(m.Items)
	m.Link = link(c, "")
	e.finish(&m, r, c)
	return m, true
}

// Digest answers what a digest rule would send now (the page's "send now").
func (e *Engine) Digest(r config.Rule, now time.Time) message.Message {
	c := e.o.Config()
	e.mu.Lock()
	defer e.mu.Unlock()
	r.OnlyOpen = false
	m, _ := e.digest(r, c, now)
	return m
}

// Sample answers a message as a rule would send it, made of sample events (the Test button
// and the template preview).
func (e *Engine) Sample(r config.Rule, now time.Time) message.Message {
	c := e.o.Config()
	if r.Source == config.SrcDigest {
		return e.Digest(r, now)
	}
	lang := c.Settings.Language
	var events []Event
	de := texts.Lang(lang) == "de"
	pick := func(d, en string) string {
		if de {
			return d
		}
		return en
	}
	switch r.Source {
	case config.SrcWarning:
		t, _ := texts.Warning(lang, "backup-delivery", "usb:no-medium", map[string]any{"name": "USB", "cause": "no-medium"})
		events = []Event{{Key: "sample-1", Severity: "error", Item: message.Item{Title: t, Severity: "error", Detail: texts.SeverityName(lang, "error") + " · " + texts.T(lang, "status-page"), Link: "/backup#targets"}}}
	case config.SrcService:
		room := pick("Bad", "Bathroom")
		events = []Event{
			{Key: "sample-1", Item: message.Item{Title: pick("Heizung Bad", "Bathroom radiator"), Text: texts.Service(lang, "UNREACH", true), Device: pick("Heizung Bad", "Bathroom radiator"), Interface: "HmIP-RF", Address: "000A1BE9A1B2C3", Rooms: []string{room}, Datapoint: "UNREACH", Value: true, Since: now, Detail: room + " · HmIP-RF 000A1BE9A1B2C3 · " + texts.Since(lang, now)}},
			{Key: "sample-2", Item: message.Item{Title: pick("Fenster Küche", "Kitchen window"), Text: texts.Service(lang, "LOW_BAT", true), Device: pick("Fenster Küche", "Kitchen window"), Interface: "HmIP-RF", Address: "0000D3C99C6B41", Rooms: []string{pick("Küche", "Kitchen")}, Datapoint: "LOW_BAT", Value: true, Since: now}},
		}
	case config.SrcDatapoint:
		// a value that meets the condition
		st := &dpState{Since: now, Value: json.RawMessage("true")}
		if d := r.Datapoint; d != nil {
			switch d.Op {
			case "false":
				st.Value = json.RawMessage("false")
			case "true":
			default:
				if f, ok := config.Number(d.Value); ok {
					switch d.Op {
					case "gt":
						f++
					case "lt":
						f--
					case "ne":
						f++
					}
					st.Value, _ = json.Marshal(f)
				} else if d.Op == "eq" {
					st.Value, _ = json.Marshal(d.Value)
				}
			}
		}
		if r.Datapoint != nil {
			events = []Event{e.dpEvent(r, st, false, now, c)}
		}
	case config.SrcSystem:
		events = []Event{{Key: "sample-1", SystemKind: texts.SysUpdateAvailable, Item: message.Item{Title: texts.T(lang, "sys-update-available", "2.0.0"), Detail: texts.T(lang, "sys-running", "1.2.0")}}}
	case config.SrcInbound:
		events = []Event{{Key: "sample-1", Tag: r.Tag, Item: message.Item{Title: pick("Nachricht eines anderen Programms", "A message from another program"), Detail: pick("So sieht eine Nachricht aus, die über den eingehenden Webhook kam.", "This is what a message that came in through the inbound webhook looks like.")}}}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.build(r, events, c, now)
}
