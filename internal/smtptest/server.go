// Package smtptest provides an in-process SMTP server for tests. It speaks
// enough ESMTP (EHLO, AUTH PLAIN/LOGIN, MAIL, RCPT, DATA, RSET, NOOP, QUIT)
// to exercise the real net/smtp client end to end.
package smtptest

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
)

// Message is a message received by the server.
type Message struct {
	From string
	To   []string
	Data []byte
}

// Config controls the behaviour of a Server. It is fixed at creation.
type Config struct {
	// Username and Password, when set, make AUTH mandatory.
	Username, Password string
	// RejectRcpt returns a non-empty reply (e.g. "550 no such user") to
	// reject a recipient.
	RejectRcpt func(rcpt string) string
	// OnData returns a non-empty reply (e.g. "451 try again") to reject a
	// message after DATA instead of accepting it.
	OnData func(msg Message) string
}

// Server is a fake SMTP server listening on 127.0.0.1.
type Server struct {
	Config
	Host string
	Port int

	ln       net.Listener
	mu       sync.Mutex
	messages []Message
	conns    int
	open     map[net.Conn]bool
	wg       sync.WaitGroup
}

// New starts a server and stops it when the test ends.
func New(t testing.TB, cfg Config) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	s := &Server{Config: cfg, Host: "127.0.0.1", Port: addr.Port, ln: ln, open: map[net.Conn]bool{}}
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(func() {
		ln.Close()
		s.mu.Lock()
		for c := range s.open {
			c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	return s
}

// Messages returns a copy of the messages received so far.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.messages...)
}

// Connections returns how many connections have been accepted.
func (s *Server) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns++
		s.open[conn] = true
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				conn.Close()
				s.mu.Lock()
				delete(s.open, conn)
				s.mu.Unlock()
			}()
			s.handle(conn)
		}()
	}
}

func (s *Server) handle(conn net.Conn) {
	tp := textproto.NewConn(conn)
	reply := func(format string, args ...any) { _ = tp.PrintfLine(format, args...) }
	reply("220 smtptest ESMTP ready")

	authed := s.Username == ""
	var cur *Message
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			reply("250-smtptest greets %s", arg)
			reply("250-8BITMIME")
			reply("250 AUTH PLAIN LOGIN")
		case "AUTH":
			authed = s.auth(tp, arg)
			if authed {
				reply("235 2.7.0 Authentication successful")
			} else {
				reply("535 5.7.8 Authentication credentials invalid")
			}
		case "MAIL":
			if !authed {
				reply("530 5.7.0 Authentication required")
				continue
			}
			cur = &Message{From: angle(arg)}
			reply("250 OK")
		case "RCPT":
			if cur == nil {
				reply("503 need MAIL first")
				continue
			}
			rcpt := angle(arg)
			if s.RejectRcpt != nil {
				if r := s.RejectRcpt(rcpt); r != "" {
					reply("%s", r)
					continue
				}
			}
			cur.To = append(cur.To, rcpt)
			reply("250 OK")
		case "DATA":
			if cur == nil || len(cur.To) == 0 {
				reply("503 need RCPT first")
				continue
			}
			reply("354 end with <CRLF>.<CRLF>")
			data, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			cur.Data = data
			if s.OnData != nil {
				if r := s.OnData(*cur); r != "" {
					cur = nil
					reply("%s", r)
					continue
				}
			}
			s.mu.Lock()
			s.messages = append(s.messages, *cur)
			s.mu.Unlock()
			cur = nil
			reply("250 OK queued")
		case "RSET":
			cur = nil
			reply("250 OK")
		case "NOOP":
			reply("250 OK")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 command not implemented")
		}
	}
}

func (s *Server) auth(tp *textproto.Conn, arg string) bool {
	mech, initial, _ := strings.Cut(arg, " ")
	readB64 := func(prompt string) string {
		_ = tp.PrintfLine("334 %s", prompt)
		line, err := tp.ReadLine()
		if err != nil {
			return ""
		}
		b, _ := base64.StdEncoding.DecodeString(line)
		return string(b)
	}
	switch strings.ToUpper(mech) {
	case "PLAIN":
		var raw string
		if initial != "" {
			b, _ := base64.StdEncoding.DecodeString(initial)
			raw = string(b)
		} else {
			raw = readB64("")
		}
		parts := strings.Split(raw, "\x00")
		return len(parts) == 3 && parts[1] == s.Username && parts[2] == s.Password
	case "LOGIN":
		user := readB64(base64.StdEncoding.EncodeToString([]byte("Username:")))
		pass := readB64(base64.StdEncoding.EncodeToString([]byte("Password:")))
		return user == s.Username && pass == s.Password
	}
	return false
}

// angle extracts the address from "FROM:<a@b> BODY=8BITMIME".
func angle(s string) string {
	start := strings.IndexByte(s, '<')
	end := strings.IndexByte(s, '>')
	if start < 0 || end < start {
		return ""
	}
	return s[start+1 : end]
}

// Addr returns "host:port".
func (s *Server) Addr() string { return fmt.Sprintf("%s:%d", s.Host, s.Port) }
