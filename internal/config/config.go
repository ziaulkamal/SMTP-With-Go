// Package config loads mailcore settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime settings.
type Config struct {
	Addr     string   // MAILCORE_ADDR
	APIKeys  []string // MAILCORE_API_KEYS (comma separated)
	LogLevel string   // MAILCORE_LOG_LEVEL: debug, info, warn, error

	MaxBodyBytes          int64 // MAILCORE_MAX_BODY_BYTES
	MaxRecipientsPerEmail int   // MAILCORE_MAX_RECIPIENTS_PER_EMAIL (to+cc+bcc in /v1/send)
	MaxBulkRecipients     int   // MAILCORE_MAX_BULK_RECIPIENTS
	MaxBulkConcurrency    int   // MAILCORE_MAX_BULK_CONCURRENCY
	MaxRetries            int   // MAILCORE_MAX_RETRIES

	JobWorkers   int           // MAILCORE_JOB_WORKERS
	JobQueueSize int           // MAILCORE_JOB_QUEUE_SIZE
	JobTTL       time.Duration // MAILCORE_JOB_TTL

	SMTPTimeout    time.Duration // MAILCORE_SMTP_TIMEOUT
	HeloName       string        // MAILCORE_HELO_NAME
	CallbackSecret string        // MAILCORE_CALLBACK_SECRET
}

// Load reads the configuration from the environment.
func Load() (Config, error) {
	host, _ := os.Hostname()
	c := Config{
		Addr:           env("MAILCORE_ADDR", ":8080"),
		LogLevel:       env("MAILCORE_LOG_LEVEL", "info"),
		HeloName:       env("MAILCORE_HELO_NAME", host),
		CallbackSecret: os.Getenv("MAILCORE_CALLBACK_SECRET"),
	}
	for _, k := range strings.Split(os.Getenv("MAILCORE_API_KEYS"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			c.APIKeys = append(c.APIKeys, k)
		}
	}
	var err error
	ints := []struct {
		name string
		dst  *int
		def  int
	}{
		{"MAILCORE_MAX_RECIPIENTS_PER_EMAIL", &c.MaxRecipientsPerEmail, 50},
		{"MAILCORE_MAX_BULK_RECIPIENTS", &c.MaxBulkRecipients, 5000},
		{"MAILCORE_MAX_BULK_CONCURRENCY", &c.MaxBulkConcurrency, 10},
		{"MAILCORE_MAX_RETRIES", &c.MaxRetries, 5},
		{"MAILCORE_JOB_WORKERS", &c.JobWorkers, 4},
		{"MAILCORE_JOB_QUEUE_SIZE", &c.JobQueueSize, 100},
	}
	for _, i := range ints {
		if *i.dst, err = envInt(i.name, i.def); err != nil {
			return c, err
		}
	}
	maxBody, err := envInt("MAILCORE_MAX_BODY_BYTES", 25<<20)
	if err != nil {
		return c, err
	}
	c.MaxBodyBytes = int64(maxBody)
	if c.JobTTL, err = envDuration("MAILCORE_JOB_TTL", 24*time.Hour); err != nil {
		return c, err
	}
	if c.SMTPTimeout, err = envDuration("MAILCORE_SMTP_TIMEOUT", 30*time.Second); err != nil {
		return c, err
	}
	return c, nil
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", name, v)
	}
	return n, nil
}

func envDuration(name string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration like 30s or 24h, got %q", name, v)
	}
	return d, nil
}
