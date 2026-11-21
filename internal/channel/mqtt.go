package channel

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/openccu-lite/lite-addon-notify/internal/config"
	"github.com/openccu-lite/lite-addon-notify/internal/message"
	"github.com/openccu-lite/lite-addon-notify/internal/tmpl"
)

// sendMQTT publishes the message: one connection per message (CONNECT, PUBLISH, DISCONNECT),
// MQTT 3.1.1, QoS 0 or 1. A publisher this small needs no client library: the addon sends a
// few messages a day.
func sendMQTT(ctx context.Context, ch config.Channel, m message.Message) error {
	topic, err := tmpl.Render(ch.Target, m.Context())
	if err != nil {
		return permanent(StepTemplate, "topic: %v", err)
	}
	if topic == "" || strings.ContainsAny(topic, "#+\x00") {
		return permanent(StepTemplate, "the topic %q is empty or has a wildcard", topic)
	}
	var payload []byte
	if ch.Body == "" {
		payload, _ = json.Marshal(m)
	} else {
		s, err := tmpl.Render(ch.Body, m.Context())
		if err != nil {
			return permanent(StepTemplate, "payload: %v", err)
		}
		payload = []byte(s)
	}

	addr := net.JoinHostPort(ch.Host, strconv.Itoa(ch.Port))
	d := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return netError(err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if ch.Security == "tls" {
		tc := tls.Client(conn, &tls.Config{ServerName: ch.Host, InsecureSkipVerify: ch.Insecure}) //nolint:gosec // the user's choice
		if err := tc.HandshakeContext(ctx); err != nil {
			return tlsError(err, "on this port; the plain port is usually 1883")
		}
		conn = tc
	}

	id := make([]byte, 6)
	_, _ = rand.Read(id)
	if _, err := conn.Write(mqttConnect("lite-notify-"+hex.EncodeToString(id), ch.User, ch.Password)); err != nil {
		return netError(err)
	}
	typ, body, err := mqttRead(conn)
	if err != nil {
		if isTimeout(err) {
			return fail(StepConnect, "%s does not answer as an MQTT broker (a TLS port? Then choose TLS)", addr)
		}
		return fail(StepConnect, "%s closed the connection before answering: not an MQTT broker, or a TLS port", addr)
	}
	if typ != 2 || len(body) < 2 {
		return permanent(StepConnect, "%s does not answer as an MQTT broker", addr)
	}
	if code := body[1]; code != 0 {
		reason := map[byte]string{1: "the broker does not speak MQTT 3.1.1", 2: "the broker refused the client id", 3: "the broker is not available", 4: "the broker refused the user name or password", 5: "the broker refused the login: not authorized"}[code]
		if reason == "" {
			reason = "the broker refused the connection (code " + strconv.Itoa(int(code)) + ")"
		}
		e := fail(StepLogin, "%s", reason)
		e.Permanent = code != 3
		return e
	}
	if _, err := conn.Write(mqttPublish(topic, payload, ch.QoS, ch.Retain, 1)); err != nil {
		return netError(err)
	}
	if ch.QoS == 1 {
		typ, _, err := mqttRead(conn)
		if err != nil || typ != 4 {
			return fail(StepSend, "the broker did not acknowledge the message (not allowed to publish to %s?)", topic)
		}
	}
	_, _ = conn.Write([]byte{0xE0, 0})
	return nil
}

func mqttString(s string) []byte {
	b := make([]byte, 2, 2+len(s))
	binary.BigEndian.PutUint16(b, uint16(len(s)))
	return append(b, s...)
}

func mqttPacket(header byte, body []byte) []byte {
	out := []byte{header}
	n := len(body)
	for {
		d := byte(n % 128)
		n /= 128
		if n > 0 {
			d |= 0x80
		}
		out = append(out, d)
		if n == 0 {
			break
		}
	}
	return append(out, body...)
}

func mqttConnect(clientID, user, password string) []byte {
	var flags byte = 0x02 // clean session
	body := append(mqttString("MQTT"), 4, 0, 0, 30)
	body = append(body, mqttString(clientID)...)
	if user != "" {
		flags |= 0x80
		body = append(body, mqttString(user)...)
		if password != "" {
			flags |= 0x40
			body = append(body, mqttString(password)...)
		}
	}
	body[7] = flags
	return mqttPacket(0x10, body)
}

func mqttPublish(topic string, payload []byte, qos int, retain bool, id uint16) []byte {
	header := byte(0x30) | byte(qos<<1)
	if retain {
		header |= 1
	}
	body := mqttString(topic)
	if qos > 0 {
		body = binary.BigEndian.AppendUint16(body, id)
	}
	return mqttPacket(header, append(body, payload...))
}

// mqttRead reads one packet: its type (the header's high nibble) and its body.
func mqttRead(r io.Reader) (byte, []byte, error) {
	var h [1]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n, shift := 0, 0
	for {
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n |= int(b[0]&0x7f) << shift
		if b[0]&0x80 == 0 {
			break
		}
		shift += 7
		if shift > 21 {
			return 0, nil, io.ErrUnexpectedEOF
		}
	}
	if n > 1<<20 {
		return 0, nil, io.ErrUnexpectedEOF
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return h[0] >> 4, body, nil
}
