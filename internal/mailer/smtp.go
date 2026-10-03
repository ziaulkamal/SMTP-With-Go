package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Encryption is the transport security used to reach the SMTP server.
type Encryption string

const (
	EncryptionNone     Encryption = "none"     // plain TCP (port 25 relays, local testing)
	EncryptionSTARTTLS Encryption = "starttls" // upgrade after connect (port 587)
	EncryptionSSL      Encryption = "ssl"      // implicit TLS (port 465)
)

// AuthMethod selects the SMTP AUTH mechanism.
type AuthMethod string

const (
	AuthAuto  AuthMethod = "auto" // PLAIN if offered, otherwise LOGIN
	AuthPlain AuthMethod = "plain"
	AuthLogin AuthMethod = "login"
	AuthNone  AuthMethod = "none"
)

// SMTPConfig is the SMTP credential set supplied by the calling application.
// It is used for the duration of a request or job and never persisted.
type SMTPConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// Encryption defaults to "ssl" on port 465 and "starttls" otherwise.
	Encryption Encryption `json:"encryption,omitempty"`
	Auth       AuthMethod `json:"auth,omitempty"`
	// InsecureSkipVerify disables TLS certificate verification. Only for
	// servers with self-signed certificates on trusted networks.
	InsecureSkipVerify bool `json:"insecure_skip_verify,omitempty"`
	// AllowInsecureAuth permits sending credentials over an unencrypted
	// connection to a non-localhost server.
	AllowInsecureAuth bool `json:"allow_insecure_auth,omitempty"`
}

// WithDefaults fills in the encryption and auth defaults.
func (c SMTPConfig) WithDefaults() SMTPConfig {
	if c.Port == 0 {
		c.Port = 587
	}
	if c.Encryption == "" {
		if c.Port == 465 {
			c.Encryption = EncryptionSSL
		} else {
			c.Encryption = EncryptionSTARTTLS
		}
	}
	if c.Auth == "" {
		c.Auth = AuthAuto
	}
	return c
}

// Validate checks the configuration after defaults are applied.
func (c SMTPConfig) Validate() error {
	c = c.WithDefaults()
	if c.Host == "" || strings.ContainsAny(c.Host, " \t\r\n/") {
		return errors.New("smtp.host is required and must be a hostname or IP")
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("smtp.port must be between 1 and 65535")
	}
	switch c.Encryption {
	case EncryptionNone, EncryptionSTARTTLS, EncryptionSSL:
	default:
		return fmt.Errorf("smtp.encryption must be one of none, starttls, ssl (got %q)", c.Encryption)
	}
	switch c.Auth {
	case AuthAuto, AuthPlain, AuthLogin, AuthNone:
	default:
		return fmt.Errorf("smtp.auth must be one of auto, plain, login, none (got %q)", c.Auth)
	}
	if c.Auth != AuthNone && c.Username != "" && c.Password == "" {
		return errors.New("smtp.password is required when smtp.username is set")
	}
	return nil
}

func (c SMTPConfig) addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// Stage identifies where in the SMTP conversation an error happened.
type Stage string

const (
	StageConfig  Stage = "config"
	StageConnect Stage = "connect"
	StageTLS     Stage = "tls"
	StageAuth    Stage = "auth"
	StageMail    Stage = "mail_from"
	StageRcpt    Stage = "rcpt_to"
	StageData    Stage = "data"
)

// Error wraps an SMTP failure with the stage it happened in.
type Error struct {
	Stage Stage
	Err   error
}

func (e *Error) Error() string { return fmt.Sprintf("smtp %s: %v", e.Stage, e.Err) }
func (e *Error) Unwrap() error { return e.Err }

// Code returns the SMTP reply code, or 0 for network/protocol errors.
func (e *Error) Code() int {
	var tp *textproto.Error
	if errors.As(e.Err, &tp) {
		return tp.Code
	}
	return 0
}

// IsPermanent reports whether retrying the same operation is pointless:
// a 5xx reply, a rejected configuration, or a TLS/auth failure that is not
// a temporary (4xx) or network error.
func IsPermanent(err error) bool {
	var se *Error
	if !errors.As(err, &se) {
		return false
	}
	code := se.Code()
	switch se.Stage {
	case StageConfig:
		return true
	case StageTLS, StageAuth:
		if code == 0 {
			return !isTransientNetErr(se.Err)
		}
	}
	return code >= 500
}

// IsAuthError reports whether err is an authentication failure.
func IsAuthError(err error) bool {
	var se *Error
	return errors.As(err, &se) && se.Stage == StageAuth
}

func isTransientNetErr(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// SendResult describes which envelope recipients the server accepted.
type SendResult struct {
	Accepted []string          `json:"accepted"`
	Rejected map[string]string `json:"rejected,omitempty"`
}

// Session is an authenticated SMTP connection that can send several
// messages in a row.
type Session interface {
	Send(from string, rcpts []string, msg []byte) (*SendResult, error)
	Close() error
}

// Dialer opens SMTP sessions.
type Dialer interface {
	Dial(ctx context.Context, cfg SMTPConfig) (Session, error)
}

// SMTPDialer is the production Dialer backed by net/smtp.
type SMTPDialer struct {
	// HeloName is the name announced in EHLO.
	HeloName string
	// Timeout bounds connecting/authenticating and each message sent.
	Timeout time.Duration
}

// Dial connects, negotiates TLS and authenticates.
func (d SMTPDialer) Dial(ctx context.Context, cfg SMTPConfig) (Session, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, &Error{Stage: StageConfig, Err: err}
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	helo := d.HeloName
	if helo == "" {
		helo = "localhost"
	}
	tlsConfig := &tls.Config{
		ServerName:         cfg.Host,
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // explicit opt-in by the caller
		MinVersion:         tls.VersionTLS12,
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	netDialer := &net.Dialer{}
	var conn net.Conn
	var err error
	if cfg.Encryption == EncryptionSSL {
		conn, err = (&tls.Dialer{NetDialer: netDialer, Config: tlsConfig}).DialContext(ctx, "tcp", cfg.addr())
	} else {
		conn, err = netDialer.DialContext(ctx, "tcp", cfg.addr())
	}
	if err != nil {
		return nil, &Error{Stage: StageConnect, Err: err}
	}
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return nil, &Error{Stage: StageConnect, Err: err}
	}
	fail := func(stage Stage, err error) (Session, error) {
		c.Close()
		return nil, &Error{Stage: stage, Err: err}
	}
	if err := c.Hello(helo); err != nil {
		return fail(StageConnect, err)
	}
	if cfg.Encryption == EncryptionSTARTTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fail(StageTLS, errors.New("server does not support STARTTLS; use encryption \"ssl\" or \"none\""))
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return fail(StageTLS, err)
		}
	}
	if cfg.Username != "" && cfg.Auth != AuthNone {
		auth, err := pickAuth(c, cfg)
		if err != nil {
			return fail(StageAuth, err)
		}
		if err := c.Auth(auth); err != nil {
			return fail(StageAuth, err)
		}
	}
	_ = conn.SetDeadline(time.Time{})
	return &client{conn: conn, c: c, timeout: timeout}, nil
}

func pickAuth(c *smtp.Client, cfg SMTPConfig) (smtp.Auth, error) {
	ok, params := c.Extension("AUTH")
	if !ok {
		return nil, errors.New("server does not advertise AUTH; set smtp.auth to \"none\" or remove the username")
	}
	mechs := strings.Fields(strings.ToUpper(params))
	has := func(m string) bool {
		for _, x := range mechs {
			if x == m {
				return true
			}
		}
		return false
	}
	method := cfg.Auth
	if method == AuthAuto {
		switch {
		case has("PLAIN"):
			method = AuthPlain
		case has("LOGIN"):
			method = AuthLogin
		default:
			return nil, fmt.Errorf("no supported AUTH mechanism (server offers %q)", params)
		}
	}
	if method == AuthPlain {
		return &plainAuth{username: cfg.Username, password: cfg.Password, allowInsecure: cfg.AllowInsecureAuth}, nil
	}
	return &loginAuth{username: cfg.Username, password: cfg.Password, allowInsecure: cfg.AllowInsecureAuth}, nil
}

type client struct {
	conn    net.Conn
	c       *smtp.Client
	timeout time.Duration
}

// Send delivers one message. Recipients the server rejects are reported in
// SendResult.Rejected; the call only fails if none were accepted.
func (cl *client) Send(from string, rcpts []string, msg []byte) (*SendResult, error) {
	_ = cl.conn.SetDeadline(time.Now().Add(cl.timeout))
	defer cl.conn.SetDeadline(time.Time{})

	if err := cl.c.Mail(from); err != nil {
		cl.reset()
		return nil, &Error{Stage: StageMail, Err: err}
	}
	res := &SendResult{}
	var lastErr error
	for _, r := range rcpts {
		if err := cl.c.Rcpt(r); err != nil {
			if res.Rejected == nil {
				res.Rejected = make(map[string]string)
			}
			res.Rejected[r] = err.Error()
			lastErr = err
			continue
		}
		res.Accepted = append(res.Accepted, r)
	}
	if len(res.Accepted) == 0 {
		cl.reset()
		return res, &Error{Stage: StageRcpt, Err: lastErr}
	}
	w, err := cl.c.Data()
	if err != nil {
		cl.reset()
		return res, &Error{Stage: StageData, Err: err}
	}
	if _, err := w.Write(msg); err != nil {
		w.Close()
		return res, &Error{Stage: StageData, Err: err}
	}
	if err := w.Close(); err != nil {
		return res, &Error{Stage: StageData, Err: err}
	}
	return res, nil
}

func (cl *client) reset() { _ = cl.c.Reset() }

func (cl *client) Close() error {
	_ = cl.conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := cl.c.Quit(); err != nil {
		return cl.c.Close()
	}
	return nil
}

// plainAuth implements AUTH PLAIN (RFC 4616). Unlike net/smtp.PlainAuth it
// lets callers opt in to unencrypted connections to remote hosts.
type plainAuth struct {
	username, password string
	allowInsecure      bool
}

func (a *plainAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if err := checkSecure(server, a.allowInsecure); err != nil {
		return "", nil, err
	}
	return "PLAIN", []byte("\x00" + a.username + "\x00" + a.password), nil
}

func (a *plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge during AUTH PLAIN")
	}
	return nil, nil
}

// loginAuth implements the AUTH LOGIN mechanism required by e.g. Office 365.
type loginAuth struct {
	username, password string
	allowInsecure      bool
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if err := checkSecure(server, a.allowInsecure); err != nil {
		return "", nil, err
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	prompt := strings.ToLower(strings.TrimSpace(string(fromServer)))
	switch {
	case strings.HasPrefix(prompt, "user"):
		return []byte(a.username), nil
	case strings.HasPrefix(prompt, "pass"):
		return []byte(a.password), nil
	default:
		return nil, fmt.Errorf("unexpected AUTH LOGIN challenge %q", fromServer)
	}
}

func checkSecure(server *smtp.ServerInfo, allowInsecure bool) error {
	if server.TLS || allowInsecure || isLocalhost(server.Name) {
		return nil
	}
	return errors.New("refusing to send credentials over an unencrypted connection; use encryption \"starttls\"/\"ssl\" or set allow_insecure_auth")
}

func isLocalhost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
