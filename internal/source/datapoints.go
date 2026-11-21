package source

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/hobbyquaker/ccu-addon-kit/go/litesource"

	"github.com/openccu-lite/lite-addon-notify/internal/engine"
)

func wantedList(w map[string]bool) []string {
	out := make([]string, 0, len(w))
	for k := range w {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// filterOf narrows the stream to the rules' interfaces, addresses and datapoints. The stream
// combines the three with AND, so it may carry a few more values than the rules watch (rule A's
// address with rule B's datapoint); the engine takes only its own.
func filterOf(w map[string]bool) litesource.Filter {
	set := [3]map[string]bool{{}, {}, {}}
	for k := range w {
		for i, p := range strings.SplitN(k, "|", 3) {
			set[i][p] = true
		}
	}
	list := func(m map[string]bool) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	return litesource.Filter{Interfaces: list(set[0]), Addresses: list(set[1]), Keys: list(set[2])}
}

// datapointLoop keeps lite-rpc's event stream open while datapoint rules exist: the values
// of the watched datapoints from the state store first, then their events. No rule, no stream.
func (s *Sources) datapointLoop(ctx context.Context) {
	for ctx.Err() == nil {
		wanted := engine.Wanted(s.o.Config())
		s.mu.Lock()
		s.state["datapoints"].Watching = len(wanted)
		s.state["datapoints"].Stream = false
		s.mu.Unlock()
		if len(wanted) == 0 || s.o.Lite == nil {
			s.setState("datapoints", nil, nil)
			select {
			case <-ctx.Done():
				return
			case <-s.dpKick:
			}
			continue
		}
		s.watch(ctx, wanted)
	}
}

// watch reads the wanted datapoints until the rules change or ctx ends.
func (s *Sources) watch(ctx context.Context, wanted map[string]bool) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	key := strings.Join(wantedList(wanted), "\n")
	f := filterOf(wanted)
	s.RefreshNames(ctx)
	seed := func() (string, bool) {
		snap, err := s.o.Lite.State(ctx, f)
		if err != nil {
			s.setState("datapoints", err, nil)
			return "", false
		}
		now := s.o.Now()
		for _, e := range snap.Entries {
			// the store's last key press is history, not a press: only an event is one
			if !wanted[e.Interface+"|"+e.Address+"|"+e.Key] || !e.Confirmed || engine.Momentary(e.Key) {
				continue
			}
			at := e.LC
			if at.IsZero() {
				at = e.TS
			}
			s.o.Engine.Value(e.Interface, e.Address, e.Key, e.Value, at, now)
		}
		s.setState("datapoints", nil, func(st *State) { st.Stream = true })
		return snap.EventID, true
	}
	id, ok := seed()
	if !ok {
		select {
		case <-ctx.Done():
		case <-s.dpKick:
		case <-time.After(15 * time.Second):
		}
		return
	}
	events := s.o.Lite.Events(ctx, f, id)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.dpKick:
			if strings.Join(wantedList(engine.Wanted(s.o.Config())), "\n") != key {
				return
			}
		case m, open := <-events:
			if !open {
				return
			}
			switch {
			case m.Event != nil && (m.Type == "event" || m.Type == "state"):
				e := m.Event
				// a "state" message is the store's sweep telling a value it read, not an event
				if wanted[e.Interface+"|"+e.Address+"|"+e.Key] && !(m.Type == "state" && engine.Momentary(e.Key)) {
					now := s.o.Now()
					at := e.TS
					if at.IsZero() {
						at = now
					}
					s.o.Engine.Value(e.Interface, e.Address, e.Key, e.Value, at, now)
					s.wake()
				}
			case m.Resync != nil:
				if _, ok := seed(); ok {
					s.wake()
				}
			}
		}
	}
}

func (s *Sources) wake() {
	if s.OnEvent != nil {
		s.OnEvent()
	}
}

// RefreshNames reads the names and rooms from the metadata.
func (s *Sources) RefreshNames(ctx context.Context) {
	if s.o.Lite == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	n, err := s.o.Lite.Names(ctx)
	if err != nil {
		s.o.Log("the names could not be read: %v", err)
		return
	}
	s.mu.Lock()
	s.names = n
	s.mu.Unlock()
}

// Describe answers a channel's name and rooms: the channel's own, else the device's.
func (s *Sources) Describe(iface, address, lang string) (string, []string) {
	s.mu.Lock()
	n := s.names
	s.mu.Unlock()
	if n == nil {
		return "", nil
	}
	name := n.Name(iface, address)
	rooms := n.RoomNames(iface, address, lang)
	if dev, ch, isChannel := strings.Cut(address, ":"); isChannel {
		if name == "" {
			if dn := n.Name(iface, dev); dn != "" {
				name = dn + ":" + ch
			}
		}
		if len(rooms) == 0 {
			rooms = n.RoomNames(iface, dev, lang)
		}
	}
	return name, rooms
}

// Device is a channel or device for the page's picker.
type Device struct {
	Interface string   `json:"interface"`
	Address   string   `json:"address"`
	Name      string   `json:"name"`
	Rooms     []string `json:"rooms"`
	// Keys are the datapoints the state store holds for it, with their values.
	Keys []DeviceKey `json:"keys"`
}

// DeviceKey is one datapoint of a Device.
type DeviceKey struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

// Devices lists the named devices and channels with the datapoints the state store knows.
func (s *Sources) Devices(ctx context.Context, lang string) ([]Device, error) {
	if s.o.Lite == nil {
		return []Device{}, nil
	}
	s.RefreshNames(ctx)
	snap, err := s.o.Lite.State(ctx, litesource.Filter{})
	if err != nil {
		return nil, err
	}
	byRef := map[string]*Device{}
	for _, e := range snap.Entries {
		ref := e.Interface + "." + e.Address
		d := byRef[ref]
		if d == nil {
			name, rooms := s.Describe(e.Interface, e.Address, lang)
			if rooms == nil {
				rooms = []string{}
			}
			d = &Device{Interface: e.Interface, Address: e.Address, Name: name, Rooms: rooms}
			byRef[ref] = d
		}
		var v any
		_ = jsonUnmarshal(e.Value, &v)
		d.Keys = append(d.Keys, DeviceKey{Key: e.Key, Value: v})
	}
	out := make([]Device, 0, len(byRef))
	for _, d := range byRef {
		sort.Slice(d.Keys, func(i, j int) bool { return d.Keys[i].Key < d.Keys[j].Key })
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if (an == "") != (bn == "") {
			return an != ""
		}
		if an != bn {
			return an < bn
		}
		return a.Interface+a.Address < b.Interface+b.Address
	})
	return out, nil
}
