package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ziaulkamal/SMTP-With-Go/internal/job"
	"github.com/ziaulkamal/SMTP-With-Go/internal/mailer"
)

const maxDetails = 20

// send delivers one message synchronously and reports the server's answer.
func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var req SendRequest
	if !s.decode(w, r, &req) {
		return
	}
	if err := req.SMTP.Validate(); err != nil {
		validationError(w, err.Error())
		return
	}
	msg := mailer.Message{
		From: req.From, To: req.To, Cc: req.Cc, Bcc: req.Bcc, ReplyTo: req.ReplyTo,
		Subject: req.Subject, Text: req.Text, HTML: req.HTML,
		Headers: req.Headers, Attachments: req.Attachments,
	}
	if err := msg.Validate(); err != nil {
		validationError(w, err.Error())
		return
	}
	rcpts := msg.Recipients()
	if len(rcpts) > s.cfg.MaxRecipientsPerEmail {
		validationError(w, fmt.Sprintf("at most %d recipients (to+cc+bcc) per message; use /v1/send/bulk for more", s.cfg.MaxRecipientsPerEmail))
		return
	}
	messageID := mailer.NewMessageID(req.From.Domain())
	raw, err := msg.Build(s.now(), messageID)
	if err != nil {
		validationError(w, err.Error())
		return
	}

	sess, err := s.dialer.Dial(r.Context(), req.SMTP)
	if err != nil {
		smtpError(w, err)
		return
	}
	defer sess.Close()
	res, err := sess.Send(req.From.Email, rcpts, raw)
	if err != nil {
		smtpError(w, err)
		return
	}
	status := "sent"
	if len(res.Rejected) > 0 {
		status = "partial"
	}
	writeJSON(w, http.StatusOK, SendResponse{Status: status, MessageID: messageID, Accepted: res.Accepted, Rejected: res.Rejected})
}

// sendBulk validates the whole batch up front, then queues it as a job.
func (s *Server) sendBulk(w http.ResponseWriter, r *http.Request) {
	var req BulkRequest
	if !s.decode(w, r, &req) {
		return
	}
	if err := req.SMTP.Validate(); err != nil {
		validationError(w, err.Error())
		return
	}
	if len(req.Recipients) == 0 {
		validationError(w, "recipients must not be empty")
		return
	}
	if len(req.Recipients) > s.cfg.MaxBulkRecipients {
		validationError(w, fmt.Sprintf("at most %d recipients per bulk request", s.cfg.MaxBulkRecipients))
		return
	}
	if req.CallbackURL != "" && !validCallbackURL(req.CallbackURL) {
		validationError(w, "callback_url must be an absolute http(s) URL")
		return
	}

	opts := req.Options
	if opts.Concurrency == 0 {
		opts.Concurrency = 2
	}
	if opts.Concurrency < 1 || opts.Concurrency > s.cfg.MaxBulkConcurrency {
		validationError(w, fmt.Sprintf("options.concurrency must be between 1 and %d", s.cfg.MaxBulkConcurrency))
		return
	}
	if opts.RatePerSecond < 0 {
		validationError(w, "options.rate_per_second must not be negative")
		return
	}
	retries := 2
	if opts.MaxRetries != nil {
		retries = *opts.MaxRetries
	}
	if retries < 0 || retries > s.cfg.MaxRetries {
		validationError(w, fmt.Sprintf("options.max_retries must be between 0 and %d", s.cfg.MaxRetries))
		return
	}

	tpl, err := mailer.ParseTemplate(req.Subject, req.Text, req.HTML)
	if err != nil {
		validationError(w, err.Error())
		return
	}

	// Validate every recipient now so the client learns about bad input
	// immediately instead of from failed results later.
	seen := make(map[string]bool, len(req.Recipients))
	recipients := make([]job.Recipient, 0, len(req.Recipients))
	var details []string
	duplicates := 0
	for i, br := range req.Recipients {
		addr := mailer.Address{Email: strings.TrimSpace(br.Email), Name: br.Name}
		key := strings.ToLower(addr.Email)
		if seen[key] {
			duplicates++
			continue
		}
		seen[key] = true
		out, err := tpl.Render(mailer.TemplateData(addr, br.Data))
		if err == nil {
			msg := mailer.Message{
				From: req.From, To: []mailer.Address{addr}, ReplyTo: req.ReplyTo,
				Subject: out.Subject, Text: out.Text, HTML: out.HTML,
				Headers: req.Headers, Attachments: req.Attachments,
			}
			err = msg.Validate()
		}
		if err != nil {
			if len(details) < maxDetails {
				details = append(details, fmt.Sprintf("recipients[%d] (%s): %v", i, br.Email, err))
			}
			continue
		}
		recipients = append(recipients, job.Recipient{Address: addr, Data: br.Data})
	}
	if len(details) > 0 {
		validationError(w, "one or more recipients are invalid; nothing was queued", details...)
		return
	}

	snap, err := s.jobs.Submit(&job.Spec{
		SMTP: req.SMTP, From: req.From, ReplyTo: req.ReplyTo, Headers: req.Headers,
		Attachments: req.Attachments, Template: tpl, Recipients: recipients,
		Concurrency: opts.Concurrency, RatePerSecond: opts.RatePerSecond, MaxRetries: retries,
		CallbackURL: req.CallbackURL, Metadata: req.Metadata,
	})
	if err != nil {
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusServiceUnavailable, ErrorBody{Code: "queue_unavailable", Message: err.Error()})
		return
	}
	w.Header().Set("Location", "/v1/jobs/"+snap.ID)
	writeJSON(w, http.StatusAccepted, BulkResponse{Snapshot: snap, DuplicatesRemoved: duplicates})
}

func (s *Server) listJobs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"jobs": s.jobs.List()})
}

// getJob returns progress; ?results=false omits per-recipient results.
func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	withResults := r.URL.Query().Get("results") != "false"
	snap, err := s.jobs.Get(r.PathValue("id"), withResults)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrorBody{Code: "not_found", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	snap, err := s.jobs.Cancel(r.PathValue("id"))
	switch {
	case errors.Is(err, job.ErrNotFound):
		writeError(w, http.StatusNotFound, ErrorBody{Code: "not_found", Message: err.Error()})
	case errors.Is(err, job.ErrFinished):
		writeError(w, http.StatusConflict, ErrorBody{Code: "job_finished", Message: err.Error()})
	default:
		writeJSON(w, http.StatusAccepted, snap)
	}
}

// verify connects and authenticates without sending anything.
func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	var req VerifyRequest
	if !s.decode(w, r, &req) {
		return
	}
	if err := req.SMTP.Validate(); err != nil {
		validationError(w, err.Error())
		return
	}
	start := time.Now()
	sess, err := s.dialer.Dial(r.Context(), req.SMTP)
	if err != nil {
		smtpError(w, err)
		return
	}
	sess.Close()
	writeJSON(w, http.StatusOK, VerifyResponse{OK: true, LatencyMS: time.Since(start).Milliseconds()})
}
