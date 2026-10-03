package api

import (
	"bytes"
	"encoding/json"

	"github.com/ziaulkamal/SMTP-With-Go/internal/job"
	"github.com/ziaulkamal/SMTP-With-Go/internal/mailer"
)

// SendRequest is the body of POST /v1/send.
type SendRequest struct {
	SMTP        mailer.SMTPConfig   `json:"smtp"`
	From        mailer.Address      `json:"from"`
	To          []mailer.Address    `json:"to"`
	Cc          []mailer.Address    `json:"cc,omitempty"`
	Bcc         []mailer.Address    `json:"bcc,omitempty"`
	ReplyTo     *mailer.Address     `json:"reply_to,omitempty"`
	Subject     string              `json:"subject"`
	Text        string              `json:"text,omitempty"`
	HTML        string              `json:"html,omitempty"`
	Headers     map[string]string   `json:"headers,omitempty"`
	Attachments []mailer.Attachment `json:"attachments,omitempty"`
}

// SendResponse is returned by POST /v1/send.
type SendResponse struct {
	Status    string            `json:"status"` // "sent" or "partial"
	MessageID string            `json:"message_id"`
	Accepted  []string          `json:"accepted"`
	Rejected  map[string]string `json:"rejected,omitempty"`
}

// BulkRecipient is one entry of BulkRequest.Recipients. It also accepts a
// plain string ("a@b.com" or "Name <a@b.com>").
type BulkRecipient struct {
	Email string         `json:"email"`
	Name  string         `json:"name,omitempty"`
	Data  map[string]any `json:"data,omitempty"`
}

// UnmarshalJSON accepts the object and the string form.
func (r *BulkRecipient) UnmarshalJSON(b []byte) error {
	if b = bytes.TrimSpace(b); len(b) > 0 && b[0] == '"' {
		var a mailer.Address
		if err := json.Unmarshal(b, &a); err != nil {
			return err
		}
		*r = BulkRecipient{Email: a.Email, Name: a.Name}
		return nil
	}
	type plain BulkRecipient
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*r = BulkRecipient(p)
	return nil
}

// BulkOptions tunes delivery of a bulk job.
type BulkOptions struct {
	// Concurrency is the number of parallel SMTP connections (default 2).
	Concurrency int `json:"concurrency,omitempty"`
	// RatePerSecond caps messages per second across the job (0 = unlimited).
	RatePerSecond float64 `json:"rate_per_second,omitempty"`
	// MaxRetries is the number of retries after a temporary failure (default 2).
	MaxRetries *int `json:"max_retries,omitempty"`
}

// BulkRequest is the body of POST /v1/send/bulk. Subject, text and html
// are Go templates rendered with each recipient's data plus "email" and
// "name".
type BulkRequest struct {
	SMTP        mailer.SMTPConfig   `json:"smtp"`
	From        mailer.Address      `json:"from"`
	ReplyTo     *mailer.Address     `json:"reply_to,omitempty"`
	Subject     string              `json:"subject"`
	Text        string              `json:"text,omitempty"`
	HTML        string              `json:"html,omitempty"`
	Headers     map[string]string   `json:"headers,omitempty"`
	Attachments []mailer.Attachment `json:"attachments,omitempty"`
	Recipients  []BulkRecipient     `json:"recipients"`
	Options     BulkOptions         `json:"options,omitempty"`
	CallbackURL string              `json:"callback_url,omitempty"`
	Metadata    map[string]string   `json:"metadata,omitempty"`
}

// BulkResponse is returned by POST /v1/send/bulk.
type BulkResponse struct {
	job.Snapshot
	DuplicatesRemoved int `json:"duplicates_removed,omitempty"`
}

// VerifyRequest is the body of POST /v1/smtp/verify.
type VerifyRequest struct {
	SMTP mailer.SMTPConfig `json:"smtp"`
}

// VerifyResponse is returned by POST /v1/smtp/verify.
type VerifyResponse struct {
	OK        bool  `json:"ok"`
	LatencyMS int64 `json:"latency_ms"`
}

// ErrorResponse is the body of every non-2xx response.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody describes an error. Code is stable and machine-readable.
type ErrorBody struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Stage   string   `json:"stage,omitempty"`   // SMTP stage for smtp_error
	Details []string `json:"details,omitempty"` // per-field validation errors
}
