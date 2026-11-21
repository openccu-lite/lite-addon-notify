package channel

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openccu-lite/lite-addon-notify/internal/config"
)

// broker is a fake MQTT broker: it takes CONNECT, answers CONNACK with the code the test set,
// records what is published and acknowledges QoS 1.
type broker struct {
	l          net.Listener
	code       byte
	noAck      bool
	mu         sync.Mutex
	user, pass string
	topic      string
	payload    []byte
	flags      byte
	clientID   string
}

func newBroker(t *testing.T) (*broker, config.Channel) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &broker{l: l}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go b.serve(c)
		}
	}()
	return b, config.Channel{Type: config.MQTT, Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, Security: "none", Target: "notify/{{ kind }}/{{ items[0].device | slug }}"}
}

func readStr(b []byte) (string, []byte) {
	n := int(binary.BigEndian.Uint16(b))
	return string(b[2 : 2+n]), b[2+n:]
}

func (b *broker) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	for {
		typ, body, err := mqttReadRaw(r)
		if err != nil {
			return
		}
		switch typ >> 4 {
		case 1: // CONNECT
			_, rest := readStr(body)
			flags := rest[1]
			rest = rest[4:]
			b.mu.Lock()
			b.clientID, rest = readStr(rest)
			b.user, b.pass = "", ""
			if flags&0x80 != 0 {
				b.user, rest = readStr(rest)
			}
			if flags&0x40 != 0 {
				b.pass, _ = readStr(rest)
			}
			code := b.code
			b.mu.Unlock()
			_, _ = c.Write([]byte{0x20, 2, 0, code})
			if code != 0 {
				return
			}
		case 3: // PUBLISH
			topic, rest := readStr(body)
			qos := (typ >> 1) & 3
			var id []byte
			if qos > 0 {
				id, rest = rest[:2], rest[2:]
			}
			b.mu.Lock()
			b.topic, b.payload, b.flags = topic, append([]byte(nil), rest...), typ&0x0f
			noAck := b.noAck
			b.mu.Unlock()
			if qos > 0 && !noAck {
				_, _ = c.Write([]byte{0x40, 2, id[0], id[1]})
			}
		case 14: // DISCONNECT
			return
		}
	}
}

// mqttReadRaw reads one packet with its full first byte.
func mqttReadRaw(r *bufio.Reader) (byte, []byte, error) {
	h, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	if err := r.UnreadByte(); err != nil {
		return 0, nil, err
	}
	_, body, err := mqttRead(r)
	return h, body, err
}

func TestMQTT(t *testing.T) {
	b, ch := newBroker(t)
	ch.User, ch.Password, ch.QoS, ch.Retain = "notify", "secret", 1, true
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	var m map[string]any
	if b.topic != "notify/service/heizung_bad" || b.user != "notify" || b.pass != "secret" || b.flags != 0x03 || json.Unmarshal(b.payload, &m) != nil || m["title"] != msg().Title || !strings.HasPrefix(b.clientID, "lite-notify-") {
		t.Errorf("topic %q user %q flags %x payload %s", b.topic, b.user, b.flags, b.payload)
	}
	b.mu.Unlock()
	// QoS 0, a payload template, no login
	ch.User, ch.Password, ch.QoS, ch.Retain, ch.Body = "", "", 0, false, "{{ headline }}"
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	b.mu.Lock()
	if string(b.payload) != msg().Title || b.flags != 0 || b.user != "" {
		t.Errorf("payload %q flags %x user %q", b.payload, b.flags, b.user)
	}
	b.code = 4
	b.mu.Unlock()
	wantErr(t, send(t, ch), StepLogin, true, "user name or password")
	b.mu.Lock()
	b.code = 5
	b.mu.Unlock()
	wantErr(t, send(t, ch), StepLogin, true, "not authorized")
	// QoS 1 without an acknowledgement
	b.mu.Lock()
	b.code, b.noAck = 0, true
	b.mu.Unlock()
	ch.QoS = 1
	old := Timeout
	Timeout = 500 * time.Millisecond
	wantErr(t, send(t, ch), StepSend, false, "did not acknowledge")
	Timeout = old
	// a topic with a wildcard, a broken template
	ch.Target = "notify/#"
	wantErr(t, send(t, ch), StepTemplate, true, "wildcard")
	ch.Target = "{{ x | nosuch }}"
	wantErr(t, send(t, ch), StepTemplate, true, "nosuch")
	// nothing listens
	b.l.Close()
	ch.Target = "notify"
	wantErr(t, send(t, ch), StepConnect, false, "refused")
}

func TestMQTTPacket(t *testing.T) {
	// the remaining length takes two bytes from 128 on
	p := mqttPublish("t", make([]byte, 200), 0, false, 0)
	if p[0] != 0x30 || p[1] != 0xCB || p[2] != 0x01 || len(p) != 3+203 {
		t.Errorf("packet: % x…, %d bytes", p[:3], len(p))
	}
}

// TestMQTTMosquitto publishes to a real Mosquitto in docker and reads the message back with
// mosquitto_sub. Run with NOTIFY_DOCKER=1 (CI does).
func TestMQTTMosquitto(t *testing.T) {
	if os.Getenv("NOTIFY_DOCKER") == "" {
		t.Skip("NOTIFY_DOCKER is not set")
	}
	const name = "notify-test-mosquitto"
	_ = exec.Command("docker", "rm", "-f", name).Run()
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "-p", "127.0.0.1:18833:1883", "eclipse-mosquitto:2", "mosquitto", "-c", "/mosquitto-no-auth.conf").CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	ch := config.Channel{Type: config.MQTT, Host: "127.0.0.1", Port: 18833, Security: "none", Target: "notify/{{ kind }}", QoS: 1}
	// the broker needs a moment; the subscriber must be there before the publish
	sub := exec.Command("docker", "exec", name, "mosquitto_sub", "-t", "notify/#", "-C", "1", "-W", "20", "-v")
	var got strings.Builder
	sub.Stdout = &got
	for i := 0; ; i++ {
		if c, err := net.DialTimeout("tcp", "127.0.0.1:18833", time.Second); err == nil {
			c.Close()
			break
		}
		if i > 40 {
			t.Fatal("the broker did not come up")
		}
		time.Sleep(250 * time.Millisecond)
	}
	time.Sleep(time.Second)
	if err := sub.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	if err := sub.Wait(); err != nil {
		t.Fatalf("mosquitto_sub: %v (%s)", err, got.String())
	}
	topic, payload, _ := strings.Cut(strings.TrimSpace(got.String()), " ")
	var m map[string]any
	if topic != "notify/service" || json.Unmarshal([]byte(payload), &m) != nil || m["title"] != msg().Title {
		t.Errorf("read back: %q", got.String())
	}
}
