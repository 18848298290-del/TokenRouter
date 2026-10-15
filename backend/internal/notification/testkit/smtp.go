package testkit

import (
	"bufio"
	"context"
	"io"
	"maps"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// smtp.go provides in-memory settings and a capturing SMTP server for
// notification tests.
// Notification tests need settings that can serve concurrent marker and secret
// operations without a database, plus a local SMTP peer that exposes received
// MIME content. MemorySettings keeps its string map under one mutex and returns
// new maps or record values for callers to inspect independently.
//
// SMTPServer handles accepted connections serially, records completed DATA under
// a capture mutex, and publishes receipt count through an atomic integer. The
// body list and counter are separate observations. Cleanup closes its listener
// and waits for the serve loop; an active connection still needs its client to
// complete or close before that waiting fixture can return.

// MemorySettings implements notification persistence with locked strings and detached read results.
type MemorySettings struct {
	// mu protects values for all fixture operations, including whole-map copies.
	// Missing requested keys stay absent in batch reads so callers can exercise
	// stored absence separately from a configured empty string.
	mu     sync.RWMutex      // stored value lock
	values map[string]string // fixture setting strings
}

// SMTPServer records accepted fixture DATA and serves loopback authentication/envelope commands for tests.
type SMTPServer struct {
	// listener and wg own the accept loop's lifetime; fixture cleanup closes
	// admission and waits for its current synchronous connection handler to return.
	listener net.Listener   // loopback SMTP listener
	wg       sync.WaitGroup // registered serve loop
	messages atomic.Int64   // completed DATA count
	// messageMu protects messageBodies, which is appended before messages is
	// incremented. Those methods expose separate captured-content and count observations.
	messageMu     sync.Mutex // captured content lock
	messageBodies []string   // received MIME messages
}

// NewMemorySettings returns a writable empty notification settings fixture.
// Its methods lock the map and return detached record/map values. Fixture contexts
// are accepted by the repository interface but do not cancel these in-memory operations.
func NewMemorySettings() *MemorySettings {
	return &MemorySettings{values: make(map[string]string)}
}

// Get returns a new setting record for key or the shared not-found error.
// It copies string values under the read lock and ignores the context, allowing
// tests to distinguish absent keys from stored empty strings without database lifetime rules.
func (r *MemorySettings) Get(_ context.Context, key string) (*settingscore.Setting, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.values[key]
	if !ok {
		return nil, settingscore.ErrSettingNotFound
	}
	return &settingscore.Setting{Key: key, Value: value}, nil
}

// GetValue returns one fixture setting string through the same lookup used by Get.
// An absent key returns the shared not-found error. The context passes through
// the repository interface, whose in-memory record lookup ignores cancellation.
func (r *MemorySettings) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

// Set stores one fixture key and string value under the settings write lock.
// The context is ignored, so cancellation cannot prevent this in-memory write.
// The fixture returns nil after replacing the value.
func (r *MemorySettings) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}

// GetMultiple returns a new map containing the supplied keys that exist in fixture storage.
// Missing keys remain absent. The read lock covers the whole copy, while the
// context is ignored; callers can edit the returned map independently of stored string values.
func (r *MemorySettings) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

// SetMultiple copies supplied string entries into the fixture under one write lock.
// Existing keys not supplied remain present. Callers keep the input map stable
// during this synchronous copy; context cancellation is not modeled by the in-memory fixture.
func (r *MemorySettings) SetMultiple(_ context.Context, settings map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	maps.Copy(r.values, settings)
	return nil
}

// GetAll returns a separately allocated snapshot of every fixture setting string.
// The read lock covers the whole map copy, so concurrent fixture writes cannot
// change this returned snapshot's values after the lock is released.
func (r *MemorySettings) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.values))
	maps.Copy(out, r.values)
	return out, nil
}

// Delete removes an existing key under the fixture write lock or returns not-found.
// A missing key is reported through the shared repository error. The in-memory
// fixture ignores the context and performs no tombstone or external persistence work.
func (r *MemorySettings) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.values[key]; !ok {
		return settingscore.ErrSettingNotFound
	}
	delete(r.values, key)
	return nil
}

// StartSMTPServer launches a loopback SMTP fixture and registers its cleanup with the test.
// It exposes one accept-loop registration before returning. Cleanup closes the
// listener and waits; clients must finish or close an active socket because this
// fixture does not install an I/O deadline for its synchronous connection handler.
func StartSMTPServer(t *testing.T) *SMTPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	server := &SMTPServer{listener: listener}
	// Register the serve lifetime before launching and returning the fixture,
	// so test cleanup's wait includes the accept loop and its current synchronous session.
	server.wg.Add(1)
	go server.serve()
	t.Cleanup(server.close)
	return server
}

// Settings returns a fresh stored-settings map targeting this fixture's bound address.
// It supplies fixed account, sender, and plaintext mode values for local SMTP
// calls. Host/port splitting errors are discarded for the listener's known TCP address representation.
func (s *SMTPServer) Settings() map[string]string {
	// A TCP listener formats its bound host and port for this fixture map.
	// Ignore split failure here; the returned strings become the caller's SMTP endpoint input.
	host, port, _ := net.SplitHostPort(s.listener.Addr().String())
	return map[string]string{
		"smtp_host":      host,
		"smtp_port":      port,
		"smtp_username":  "user",
		"smtp_password":  "password",
		"smtp_from":      "noreply@example.com",
		"smtp_from_name": "TokenRouter",
		"smtp_use_tls":   "false",
	}
}

// MessageCount returns the atomic count of completed fixture DATA messages.
// Receipt is counted before the acceptance reply, so this value alone cannot
// establish client success. Captured bodies are observed through a separate lock.
func (s *SMTPServer) MessageCount() int64 {
	return s.messages.Load()
}

// LastMessage returns the most recently captured MIME string or empty when none exists.
// It holds the capture mutex while selecting that value. MessageCount is observed
// separately, so callers synchronize with send completion when asserting both body and count.
func (s *SMTPServer) LastMessage() string {
	s.messageMu.Lock()
	defer s.messageMu.Unlock()
	if len(s.messageBodies) == 0 {
		return ""
	}
	return s.messageBodies[len(s.messageBodies)-1]
}

// LastMessageBody parses the latest captured MIME message and returns its decoded body.
// Quoted-printable content is decoded when declared; other content is read directly.
// Parsing or body-read failures fail the owning test, so callers obtain usable
// body text after arranging a completed message capture.
func (s *SMTPServer) LastMessageBody(t *testing.T) string {
	t.Helper()

	message, err := mail.ReadMessage(strings.NewReader(s.LastMessage()))
	require.NoError(t, err)

	bodyReader := message.Body
	if strings.EqualFold(message.Header.Get("Content-Transfer-Encoding"), "quoted-printable") {
		bodyReader = quotedprintable.NewReader(message.Body)
	}
	body, err := io.ReadAll(bodyReader)
	require.NoError(t, err)
	return string(body)
}

// close ends listener admission and waits for the registered serve loop to finish.
// Listener close errors are ignored. The serve loop handles a current connection
// synchronously, so the client must finish or close that connection before fixture
// cleanup can complete its WaitGroup wait.
func (s *SMTPServer) close() {
	// End admission before waiting for the serve registration to return.
	// A current client session still owns its protocol lifetime, while listener cleanup errors are discarded.
	_ = s.listener.Close()
	s.wg.Wait()
}

// serve accepts one connection at a time until its listener reports an error.
// It completes its registered lifetime on return and delegates each accepted
// socket to handleConn synchronously, keeping cleanup waiting through that current
// session rather than spawning separate untracked connection goroutines.
func (s *SMTPServer) serve() {
	// Return the fixture's serve registration after admission or current-session
	// handling ends, allowing test cleanup to keep the listener dependency alive through that work.
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.handleConn(conn)
	}
}

// handleConn records one fixture message after consuming DATA to its dot terminator.
// It sends deterministic greeting, authentication, envelope, and acceptance responses,
// then returns on read/write failure or QUIT. The method owns and closes its socket.
//
// Captured MIME text is appended under messageMu before the independent receipt
// counter increments and the acceptance reply is attempted. Receipt therefore
// records completed DATA bytes even if the final reply fails. Tests wait for the
// client outcome when they need to relate capture with acknowledgement; authentication
// here accepts fixture input rather than checking account secrets.
func (s *SMTPServer) handleConn(conn net.Conn) {
	// This synchronous handler owns the accepted socket. Close on all protocol
	// exits, ignoring cleanup failure so the fixture serve loop can proceed to its next accept.
	defer func() { _ = conn.Close() }()
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	// Reply failure ends the current fixture branch through its boolean result.
	// The owning handler then releases the socket so the synchronous serve loop can leave that session.
	writeLine := func(line string) bool {
		if _, err := rw.WriteString(line + "\r\n"); err != nil {
			return false
		}
		return rw.Flush() == nil
	}
	if !writeLine("220 localhost ESMTP") {
		return
	}
	for {
		line, err := rw.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimRight(line, "\r\n"))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			if _, err := rw.WriteString("250-localhost\r\n250 AUTH PLAIN\r\n"); err != nil {
				return
			}
			if err := rw.Flush(); err != nil {
				return
			}
		case strings.HasPrefix(cmd, "AUTH"):
			if !writeLine("235 2.7.0 Authentication successful") {
				return
			}
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			if !writeLine("250 2.1.0 OK") {
				return
			}
		case strings.HasPrefix(cmd, "RCPT TO:"):
			if !writeLine("250 2.1.5 OK") {
				return
			}
		case strings.HasPrefix(cmd, "DATA"):
			if !writeLine("354 End data with <CR><LF>.<CR><LF>") {
				return
			}
			// Collect DATA bytes before reporting receipt. Builder writes target memory
			// and return nil errors; their results are discarded while protocol reads still determine completion.
			var message strings.Builder
			for {
				dataLine, err := rw.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
				_, _ = message.WriteString(dataLine)
			}
			// Publish the complete MIME string under the capture lock, then increment
			// the independent receipt counter before attempting acceptance so tests can inspect bytes even if that reply fails.
			s.messageMu.Lock()
			s.messageBodies = append(s.messageBodies, message.String())
			s.messageMu.Unlock()
			s.messages.Add(1)
			if !writeLine("250 2.0.0 OK") {
				return
			}
		case strings.HasPrefix(cmd, "QUIT"):
			// QUIT selects fixture-session completion. Attempt the final reply once,
			// discard its failure, and let deferred socket cleanup release the accepted connection.
			_ = writeLine("221 2.0.0 Bye")
			return
		default:
			if !writeLine("250 OK") {
				return
			}
		}
	}
}
