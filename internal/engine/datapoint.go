package engine

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/openccu-lite/lite-addon-notify/internal/config"
	"github.com/openccu-lite/lite-addon-notify/internal/message"
	"github.com/openccu-lite/lite-addon-notify/internal/texts"
)

// sig identifies a condition: a rule whose condition changed starts with a clean memory.
func sig(d *config.Datapoint) string {
	return strings.Join([]string{d.Interface, d.Address, d.Key, d.Op, d.Value, strconv.Itoa(d.ForS)}, "|")
}

// Eval says whether a value meets a condition; ok is false for a value the operator cannot
// judge (a text against "greater than").
func Eval(d *config.Datapoint, raw json.RawMessage) (truth, ok bool) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false, false
	}
	num, isNum := 0.0, false
	switch x := v.(type) {
	case float64:
		num, isNum = x, true
	case bool:
		if x {
			num = 1
		}
		isNum = true
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			num, isNum = f, true
		}
	}
	switch d.Op {
	case "true":
		if b, isBool := v.(bool); isBool {
			return b, true
		}
		return isNum && num != 0, isNum
	case "false":
		if b, isBool := v.(bool); isBool {
			return !b, true
		}
		return isNum && num == 0, isNum
	case "eq", "ne":
		var eq bool
		want, wantNum := config.Number(d.Value)
		switch {
		case isNum && wantNum:
			eq = num == want
		case isNum && (strings.EqualFold(d.Value, "true") || strings.EqualFold(d.Value, "false")):
			eq = (num != 0) == strings.EqualFold(d.Value, "true")
		default:
			s, isStr := v.(string)
			if !isStr {
				return false, false
			}
			eq = s == d.Value
		}
		return eq == (d.Op == "eq"), true
	}
	want, wantNum := config.Number(d.Value)
	if !isNum || !wantNum {
		return false, false
	}
	switch d.Op {
	case "gt":
		return num > want, true
	case "ge":
		return num >= want, true
	case "lt":
		return num < want, true
	case "le":
		return num <= want, true
	}
	return false, false
}

// Wanted answers the datapoints the enabled datapoint rules watch, as "interface|address|key".
func Wanted(c config.Config) map[string]bool {
	out := map[string]bool{}
	for _, r := range c.Rules {
		if r.Enabled && r.Source == config.SrcDatapoint && r.Datapoint != nil {
			out[r.Datapoint.Interface+"|"+r.Datapoint.Address+"|"+r.Datapoint.Key] = true
		}
	}
	return out
}

// Value takes a datapoint's value: from the state store at start (at is since when it stands)
// or from the event stream. A rule whose condition becomes true starts its time; one that has
// held long enough fires at the next Tick; one that turns false after it fired is resolved.
func (e *Engine) Value(iface, address, key string, raw json.RawMessage, at, now time.Time) {
	c := e.o.Config()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range c.Rules {
		d := r.Datapoint
		if !r.Enabled || r.Source != config.SrcDatapoint || d == nil || d.Interface != iface || d.Address != address || d.Key != key {
			continue
		}
		st := e.st.Datapoints[r.ID]
		if st == nil || st.Sig != sig(d) {
			st = &dpState{Sig: sig(d)}
			e.st.Datapoints[r.ID] = st
		}
		truth, ok := Eval(d, raw)
		if !ok {
			continue
		}
		st.Value = append(json.RawMessage(nil), raw...)
		e.dirty = true
		if truth {
			if st.Since.IsZero() {
				st.Since = at
				if at.IsZero() || at.After(now) {
					st.Since = now
				}
			}
			continue
		}
		if st.Fired {
			e.handle(e.dpEvent(r, st, true, now, c), now, c)
		}
		st.Since, st.Fired = time.Time{}, false
	}
}

func valueText(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return string(raw)
}

// dpEvent is a datapoint rule's event: named by the channel's name, saying the rule's label
// (or the value), with the room and since when below.
func (e *Engine) dpEvent(r config.Rule, st *dpState, resolved bool, now time.Time, c config.Config) Event {
	d := r.Datapoint
	lang := c.Settings.Language
	name, rooms := d.Address, []string(nil)
	if e.o.Describe != nil {
		if n, rr := e.o.Describe(d.Interface, d.Address, lang); n != "" {
			name, rooms = n, rr
		} else {
			rooms = rr
		}
	}
	val := d.Key + " = " + valueText(st.Value)
	text := d.Label
	if text == "" {
		text = val
	}
	detail := []string{}
	if d.Label != "" {
		detail = append(detail, val)
	}
	if len(rooms) > 0 {
		detail = append(detail, strings.Join(rooms, ", "))
	}
	if !resolved && !st.Since.IsZero() {
		detail = append(detail, texts.Since(lang, st.Since))
	}
	var v any
	_ = json.Unmarshal(st.Value, &v)
	return Event{Source: config.SrcDatapoint, RuleID: r.ID, Key: "dp", Resolved: resolved, At: now,
		Item: message.Item{Title: name, Text: text, Detail: strings.Join(detail, " · "), Device: name, Interface: d.Interface, Address: d.Address, Rooms: rooms, Datapoint: d.Key, Value: v, Since: st.Since}}
}

// openDatapoints lists the datapoint rules that hold now.
func (e *Engine) openDatapoints(c config.Config, lang string) []message.Item {
	var out []message.Item
	for _, r := range c.Rules {
		st := e.st.Datapoints[r.ID]
		if st == nil || !st.Fired || !r.Enabled || r.Source != config.SrcDatapoint || r.Datapoint == nil {
			continue
		}
		it := e.dpEvent(r, st, false, time.Time{}, c).Item
		it.Key = r.ID
		it.Line = it.Title + ": " + it.Text
		out = append(out, it)
	}
	return out
}

// OpenDatapoints lists the datapoint rules that hold now (the page's Status tab).
func (e *Engine) OpenDatapoints() []message.Item {
	c := e.o.Config()
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.openDatapoints(c, c.Settings.Language)
}
