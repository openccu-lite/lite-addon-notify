// Package source reads what the system knows and turns changes into the engine's events: the
// Status page's warnings (polled), the device service messages (the stream, with a poll as
// the backstop), a few system facts (the running version, the newest backup, the boot), and
// the values of the datapoints that datapoint rules watch (lite-rpc's event stream, open only
// while such rules exist). The first read of each source is its baseline and notifies nothing.
package source

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/ccu-addon-kit/go/litesource"

	"github.com/openccu-lite/lite-addon-notify/internal/config"
	"github.com/openccu-lite/lite-addon-notify/internal/engine"
	"github.com/openccu-lite/lite-addon-notify/internal/message"
	"github.com/openccu-lite/lite-addon-notify/internal/texts"
)

// The intervals.
var (
	WarningsEvery = 30 * time.Second
	SystemEvery   = 60 * time.Second
	ServiceEvery  = 60 * time.Second
	NamesEvery    = 15 * time.Minute
	// StreamSilence is how long the service-message stream may say nothing (it pings every 30 s).
	StreamSilence = 90 * time.Second
)

// warnEntry is a warning as it was announced: kept so that its end can be said in the same words.
type warnEntry struct {
	ID       string `json:"id"`
	Variant  string `json:"variant"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	Href     string `json:"href,omitempty"`
}

// svcEntry is a service message as it was seen.
type svcEntry struct {
	Interface string    `json:"interface"`
	Address   string    `json:"address"`
	Key       string    `json:"key"`
	Value     any       `json:"value"`
	Since     time.Time `json:"since,omitzero"`
	Name      string    `json:"name,omitempty"`
	Type      string    `json:"type,omitempty"`
	Rooms     []string  `json:"rooms,omitempty"`
}

// memory is what the sources keep across restarts.
type memory struct {
	WarningsSeen bool                 `json:"warnings_seen"`
	Warnings     map[string]warnEntry `json:"warnings"`
	ServiceSeen  bool                 `json:"service_seen"`
	Service      map[string]svcEntry  `json:"service"`
	SystemSeen   bool                 `json:"system_seen"`
	Version      string               `json:"version,omitempty"`
	Backup       time.Time            `json:"backup,omitzero"`
	BootID       string               `json:"boot_id,omitempty"`
}

// State is one source's state for the page.
type State struct {
	OK    bool      `json:"ok"`
	At    time.Time `json:"at,omitzero"`
	Error string    `json:"error,omitempty"`
	// Open is how many warnings or service messages stand; Watching how many datapoints the
	// stream is filtered to.
	Open     int  `json:"open"`
	Watching int  `json:"watching"`
	Stream   bool `json:"stream"`
}

// Options configure the sources.
type Options struct {
	// Base is the system's URL, HTTP a client that carries the addon's token.
	Base   string
	HTTP   *http.Client
	Lite   *litesource.Client
	Engine *engine.Engine
	Config func() config.Config
	Now    func() time.Time
	// BootID answers the kernel's boot id ("" when it cannot be read).
	BootID func() string
	Log    func(format string, args ...any)
}

// Sources reads the system.
type Sources struct {
	o     Options
	mu    sync.Mutex
	mem   memory
	dirty bool
	state map[string]*State
	names *litesource.Names
	kick  chan struct{}
	// dpKick wakes the datapoint loop after a configuration change.
	dpKick chan struct{}
	// OnEvent is called after events or a datapoint's value reached the engine, so the daemon's
	// loop looks at what waits now.
	OnEvent func()
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// New makes the sources.
func New(o Options) *Sources {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.BootID == nil {
		o.BootID = func() string { return "" }
	}
	return &Sources{o: o, mem: memory{Warnings: map[string]warnEntry{}, Service: map[string]svcEntry{}},
		state: map[string]*State{"warnings": {}, "service": {}, "system": {}, "datapoints": {}}, kick: make(chan struct{}, 1), dpKick: make(chan struct{}, 1)}
}

// Export answers the memory as JSON, and whether it changed since the last Export.
func (s *Sources) Export() (json.RawMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.mem)
	d := s.dirty
	s.dirty = false
	return b, d
}

// Import restores the memory.
func (s *Sources) Import(b json.RawMessage) {
	if len(b) == 0 {
		return
	}
	var m memory
	if json.Unmarshal(b, &m) != nil {
		return
	}
	if m.Warnings == nil {
		m.Warnings = map[string]warnEntry{}
	}
	if m.Service == nil {
		m.Service = map[string]svcEntry{}
	}
	s.mu.Lock()
	s.mem = m
	s.mu.Unlock()
}

// States answers the sources' states.
func (s *Sources) States() map[string]State {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]State{}
	for k, v := range s.state {
		out[k] = *v
	}
	return out
}

func (s *Sources) setState(name string, err error, f func(*State)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state[name]
	st.At = s.o.Now()
	st.OK = err == nil
	st.Error = ""
	if err != nil {
		st.Error = err.Error()
	}
	if f != nil {
		f(st)
	}
}

// Kick reads the polled sources now, and lets the datapoint loop look at the rules again.
func (s *Sources) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
	select {
	case s.dpKick <- struct{}{}:
	default:
	}
}

// errNoRoute: the system does not have the route (an older openccu-lite).
var errNoRoute = errors.New("the system does not offer this route")

// get reads a route of the system API.
func (s *Sources) get(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.o.Base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := s.o.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != 200 {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
			Scope   string `json:"scope"`
		}
		_ = json.Unmarshal(body, &e)
		switch {
		case res.StatusCode == 404:
			return errNoRoute
		case res.StatusCode == 403 && e.Scope != "":
			return fmt.Errorf("the addon's token lacks the scope %s", e.Scope)
		case e.Error != "":
			return fmt.Errorf("%s: %s %s", path, res.Status, e.Error)
		}
		return fmt.Errorf("%s: %s", path, res.Status)
	}
	return json.Unmarshal(body, out)
}

// Run reads the sources until ctx ends.
func (s *Sources) Run(ctx context.Context) {
	s.boot()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); s.pollLoop(ctx) }()
	go func() { defer wg.Done(); s.serviceStream(ctx) }()
	go func() { defer wg.Done(); s.datapointLoop(ctx) }()
	wg.Wait()
}

func (s *Sources) pollLoop(ctx context.Context) {
	var lastSystem, lastService, lastNames time.Time
	t := time.NewTicker(WarningsEvery)
	defer t.Stop()
	for {
		now := s.o.Now()
		s.PollWarnings(ctx)
		if now.Sub(lastSystem) >= SystemEvery-time.Second {
			s.PollSystem(ctx)
			lastSystem = now
		}
		// the stream delivers every change; the poll is the backstop for a stream that hangs
		if now.Sub(lastService) >= ServiceEvery-time.Second {
			s.PollService(ctx)
			lastService = now
		}
		if now.Sub(lastNames) >= NamesEvery {
			s.RefreshNames(ctx)
			lastNames = now
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
			lastSystem, lastService = time.Time{}, time.Time{}
		}
	}
}

// routed says which warnings are system events rather than warnings.
func routed(id string) string {
	switch id {
	case "system-update-available":
		return texts.SysUpdateAvailable
	case "backup-delivery", "backup-target":
		return texts.SysBackupFailed
	}
	return ""
}

func (s *Sources) lang() string { return s.o.Config().Settings.Language }

func warnEvent(w warnEntry, lang string, resolved bool, at time.Time) engine.Event {
	detail := w.Detail
	if detail == "" {
		detail = texts.SeverityName(lang, w.Severity) + " · " + texts.T(lang, "status-page")
	}
	ev := engine.Event{Source: config.SrcWarning, Key: w.ID + "/" + w.Variant, Resolved: resolved, At: at, Severity: w.Severity, WarningID: w.ID,
		Item: message.Item{Title: w.Title, Detail: detail, Severity: w.Severity, Link: w.Href}}
	if kind := routed(w.ID); kind != "" {
		ev.Source, ev.SystemKind = config.SrcSystem, kind
		if kind == texts.SysBackupFailed {
			ev.Item.Title = texts.T(lang, "sys-backup-failed") + ": " + w.Title
		}
	}
	return ev
}

// PollWarnings reads the Status page's warnings and reports what is new and what is over.
func (s *Sources) PollWarnings(ctx context.Context) {
	var view struct {
		Warnings []struct {
			ID       string         `json:"id"`
			Variant  string         `json:"variant"`
			Severity string         `json:"severity"`
			Params   map[string]any `json:"params"`
			Href     string         `json:"href"`
		} `json:"warnings"`
	}
	if err := s.get(ctx, "/api/system/v1/warnings", &view); err != nil {
		s.setState("warnings", err, nil)
		return
	}
	lang, now := s.lang(), s.o.Now()
	seen := map[string]warnEntry{}
	for _, w := range view.Warnings {
		title, detail := texts.Warning(lang, w.ID, w.Variant, w.Params)
		seen[w.ID+"/"+w.Variant] = warnEntry{ID: w.ID, Variant: w.Variant, Severity: w.Severity, Title: title, Detail: detail, Href: w.Href}
	}
	var events []engine.Event
	s.mu.Lock()
	if s.mem.WarningsSeen {
		for k, w := range seen {
			if _, ok := s.mem.Warnings[k]; !ok {
				events = append(events, warnEvent(w, lang, false, now))
			}
		}
		for k, w := range s.mem.Warnings {
			if _, ok := seen[k]; !ok {
				events = append(events, warnEvent(w, lang, true, now))
			}
		}
	}
	if !s.mem.WarningsSeen || len(events) > 0 {
		s.dirty = true
	}
	s.mem.Warnings, s.mem.WarningsSeen = seen, true
	s.mu.Unlock()
	sort.Slice(events, func(i, j int) bool { return events[i].Key < events[j].Key })
	for _, ev := range events {
		s.o.Engine.Handle(ev, now)
	}
	if len(events) > 0 {
		s.wake()
	}
	s.setState("warnings", nil, func(st *State) { st.Open = len(seen) })
}

type serviceView struct {
	Errors map[string]string `json:"errors"`
	Feed   *struct {
		Connected  bool `json:"connected"`
		Interfaces []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"interfaces"`
	} `json:"feed"`
	Messages []struct {
		Interface string    `json:"interface"`
		Address   string    `json:"address"`
		Key       string    `json:"key"`
		Value     any       `json:"value"`
		Since     time.Time `json:"since"`
		Type      string    `json:"type"`
		Name      string    `json:"name"`
		EnumNames []string  `json:"enum_names"`
		Enums     []string  `json:"enums"`
	} `json:"messages"`
	Swept time.Time `json:"swept"`
}

// PollService reads the service messages once.
func (s *Sources) PollService(ctx context.Context) {
	var v serviceView
	if err := s.get(ctx, "/api/system/v1/service-messages", &v); err != nil {
		s.setState("service", err, nil)
		return
	}
	s.handleService(v)
}

func svcKey(iface, address, key string) string { return iface + "|" + address + "|" + key }

func svcEvent(m svcEntry, lang string, resolved bool, at time.Time) engine.Event {
	name := m.Name
	if name == "" {
		name = m.Address
	}
	var detail []string
	if len(m.Rooms) > 0 {
		detail = append(detail, strings.Join(m.Rooms, ", "))
	}
	detail = append(detail, strings.TrimSpace(m.Interface+" "+m.Address))
	if !resolved && !m.Since.IsZero() {
		detail = append(detail, texts.Since(lang, m.Since))
	}
	return engine.Event{Source: config.SrcService, Key: svcKey(m.Interface, m.Address, m.Key), Resolved: resolved, At: at, ServiceKind: texts.ServiceKind(m.Key), Address: m.Address,
		Item: message.Item{Title: name, Text: texts.Service(lang, m.Key, m.Value), Detail: strings.Join(detail, " · "), Device: name, Interface: m.Interface, Address: m.Address, Rooms: m.Rooms, Datapoint: m.Key, Value: m.Value, Since: m.Since}}
}

// handleService takes a view of the service messages. A message of an interface that is down
// or whose sweep failed is kept as it was: its absence says nothing.
func (s *Sources) handleService(v serviceView) {
	lang, now := s.lang(), s.o.Now()
	// before its first sweep the system has no view to compare with
	if v.Swept.IsZero() && len(v.Messages) == 0 {
		s.setState("service", nil, nil)
		return
	}
	unsure := map[string]bool{}
	for iface := range v.Errors {
		unsure[iface] = true
	}
	if v.Feed != nil {
		for _, i := range v.Feed.Interfaces {
			if i.State != "up" {
				unsure[i.Name] = true
			}
		}
	}
	seen := map[string]svcEntry{}
	for _, m := range v.Messages {
		// the rooms' names: the enums are rooms and functions side by side
		var rooms []string
		for i, n := range m.EnumNames {
			if n != "" && (len(m.Enums) != len(m.EnumNames) || strings.HasPrefix(m.Enums[i], "room/")) {
				rooms = append(rooms, n)
			}
		}
		seen[svcKey(m.Interface, m.Address, m.Key)] = svcEntry{Interface: m.Interface, Address: m.Address, Key: m.Key, Value: m.Value, Since: m.Since, Name: m.Name, Type: m.Type, Rooms: rooms}
	}
	var events []engine.Event
	s.mu.Lock()
	for k, m := range s.mem.Service {
		if _, ok := seen[k]; ok {
			continue
		}
		if unsure[m.Interface] || (v.Feed != nil && !v.Feed.Connected) {
			seen[k] = m
			continue
		}
		events = append(events, svcEvent(m, lang, true, now))
	}
	if s.mem.ServiceSeen {
		for k, m := range seen {
			if _, ok := s.mem.Service[k]; !ok {
				events = append(events, svcEvent(m, lang, false, now))
			}
		}
	}
	if !s.mem.ServiceSeen || len(events) > 0 {
		s.dirty = true
	}
	s.mem.Service, s.mem.ServiceSeen = seen, true
	s.mu.Unlock()
	sort.Slice(events, func(i, j int) bool { return events[i].Key < events[j].Key })
	for _, ev := range events {
		s.o.Engine.Handle(ev, now)
	}
	if len(events) > 0 {
		s.wake()
	}
	s.setState("service", nil, func(st *State) { st.Open = len(seen) })
}

// serviceStream holds the service-message stream open: the whole view arrives on every change.
func (s *Sources) serviceStream(ctx context.Context) {
	wait := time.Second
	for ctx.Err() == nil {
		start := s.o.Now()
		err := s.readStream(ctx)
		s.mu.Lock()
		s.state["service"].Stream = false
		s.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.o.Log("the service-message stream ended: %v", err)
		}
		if s.o.Now().Sub(start) > time.Minute {
			wait = time.Second
		} else if wait < time.Minute {
			wait *= 2
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (s *Sources) readStream(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.o.Base+"/api/system/v1/service-messages/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	res, err := s.o.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("%s", res.Status)
	}
	s.mu.Lock()
	s.state["service"].Stream = true
	s.mu.Unlock()
	// a stream that says nothing for too long is dead: the watchdog ends the request
	alive := make(chan struct{}, 1)
	go func() {
		t := time.NewTimer(StreamSilence)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-alive:
				if !t.Stop() {
					select {
					case <-t.C:
					default:
					}
				}
				t.Reset(StreamSilence)
			case <-t.C:
				cancel()
				return
			}
		}
	}()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var event string
	var data []string
	for sc.Scan() {
		select {
		case alive <- struct{}{}:
		default:
		}
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			if event == "messages" && len(data) > 0 {
				var v serviceView
				if json.Unmarshal([]byte(strings.Join(data, "\n")), &v) == nil {
					s.handleService(v)
				}
			}
			event, data = "", nil
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

// boot reports a restart of the system: another boot id than the one remembered.
func (s *Sources) boot() {
	id := s.o.BootID()
	if id == "" {
		return
	}
	s.mu.Lock()
	old := s.mem.BootID
	s.mem.BootID = id
	if old != id {
		s.dirty = true
	}
	s.mu.Unlock()
	if old == "" || old == id {
		return
	}
	lang, now := s.lang(), s.o.Now()
	s.o.Engine.Handle(engine.Event{Source: config.SrcSystem, Key: "reboot/" + id, Once: true, At: now, SystemKind: texts.SysReboot,
		Item: message.Item{Title: texts.T(lang, "sys-reboot"), Detail: texts.Clock(lang, now)}}, now)
	s.wake()
}

type lastBackup struct {
	Time      time.Time `json:"time"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	Encrypted bool      `json:"encrypted"`
	By        string    `json:"by"`
}

// PollSystem reads the running version and the newest backup.
func (s *Sources) PollSystem(ctx context.Context) {
	var up struct {
		Running struct {
			Version string `json:"version"`
			Lite    string `json:"lite"`
		} `json:"running"`
	}
	if err := s.get(ctx, "/api/system/v1/system-update", &up); err != nil {
		s.setState("system", err, nil)
		return
	}
	// openccu-lite 1.x does not say which backup was the last: no "backup made" there
	var last map[string]*lastBackup
	if err := s.get(ctx, "/api/system/v1/backup/last", &last); err != nil && !errors.Is(err, errNoRoute) {
		s.setState("system", err, nil)
		return
	}
	version := up.Running.Lite
	if version == "" {
		version = up.Running.Version
	}
	var newest *lastBackup
	way := ""
	for w, b := range last {
		if b != nil && !b.Time.IsZero() && (newest == nil || b.Time.After(newest.Time)) {
			newest, way = b, w
		}
	}
	lang, now := s.lang(), s.o.Now()
	var events []engine.Event
	s.mu.Lock()
	if s.mem.SystemSeen {
		if version != "" && s.mem.Version != "" && version != s.mem.Version {
			events = append(events, engine.Event{Source: config.SrcSystem, Key: "update-installed/" + version, Once: true, At: now, SystemKind: texts.SysUpdateInstalled,
				Item: message.Item{Title: texts.T(lang, "sys-update-installed", version), Detail: texts.T(lang, "sys-update-from", s.mem.Version)}})
		}
		if newest != nil && newest.Time.After(s.mem.Backup) {
			detail := []string{newest.Name, fmt.Sprintf("%.1f MB", float64(newest.Size)/1e6)}
			if w := wayName(lang, way, newest.By); w != "" {
				detail = append(detail, w)
			}
			events = append(events, engine.Event{Source: config.SrcSystem, Key: "backup/" + newest.Time.UTC().Format(time.RFC3339), Once: true, At: now, SystemKind: texts.SysBackupDone,
				Item: message.Item{Title: texts.T(lang, "sys-backup-done"), Detail: strings.Join(detail, " · "), Link: "/backup"}})
		}
	}
	if !s.mem.SystemSeen || len(events) > 0 || (version != "" && version != s.mem.Version) {
		s.dirty = true
	}
	s.mem.SystemSeen = true
	if version != "" {
		s.mem.Version = version
	}
	if newest != nil {
		s.mem.Backup = newest.Time
	}
	s.mu.Unlock()
	for _, ev := range events {
		s.o.Engine.Handle(ev, now)
	}
	if len(events) > 0 {
		s.wake()
	}
	s.setState("system", nil, nil)
}

func wayName(lang, way, by string) string {
	de := texts.Lang(lang) == "de"
	var s string
	switch way {
	case "automatic":
		s = map[bool]string{true: "automatisch", false: "automatic"}[de]
	case "run":
		s = map[bool]string{true: "Jetzt sichern", false: "Back up now"}[de]
	case "download":
		s = map[bool]string{true: "Download im Browser", false: "download in the browser"}[de]
	case "client":
		s = map[bool]string{true: "von einem Programm abgeholt", false: "fetched by a program"}[de]
	}
	if by != "" && s != "" {
		s += " (" + by + ")"
	}
	return s
}

// Open answers what stands now, for the digest and the page: the warnings (not the quiet
// notices) and the service messages.
func (s *Sources) Open(lang string) engine.Open {
	s.mu.Lock()
	defer s.mu.Unlock()
	var o engine.Open
	for k, w := range s.mem.Warnings {
		if w.Severity == "info" {
			continue
		}
		o.Warnings = append(o.Warnings, message.Item{Key: k, Title: w.Title, Line: w.Title, Severity: w.Severity, Link: w.Href})
	}
	for k, m := range s.mem.Service {
		ev := svcEvent(m, lang, false, time.Time{})
		it := ev.Item
		it.Key = k
		it.Line = it.Title + ": " + it.Text
		o.Service = append(o.Service, it)
	}
	sort.Slice(o.Warnings, func(i, j int) bool {
		if o.Warnings[i].Severity != o.Warnings[j].Severity {
			return o.Warnings[i].Severity < o.Warnings[j].Severity
		}
		return o.Warnings[i].Key < o.Warnings[j].Key
	})
	sort.Slice(o.Service, func(i, j int) bool { return o.Service[i].Line < o.Service[j].Line })
	return o
}
