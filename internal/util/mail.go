package util

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"strings"
	"time"

	"swiftab/server/internal/config"
)

// Mailer sends transactional email over SMTP. With no SMTP settings it is
// disabled and Send returns ErrMailDisabled so callers can degrade honestly
// instead of pretending a message went out.
type Mailer struct {
	cfg config.SMTPConfig
}

var ErrMailDisabled = errors.New("email delivery is not configured")

func NewMailer(cfg config.SMTPConfig) *Mailer {
	return &Mailer{cfg: cfg}
}

func (m *Mailer) Enabled() bool {
	return m != nil && m.cfg.Enabled()
}

// Send delivers a plain-text message.
func (m *Mailer) Send(to, subject, body string) error {
	if !m.Enabled() {
		return ErrMailDisabled
	}
	if strings.ContainsAny(to, "\r\n") || strings.ContainsAny(subject, "\r\n") {
		return errors.New("invalid recipient or subject")
	}

	addr := net.JoinHostPort(m.cfg.Host, m.cfg.Port)
	msg := strings.Join([]string{
		"From: Swiftab <" + m.cfg.From + ">",
		"To: " + to,
		"Subject: " + subject,
		"Date: " + time.Now().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")

	var auth smtp.Auth
	if m.cfg.User != "" {
		auth = smtp.PlainAuth("", m.cfg.User, m.cfg.Pass, m.cfg.Host)
	}

	// Port 465 expects TLS from the first byte; smtp.SendMail only does STARTTLS.
	if m.cfg.Port == "465" {
		return m.sendImplicitTLS(addr, auth, to, []byte(msg))
	}
	return smtp.SendMail(addr, auth, m.cfg.From, []string{to}, []byte(msg))
}

func (m *Mailer) sendImplicitTLS(addr string, auth smtp.Auth, to string, msg []byte) error {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: m.cfg.Host})
	if err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer client.Close()

	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(m.cfg.From); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// SendAsync delivers in the background and logs failures. Use it when the
// caller has another way to surface the content (for example the dashboard
// showing a waiter's invite code).
func (m *Mailer) SendAsync(to, subject, body string) {
	if !m.Enabled() {
		return
	}
	go func() {
		if err := m.Send(to, subject, body); err != nil {
			log.Printf("email to %s failed: %v", to, err)
		}
	}()
}

func WaiterInviteEmail(firstName, restaurantName, code string) (subject, body string) {
	if restaurantName == "" {
		restaurantName = "your restaurant"
	}
	subject = "Your Swiftab Waiter invite code"
	body = fmt.Sprintf(
		"Hi %s,\n\nYou have been added to the floor team at %s on Swiftab.\n\n"+
			"Your invite code is: %s\n\n"+
			"Open the Swiftab Waiter app, choose \"Sign up\", enter this email address and the code, "+
			"then create your password. The code expires in 48 hours.\n\n— Swiftab",
		firstName, restaurantName, code)
	return
}

func PasswordResetEmail(name, code string) (subject, body string) {
	subject = "Your Swiftab password reset code"
	body = fmt.Sprintf(
		"Hi %s,\n\nUse this code to reset your Swiftab password: %s\n\n"+
			"It expires in 10 minutes. If you did not ask for it you can ignore this email.\n\n— Swiftab",
		name, code)
	return
}
