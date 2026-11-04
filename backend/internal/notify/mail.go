package notify

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Mail is the mail server that sends the emails, from DATA_DEN_SMTP_*.
type Mail struct {
	Host string
	// Port 465 speaks TLS from the start; any other port must offer
	// STARTTLS, as passwords never go out unencrypted.
	Port     int
	Username string
	Password string
	// From is the sender, such as "DATA-DEN <nas@example.org>".
	From string
}

// MailFromEnv reads the mail server from the settings; nil means no email.
// get is os.Getenv.
func MailFromEnv(get func(string) string) (*Mail, error) {
	m := &Mail{
		Host:     get("DATA_DEN_SMTP_HOST"),
		Username: get("DATA_DEN_SMTP_USERNAME"),
		Password: get("DATA_DEN_SMTP_PASSWORD"),
		From:     get("DATA_DEN_SMTP_FROM"),
	}
	port := get("DATA_DEN_SMTP_PORT")
	if m.Host == "" {
		if port != "" || m.Username != "" || m.Password != "" || m.From != "" {
			return nil, errors.New("DATA_DEN_SMTP_HOST is empty, but other DATA_DEN_SMTP_ settings are set")
		}
		return nil, nil
	}
	if port == "" {
		return nil, errors.New("set DATA_DEN_SMTP_PORT as well, usually 587 (STARTTLS) or 465 (TLS)")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return nil, fmt.Errorf("DATA_DEN_SMTP_PORT must be a port number, not %q", port)
	}
	m.Port = p
	if _, err := mail.ParseAddress(m.From); err != nil {
		return nil, fmt.Errorf("DATA_DEN_SMTP_FROM must be an email address, such as DATA-DEN <nas@example.org>, not %q", m.From)
	}
	if (m.Username == "") != (m.Password == "") {
		return nil, errors.New("set both DATA_DEN_SMTP_USERNAME and DATA_DEN_SMTP_PASSWORD, or neither")
	}
	return m, nil
}

// Address is the sender's bare address, for the SMTP envelope.
func (m *Mail) Address() string {
	a, err := mail.ParseAddress(m.From)
	if err != nil {
		return m.From
	}
	return a.Address
}

// validEmail reports whether s is one plain email address, as a user may
// enter it.
func validEmail(s string) bool {
	if len(s) > 254 || strings.ContainsAny(s, "\r\n<>,; ") {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && a.Name == ""
}

// oneLine keeps a subject on one line, so text from a file name cannot add
// headers.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// buildMail is the message, UTF-8 text as quoted-printable.
func buildMail(from, to, subject, body string, now time.Time, id string) []byte {
	var b bytes.Buffer
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", oneLine(subject)) + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: <" + id + ">\r\n")
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	w := quotedprintable.NewWriter(&b)
	_, _ = w.Write([]byte(strings.ReplaceAll(body, "\n", "\r\n")))
	_ = w.Close()
	b.WriteString("\r\n")
	return b.Bytes()
}

// sendMail sends msg over the mail server m.
func sendMail(m *Mail, to string, msg []byte) error {
	addr := net.JoinHostPort(m.Host, strconv.Itoa(m.Port))
	tlsConfig := &tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	var conn net.Conn
	var err error
	if m.Port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()
	if m.Port != 465 {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New(m.Host + " offers no STARTTLS; DATA-DEN sends no unencrypted email")
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if m.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", m.Username, m.Password, m.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(m.Address()); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
