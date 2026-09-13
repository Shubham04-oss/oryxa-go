package mail

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"net/smtp"
	"strings"
)

// SMTPSender delivers rendered mail over SMTP. Local runs point this at
// Mailpit; production keeps using SES.
type SMTPSender struct {
	addr string
	from string
}

func NewSMTPSender(addr, from string) *SMTPSender {
	return &SMTPSender{addr: addr, from: from}
}

func (s *SMTPSender) Send(_ context.Context, email Email) (string, error) {
	if email.To == "" {
		return "", errors.New("recipient is required")
	}
	if s.addr == "" {
		return "", errors.New("smtp address is required")
	}
	parsed, err := mail.ParseAddress(s.from)
	if err != nil {
		return "", fmt.Errorf("sender address: %w", err)
	}
	if err := smtp.SendMail(s.addr, nil, parsed.Address, []string{email.To}, formatSMTPMessage(s.from, email.To, email)); err != nil {
		return "", fmt.Errorf("send %s email: %w", email.Message.Template, err)
	}
	if email.DeliveryID != "" {
		return "smtp-" + email.DeliveryID, nil
	}
	return "smtp", nil
}

func formatSMTPMessage(from, to string, email Email) []byte {
	var body strings.Builder
	body.WriteString("From: " + from + "\r\n")
	body.WriteString("To: " + to + "\r\n")
	body.WriteString("Subject: " + email.Message.Subject + "\r\n")
	body.WriteString("MIME-Version: 1.0\r\n")
	body.WriteString("Content-Type: multipart/alternative; boundary=\"oryxa\"\r\n\r\n")
	body.WriteString("--oryxa\r\n")
	body.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	body.WriteString(email.Message.Text)
	if !strings.HasSuffix(email.Message.Text, "\n") {
		body.WriteString("\r\n")
	}
	body.WriteString("\r\n--oryxa\r\n")
	body.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	body.WriteString(email.Message.HTML)
	if !strings.HasSuffix(email.Message.HTML, "\n") {
		body.WriteString("\r\n")
	}
	body.WriteString("\r\n--oryxa--\r\n")
	return []byte(body.String())
}
