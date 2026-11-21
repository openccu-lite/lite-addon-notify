package channel

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openccu-lite/lite-addon-notify/internal/config"
	"github.com/openccu-lite/lite-addon-notify/internal/message"
)

// sendEmail delivers the message over SMTP: with STARTTLS (required when chosen), implicit TLS
// or plain; AUTH PLAIN or LOGIN when a user is set. Each step that can fail is named, with the
// server's own reply: the CCU's Email addon left its users guessing between a wrong port, a
// refused login and a rejected sender.
func sendEmail(ctx context.Context, ch config.Channel, m message.Message) error {
	addr := net.JoinHostPort(ch.Host, strconv.Itoa(ch.Port))
	from, err := mail.ParseAddress(ch.Sender)
	if err != nil {
		return permanent(StepConfig, "the sender %q is not an email address", ch.Sender)
	}
	var rcpts []*mail.Address
	for _, r := range config.SplitList(ch.Target) {
		a, err := mail.ParseAddress(r)
		if err != nil {
			return permanent(StepConfig, "the recipient %q is not an email address", r)
		}
		rcpts = append(rcpts, a)
	}
	if len(rcpts) == 0 {
		return permanent(StepConfig, "no recipient")
	}

	d := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return netError(err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsConf := &tls.Config{ServerName: ch.Host, InsecureSkipVerify: ch.Insecure} //nolint:gosec // the user's choice
	if ch.Security == "tls" {
		tc := tls.Client(conn, tlsConf)
		if err := tc.HandshakeContext(ctx); err != nil {
			return tlsError(err, "on this port; with STARTTLS the usual port is 587")
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, ch.Host)
	if err != nil {
		if ch.Security != "tls" && isTimeout(err) {
			return fail(StepConnect, "%s sent no greeting: a port that expects TLS from the first byte (465)? Then choose TLS", addr)
		}
		return smtpError(StepConnect, err)
	}
	defer c.Close()
	if err := c.Hello(localName()); err != nil {
		return smtpError(StepConnect, err)
	}
	if ch.Security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return permanent(StepTLS, "the server offers no STARTTLS on port %d: use port 587, or TLS on port 465", ch.Port)
		}
		if err := c.StartTLS(tlsConf); err != nil {
			return tlsError(err, "")
		}
	}
	if ch.User != "" {
		ok, mechs := c.Extension("AUTH")
		if !ok {
			hint := ""
			if ch.Security == "none" {
				hint = ": most servers offer it only on an encrypted connection"
			}
			return permanent(StepLogin, "the server offers no login here%s", hint)
		}
		var auth smtp.Auth
		switch {
		case strings.Contains(" "+mechs+" ", " PLAIN "):
			auth = &plainAuth{user: ch.User, password: ch.Password}
		case strings.Contains(" "+mechs+" ", " LOGIN "):
			auth = &loginAuth{user: ch.User, password: ch.Password}
		default:
			return permanent(StepLogin, "the server offers only %s; this addon speaks PLAIN and LOGIN", mechs)
		}
		if err := c.Auth(auth); err != nil {
			e := smtpError(StepLogin, err)
			e.Msg = "the server refused the login (user name or password; some providers need an app password): " + e.Msg
			return e
		}
	}
	if err := c.Mail(from.Address); err != nil {
		e := smtpError(StepSender, err)
		e.Msg = "the server refused the sender " + from.Address + ": " + e.Msg
		return e
	}
	for _, r := range rcpts {
		if err := c.Rcpt(r.Address); err != nil {
			e := smtpError(StepRecipient, err)
			e.Msg = "the server refused the recipient " + r.Address + ": " + e.Msg
			return e
		}
	}
	w, err := c.Data()
	if err != nil {
		return smtpError(StepSend, err)
	}
	if _, err := w.Write(buildMail(from, rcpts, m, ch.Host)); err != nil {
		return smtpError(StepSend, err)
	}
	if err := w.Close(); err != nil {
		return smtpError(StepSend, err)
	}
	_ = c.Quit()
	return nil
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func localName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "localhost"
}

func tlsError(err error, hint string) *Error {
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return permanent(StepTLS, "the server's certificate is not trusted: %v", certErr.Err)
	}
	var rec tls.RecordHeaderError
	if errors.As(err, &rec) {
		msg := "the server does not speak TLS"
		if hint != "" {
			msg += " " + hint
		}
		return permanent(StepTLS, "%s", msg)
	}
	if isTimeout(err) {
		return fail(StepTLS, "no answer to the TLS handshake in time")
	}
	return fail(StepTLS, "%v", err)
}

// smtpError keeps the server's reply (code and text) and marks a 5xx as permanent.
func smtpError(step string, err error) *Error {
	var te *textproto.Error
	if errors.As(err, &te) {
		e := fail(step, "%d %s", te.Code, te.Msg)
		e.Permanent = te.Code >= 500
		return e
	}
	if isTimeout(err) {
		return fail(step, "no answer in time")
	}
	return fail(step, "%v", err)
}

// plainAuth is AUTH PLAIN. net/smtp's own refuses a connection without TLS; here the user
// chose the encryption, and "none" is the choice for a relay in the local network.
type plainAuth struct{ user, password string }

func (a *plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.password), nil
}

func (a *plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge")
	}
	return nil, nil
}

// loginAuth is AUTH LOGIN, which some servers offer instead of PLAIN.
type loginAuth struct{ user, password string }

func (a *loginAuth) Start(*smtp.ServerInfo) (string, []byte, error) { return "LOGIN", nil, nil }

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSuffix(strings.TrimSpace(string(fromServer)), ":")) {
	case "username":
		return []byte(a.user), nil
	case "password":
		return []byte(a.password), nil
	}
	return nil, fmt.Errorf("unexpected server challenge %q", fromServer)
}

func encodeHeader(s string) string { return mime.QEncoding.Encode("utf-8", s) }

func formatAddr(a *mail.Address) string {
	if a.Name == "" {
		return "<" + a.Address + ">"
	}
	return (&mail.Address{Name: a.Name, Address: a.Address}).String()
}

// buildMail is the message as RFC 5322 text: UTF-8, base64, with the headers a mail filter
// expects of an automatic message.
func buildMail(from *mail.Address, rcpts []*mail.Address, m message.Message, host string) []byte {
	var b strings.Builder
	to := make([]string, 0, len(rcpts))
	for _, r := range rcpts {
		to = append(to, formatAddr(r))
	}
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := from.Address[strings.LastIndex(from.Address, "@")+1:]
	if domain == "" {
		domain = host
	}
	hdr := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	hdr("From", formatAddr(from))
	hdr("To", strings.Join(to, ", "))
	hdr("Subject", encodeHeader(clip(strings.ReplaceAll(m.Title, "\n", " "), 200)))
	hdr("Date", m.Time.Format(time.RFC1123Z))
	hdr("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	hdr("MIME-Version", "1.0")
	hdr("Content-Type", "text/plain; charset=utf-8")
	hdr("Content-Transfer-Encoding", "base64")
	hdr("Auto-Submitted", "auto-generated")
	hdr("X-Mailer", UserAgent)
	switch m.Priority {
	case message.High:
		hdr("X-Priority", "1")
		hdr("Importance", "high")
	case message.Low:
		hdr("X-Priority", "5")
		hdr("Importance", "low")
	}
	b.WriteString("\r\n")
	text := bodyWithLink(m)
	if strings.TrimSpace(text) == "" {
		text = m.Title
	}
	if m.System != "" {
		text += "\n\n-- \n" + m.System
	}
	enc := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(text, "\n", "\r\n") + "\r\n"))
	w := bufio.NewWriter(&b)
	for len(enc) > 76 {
		_, _ = w.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	_, _ = w.WriteString(enc + "\r\n")
	_ = w.Flush()
	return []byte(b.String())
}
