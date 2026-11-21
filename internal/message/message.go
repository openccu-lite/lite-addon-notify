// Package message is what a channel sends: one message, made of the events a rule batched.
package message

import (
	"strings"
	"time"
)

// The priorities a rule gives its messages; every channel maps them to its own scale.
const (
	Low    = "low"
	Normal = "normal"
	High   = "high"
)

// The kinds of message: the rule's source, or test for a Test button's.
const (
	KindWarning   = "warning"
	KindService   = "service"
	KindDatapoint = "datapoint"
	KindSystem    = "system"
	KindInbound   = "inbound"
	KindDigest    = "digest"
	KindTest      = "test"
)

// Item is one event of a message: a warning, a device's service message, a datapoint rule
// that fired, a system event - new or resolved.
type Item struct {
	// Key tells the same subject from another (a warning's id and variant, a device's message).
	Key string `json:"key"`
	// Title names the subject (the device, the warning), Text says what is the matter; both in
	// the configured language. Line is the two as one line, with "resolved" when it is over.
	Title string `json:"title"`
	Text  string `json:"text,omitempty"`
	Line  string `json:"line"`
	// Detail is what a single message says below its title: the room, since when, the value.
	Detail   string `json:"detail,omitempty"`
	Resolved bool   `json:"resolved"`
	// Severity is error, warning or info where the source has one.
	Severity  string    `json:"severity,omitempty"`
	Device    string    `json:"device,omitempty"`
	Interface string    `json:"interface,omitempty"`
	Address   string    `json:"address,omitempty"`
	Rooms     []string  `json:"rooms,omitempty"`
	Datapoint string    `json:"datapoint,omitempty"`
	Value     any       `json:"value,omitempty"`
	Since     time.Time `json:"since,omitzero"`
	// Link is a path of the system's page for this item (a warning's own link).
	Link string `json:"link,omitempty"`
}

// Message is one notification.
type Message struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Rule is the id of the rule that made it, RuleName its name; empty for a test.
	Rule     string `json:"rule,omitempty"`
	RuleName string `json:"rule_name,omitempty"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Priority string `json:"priority"`
	// Resolved says that every item is one that is over.
	Resolved bool `json:"resolved"`
	// Link is an absolute URL into the system's page, when the settings name the system's address.
	Link   string    `json:"link,omitempty"`
	System string    `json:"system"`
	Time   time.Time `json:"time"`
	Count  int       `json:"count"`
	Items  []Item    `json:"items"`
}

// Context is the message as a template sees it.
func (m Message) Context() map[string]any {
	items := make([]map[string]any, 0, len(m.Items))
	lines := make([]string, 0, len(m.Items))
	for _, it := range m.Items {
		rooms := it.Rooms
		if rooms == nil {
			rooms = []string{}
		}
		items = append(items, map[string]any{
			"key": it.Key, "title": it.Title, "text": it.Text, "line": it.Line, "detail": it.Detail, "resolved": it.Resolved,
			"severity": it.Severity, "device": it.Device, "interface": it.Interface, "address": it.Address, "rooms": rooms,
			"datapoint": it.Datapoint, "value": it.Value, "since": it.Since, "link": it.Link,
		})
		lines = append(lines, it.Line)
	}
	return map[string]any{
		"id": m.ID, "system": m.System, "kind": m.Kind, "rule": m.RuleName, "resolved": m.Resolved, "count": len(m.Items),
		"headline": m.Title, "title": m.Title, "body": m.Body, "lines": lines, "items": items, "priority": m.Priority,
		"link": m.Link, "time": m.Time,
	}
}

// Text is the message as one plain text: the title, an empty line, the body - for a channel
// that has no title of its own. A body that only repeats the title is left out.
func (m Message) Text() string {
	if strings.TrimSpace(m.Body) == "" || m.Body == m.Title {
		return m.Title
	}
	return m.Title + "\n\n" + m.Body
}
