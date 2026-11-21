// Package deliver takes the engine's messages to the channels: one queue per channel, in
// order, with retries, the rate limit and a history of what was sent, what failed and why.
package deliver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/openccu-lite/lite-addon-notify/internal/channel"
	"github.com/openccu-lite/lite-addon-notify/internal/config"
	"github.com/openccu-lite/lite-addon-notify/internal/message"
	"github.com/openccu-lite/lite-addon-notify/internal/texts"
)

// The results a history row can have.
const (
	Sent    = "sent"
	Failed  = "failed"  // an attempt failed; another follows
	GaveUp  = "gave-up" // the last attempt failed, or the error is one a retry cannot cure
	Limited = "limited" // held back by the rate limit
	Dropped = "dropped" // the queue was full, or the channel is gone or switched off
)

// Record is one row of the history.
type Record struct {
	Time        time.Time `json:"time"`
	Message     string    `json:"message"`
	Channel     string    `json:"channel"`
	ChannelName string    `json:"channel_name"`
	ChannelType string    `json:"channel_type"`
	Rule        string    `json:"rule,omitempty"`
	RuleName    string    `json:"rule_name,omitempty"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	Body        string    `json:"body,omitempty"`
	Result      string    `json:"result"`
	Attempt     int       `json:"attempt,omitempty"`
	Step        string    `json:"step,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// Status is a channel's delivery state.
type Status struct {
	Queued      int       `json:"queued"`
	Sent        int       `json:"sent"`
	Failed      int       `json:"failed"`
	LastOK      time.Time `json:"last_ok,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
	LastStep    string    `json:"last_step,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitzero"`
	// Failing: the last message was given up; cleared by the next success.
	Failing bool `json:"failing"`
	// Held: messages the rate limit held back since the last one that passed.
	Held int `json:"held"`
}

// Options configure the dispatcher.
type Options struct {
	// Channel answers the channel of an id as configured now.
	Channel func(id string) (config.Channel, bool)
	// Settings answers the settings in force (the rate limit, the language).
	Settings func() config.Settings
	// Send sends once; channel.Send by default.
	Send func(ctx context.Context, ch config.Channel, m message.Message) error
	Now  func() time.Time
	// Backoff are the waits before the second and later attempts; DefaultBackoff when nil.
	Backoff []time.Duration
	// After waits (time.After by default; a test's fake).
	After func(time.Duration) <-chan time.Time
	// HistoryFile keeps the history across restarts; "" keeps it in memory only.
	HistoryFile string
	HistorySize int
	// Context ends the workers.
	Context context.Context
	// Failing is told when a channel starts failing (a message was given up) and when it works
	// again: the daemon raises and clears the Status page's notice there.
	Failing func(ch config.Channel, failing bool, reason string)
	Log     func(format string, args ...any)
}

// DefaultBackoff: a failed send is tried again after 10 s, 1 min, 5 min and 15 min.
var DefaultBackoff = []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute}

// QueueSize is how many messages wait per channel before new ones are dropped.
const QueueSize = 100

type job struct {
	m message.Message
}

type worker struct {
	jobs   chan job
	status Status
	sends  []time.Time // the sends of the last hour, for the rate limit
}

// Dispatcher delivers messages.
type Dispatcher struct {
	o       Options
	mu      sync.Mutex
	workers map[string]*worker
	history []Record
	dirty   bool
	ctx     context.Context
	wg      sync.WaitGroup
}

// New makes a dispatcher and loads its history.
func New(o Options) *Dispatcher {
	if o.Send == nil {
		o.Send = channel.Send
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Backoff == nil {
		o.Backoff = DefaultBackoff
	}
	if o.After == nil {
		o.After = time.After
	}
	if o.HistorySize == 0 {
		o.HistorySize = 200
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.Context == nil {
		o.Context = context.Background()
	}
	d := &Dispatcher{o: o, workers: map[string]*worker{}, ctx: o.Context}
	if o.HistoryFile != "" {
		if b, err := os.ReadFile(o.HistoryFile); err == nil {
			_ = json.Unmarshal(b, &d.history)
		}
	}
	return d
}

// Run keeps the history file current until the context ends, then waits for the workers.
func (d *Dispatcher) Run() {
	ctx := d.ctx
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			d.wg.Wait()
			d.save()
			return
		case <-t.C:
			d.save()
		}
	}
}

func (d *Dispatcher) save() {
	d.mu.Lock()
	if !d.dirty || d.o.HistoryFile == "" {
		d.mu.Unlock()
		return
	}
	b, _ := json.Marshal(d.history)
	d.dirty = false
	d.mu.Unlock()
	tmp := d.o.HistoryFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		d.o.Log("the history could not be written: %v", err)
		return
	}
	_ = os.Rename(tmp, d.o.HistoryFile)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func (d *Dispatcher) record(ch config.Channel, m message.Message, result string, attempt int, err error) {
	r := Record{Time: d.o.Now(), Message: m.ID, Channel: ch.ID, ChannelName: ch.Name, ChannelType: ch.Type, Rule: m.Rule, RuleName: m.RuleName, Kind: m.Kind, Title: clip(m.Title, 200), Body: clip(m.Body, 600), Result: result, Attempt: attempt}
	if err != nil {
		var ce *channel.Error
		if errors.As(err, &ce) {
			r.Step, r.Error = ce.Step, ce.Msg
		} else {
			r.Error = err.Error()
		}
	}
	d.mu.Lock()
	d.history = append(d.history, r)
	if n := len(d.history) - d.o.HistorySize; n > 0 {
		d.history = append([]Record(nil), d.history[n:]...)
	}
	d.dirty = true
	d.mu.Unlock()
}

// History answers the rows, newest first.
func (d *Dispatcher) History() []Record {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Record, 0, len(d.history))
	for i := len(d.history) - 1; i >= 0; i-- {
		out = append(out, d.history[i])
	}
	return out
}

// Clear empties the history.
func (d *Dispatcher) Clear() {
	d.mu.Lock()
	d.history = nil
	d.dirty = true
	d.mu.Unlock()
}

// Status answers every channel's delivery state by id.
func (d *Dispatcher) Status() map[string]Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]Status{}
	for id, w := range d.workers {
		s := w.status
		s.Queued = len(w.jobs)
		out[id] = s
	}
	return out
}

// Enqueue hands a message to the channels' queues.
func (d *Dispatcher) Enqueue(channels []string, m message.Message) {
	for _, id := range channels {
		ch, ok := d.o.Channel(id)
		if !ok {
			continue
		}
		d.mu.Lock()
		w := d.workers[id]
		if w == nil {
			w = &worker{jobs: make(chan job, QueueSize)}
			d.workers[id] = w
			d.wg.Add(1)
			go d.work(d.ctx, id, w)
		}
		d.mu.Unlock()
		select {
		case w.jobs <- job{m: m}:
		default:
			d.record(ch, m, Dropped, 0, errors.New("the channel's queue is full"))
		}
	}
}

func (d *Dispatcher) work(ctx context.Context, id string, w *worker) {
	defer d.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case j, ok := <-w.jobs:
			if !ok {
				return
			}
			d.deliver(ctx, id, w, j.m)
		}
	}
}

// allowed applies the rate limit: at most n sends in a sliding hour.
func (d *Dispatcher) allowed(w *worker, now time.Time) bool {
	limit := d.o.Settings().RatePerHour
	d.mu.Lock()
	defer d.mu.Unlock()
	keep := w.sends[:0]
	for _, t := range w.sends {
		if now.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	w.sends = keep
	if limit > 0 && len(w.sends) >= limit {
		w.status.Held++
		return false
	}
	w.sends = append(w.sends, now)
	return true
}

func (d *Dispatcher) deliver(ctx context.Context, id string, w *worker, m message.Message) {
	ch, ok := d.o.Channel(id)
	if !ok || !ch.Enabled {
		if ok {
			d.record(ch, m, Dropped, 0, errors.New("the channel is switched off"))
		}
		return
	}
	if !d.allowed(w, d.o.Now()) {
		d.record(ch, m, Limited, 0, nil)
		return
	}
	d.mu.Lock()
	held := w.status.Held
	w.status.Held = 0
	failing := w.status.Failing
	d.mu.Unlock()
	if held > 0 {
		if m.Body != "" {
			m.Body += "\n\n"
		}
		m.Body += texts.T(d.o.Settings().Language, "held-back", held)
	}
	for attempt := 1; ; attempt++ {
		// the channel as it is configured now: a corrected password takes effect on the retry
		if cur, ok := d.o.Channel(id); !ok || !cur.Enabled {
			d.record(ch, m, Dropped, attempt, errors.New("the channel was removed or switched off"))
			return
		} else {
			ch = cur
		}
		err := d.o.Send(ctx, ch, m)
		if err == nil {
			d.record(ch, m, Sent, attempt, nil)
			d.mu.Lock()
			w.status.Sent++
			w.status.LastOK = d.o.Now()
			was := w.status.Failing
			w.status.Failing = false
			d.mu.Unlock()
			if was && d.o.Failing != nil {
				d.o.Failing(ch, false, "")
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		var ce *channel.Error
		perm := errors.As(err, &ce) && ce.Permanent
		// a channel that is failing gets one attempt per message until one goes through: a
		// queue behind a dead target must not take twenty minutes per message
		last := perm || failing || attempt > len(d.o.Backoff)
		d.mu.Lock()
		w.status.LastError, w.status.LastErrorAt = err.Error(), d.o.Now()
		if ce != nil {
			w.status.LastError, w.status.LastStep = ce.Msg, ce.Step
		} else {
			w.status.LastStep = ""
		}
		if last {
			w.status.Failed++
			w.status.Failing = true
		}
		d.mu.Unlock()
		if last {
			d.record(ch, m, GaveUp, attempt, err)
			d.o.Log("channel %s: %q given up after %d attempt(s): %v", ch.ID, m.Title, attempt, err)
			if !failing && d.o.Failing != nil {
				d.o.Failing(ch, true, err.Error())
			}
			return
		}
		d.record(ch, m, Failed, attempt, err)
		select {
		case <-ctx.Done():
			return
		case <-d.o.After(d.o.Backoff[attempt-1]):
		}
	}
}

// Test sends a message to a channel at once, outside the queue and the rate limit, and
// records the result.
func (d *Dispatcher) Test(ctx context.Context, ch config.Channel, m message.Message) error {
	err := d.o.Send(ctx, ch, m)
	if ch.ID != "" {
		if err != nil {
			d.record(ch, m, GaveUp, 1, err)
		} else {
			d.record(ch, m, Sent, 1, nil)
		}
	}
	return err
}
