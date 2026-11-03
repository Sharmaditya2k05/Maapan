package providers

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeSMTP accepts one plain-text SMTP session and returns the DATA it got.
func fakeSMTP(t *testing.T) (addr string, got <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ch := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		w := func(s string) { _, _ = io.WriteString(c, s+"\r\n") }
		w("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					w("250 queued")
					ch <- data.String()
					continue
				}
				data.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				w("250 fake")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				w("250 ok")
			case cmd == "DATA":
				inData = true
				w("354 go ahead")
			case cmd == "QUIT":
				w("221 bye")
				return
			default:
				w("250 ok")
			}
		}
	}()
	return ln.Addr().String(), ch
}

func TestSMTPSendsWithPDFAttachment(t *testing.T) {
	pdf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "%PDF-1.7 fake")
	}))
	defer pdf.Close()
	addr, got := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	s := NewSMTP(SMTPConfig{Host: host, Port: atoi(port), From: "bills@maapan.in", FromName: "Maapan"})
	ref, err := s.SendEmail(context.Background(), Email{
		To: "ravi@example.com", Subject: "Bill INV/1 from शर्मा Store", Text: "Dear Ravi,\nthanks",
		AttachmentURL: pdf.URL + "/v1/invoices/abc/pdf?sig=x", AttachmentName: "INV-1.pdf",
		FromName: "Sharma Store", ReplyTo: "owner@sharma.example",
	})
	if err != nil || !strings.HasSuffix(ref, "@maapan.in>") {
		t.Fatalf("ref=%q err=%v", ref, err)
	}
	data := <-got
	for _, want := range []string{"From: \"Sharma Store\" <bills@maapan.in>", "To: ravi@example.com", "Reply-To: owner@sharma.example",
		"multipart/mixed", `Content-Type: application/pdf; name="INV-1.pdf"`, base64.StdEncoding.EncodeToString([]byte("%PDF-1.7 fake"))} {
		if !strings.Contains(data, want) {
			t.Errorf("missing %q in\n%s", want, data)
		}
	}
	if strings.Contains(data, "शर्मा") {
		t.Error("subject must be Q-encoded")
	}
}

func TestSMTPAttachmentFailureFailsSend(t *testing.T) {
	missing := httptest.NewServer(http.NotFoundHandler())
	defer missing.Close()
	s := NewSMTP(SMTPConfig{Host: "127.0.0.1", Port: 1, From: "bills@maapan.in"})
	if _, err := s.SendEmail(context.Background(), Email{To: "a@b.c", AttachmentURL: missing.URL + "/x.pdf"}); err == nil {
		t.Fatal("a missing attachment must fail the send")
	}
}

func TestBuildMIMERejectsHeaderInjection(t *testing.T) {
	if _, err := BuildMIME(MIMEInput{From: "a@b.c", To: "x@y.z\r\nBcc: evil@x"}); err == nil {
		t.Fatal("CRLF in To must be rejected")
	}
}

func TestGupshupTenantOverridesAndDocument(t *testing.T) {
	var last url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		last = r.PostForm
		_, _ = io.WriteString(w, `{"status":"submitted","messageId":"gs-2"}`)
	}))
	defer srv.Close()
	g := NewGupshup(Config{GupshupAPIKey: "k", GupshupSource: "919000000000", GupshupAppName: "maapan", GupshupBaseURL: srv.URL})
	_, err := g.Send(context.Background(), Message{To: "+919876543210", Template: "invoice", Body: "bill",
		MediaURL: "https://files.example/inv/abc.pdf?sig=1", SenderID: "+919111111111", AppName: "sharma", TemplateID: "wa-inv-sharma",
		Params: map[string]string{"party_name": "R", "business": "S", "number": "1", "amount": "₹1", "date": "d"}})
	if err != nil {
		t.Fatal(err)
	}
	if last.Get("source") != "919111111111" || last.Get("src.name") != "sharma" {
		t.Fatalf("overrides ignored: %v", last)
	}
	if !strings.Contains(last.Get("template"), `"id":"wa-inv-sharma"`) || !strings.Contains(last.Get("message"), `"filename":"abc.pdf"`) {
		t.Fatalf("document header: %v", last)
	}
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}
