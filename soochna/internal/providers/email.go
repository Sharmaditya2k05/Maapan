package providers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Email is one transactional e-mail.
type Email struct {
	To       string // address
	Template string
	Subject  string
	Text     string // plain-text body

	// Attachment by URL (https). The driver downloads it at send time so the
	// PDF reflects the current document; a failed download fails the send
	// (and the message is retried) rather than going out without it.
	AttachmentURL  string
	AttachmentName string

	// Per-tenant overrides from the registry ("" = provider default).
	FromAddress string
	FromName    string
	ReplyTo     string
}

// Mailer delivers one e-mail and returns the provider's reference (Message-ID).
type Mailer interface {
	Name() string
	SendEmail(ctx context.Context, e Email) (ref string, err error)
}

// ConsoleMailer logs e-mails instead of sending them (local dev, tests).
type ConsoleMailer struct{ Log *slog.Logger }

func (c *ConsoleMailer) Name() string { return "console" }

func (c *ConsoleMailer) SendEmail(_ context.Context, e Email) (string, error) {
	c.Log.Info("CONSOLE EMAIL", "to", e.To, "template", e.Template, "subject", e.Subject, "body", e.Text,
		"attachment_url", e.AttachmentURL, "from", e.FromAddress)
	return "console-" + uuid.NewString(), nil
}

// SMTPConfig configures the SMTP driver. Any transactional provider that
// speaks SMTP works (Amazon SES, Postmark, SendGrid, Zoho ZeptoMail, Brevo).
type SMTPConfig struct {
	Host     string
	Port     int    // 587 (STARTTLS, default) or 465 (implicit TLS)
	Username string // empty = no AUTH
	Password string
	From     string // default From address
	FromName string
	ReplyTo  string

	// MaxAttachmentBytes caps downloaded attachments (default 10 MiB).
	MaxAttachmentBytes int64
}

// SMTP sends e-mail over SMTP with STARTTLS (or implicit TLS on port 465).
type SMTP struct {
	cfg  SMTPConfig
	http *http.Client
	// dial is replaceable in tests.
	dial func(ctx context.Context, addr string) (net.Conn, error)
}

// NewSMTP builds the driver.
func NewSMTP(cfg SMTPConfig) *SMTP {
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.MaxAttachmentBytes == 0 {
		cfg.MaxAttachmentBytes = 10 << 20
	}
	s := &SMTP{cfg: cfg, http: &http.Client{Timeout: 20 * time.Second}}
	s.dial = func(ctx context.Context, addr string) (net.Conn, error) {
		d := &net.Dialer{Timeout: 10 * time.Second}
		if cfg.Port == 465 {
			return (&tls.Dialer{NetDialer: d, Config: &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", addr)
		}
		return d.DialContext(ctx, "tcp", addr)
	}
	return s
}

func (s *SMTP) Name() string { return "smtp" }

// SendEmail builds a MIME message (text + optional PDF attachment) and sends it.
func (s *SMTP) SendEmail(ctx context.Context, e Email) (string, error) {
	from := firstNonEmpty(e.FromAddress, s.cfg.From)
	fromAddr, err := mail.ParseAddress(from)
	if err != nil {
		return "", fmt.Errorf("%w: bad From address %q", ErrNotConfigured, from)
	}
	fromAddr.Name = firstNonEmpty(e.FromName, s.cfg.FromName, fromAddr.Name)
	var attachment []byte
	if e.AttachmentURL != "" {
		if attachment, err = s.fetch(ctx, e.AttachmentURL); err != nil {
			return "", err
		}
	}
	msgID := fmt.Sprintf("<%s@%s>", uuid.NewString(), domainOf(fromAddr.Address))
	raw, err := BuildMIME(MIMEInput{
		From: fromAddr.String(), To: e.To, ReplyTo: firstNonEmpty(e.ReplyTo, s.cfg.ReplyTo), Subject: e.Subject,
		Text: e.Text, MessageID: msgID, Attachment: attachment,
		AttachmentName: fileName(e.AttachmentName, e.AttachmentURL),
	})
	if err != nil {
		return "", err
	}
	if err := s.deliver(ctx, fromAddr.Address, e.To, raw); err != nil {
		return "", fmt.Errorf("smtp: %w", err)
	}
	return msgID, nil
}

func (s *SMTP) fetch(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("attachment: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("attachment: %s returned %d", rawURL, res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, s.cfg.MaxAttachmentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("attachment: %w", err)
	}
	if int64(len(b)) > s.cfg.MaxAttachmentBytes {
		return nil, fmt.Errorf("attachment larger than %d bytes", s.cfg.MaxAttachmentBytes)
	}
	return b, nil
}

func (s *SMTP) deliver(ctx context.Context, from, to string, raw []byte) error {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	conn, err := s.dial(ctx, addr)
	if err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if s.cfg.Port != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
				return err
			}
		} else if s.cfg.Username != "" {
			return errors.New("server does not offer STARTTLS; refusing to send credentials in clear text")
		}
	}
	if s.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// MIMEInput is what BuildMIME needs.
type MIMEInput struct {
	From, To, ReplyTo, Subject, Text, MessageID string
	Attachment                                  []byte
	AttachmentName                              string
}

// BuildMIME renders an RFC 5322 message: text/plain, or multipart/mixed with
// one base64 attachment (PDFs are typed application/pdf).
func BuildMIME(in MIMEInput) ([]byte, error) {
	if strings.ContainsAny(in.To+in.From+in.ReplyTo, "\r\n") {
		return nil, errors.New("header injection")
	}
	var b bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	h("From", in.From)
	h("To", in.To)
	if in.ReplyTo != "" {
		h("Reply-To", in.ReplyTo)
	}
	h("Subject", mime.QEncoding.Encode("utf-8", strings.Join(strings.Fields(in.Subject), " ")))
	h("Date", time.Now().UTC().Format(time.RFC1123Z))
	h("Message-ID", in.MessageID)
	h("MIME-Version", "1.0")
	text := base64Lines([]byte(strings.ReplaceAll(in.Text, "\n", "\r\n")))
	if len(in.Attachment) == 0 {
		h("Content-Type", `text/plain; charset="utf-8"`)
		h("Content-Transfer-Encoding", "base64")
		b.WriteString("\r\n")
		b.WriteString(text)
		return b.Bytes(), nil
	}
	var rnd [12]byte
	_, _ = rand.Read(rnd[:])
	boundary := "maapan-" + hex.EncodeToString(rnd[:])
	h("Content-Type", `multipart/mixed; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=\"utf-8\"\r\nContent-Transfer-Encoding: base64\r\n\r\n%s", boundary, text)
	name := strings.NewReplacer(`"`, "", "\r", "", "\n", "").Replace(in.AttachmentName)
	ctype := mime.TypeByExtension(extOf(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; name=\"%s\"\r\nContent-Disposition: attachment; filename=\"%s\"\r\nContent-Transfer-Encoding: base64\r\n\r\n%s",
		boundary, ctype, name, name, base64Lines(in.Attachment))
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes(), nil
}

func base64Lines(p []byte) string {
	enc := base64.StdEncoding.EncodeToString(p)
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	b.WriteString("\r\n")
	return b.String()
}

func extOf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return strings.ToLower(name[i:])
	}
	return ""
}

func domainOf(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		return addr[i+1:]
	}
	return "maapan.in"
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
