package fakesys

import (
	"bufio"
	"encoding/base64"
	"net"
	"strings"
)

// ServeSMTP is a small SMTP server for the page's development and a lab check: no TLS, AUTH
// PLAIN with the user "user" and the password "pass" (anything else is refused with 535), a
// recipient at "nobody.invalid" is refused with 550. What it accepts shows under GET /dev/mails.
func (s *Server) ServeSMTP(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go s.smtp(c)
	}
}

// Mails answers the mails the SMTP server accepted, oldest first.
func (s *Server) Mails() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.mails...)
}

func (s *Server) smtp(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(line string) { _, _ = c.Write([]byte(line + "\r\n")) }
	say("220 fakesystem ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			say("250-fakesystem")
			say("250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN"):]))
			if string(raw) == "\x00user\x00pass" {
				say("235 2.7.0 accepted")
			} else {
				say("535 5.7.8 Username and Password not accepted")
			}
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			if strings.Contains(cmd, "NOBODY.INVALID") {
				say("550 5.1.1 User unknown")
			} else {
				say("250 ok")
			}
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
			s.mu.Lock()
			s.mails = append(s.mails, b.String())
			s.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("502 not implemented")
		}
	}
}
