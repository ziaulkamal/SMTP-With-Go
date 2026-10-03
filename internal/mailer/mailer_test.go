package mailer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/ziaulkamal/SMTP-With-Go/internal/mailer"
	"github.com/ziaulkamal/SMTP-With-Go/internal/smtptest"
)

func TestAddressUnmarshal(t *testing.T) {
	var got []mailer.Address
	in := `["a@example.com", "Budi Santoso <budi@example.com>", {"email": "c@example.com", "name": "C"}]`
	if err := json.Unmarshal([]byte(in), &got); err != nil {
		t.Fatal(err)
	}
	want := []mailer.Address{{Email: "a@example.com"}, {Email: "budi@example.com", Name: "Budi Santoso"}, {Email: "c@example.com", Name: "C"}}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("address %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if err := json.Unmarshal([]byte(`"not an email"`), new(mailer.Address)); err == nil {
		t.Error("expected error for invalid string address")
	}
}

func TestValidateRejectsHeaderInjection(t *testing.T) {
	base := func() mailer.Message {
		return mailer.Message{
			From: mailer.Address{Email: "from@example.com"}, To: []mailer.Address{{Email: "to@example.com"}},
			Subject: "Hi", Text: "body",
		}
	}
	cases := map[string]func(*mailer.Message){
		"subject newline":  func(m *mailer.Message) { m.Subject = "Hi\r\nBcc: evil@example.com" },
		"header newline":   func(m *mailer.Message) { m.Headers = map[string]string{"X-Tag": "a\nb"} },
		"reserved header":  func(m *mailer.Message) { m.Headers = map[string]string{"from": "x@example.com"} },
		"bad address":      func(m *mailer.Message) { m.To = []mailer.Address{{Email: "x@example.com>, <y@example.com"}} },
		"name newline":     func(m *mailer.Message) { m.From.Name = "A\r\nB" },
		"no body":          func(m *mailer.Message) { m.Text = "" },
		"no recipients":    func(m *mailer.Message) { m.To = nil },
		"empty attachment": func(m *mailer.Message) { m.Attachments = []mailer.Attachment{{Filename: "a.txt"}} },
	}
	for name, mutate := range cases {
		m := base()
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	m := base()
	if err := m.Validate(); err != nil {
		t.Fatalf("valid message rejected: %v", err)
	}
}

func TestBuildMultipart(t *testing.T) {
	m := mailer.Message{
		From:        mailer.Address{Email: "from@example.com", Name: "Panitia Simpora"},
		To:          []mailer.Address{{Email: "to@example.com", Name: "Peserta"}},
		Cc:          []mailer.Address{{Email: "cc@example.com"}},
		Bcc:         []mailer.Address{{Email: "secret@example.com"}},
		Subject:     "Pengumuman jadwal ✓",
		Text:        "Halo,\nIni jadwal.",
		HTML:        "<p>Halo, <b>ini jadwal</b>.</p>",
		Headers:     map[string]string{"X-Campaign": "jadwal-1"},
		Attachments: []mailer.Attachment{{Filename: "jadwal.txt", Content: []byte("08:00 pembukaan")}},
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := m.Build(time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC), "<id@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("secret@example.com")) {
		t.Fatal("Bcc address leaked into message headers")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if subject != m.Subject {
		t.Errorf("subject = %q, want %q", subject, m.Subject)
	}
	if got := msg.Header.Get("X-Campaign"); got != "jadwal-1" {
		t.Errorf("X-Campaign = %q", got)
	}
	if got := msg.Header.Get("Message-Id"); got != "<id@example.com>" {
		t.Errorf("Message-ID = %q", got)
	}

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" {
		t.Fatalf("content type = %q, %v", mediaType, err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	first, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	altType, altParams, _ := mime.ParseMediaType(first.Header.Get("Content-Type"))
	if altType != "multipart/alternative" {
		t.Fatalf("first part = %q, want multipart/alternative", altType)
	}
	ar := multipart.NewReader(first, altParams["boundary"])
	var bodies []string
	for {
		p, err := ar.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p) // multipart.Reader decodes quoted-printable
		bodies = append(bodies, string(b))
	}
	if len(bodies) != 2 || !strings.Contains(bodies[0], "Ini jadwal.") || !strings.Contains(bodies[1], "<b>ini jadwal</b>") {
		t.Fatalf("unexpected alternative bodies: %q", bodies)
	}
	att, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if att.FileName() != "jadwal.txt" {
		t.Errorf("attachment filename = %q", att.FileName())
	}
	if !strings.HasPrefix(att.Header.Get("Content-Type"), "text/plain") {
		t.Errorf("attachment content type = %q", att.Header.Get("Content-Type"))
	}
}

func TestTemplate(t *testing.T) {
	tpl, err := mailer.ParseTemplate("Halo {{.name}}", "Kode: {{.kode}}", "<p>{{.name}}</p>")
	if err != nil {
		t.Fatal(err)
	}
	data := mailer.TemplateData(mailer.Address{Email: "a@example.com", Name: "<Budi>"}, map[string]any{"kode": "X1"})
	r, err := tpl.Render(data)
	if err != nil {
		t.Fatal(err)
	}
	if r.Subject != "Halo <Budi>" || r.Text != "Kode: X1" || r.HTML != "<p>&lt;Budi&gt;</p>" {
		t.Fatalf("unexpected render: %+v", r)
	}
	if _, err := tpl.Render(mailer.TemplateData(mailer.Address{Email: "a@example.com"}, nil)); err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestSMTPSendWithAuth(t *testing.T) {
	for _, auth := range []mailer.AuthMethod{mailer.AuthPlain, mailer.AuthLogin, mailer.AuthAuto} {
		t.Run(string(auth), func(t *testing.T) {
			srv := smtptest.New(t, smtptest.Config{
				Username: "user", Password: "secret",
				RejectRcpt: func(r string) string {
					if r == "nobody@example.com" {
						return "550 5.1.1 no such user"
					}
					return ""
				},
			})
			cfg := mailer.SMTPConfig{Host: srv.Host, Port: srv.Port, Username: "user", Password: "secret", Encryption: mailer.EncryptionNone, Auth: auth}
			sess, err := mailer.SMTPDialer{Timeout: 5 * time.Second}.Dial(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer sess.Close()
			res, err := sess.Send("from@example.com", []string{"to@example.com", "nobody@example.com"}, []byte("Subject: x\r\n\r\nhello\r\n.dot line\r\n"))
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Accepted) != 1 || res.Rejected["nobody@example.com"] == "" {
				t.Fatalf("unexpected result %+v", res)
			}
			// The connection is reusable for a second message.
			if _, err := sess.Send("from@example.com", []string{"to2@example.com"}, []byte("Subject: y\r\n\r\nagain\r\n")); err != nil {
				t.Fatal(err)
			}
			msgs := srv.Messages()
			if len(msgs) != 2 || !bytes.Contains(msgs[0].Data, []byte("\n.dot line\n")) {
				t.Fatalf("server got %d messages: %q", len(msgs), msgs)
			}
		})
	}
}

func TestSMTPAuthFailureIsPermanent(t *testing.T) {
	srv := smtptest.New(t, smtptest.Config{Username: "user", Password: "secret"})
	cfg := mailer.SMTPConfig{Host: srv.Host, Port: srv.Port, Username: "user", Password: "wrong", Encryption: mailer.EncryptionNone}
	_, err := mailer.SMTPDialer{Timeout: 5 * time.Second}.Dial(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected auth error")
	}
	if !mailer.IsAuthError(err) || !mailer.IsPermanent(err) {
		t.Fatalf("expected permanent auth error, got %v", err)
	}
}

func TestSMTPStartTLSRequired(t *testing.T) {
	srv := smtptest.New(t, smtptest.Config{})
	cfg := mailer.SMTPConfig{Host: srv.Host, Port: srv.Port} // defaults to starttls on a non-465 port
	_, err := mailer.SMTPDialer{Timeout: 5 * time.Second}.Dial(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") || !mailer.IsPermanent(err) {
		t.Fatalf("expected permanent STARTTLS error, got %v", err)
	}
}
