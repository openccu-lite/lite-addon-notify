// Package fakesys is the slice of openccu-lite's API the addon talks to, for the tests and the
// page's development: the kit's fake of lite-rpc, the state store and the metadata, and on top
// of it the Status page's warnings, the service messages with their stream, the running
// version and the newest backup - all set by the tests, or over /dev/… by the e2e specs. /sink/…
// records whatever a channel sends there.
package fakesys

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/ccu-addon-kit/go/litesource/fakelite"
)

// Request is one request a sink received.
type Request struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  string            `json:"query,omitempty"`
	Header map[string]string `json:"header"`
	Body   string            `json:"body"`
}

// Server is the fake system.
type Server struct {
	// Lite is the kit's fake behind the routes this one does not answer itself.
	Lite *fakelite.Server
	mu   sync.Mutex
	// Warnings is GET /warnings' list.
	Warnings []map[string]any
	// Messages is the service messages' list; Swept the view's sweep time.
	Messages []map[string]any
	Swept    time.Time
	// Down are interfaces the feed reports as not up.
	Down map[string]bool
	// Version is the running openccu-lite version; Backups GET /backup/last.
	Version string
	Backups map[string]any
	subs    map[chan []byte]struct{}
	// Sink holds what the sinks received, oldest first; SinkStatus answers per sink name
	// (the path's first element after /sink/) another status than 200.
	Sink       []Request
	SinkStatus map[string]int
	mails      []string
}

// New makes a fake system with a few named devices.
func New() *Server {
	s := &Server{Lite: fakelite.New(), Version: "2.0.0", Backups: map[string]any{}, subs: map[chan []byte]struct{}{}, Swept: time.Now(), Down: map[string]bool{}, SinkStatus: map[string]int{}}
	s.Lite.Meta = map[string]any{"format": 1, "revision": 1,
		"objects": map[string]any{
			"HmIP-RF.0000D3C99C6B41":   map[string]any{"name": "Fenster Küche", "enums": []string{"room/kitchen"}},
			"HmIP-RF.0000D3C99C6B41:1": map[string]any{"name": "Fenster Küche", "enums": []string{"room/kitchen"}},
			"HmIP-RF.000A1BE9A1B2C3":   map[string]any{"name": "Heizung Bad", "enums": []string{"room/bath"}},
			"HmIP-RF.000A1BE9A1B2C3:1": map[string]any{"name": "Heizung Bad:1", "enums": []string{"room/bath"}},
		},
		"enums": map[string]any{"room": map[string]any{"name": map[string]string{"de": "Räume", "en": "Rooms"}, "tree": []any{
			map[string]any{"id": "kitchen", "name": map[string]string{"de": "Küche", "en": "Kitchen"}},
			map[string]any{"id": "bath", "name": map[string]string{"de": "Bad", "en": "Bathroom"}},
		}}}}
	now := time.Now().UTC().Format(time.RFC3339)
	s.Lite.State = []map[string]any{
		{"interface": "HmIP-RF", "address": "0000D3C99C6B41:1", "datapoint": "STATE", "value": 0, "ts": now, "lc": now, "confirmed": true, "source": "event"},
		{"interface": "HmIP-RF", "address": "0000D3C99C6B41:0", "datapoint": "LOW_BAT", "value": false, "ts": now, "lc": now, "confirmed": true, "source": "event"},
		{"interface": "HmIP-RF", "address": "000A1BE9A1B2C3:1", "datapoint": "ACTUAL_TEMPERATURE", "value": 21.5, "ts": now, "lc": now, "confirmed": true, "source": "event"},
	}
	s.Lite.EventID = "boot1-0"
	return s
}

// Close stops the fake.
func (s *Server) Close() { s.Lite.Close() }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// SetWarnings replaces the warnings.
func (s *Server) SetWarnings(w ...map[string]any) {
	s.mu.Lock()
	s.Warnings = w
	s.mu.Unlock()
}

// Warning makes a warning as the system answers it.
func Warning(id, variant, severity string, params map[string]any) map[string]any {
	return map[string]any{"id": id, "variant": variant, "severity": severity, "params": params, "href": "/#" + id}
}

// Message makes a service message as the system answers it.
func Message(address, key, name, room string) map[string]any {
	m := map[string]any{"interface": "HmIP-RF", "address": address, "channel": "0", "key": key, "value": true, "since": time.Now().Format(time.RFC3339), "seen": "event", "type": "HmIP-eTRV-2"}
	if name != "" {
		m["name"] = name
	}
	if room != "" {
		m["enums"], m["enum_names"] = []string{"room/x"}, []string{room}
	}
	return m
}

// SetMessages replaces the service messages and tells the stream.
func (s *Server) SetMessages(m ...map[string]any) {
	s.mu.Lock()
	s.Messages = m
	s.Swept = time.Now()
	b, _ := json.Marshal(s.view())
	for ch := range s.subs {
		select {
		case ch <- b:
		default:
		}
	}
	s.mu.Unlock()
}

// SetVersion sets the running version; SetBackup the newest backup of a way.
func (s *Server) SetVersion(v string) {
	s.mu.Lock()
	s.Version = v
	s.mu.Unlock()
}

// SetBackup records a backup of a way (automatic, run, download, client).
func (s *Server) SetBackup(way, name string, size int64, at time.Time) {
	s.mu.Lock()
	s.Backups[way] = map[string]any{"time": at.Format(time.RFC3339Nano), "name": name, "size": size, "encrypted": true}
	s.mu.Unlock()
}

// Sunk answers what the sinks received.
func (s *Server) Sunk() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.Sink...)
}

func (s *Server) view() map[string]any {
	var ifaces []map[string]any
	for _, n := range []string{"BidCos-RF", "HmIP-RF"} {
		state := "up"
		if s.Down[n] {
			state = "down"
		}
		ifaces = append(ifaces, map[string]any{"name": n, "state": state})
	}
	msgs := s.Messages
	if msgs == nil {
		msgs = []map[string]any{}
	}
	return map[string]any{"count": len(msgs), "errors": map[string]string{}, "feed": map[string]any{"connected": true, "boot_id": "b", "interfaces": ifaces}, "messages": msgs, "swept": s.Swept.Format(time.RFC3339Nano)}
}

// Handler is the fake's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/system/v1/warnings", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		l := s.Warnings
		s.mu.Unlock()
		if l == nil {
			l = []map[string]any{}
		}
		writeJSON(w, 200, map[string]any{"warnings": l})
	})
	mux.HandleFunc("GET /api/system/v1/service-messages", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		v := s.view()
		s.mu.Unlock()
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("GET /api/system/v1/service-messages/stream", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flush", 500)
			return
		}
		ch := make(chan []byte, 8)
		s.mu.Lock()
		first, _ := json.Marshal(s.view())
		s.subs[ch] = struct{}{}
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.subs, ch)
			s.mu.Unlock()
		}()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprintf(w, "event: messages\ndata: %s\n\n", first)
		fl.Flush()
		ping := time.NewTicker(10 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case b := <-ch:
				fmt.Fprintf(w, "event: messages\ndata: %s\n\n", b)
			case <-ping.C:
				fmt.Fprint(w, ": ping\n\n")
			}
			fl.Flush()
		}
	})
	mux.HandleFunc("GET /api/system/v1/system-update", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		v := s.Version
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"busy": nil, "feed": map[string]any{"enabled": false}, "running": map[string]any{"version": "3.89.11", "lite": v}, "staged": nil})
	})
	mux.HandleFunc("GET /api/system/v1/backup/last", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, 200, s.Backups)
	})
	// the sinks: whatever a channel sends
	mux.HandleFunc("/sink/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		h := map[string]string{}
		for k := range r.Header {
			h[k] = r.Header.Get(k)
		}
		name, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/sink/"), "/")
		s.mu.Lock()
		s.Sink = append(s.Sink, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: h, Body: string(body)})
		status := s.SinkStatus[name]
		s.mu.Unlock()
		if status != 0 && status != 200 {
			writeJSON(w, status, map[string]any{"error": "the sink is told to refuse", "errors": []string{"the sink is told to refuse"}, "description": "the sink is told to refuse"})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "status": 1})
	})
	// the dev routes the e2e specs drive
	mux.HandleFunc("POST /dev/warnings", func(w http.ResponseWriter, r *http.Request) {
		var l []map[string]any
		if json.NewDecoder(r.Body).Decode(&l) != nil {
			http.Error(w, "a JSON list", 400)
			return
		}
		s.SetWarnings(l...)
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /dev/messages", func(w http.ResponseWriter, r *http.Request) {
		var l []map[string]any
		if json.NewDecoder(r.Body).Decode(&l) != nil {
			http.Error(w, "a JSON list", 400)
			return
		}
		s.SetMessages(l...)
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /dev/event", func(w http.ResponseWriter, r *http.Request) {
		var e struct {
			Interface, Address, Key string
			Value                   any
		}
		if json.NewDecoder(r.Body).Decode(&e) != nil {
			http.Error(w, "a JSON object", 400)
			return
		}
		s.Lite.PushEvent(e.Interface, e.Address, e.Key, e.Value)
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /dev/sink-status", func(w http.ResponseWriter, r *http.Request) {
		var m map[string]int
		if json.NewDecoder(r.Body).Decode(&m) != nil {
			http.Error(w, "a JSON object", 400)
			return
		}
		s.mu.Lock()
		for k, v := range m {
			s.SinkStatus[k] = v
		}
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /dev/sink", func(w http.ResponseWriter, r *http.Request) {
		l := s.Sunk()
		if l == nil {
			l = []Request{}
		}
		writeJSON(w, 200, l)
	})
	mux.HandleFunc("GET /dev/mails", func(w http.ResponseWriter, r *http.Request) {
		l := s.Mails()
		if l == nil {
			l = []string{}
		}
		writeJSON(w, 200, l)
	})
	mux.HandleFunc("DELETE /dev/sink", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.Sink = nil
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.Handle("/", s.Lite.Config.Handler)
	return mux
}
