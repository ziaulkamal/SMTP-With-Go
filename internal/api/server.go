// Package api exposes mailcore over HTTP/JSON.
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ziaulkamal/SMTP-With-Go/internal/config"
	"github.com/ziaulkamal/SMTP-With-Go/internal/job"
	"github.com/ziaulkamal/SMTP-With-Go/internal/mailer"
)

// Version is reported by /healthz.
var Version = "dev"

// Server wires HTTP handlers to the mailer and the job manager.
type Server struct {
	cfg    config.Config
	dialer mailer.Dialer
	jobs   *job.Manager
	log    *slog.Logger
	now    func() time.Time
}

// New creates the API server.
func New(cfg config.Config, dialer mailer.Dialer, jobs *job.Manager, log *slog.Logger) *Server {
	return &Server{cfg: cfg, dialer: dialer, jobs: jobs, log: log, now: time.Now}
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)

	v1 := http.NewServeMux()
	v1.HandleFunc("POST /v1/send", s.send)
	v1.HandleFunc("POST /v1/send/bulk", s.sendBulk)
	v1.HandleFunc("GET /v1/jobs", s.listJobs)
	v1.HandleFunc("GET /v1/jobs/{id}", s.getJob)
	v1.HandleFunc("DELETE /v1/jobs/{id}", s.cancelJob)
	v1.HandleFunc("POST /v1/smtp/verify", s.verify)
	mux.Handle("/v1/", s.auth(v1))

	return s.recoverer(s.logRequests(mux))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": Version})
}

// auth accepts "Authorization: Bearer <key>" or "X-API-Key: <key>".
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.cfg.APIKeys) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		key := r.Header.Get("X-API-Key")
		if key == "" {
			if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
				key = h[7:]
			}
		}
		valid := 0
		for _, k := range s.cfg.APIKeys {
			valid |= subtle.ConstantTimeCompare([]byte(key), []byte(k))
		}
		if key == "" || valid != 1 {
			writeError(w, http.StatusUnauthorized, ErrorBody{Code: "unauthorized", Message: "missing or invalid API key"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 64 {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-Id", id)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		s.log.Info("http request", "request_id", id, "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration_ms", time.Since(start).Milliseconds(), "remote", r.RemoteAddr)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic in handler", "panic", v, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, ErrorBody{Code: "internal_error", Message: "internal server error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// decode reads a single JSON object, rejecting unknown fields so typos in
// field names surface as errors instead of being silently ignored.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil && dec.More() {
		err = errors.New("body must contain a single JSON object")
	}
	if err == nil {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, ErrorBody{Code: "payload_too_large",
			Message: fmt.Sprintf("request body exceeds %d bytes", s.cfg.MaxBodyBytes)})
		return false
	}
	if errors.Is(err, io.EOF) {
		err = errors.New("request body is empty")
	}
	writeError(w, http.StatusBadRequest, ErrorBody{Code: "invalid_json", Message: err.Error()})
	return false
}

func validationError(w http.ResponseWriter, msg string, details ...string) {
	writeError(w, http.StatusUnprocessableEntity, ErrorBody{Code: "validation_error", Message: msg, Details: details})
}

func smtpError(w http.ResponseWriter, err error) {
	body := ErrorBody{Code: "smtp_error", Message: err.Error()}
	var se *mailer.Error
	if errors.As(err, &se) {
		body.Stage = string(se.Stage)
		if se.Stage == mailer.StageAuth {
			body.Code = "smtp_auth_failed"
		}
	}
	writeError(w, http.StatusBadGateway, body)
}

func validCallbackURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, body ErrorBody) {
	writeJSON(w, status, ErrorResponse{Error: body})
}
