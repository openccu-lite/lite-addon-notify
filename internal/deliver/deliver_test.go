package deliver

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openccu-lite/lite-addon-notify/internal/channel"
	"github.com/openccu-lite/lite-addon-notify/internal/config"
	"github.com/openccu-lite/lite-addon-notify/internal/message"
)

// rig is a dispatcher with a fake send, a fake clock and waits that end at once.
type rig struct {
	t        *testing.T
	d        *Dispatcher
	mu       sync.Mutex
	now      time.Time
	sent     []message.Message
	fail     func(n int, m message.Message) error
	calls    int
	waits    []time.Duration
	failing  []string
	settings config.Settings
	channels map[string]config.Channel
	cancel   context.CancelFunc
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, now: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC), settings: config.Settings{Language: "en", RatePerHour: 0},
		channels: map[string]config.Channel{"a": {ID: "a", Name: "A", Type: config.Ntfy, Enabled: true}, "b": {ID: "b", Name: "B", Type: config.Ntfy, Enabled: true}}}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	t.Cleanup(cancel)
	r.d = New(Options{Context: ctx, HistoryFile: filepath.Join(t.TempDir(), "history.json"), HistorySize: 50,
		Channel: func(id string) (config.Channel, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			ch, ok := r.channels[id]
			return ch, ok
		},
		Settings: func() config.Settings { r.mu.Lock(); defer r.mu.Unlock(); return r.settings },
		Now:      func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now },
		After: func(d time.Duration) <-chan time.Time {
			r.mu.Lock()
			r.waits = append(r.waits, d)
			r.mu.Unlock()
			c := make(chan time.Time, 1)
			c <- time.Time{}
			return c
		},
		Send: func(ctx context.Context, ch config.Channel, m message.Message) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.calls++
			if r.fail != nil {
				if err := r.fail(r.calls, m); err != nil {
					return err
				}
			}
			r.sent = append(r.sent, m)
			return nil
		},
		Failing: func(ch config.Channel, failing bool, reason string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.failing = append(r.failing, ch.ID+map[bool]string{true: " failing", false: " ok"}[failing])
		}})
	return r
}

// settle waits until the queues are empty and the last send returned.
func (r *rig) settle(want int) {
	r.t.Helper()
	for i := 0; i < 400; i++ {
		if len(r.d.History()) >= want {
			queued := 0
			for _, s := range r.d.Status() {
				queued += s.Queued
			}
			if queued == 0 {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("history has %d rows, want %d: %+v", len(r.d.History()), want, r.d.History())
}

func m(title string) message.Message {
	return message.Message{ID: title, Kind: message.KindService, Title: title, Body: "body", Rule: "r", RuleName: "Rule"}
}

func TestDeliverAndRetry(t *testing.T) {
	r := newRig(t)
	r.fail = func(n int, _ message.Message) error {
		if n <= 2 {
			return &channel.Error{Step: channel.StepConnect, Msg: "connection refused"}
		}
		return nil
	}
	r.d.Enqueue([]string{"a", "gone"}, m("one"))
	r.settle(3)
	h := r.d.History()
	if h[0].Result != Sent || h[0].Attempt != 3 || h[1].Result != Failed || h[2].Result != Failed || h[2].Step != channel.StepConnect || h[2].Error != "connection refused" || h[0].ChannelName != "A" || h[0].RuleName != "Rule" {
		t.Errorf("history: %+v", h)
	}
	r.mu.Lock()
	if len(r.waits) != 2 || r.waits[0] != DefaultBackoff[0] || r.waits[1] != DefaultBackoff[1] {
		t.Errorf("waits: %v", r.waits)
	}
	r.mu.Unlock()
	st := r.d.Status()["a"]
	if st.Sent != 1 || st.Failed != 0 || st.Failing || st.LastOK.IsZero() || st.LastError != "connection refused" {
		t.Errorf("status: %+v", st)
	}
}

func TestGiveUpAndRecover(t *testing.T) {
	r := newRig(t)
	down := true
	r.fail = func(int, message.Message) error {
		if down {
			return errors.New("boom")
		}
		return nil
	}
	r.d.Enqueue([]string{"a"}, m("one"))
	r.settle(5)
	if h := r.d.History(); h[0].Result != GaveUp || h[0].Attempt != 5 {
		t.Fatalf("history: %+v", h)
	}
	// while it fails every further message gets one attempt
	r.d.Enqueue([]string{"a"}, m("two"))
	r.settle(6)
	if h := r.d.History(); h[0].Result != GaveUp || h[0].Attempt != 1 || h[0].Title != "two" {
		t.Fatalf("history: %+v", h)
	}
	r.mu.Lock()
	down = false
	r.mu.Unlock()
	r.d.Enqueue([]string{"a"}, m("three"))
	r.settle(7)
	if st := r.d.Status()["a"]; st.Failing || st.Sent != 1 || st.Failed != 2 {
		t.Errorf("status: %+v", st)
	}
	r.mu.Lock()
	if strings.Join(r.failing, ",") != "a failing,a ok" {
		t.Errorf("the Status page was told: %v", r.failing)
	}
	r.mu.Unlock()
}

func TestHistoryIsARing(t *testing.T) {
	d := New(Options{HistorySize: 2, Channel: func(string) (config.Channel, bool) { return config.Channel{}, false }, Settings: func() config.Settings { return config.Settings{} },
		Send: func(context.Context, config.Channel, message.Message) error { return nil }})
	for _, title := range []string{"1", "2", "3"} {
		_ = d.Test(context.Background(), config.Channel{ID: "a"}, m(title))
	}
	if h := d.History(); len(h) != 2 || h[0].Title != "3" || h[1].Title != "2" {
		t.Errorf("history: %+v", h)
	}
}

func TestPermanentErrorIsNotRetried(t *testing.T) {
	r := newRig(t)
	r.fail = func(int, message.Message) error {
		return &channel.Error{Step: channel.StepLogin, Msg: "535 refused", Permanent: true}
	}
	r.d.Enqueue([]string{"a"}, m("one"))
	r.settle(1)
	if h := r.d.History(); len(h) != 1 || h[0].Result != GaveUp || h[0].Step != channel.StepLogin {
		t.Errorf("history: %+v", h)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls != 1 || len(r.waits) != 0 {
		t.Errorf("calls %d waits %v", r.calls, r.waits)
	}
}

func TestRateLimit(t *testing.T) {
	r := newRig(t)
	r.settings.RatePerHour = 2
	for _, title := range []string{"1", "2", "3", "4"} {
		r.d.Enqueue([]string{"a"}, m(title))
	}
	r.d.Enqueue([]string{"b"}, m("other channel"))
	r.settle(5)
	var results []string
	for _, h := range r.d.History() {
		if h.Channel == "a" {
			results = append(results, h.Title+" "+h.Result)
		}
	}
	if strings.Join(results, ",") != "4 limited,3 limited,2 sent,1 sent" {
		t.Errorf("results: %v", results)
	}
	if st := r.d.Status()["a"]; st.Held != 2 {
		t.Errorf("held: %+v", st)
	}
	// an hour later the next one passes and says how many were held back
	r.mu.Lock()
	r.now = r.now.Add(61 * time.Minute)
	r.mu.Unlock()
	r.d.Enqueue([]string{"a"}, m("5"))
	r.settle(6)
	r.mu.Lock()
	last := r.sent[len(r.sent)-1]
	r.mu.Unlock()
	if last.Title != "5" || !strings.Contains(last.Body, "2 more messages were held back") {
		t.Errorf("the next message: %+v", last)
	}
}

func TestSwitchedOffAndHistoryFile(t *testing.T) {
	r := newRig(t)
	ch := r.channels["a"]
	ch.Enabled = false
	r.channels["a"] = ch
	r.d.Enqueue([]string{"a"}, m("one"))
	r.settle(1)
	if h := r.d.History(); h[0].Result != Dropped {
		t.Errorf("history: %+v", h)
	}
	// a test goes out at once and is recorded for a saved channel
	if err := r.d.Test(context.Background(), config.Channel{ID: "b", Name: "B"}, m("test")); err != nil {
		t.Fatal(err)
	}
	if err := r.d.Test(context.Background(), config.Channel{Name: "unsaved"}, m("test")); err != nil {
		t.Fatal(err)
	}
	if len(r.d.History()) != 2 {
		t.Errorf("history: %+v", r.d.History())
	}
	// the history survives a restart
	done := make(chan struct{})
	go func() { r.d.Run(); close(done) }()
	r.cancel()
	<-done
	d2 := New(Options{HistoryFile: r.d.o.HistoryFile, Channel: func(string) (config.Channel, bool) { return config.Channel{}, false }, Settings: func() config.Settings { return config.Settings{} }})
	if len(d2.History()) != 2 {
		t.Errorf("after a restart: %+v", d2.History())
	}
	d2.Clear()
	if len(d2.History()) != 0 {
		t.Error("clear")
	}
}
