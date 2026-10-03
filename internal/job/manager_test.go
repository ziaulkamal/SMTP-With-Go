package job_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziaulkamal/mailcore/internal/job"
	"github.com/ziaulkamal/mailcore/internal/mailer"
	"github.com/ziaulkamal/mailcore/internal/smtptest"
)

func newManager(t *testing.T, opts job.Options) *job.Manager {
	t.Helper()
	opts.RetryBaseDelay = 5 * time.Millisecond
	m := job.NewManager(mailer.SMTPDialer{Timeout: 5 * time.Second}, opts)
	m.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	return m
}

func spec(t *testing.T, srv *smtptest.Server, n int) *job.Spec {
	t.Helper()
	tpl, err := mailer.ParseTemplate("Halo {{.name}}", "Nomor peserta {{.no}}", "")
	if err != nil {
		t.Fatal(err)
	}
	s := &job.Spec{
		SMTP:        mailer.SMTPConfig{Host: srv.Host, Port: srv.Port, Username: "user", Password: "secret", Encryption: mailer.EncryptionNone},
		From:        mailer.Address{Email: "panitia@example.com", Name: "Panitia"},
		Template:    tpl,
		Concurrency: 3,
		MaxRetries:  2,
	}
	for i := 0; i < n; i++ {
		s.Recipients = append(s.Recipients, job.Recipient{
			Address: mailer.Address{Email: fmt.Sprintf("p%d@example.com", i), Name: fmt.Sprintf("Peserta %d", i)},
			Data:    map[string]any{"no": i},
		})
	}
	return s
}

func wait(t *testing.T, m *job.Manager, id string) job.Snapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s, err := m.Get(id, true)
		if err != nil {
			t.Fatal(err)
		}
		switch s.Status {
		case job.StatusCompleted, job.StatusFailed, job.StatusCancelled:
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish in time")
	return job.Snapshot{}
}

func authServer(t *testing.T, cfg smtptest.Config) *smtptest.Server {
	cfg.Username, cfg.Password = "user", "secret"
	return smtptest.New(t, cfg)
}

func TestBulkPersonalisedAndConnectionReuse(t *testing.T) {
	srv := authServer(t, smtptest.Config{})
	m := newManager(t, job.Options{})
	snap, err := m.Submit(spec(t, srv, 12))
	if err != nil {
		t.Fatal(err)
	}
	got := wait(t, m, snap.ID)
	if got.Status != job.StatusCompleted || got.Sent != 12 || got.Failed != 0 {
		t.Fatalf("unexpected snapshot %+v", got)
	}
	msgs := srv.Messages()
	if len(msgs) != 12 {
		t.Fatalf("server received %d messages, want 12", len(msgs))
	}
	for _, msg := range msgs {
		var n int
		fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(msg.To[0], "p"), "@example.com"), "%d", &n)
		if !strings.Contains(string(msg.Data), fmt.Sprintf("Subject: Halo Peserta %d", n)) ||
			!strings.Contains(string(msg.Data), fmt.Sprintf("Nomor peserta %d", n)) {
			t.Errorf("message for %s not personalised:\n%s", msg.To[0], msg.Data)
		}
	}
	if c := srv.Connections(); c > 3 {
		t.Errorf("expected at most 3 connections (one per worker), got %d", c)
	}
}

func TestBulkRetriesTransientAndFailsPermanent(t *testing.T) {
	var tempFails atomic.Int32
	srv := authServer(t, smtptest.Config{
		OnData: func(msg smtptest.Message) string {
			if msg.To[0] == "p0@example.com" && tempFails.Add(1) <= 2 {
				return "451 4.3.0 try again later"
			}
			return ""
		},
		RejectRcpt: func(r string) string {
			if r == "p1@example.com" {
				return "550 5.1.1 mailbox unavailable"
			}
			return ""
		},
	})
	m := newManager(t, job.Options{})
	snap, _ := m.Submit(spec(t, srv, 3))
	got := wait(t, m, snap.ID)
	if got.Status != job.StatusCompleted || got.Sent != 2 || got.Failed != 1 {
		t.Fatalf("unexpected snapshot %+v", got)
	}
	byEmail := map[string]job.Result{}
	for _, r := range got.Results {
		byEmail[r.Email] = r
	}
	if r := byEmail["p0@example.com"]; r.Status != job.RecipientSent || r.Attempts != 3 {
		t.Errorf("p0 = %+v, want sent after 3 attempts", r)
	}
	if r := byEmail["p1@example.com"]; r.Status != job.RecipientFailed || r.Attempts != 1 || !strings.Contains(r.Error, "550") {
		t.Errorf("p1 = %+v, want permanent failure without retry", r)
	}
}

func TestBulkAbortsOnAuthFailure(t *testing.T) {
	srv := authServer(t, smtptest.Config{})
	s := spec(t, srv, 5)
	s.SMTP.Password = "wrong"
	m := newManager(t, job.Options{})
	snap, _ := m.Submit(s)
	got := wait(t, m, snap.ID)
	if got.Status != job.StatusFailed || got.Failed != 5 || !strings.Contains(got.Error, "auth") {
		t.Fatalf("unexpected snapshot %+v", got)
	}
	if len(srv.Messages()) != 0 {
		t.Fatal("no message should be sent")
	}
}

func TestBulkCancel(t *testing.T) {
	srv := authServer(t, smtptest.Config{})
	s := spec(t, srv, 50)
	s.Concurrency = 1
	s.RatePerSecond = 20
	m := newManager(t, job.Options{})
	snap, _ := m.Submit(s)
	time.Sleep(150 * time.Millisecond)
	if _, err := m.Cancel(snap.ID); err != nil {
		t.Fatal(err)
	}
	got := wait(t, m, snap.ID)
	if got.Status != job.StatusCancelled || got.Cancelled == 0 || got.Sent == 0 || got.Sent+got.Cancelled != 50 {
		t.Fatalf("unexpected snapshot %+v", got)
	}
}

func TestBulkCallbackIsSigned(t *testing.T) {
	srv := authServer(t, smtptest.Config{})
	var mu sync.Mutex
	var gotBody []byte
	var gotSig string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotBody, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get("X-Mailcore-Signature")
	}))
	defer hook.Close()

	s := spec(t, srv, 2)
	s.CallbackURL = hook.URL
	m := newManager(t, job.Options{CallbackSecret: "s3cret"})
	snap, _ := m.Submit(s)
	wait(t, m, snap.ID)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		body, sig := gotBody, gotSig
		mu.Unlock()
		if body != nil {
			mac := hmac.New(sha256.New, []byte("s3cret"))
			mac.Write(body)
			if sig != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
				t.Fatalf("bad signature %q", sig)
			}
			var payload struct {
				Event string       `json:"event"`
				Job   job.Snapshot `json:"job"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Event != "job.finished" || payload.Job.Sent != 2 || len(payload.Job.Results) != 2 {
				t.Fatalf("unexpected payload %s", body)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("callback not received")
}
