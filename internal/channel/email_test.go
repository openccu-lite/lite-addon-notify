package channel

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"mime"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/openccu-lite/lite-addon-notify/internal/config"
	"github.com/openccu-lite/lite-addon-notify/internal/fakesys"
)

// smtpMock is a small SMTP server: EHLO, STARTTLS, AUTH PLAIN and LOGIN, MAIL, RCPT, DATA.
type smtpMock struct {
	l    net.Listener
	cert tls.Certificate
	// implicit: TLS from the first byte; starttls: offered after EHLO; authPlain/authLogin:
	// the mechanisms offered (authTLSOnly: only on an encrypted connection).
	implicit, starttls       bool
	authPlain, authLogin     bool
	authTLSOnly              bool
	user, password           string
	rejectSender, rejectRcpt string
	mu                       sync.Mutex
	mails                    []string
	from                     string
	rcpts                    []string
	authed                   bool
	sawTLS                   bool
}

func newSMTP(t *testing.T, configure func(*smtpMock)) (*smtpMock, config.Channel) {
	t.Helper()
	ts := httptest.NewTLSServer(nil)
	cert := ts.TLS.Certificates[0]
	ts.Close()
	m := &smtpMock{cert: cert, user: "user", password: "pass word", authPlain: true}
	configure(m)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if m.implicit {
		l = tls.NewListener(l, &tls.Config{Certificates: []tls.Certificate{cert}})
	}
	m.l = l
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go m.serve(c)
		}
	}()
	port, _ := strconv.Atoi(l.Addr().String()[strings.LastIndex(l.Addr().String(), ":")+1:])
	sec := "none"
	switch {
	case m.implicit:
		sec = "tls"
	case m.starttls:
		sec = "starttls"
	}
	return m, config.Channel{Type: config.Email, Host: "127.0.0.1", Port: port, Security: sec, Insecure: true, User: "user", Password: "pass word", Sender: "CCU <ccu@example.org>", Target: "a@example.org, b@example.org"}
}

func (m *smtpMock) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
	encrypted := m.implicit
	say("220 mock ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			ext := []string{"mock"}
			if m.starttls && !encrypted {
				ext = append(ext, "STARTTLS")
			}
			var mechs []string
			if m.authPlain {
				mechs = append(mechs, "PLAIN")
			}
			if m.authLogin {
				mechs = append(mechs, "LOGIN")
			}
			if len(mechs) > 0 && (encrypted || !m.authTLSOnly) {
				ext = append(ext, "AUTH "+strings.Join(mechs, " "))
			}
			for i, e := range ext {
				sep := "-"
				if i == len(ext)-1 {
					sep = " "
				}
				say("250" + sep + e)
			}
		case cmd == "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{m.cert}})
			if tc.Handshake() != nil {
				return
			}
			c, r, encrypted = tc, bufio.NewReader(tc), true
			m.mu.Lock()
			m.sawTLS = true
			m.mu.Unlock()
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN"):]))
			if string(raw) == "\x00"+m.user+"\x00"+m.password {
				m.mu.Lock()
				m.authed = true
				m.mu.Unlock()
				say("235 2.7.0 accepted")
			} else {
				say("535 5.7.8 Username and Password not accepted")
			}
		case cmd == "AUTH LOGIN":
			say("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
			u, _ := r.ReadString('\n')
			say("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
			p, _ := r.ReadString('\n')
			ub, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(u))
			pb, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(p))
			if string(ub) == m.user && string(pb) == m.password {
				m.mu.Lock()
				m.authed = true
				m.mu.Unlock()
				say("235 2.7.0 accepted")
			} else {
				say("535 5.7.8 Username and Password not accepted")
			}
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			if m.rejectSender != "" && strings.Contains(line, m.rejectSender) {
				say("550 5.7.1 Sender address rejected: not owned by user")
				continue
			}
			m.mu.Lock()
			m.from = line[len("MAIL FROM:"):]
			m.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			if m.rejectRcpt != "" && strings.Contains(line, m.rejectRcpt) {
				say("550 5.1.1 User unknown")
				continue
			}
			m.mu.Lock()
			m.rcpts = append(m.rcpts, line[len("RCPT TO:"):])
			m.mu.Unlock()
			say("250 ok")
		case cmd == "DATA":
			say("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			m.mu.Lock()
			m.mails = append(m.mails, b.String())
			m.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("502 not implemented")
		}
	}
}

func TestEmailSTARTTLS(t *testing.T) {
	m, ch := newSMTP(t, func(m *smtpMock) { m.starttls, m.authTLSOnly = true, true })
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.sawTLS || !m.authed || len(m.mails) != 1 || m.from != "<ccu@example.org>" || len(m.rcpts) != 2 {
		t.Fatalf("tls %v authed %v mails %d from %q rcpts %v", m.sawTLS, m.authed, len(m.mails), m.from, m.rcpts)
	}
	hdr, body := decodeMail(t, m.mails[0])
	subject, _ := new(mime.WordDecoder).DecodeHeader(hdr["Subject"])
	if subject != msg().Title || hdr["To"] != "<a@example.org>, <b@example.org>" || !strings.Contains(hdr["From"], "<ccu@example.org>") || hdr["X-Priority"] != "1" || hdr["Auto-Submitted"] != "auto-generated" ||
		!strings.HasPrefix(hdr["Message-ID"], "<") || !strings.HasSuffix(hdr["Message-ID"], "@example.org>") {
		t.Errorf("headers: %v (subject %q)", hdr, subject)
	}
	if body != "Bad · HmIP-RF 000A1BE9A1B2C3\r\n\r\nhttps://ccu.example/\r\n\r\n-- \r\nccu\r\n" {
		t.Errorf("body: %q", body)
	}
}

func TestEmailImplicitTLSAndLogin(t *testing.T) {
	m, ch := newSMTP(t, func(m *smtpMock) { m.implicit, m.authPlain, m.authLogin = true, false, true })
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	if !m.authed || len(m.mails) != 1 {
		t.Fatalf("authed %v mails %d", m.authed, len(m.mails))
	}
	// the certificate is checked unless the channel says otherwise
	ch.Insecure = false
	wantErr(t, send(t, ch), StepTLS, true, "certificate")
}

func TestEmailPlainNoAuth(t *testing.T) {
	m, ch := newSMTP(t, func(m *smtpMock) {})
	ch.User, ch.Password = "", ""
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	if m.authed || len(m.mails) != 1 {
		t.Fatalf("authed %v mails %d", m.authed, len(m.mails))
	}
	// with a user on a plain connection the login is sent: "none" is the user's choice
	ch.User, ch.Password = "user", "pass word"
	if err := send(t, ch); err != nil || !m.authed {
		t.Fatalf("plain auth: %v", err)
	}
}

func TestEmailErrors(t *testing.T) {
	// a wrong password: the server's own words
	_, ch := newSMTP(t, func(m *smtpMock) { m.starttls = true })
	ch.Password = "wrong"
	wantErr(t, send(t, ch), StepLogin, true, "535 5.7.8 Username and Password not accepted")
	// STARTTLS chosen, not offered
	_, ch = newSMTP(t, func(m *smtpMock) {})
	ch.Security = "starttls"
	wantErr(t, send(t, ch), StepTLS, true, "no STARTTLS")
	// a login, and the server offers none on a plain connection
	_, ch = newSMTP(t, func(m *smtpMock) { m.authTLSOnly = true })
	wantErr(t, send(t, ch), StepLogin, true, "only on an encrypted connection")
	// the server offers a mechanism this addon does not speak
	_, ch = newSMTP(t, func(m *smtpMock) { m.authPlain = false })
	wantErr(t, send(t, ch), StepLogin, true, "no login")
	// a rejected sender and an unknown recipient
	_, ch = newSMTP(t, func(m *smtpMock) { m.rejectSender = "ccu@example.org" })
	wantErr(t, send(t, ch), StepSender, true, "Sender address rejected")
	_, ch = newSMTP(t, func(m *smtpMock) { m.rejectRcpt = "b@example.org" })
	wantErr(t, send(t, ch), StepRecipient, true, "b@example.org: 550 5.1.1 User unknown")
	// TLS chosen on a port that speaks plain SMTP
	_, ch = newSMTP(t, func(m *smtpMock) {})
	ch.Security = "tls"
	wantErr(t, send(t, ch), StepTLS, true, "does not speak TLS")
	// nothing listens
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	ch.Port, ch.Security = port, "none"
	wantErr(t, send(t, ch), StepConnect, false, "refused")
	ch.Host = "no-such-host.invalid"
	wantErr(t, send(t, ch), StepResolve, false, "no-such-host.invalid")
	// addresses
	ch.Host, ch.Sender = "127.0.0.1", "nobody"
	wantErr(t, send(t, ch), StepConfig, true, "sender")
}

// The fake system's SMTP server (the page's development, a lab check) takes the channel's mail.
func TestEmailToTheFakeSystem(t *testing.T) {
	fs := fakesys.New()
	defer fs.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() { _ = fs.ServeSMTP(l) }()
	ch := config.Channel{Type: config.Email, Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, Security: "none", User: "user", Password: "pass", Sender: "ccu@example.org", Target: "a@example.org"}
	if err := send(t, ch); err != nil {
		t.Fatal(err)
	}
	if m := fs.Mails(); len(m) != 1 || !strings.Contains(m[0], "Subject: ") {
		t.Fatalf("mails: %v", m)
	}
	ch.Password = "wrong"
	wantErr(t, send(t, ch), StepLogin, true, "535")
	ch.Password, ch.Target = "pass", "x@nobody.invalid"
	wantErr(t, send(t, ch), StepRecipient, true, "550 5.1.1 User unknown")
}
