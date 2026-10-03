package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ziaulkamal/mailcore/internal/api"
	"github.com/ziaulkamal/mailcore/internal/config"
	"github.com/ziaulkamal/mailcore/internal/job"
	"github.com/ziaulkamal/mailcore/internal/mailer"
	"github.com/ziaulkamal/mailcore/internal/smtptest"
)

const apiKey = "test-key"

type env struct {
	t    *testing.T
	http *httptest.Server
	smtp *smtptest.Server
}

func setup(t *testing.T) *env {
	t.Helper()
	smtpSrv := smtptest.New(t, smtptest.Config{Username: "user", Password: "secret"})
	cfg := config.Config{
		APIKeys: []string{apiKey}, MaxBodyBytes: 1 << 20, MaxRecipientsPerEmail: 10,
		MaxBulkRecipients: 100, MaxBulkConcurrency: 5, MaxRetries: 3,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dialer := mailer.SMTPDialer{Timeout: 5 * time.Second}
	jobs := job.NewManager(dialer, job.Options{Logger: log, RetryBaseDelay: time.Millisecond})
	jobs.Start()
	httpSrv := httptest.NewServer(api.New(cfg, dialer, jobs, log).Handler())
	t.Cleanup(func() {
		httpSrv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = jobs.Shutdown(ctx)
	})
	return &env{t: t, http: httpSrv, smtp: smtpSrv}
}

func (e *env) smtpJSON() string {
	b, _ := json.Marshal(map[string]any{
		"host": e.smtp.Host, "port": e.smtp.Port, "username": "user", "password": "secret", "encryption": "none",
	})
	return string(b)
}

func (e *env) do(method, path, body string, out any) int {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.http.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("decode %s: %v", raw, err)
		}
	}
	return resp.StatusCode
}

func TestAuthRequired(t *testing.T) {
	e := setup(t)
	resp, err := http.Post(e.http.URL+"/v1/send", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	resp, _ = http.Get(e.http.URL + "/healthz")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", resp.StatusCode)
	}
}

func TestSendSingle(t *testing.T) {
	e := setup(t)
	body := `{
		"smtp": ` + e.smtpJSON() + `,
		"from": {"email": "panitia@example.com", "name": "Panitia"},
		"to": ["peserta@example.com"],
		"bcc": [{"email": "arsip@example.com"}],
		"subject": "Konfirmasi pendaftaran",
		"html": "<p>Terima kasih</p>",
		"attachments": [{"filename": "info.txt", "content": "aGFsbw=="}]
	}`
	var res api.SendResponse
	if code := e.do("POST", "/v1/send", body, &res); code != http.StatusOK {
		t.Fatalf("status = %d, body %+v", code, res)
	}
	if res.Status != "sent" || len(res.Accepted) != 2 || res.MessageID == "" {
		t.Fatalf("unexpected response %+v", res)
	}
	msgs := e.smtp.Messages()
	if len(msgs) != 1 || len(msgs[0].To) != 2 || bytes.Contains(msgs[0].Data, []byte("arsip@example.com")) {
		t.Fatalf("unexpected delivery %+v", msgs)
	}
}

func TestSendValidationAndSMTPErrors(t *testing.T) {
	e := setup(t)
	var errRes api.ErrorResponse
	code := e.do("POST", "/v1/send", `{"smtp": `+e.smtpJSON()+`, "from": "a@example.com", "to": ["b@example.com"], "subject": "x\nBcc: c@example.com", "text": "hi"}`, &errRes)
	if code != http.StatusUnprocessableEntity || errRes.Error.Code != "validation_error" {
		t.Fatalf("header injection: status %d, %+v", code, errRes)
	}
	code = e.do("POST", "/v1/send", `{"smtp": {"host": "x"}, "form": "typo"}`, &errRes)
	if code != http.StatusBadRequest || errRes.Error.Code != "invalid_json" {
		t.Fatalf("unknown field: status %d, %+v", code, errRes)
	}
	badSMTP := strings.Replace(e.smtpJSON(), `"secret"`, `"wrong"`, 1)
	code = e.do("POST", "/v1/send", `{"smtp": `+badSMTP+`, "from": "a@example.com", "to": ["b@example.com"], "subject": "x", "text": "hi"}`, &errRes)
	if code != http.StatusBadGateway || errRes.Error.Code != "smtp_auth_failed" || errRes.Error.Stage != "auth" {
		t.Fatalf("bad password: status %d, %+v", code, errRes)
	}
}

func TestVerify(t *testing.T) {
	e := setup(t)
	var res api.VerifyResponse
	if code := e.do("POST", "/v1/smtp/verify", `{"smtp": `+e.smtpJSON()+`}`, &res); code != http.StatusOK || !res.OK {
		t.Fatalf("status %d, %+v", code, res)
	}
}

func TestBulkFlow(t *testing.T) {
	e := setup(t)
	body := `{
		"smtp": ` + e.smtpJSON() + `,
		"from": "Panitia <panitia@example.com>",
		"subject": "Jadwal {{.name}}",
		"text": "Halo {{.name}}, kode kamu {{.kode}}",
		"recipients": [
			{"email": "a@example.com", "name": "Andi", "data": {"kode": "A1"}},
			{"email": "b@example.com", "name": "Budi", "data": {"kode": "B2"}},
			{"email": "A@example.com", "name": "Andi lagi", "data": {"kode": "A1"}}
		],
		"options": {"concurrency": 2, "max_retries": 1},
		"metadata": {"event": "simpora-2026"}
	}`
	var queued api.BulkResponse
	if code := e.do("POST", "/v1/send/bulk", body, &queued); code != http.StatusAccepted {
		t.Fatalf("status %d, %+v", code, queued)
	}
	if queued.Total != 2 || queued.DuplicatesRemoved != 1 || queued.ID == "" {
		t.Fatalf("unexpected queued response %+v", queued)
	}

	var snap job.Snapshot
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		e.do("GET", "/v1/jobs/"+queued.ID, "", &snap)
		if snap.Status == job.StatusCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snap.Status != job.StatusCompleted || snap.Sent != 2 || snap.Metadata["event"] != "simpora-2026" {
		t.Fatalf("unexpected final snapshot %+v", snap)
	}
	var found bool
	for _, m := range e.smtp.Messages() {
		if m.To[0] == "b@example.com" && bytes.Contains(m.Data, []byte("Halo Budi, kode kamu B2")) {
			found = true
		}
	}
	if !found {
		t.Fatal("personalised message for Budi not delivered")
	}
}

func TestBulkRejectsMissingTemplateData(t *testing.T) {
	e := setup(t)
	body := `{
		"smtp": ` + e.smtpJSON() + `,
		"from": "panitia@example.com",
		"subject": "Halo {{.name}}",
		"text": "Kode {{.kode}}",
		"recipients": [{"email": "a@example.com", "data": {"kode": "A1"}}, "b@example.com"]
	}`
	var errRes api.ErrorResponse
	code := e.do("POST", "/v1/send/bulk", body, &errRes)
	if code != http.StatusUnprocessableEntity || len(errRes.Error.Details) != 1 || !strings.Contains(errRes.Error.Details[0], "recipients[1]") {
		t.Fatalf("status %d, %+v", code, errRes)
	}
	if n := len(e.smtp.Messages()); n != 0 {
		t.Fatalf("nothing should be sent, got %d", n)
	}
}
