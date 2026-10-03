package mailer

import (
	"net/smtp"
	"testing"
)

func TestPlaintextAuthPolicy(t *testing.T) {
	cases := []struct {
		server        smtp.ServerInfo
		allowInsecure bool
		wantErr       bool
	}{
		{smtp.ServerInfo{Name: "smtp.example.com", TLS: true}, false, false},
		{smtp.ServerInfo{Name: "smtp.example.com"}, false, true},
		{smtp.ServerInfo{Name: "smtp.example.com"}, true, false},
		{smtp.ServerInfo{Name: "127.0.0.1"}, false, false},
		{smtp.ServerInfo{Name: "localhost"}, false, false},
	}
	for _, c := range cases {
		for _, a := range []smtp.Auth{
			&plainAuth{username: "u", password: "p", allowInsecure: c.allowInsecure},
			&loginAuth{username: "u", password: "p", allowInsecure: c.allowInsecure},
		} {
			_, _, err := a.Start(&c.server)
			if (err != nil) != c.wantErr {
				t.Errorf("%T on %+v allowInsecure=%v: err=%v, wantErr=%v", a, c.server, c.allowInsecure, err, c.wantErr)
			}
		}
	}
}
