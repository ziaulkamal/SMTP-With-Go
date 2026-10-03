// Package job runs bulk sends asynchronously: one personalised message per
// recipient, sent by a pool of workers that reuse SMTP connections, with
// rate limiting and retries.
package job

import (
	"sync"
	"time"

	"github.com/ziaulkamal/mailcore/internal/mailer"
)

// Status is the lifecycle state of a job.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed" // every recipient was processed (see counters for failures)
	StatusFailed    Status = "failed"    // aborted, e.g. SMTP authentication failed
	StatusCancelled Status = "cancelled"
)

// RecipientStatus is the delivery state of one recipient.
type RecipientStatus string

const (
	RecipientPending   RecipientStatus = "pending"
	RecipientSent      RecipientStatus = "sent"
	RecipientFailed    RecipientStatus = "failed"
	RecipientCancelled RecipientStatus = "cancelled"
)

// Recipient is one bulk target with its template data.
type Recipient struct {
	Address mailer.Address
	Data    map[string]any
}

// Spec describes a bulk send. The SMTP credentials are kept only while
// the job runs and are wiped when it finishes.
type Spec struct {
	SMTP        mailer.SMTPConfig
	From        mailer.Address
	ReplyTo     *mailer.Address
	Headers     map[string]string
	Attachments []mailer.Attachment
	Template    *mailer.Template
	Recipients  []Recipient

	Concurrency   int
	RatePerSecond float64
	MaxRetries    int

	CallbackURL string
	Metadata    map[string]string
}

// Result is the outcome for one recipient.
type Result struct {
	Email     string          `json:"email"`
	Status    RecipientStatus `json:"status"`
	Attempts  int             `json:"attempts"`
	MessageID string          `json:"message_id,omitempty"`
	Error     string          `json:"error,omitempty"`
	SentAt    *time.Time      `json:"sent_at,omitempty"`
}

// Snapshot is a point-in-time, JSON-friendly view of a job.
type Snapshot struct {
	ID         string            `json:"job_id"`
	Status     Status            `json:"status"`
	Error      string            `json:"error,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	StartedAt  *time.Time        `json:"started_at,omitempty"`
	FinishedAt *time.Time        `json:"finished_at,omitempty"`
	Total      int               `json:"total"`
	Pending    int               `json:"pending"`
	Sent       int               `json:"sent"`
	Failed     int               `json:"failed"`
	Cancelled  int               `json:"cancelled"`
	Results    []Result          `json:"results,omitempty"`
}

// Job is a bulk send tracked by the Manager.
type Job struct {
	mu         sync.Mutex
	id         string
	status     Status
	err        string
	metadata   map[string]string
	createdAt  time.Time
	startedAt  time.Time
	finishedAt time.Time
	results    []Result
	spec       *Spec
	cancel     func()
	cancelReq  bool
	// callbackURL is immutable after creation.
	callbackURL string
}

func newJob(id string, spec *Spec, now time.Time) *Job {
	results := make([]Result, len(spec.Recipients))
	for i, r := range spec.Recipients {
		results[i] = Result{Email: r.Address.Email, Status: RecipientPending}
	}
	return &Job{id: id, status: StatusQueued, metadata: spec.Metadata, createdAt: now, results: results, spec: spec, callbackURL: spec.CallbackURL}
}

// Snapshot returns a copy of the job state. Results are included only when
// withResults is true.
func (j *Job) Snapshot(withResults bool) Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := Snapshot{
		ID: j.id, Status: j.status, Error: j.err, Metadata: j.metadata,
		CreatedAt: j.createdAt, Total: len(j.results),
	}
	if !j.startedAt.IsZero() {
		t := j.startedAt
		s.StartedAt = &t
	}
	if !j.finishedAt.IsZero() {
		t := j.finishedAt
		s.FinishedAt = &t
	}
	for _, r := range j.results {
		switch r.Status {
		case RecipientPending:
			s.Pending++
		case RecipientSent:
			s.Sent++
		case RecipientFailed:
			s.Failed++
		case RecipientCancelled:
			s.Cancelled++
		}
	}
	if withResults {
		s.Results = append([]Result(nil), j.results...)
	}
	return s
}

func (j *Job) finished() bool {
	return j.status == StatusCompleted || j.status == StatusFailed || j.status == StatusCancelled
}

func (j *Job) markSent(i, attempts int, messageID string, at time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.results[i] = Result{Email: j.results[i].Email, Status: RecipientSent, Attempts: attempts, MessageID: messageID, SentAt: &at}
}

func (j *Job) markFailed(i, attempts int, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.results[i].Status = RecipientFailed
	j.results[i].Attempts = attempts
	j.results[i].Error = err.Error()
}

// finish settles every still-pending recipient and records the final state.
func (j *Job) finish(status Status, reason string, pendingAs RecipientStatus, now time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for i := range j.results {
		if j.results[i].Status == RecipientPending {
			j.results[i].Status = pendingAs
			j.results[i].Error = reason
		}
	}
	j.status = status
	if status != StatusCompleted {
		j.err = reason
	}
	j.finishedAt = now
	j.spec = nil // drop credentials, attachments and recipient data
	j.cancel = nil
}
