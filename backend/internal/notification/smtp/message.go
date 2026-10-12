package smtp

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
)

// SMTP envelopes need bare addresses even when display headers carry names.
// Message preparation parses those representations once, builds encoded UTF-8
// headers and a quoted-printable HTML body, and returns the envelope values
// alongside the complete wire bytes for transport.
//
// Sender and recipient names remain in their respective display headers, while
// MAIL and RCPT use the parsed address parts. CR/LF rejection protects address
// parsing, and displayed subject/name input has line separators removed before
// encoding. Body encoding is closed before publishing bytes so buffered content
// reaches the message, and each preparation receives fresh random Message-ID data.

// smtpMessage contains envelope addresses and the complete MIME message.
type smtpMessage struct {
	// envelopeFrom and envelopeTo are parsed address parts used by SMTP commands;
	// the separately encoded display names remain in data's From and To headers.
	envelopeFrom string // bare sender address
	envelopeTo   string // bare recipient address
	data         []byte // complete MIME bytes
}

// buildSMTPMessage returns validated envelope addresses and an encoded HTML MIME message.
// Nil configuration and invalid sender/recipient input return an error before
// transport setup. A blank sanitized FromName uses the sender address's parsed
// name, while a provided name replaces that display name in the From header.
//
// Headers use CRLF, the subject uses UTF-8 encoded words, and quoted-printable body
// encoding normalizes its line endings. Closing that encoder flushes pending bytes
// before the returned buffer is exposed. The message's random identifier uses the
// sender's domain when available, while envelope commands use bare parsed addresses
// independently of the names carried in display headers.
func buildSMTPMessage(config *SMTPConfig, to, subject, body string) (smtpMessage, error) {
	if config == nil {
		return smtpMessage{}, errors.New("missing SMTP configuration")
	}

	fromAddress, err := parseSMTPAddress(config.From, "from")
	if err != nil {
		return smtpMessage{}, err
	}
	recipientAddress, err := parseSMTPAddress(to, "recipient")
	if err != nil {
		return smtpMessage{}, err
	}
	messageID, err := generateEmailMessageID(fromAddress.Address, config.Host)
	if err != nil {
		return smtpMessage{}, fmt.Errorf("generate message ID: %w", err)
	}

	// Display names can contain header separators independently of their address.
	// Sanitize the configured name before net/mail encodes it, retaining a parsed sender name when none was supplied.
	fromName := sanitizeEmailHeader(config.FromName)
	if strings.TrimSpace(fromName) == "" {
		fromName = fromAddress.Name
	}
	fromHeader := (&mail.Address{
		Name:    fromName,
		Address: fromAddress.Address,
	}).String()
	toHeader := (&mail.Address{
		Name:    recipientAddress.Name,
		Address: recipientAddress.Address,
	}).String()
	// Encode subject text as UTF-8 words after separator removal so non-ASCII
	// subjects remain header text while CR/LF cannot introduce another header field.
	subjectHeader := mime.QEncoding.Encode("UTF-8", sanitizeEmailHeader(subject))

	// Header writes target an in-memory buffer, whose writes return nil errors.
	// Use protocol CRLF separators here before the transfer encoder owns normalization of HTML body bytes.
	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\n", fromHeader)
	fmt.Fprintf(&message, "To: %s\r\n", toHeader)
	fmt.Fprintf(&message, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&message, "Message-ID: %s\r\n", messageID)
	fmt.Fprintf(&message, "Subject: %s\r\n", subjectHeader)
	fmt.Fprint(&message, "MIME-Version: 1.0\r\n"+
		"Content-Type: text/html; charset=UTF-8\r\n"+
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n")

	// The MIME declaration promises quoted-printable UTF-8 content. Encode the
	// body through that writer and close it before taking buffer bytes so its final buffered output is included.
	bodyWriter := quotedprintable.NewWriter(&message)
	if _, err := bodyWriter.Write([]byte(body)); err != nil {
		return smtpMessage{}, fmt.Errorf("encode email body: %w", err)
	}
	if err := bodyWriter.Close(); err != nil {
		return smtpMessage{}, fmt.Errorf("close email body encoder: %w", err)
	}

	return smtpMessage{
		envelopeFrom: fromAddress.Address,
		envelopeTo:   recipientAddress.Address,
		data:         message.Bytes(),
	}, nil
}

// parseSMTPAddress returns one parsed address after rejecting CR/LF in the supplied text.
// It trims surrounding whitespace and delegates address syntax to net/mail.
// Invalid or empty address parts return an error labeled with the caller's field
// name so message preparation can distinguish sender and recipient failures.
func parseSMTPAddress(value, field string) (*mail.Address, error) {
	if strings.ContainsAny(value, "\r\n") {
		return nil, fmt.Errorf("invalid SMTP %s address: contains a line break", field)
	}

	cleaned := strings.TrimSpace(value)
	address, err := mail.ParseAddress(cleaned)
	if err != nil || strings.TrimSpace(address.Address) == "" {
		if err == nil {
			err = errors.New("address is empty")
		}
		return nil, fmt.Errorf("invalid SMTP %s address: %w", field, err)
	}
	return address, nil
}

// generateEmailMessageID returns a random hexadecimal identifier with a chosen message domain.
// It reads sixteen random bytes, prefers the sender suffix after its last at-sign,
// and otherwise uses the sanitized trimmed SMTP host. Surrounding bracket characters
// are removed; an empty resulting domain selects localhost for the message identifier.
func generateEmailMessageID(fromAddress, smtpHost string) (string, error) {
	// Sixteen random bytes become thirty-two hexadecimal identifier characters.
	randomID := make([]byte, 16)
	if _, err := rand.Read(randomID); err != nil {
		return "", err
	}

	// Message identity prefers the sender's domain over a connection host name.
	// Sanitized host and localhost provide successive display domains when no sender suffix or usable host remains.
	domain := strings.TrimSpace(sanitizeEmailHeader(smtpHost))
	if at := strings.LastIndexByte(fromAddress, '@'); at >= 0 && at < len(fromAddress)-1 {
		domain = fromAddress[at+1:]
	}
	domain = strings.Trim(domain, "[]<>")
	if domain == "" {
		domain = "localhost"
	}

	return fmt.Sprintf("<%s@%s>", hex.EncodeToString(randomID), domain), nil
}
