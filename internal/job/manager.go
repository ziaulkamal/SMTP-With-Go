package job

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/ziaulkamal/mailcore/internal/mailer"
)

// Errors returned by the Manager.
var (
	ErrQueueFull    = errors.New("job queue is full, try again later")
	ErrNotFound     = errors.New("job not found")
	ErrFinished     = errors.New("job already finished")
	ErrShuttingDown = errors.New("server is shutting down")
)

// Options configures a Manager.
type Options struct {
	Workers        int           // jobs processed in parallel
	QueueSize      int           // jobs waiting to start
	TTL            time.Duration // how long finished jobs stay queryable
	RetryBaseDelay time.Duration // first retry delay, doubled per attempt (max 30s)
	CallbackSecret string        // HMAC key for callback signatures
	HTTPClient     *http.Client  // used for callbacks
	Logger         *slog.Logger
}

// Manager queues, runs and tracks bulk jobs in memory.
type Manager struct {
	dialer mailer.Dialer
	opts   Options
	now    func() time.Time

	mu    sync.RWMutex
	jobs  map[string]*Job
	queue chan *Job

	ctx    context.Context
	stop   context.CancelFunc
	wg     sync.WaitGroup
	closed bool
}

// NewManager creates a Manager. Call Start to begin processing.
func NewManager(dialer mailer.Dialer, opts Options) *Manager {
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = 100
	}
	if opts.TTL <= 0 {
		opts.TTL = 24 * time.Hour
	}
	if opts.RetryBaseDelay <= 0 {
		opts.RetryBaseDelay = 2 * time.Second
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	ctx, stop := context.WithCancel(context.Background())
	return &Manager{
		dialer: dialer, opts: opts, now: time.Now,
		jobs: make(map[string]*Job), queue: make(chan *Job, opts.QueueSize),
		ctx: ctx, stop: stop,
	}
}

// Start launches the job runners and the janitor that expires old jobs.
func (m *Manager) Start() {
	for i := 0; i < m.opts.Workers; i++ {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			for {
				select {
				case <-m.ctx.Done():
					return
				case j := <-m.queue:
					m.run(j)
				}
			}
		}()
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-t.C:
				m.expire()
			}
		}
	}()
}

// Submit validates nothing (the API layer does) and enqueues the job.
func (m *Manager) Submit(spec *Spec) (Snapshot, error) {
	j := newJob(newID(), spec, m.now())
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Snapshot{}, ErrShuttingDown
	}
	select {
	case m.queue <- j:
	default:
		return Snapshot{}, ErrQueueFull
	}
	m.jobs[j.id] = j
	return j.Snapshot(false), nil
}

// Get returns a job snapshot.
func (m *Manager) Get(id string, withResults bool) (Snapshot, error) {
	m.mu.RLock()
	j, ok := m.jobs[id]
	m.mu.RUnlock()
	if !ok {
		return Snapshot{}, ErrNotFound
	}
	return j.Snapshot(withResults), nil
}

// List returns summaries of all known jobs, newest first.
func (m *Manager) List() []Snapshot {
	m.mu.RLock()
	out := make([]Snapshot, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j.Snapshot(false))
	}
	m.mu.RUnlock()
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.After(out[b].CreatedAt) })
	return out
}

// Cancel stops a queued or running job. Recipients already sent stay sent.
func (m *Manager) Cancel(id string) (Snapshot, error) {
	m.mu.RLock()
	j, ok := m.jobs[id]
	m.mu.RUnlock()
	if !ok {
		return Snapshot{}, ErrNotFound
	}
	j.mu.Lock()
	if j.finished() {
		j.mu.Unlock()
		return j.Snapshot(false), ErrFinished
	}
	j.cancelReq = true
	cancel := j.cancel
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return j.Snapshot(false), nil
}

// Shutdown stops accepting jobs, cancels running ones and waits for the
// runners to exit or ctx to expire.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.stop()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	// Jobs still queued never started.
	for {
		select {
		case j := <-m.queue:
			j.finish(StatusCancelled, ErrShuttingDown.Error(), RecipientCancelled, m.now())
		default:
			return nil
		}
	}
}

func (m *Manager) expire() {
	cutoff := m.now().Add(-m.opts.TTL)
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, j := range m.jobs {
		j.mu.Lock()
		old := j.finished() && j.finishedAt.Before(cutoff)
		j.mu.Unlock()
		if old {
			delete(m.jobs, id)
		}
	}
}

// run executes a job to completion.
func (m *Manager) run(j *Job) {
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()

	j.mu.Lock()
	if j.cancelReq {
		j.mu.Unlock()
		j.finish(StatusCancelled, "cancelled by client", RecipientCancelled, m.now())
		m.callback(j)
		return
	}
	j.status = StatusRunning
	j.startedAt = m.now()
	j.cancel = cancel
	spec := j.spec
	j.mu.Unlock()

	log := m.opts.Logger.With("job_id", j.id)
	log.Info("bulk job started", "recipients", len(spec.Recipients), "concurrency", spec.Concurrency)

	var abortOnce sync.Once
	var abortErr error
	abort := func(err error) {
		abortOnce.Do(func() {
			abortErr = err
			cancel()
		})
	}

	limiter := newLimiter(spec.RatePerSecond)
	defer limiter.stop()

	indexes := make(chan int)
	var wg sync.WaitGroup
	workers := spec.Concurrency
	if workers > len(spec.Recipients) {
		workers = len(spec.Recipients)
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.worker(ctx, j, spec, indexes, limiter, abort)
		}()
	}
feed:
	for i := range spec.Recipients {
		select {
		case indexes <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(indexes)
	wg.Wait()

	j.mu.Lock()
	cancelled := j.cancelReq
	j.mu.Unlock()
	switch {
	case abortErr != nil:
		j.finish(StatusFailed, "job aborted: "+abortErr.Error(), RecipientFailed, m.now())
	case cancelled:
		j.finish(StatusCancelled, "cancelled by client", RecipientCancelled, m.now())
	case m.ctx.Err() != nil:
		j.finish(StatusCancelled, ErrShuttingDown.Error(), RecipientCancelled, m.now())
	default:
		j.finish(StatusCompleted, "", RecipientFailed, m.now())
	}
	snap := j.Snapshot(false)
	log.Info("bulk job finished", "status", snap.Status, "sent", snap.Sent, "failed", snap.Failed, "cancelled", snap.Cancelled)
	m.callback(j)
}

// worker sends to the recipients it receives, keeping one SMTP session open
// across messages and reconnecting after transient failures.
func (m *Manager) worker(ctx context.Context, j *Job, spec *Spec, indexes <-chan int, lim *limiter, abort func(error)) {
	var sess mailer.Session
	defer func() {
		if sess != nil {
			sess.Close()
		}
	}()
	for i := range indexes {
		r := spec.Recipients[i]
		raw, messageID, err := m.render(spec, r)
		if err != nil {
			j.markFailed(i, 0, err)
			continue
		}
		for attempt := 1; ; attempt++ {
			if lim.wait(ctx) != nil {
				return // cancelled; the recipient stays pending and is settled by run
			}
			if sess == nil {
				sess, err = m.dialer.Dial(ctx, spec.SMTP)
				if err != nil {
					sess = nil
					if mailer.IsPermanent(err) {
						abort(err)
						return
					}
				}
			}
			if sess != nil {
				_, err = sess.Send(spec.From.Email, []string{r.Address.Email}, raw)
				if err == nil {
					j.markSent(i, attempt, messageID, m.now())
					break
				}
				if !mailer.IsPermanent(err) {
					// 4xx and network errors often leave the connection unusable.
					sess.Close()
					sess = nil
				}
			}
			if ctx.Err() != nil {
				return
			}
			if mailer.IsPermanent(err) || attempt > spec.MaxRetries {
				j.markFailed(i, attempt, err)
				break
			}
			if sleep(ctx, m.backoff(attempt)) != nil {
				return
			}
		}
	}
}

func (m *Manager) render(spec *Spec, r Recipient) ([]byte, string, error) {
	out, err := spec.Template.Render(mailer.TemplateData(r.Address, r.Data))
	if err != nil {
		return nil, "", err
	}
	msg := mailer.Message{
		From: spec.From, To: []mailer.Address{r.Address}, ReplyTo: spec.ReplyTo,
		Subject: out.Subject, Text: out.Text, HTML: out.HTML,
		Headers: spec.Headers, Attachments: spec.Attachments,
	}
	if err := msg.Validate(); err != nil {
		return nil, "", err
	}
	id := mailer.NewMessageID(spec.From.Domain())
	raw, err := msg.Build(m.now(), id)
	return raw, id, err
}

func (m *Manager) backoff(attempt int) time.Duration {
	d := m.opts.RetryBaseDelay << (attempt - 1)
	if d > 30*time.Second || d <= 0 {
		d = 30 * time.Second
	}
	return d
}

// callback POSTs the final job summary to the client's callback URL.
func (m *Manager) callback(j *Job) {
	url := j.callbackURL
	if url == "" {
		return
	}
	snap := j.Snapshot(true)
	body, err := json.Marshal(map[string]any{"event": "job.finished", "job": snap})
	if err != nil {
		return
	}
	log := m.opts.Logger.With("job_id", j.id)
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			log.Warn("invalid callback url", "error", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Mailcore-Event", "job.finished")
		if m.opts.CallbackSecret != "" {
			mac := hmac.New(sha256.New, []byte(m.opts.CallbackSecret))
			mac.Write(body)
			req.Header.Set("X-Mailcore-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
		resp, err := m.opts.HTTPClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 300 {
				return
			}
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		log.Warn("callback failed", "attempt", attempt, "error", err)
		time.Sleep(time.Duration(attempt) * time.Second)
	}
}

func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "job_" + hex.EncodeToString(b)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// limiter spaces sends evenly across all workers of a job.
type limiter struct{ t *time.Ticker }

func newLimiter(perSecond float64) *limiter {
	if perSecond <= 0 {
		return &limiter{}
	}
	return &limiter{t: time.NewTicker(time.Duration(float64(time.Second) / perSecond))}
}

func (l *limiter) wait(ctx context.Context) error {
	if l.t == nil {
		return ctx.Err()
	}
	select {
	case <-l.t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *limiter) stop() {
	if l.t != nil {
		l.t.Stop()
	}
}
