package messageforward

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// gatewayTransportErrorTempUnschedDuration fixes the expected
// persistent-failure cooldown window used by the transport health assertions
// for this test fixture.
const gatewayTransportErrorTempUnschedDuration = 10 * time.Minute

// gatewayTransportFailoverBody fixes the client-safe Anthropic payload expected
// when a transport failure requests another provider attempt.

var gatewayTransportFailoverBody = []byte(`{"type":"error","error":{"type":"upstream_error","message":"Upstream request failed"}}`)

// transportTempUnschedRepoStub records transport cooldown writes for the
// selected test provider.
type transportTempUnschedRepoStub struct {
	// ExecutionProviderStore embeds unused repository methods; transport
	// fixtures implement the exclusion write and expose unexpected
	// repository operations through nil dispatch.
	gatewayprovider.ExecutionProviderStore // unused provider repository methods

	calls      int       // recorded exclusion write count
	lastID     int64     // last excluded provider identifier
	lastUntil  time.Time // last cooldown expiration time
	lastReason string    // last exclusion diagnostic text
}

// SetTempUnschedulable records one exclusion write and its provider, deadline,
// and reason values.
func (r *transportTempUnschedRepoStub) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.calls++
	r.lastID = id
	r.lastUntil = until
	r.lastReason = reason
	return nil
}

// newTransportErrorFixture creates a recording Messages boundary with an
// inbound request.
func newTransportErrorFixture(t *testing.T) *privateHTTPBoundary {
	t.Helper()

	c, _ := newPrivateHTTPFixture(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c
}

// TestHandleUpstreamTransportError_TransientFailsOverWithoutEviction supplies
// transient EOF before response output with a persistence recorder. The result
// requests another provider using the safe body, and no exclusion write or
// client output occurs.
func TestHandleUpstreamTransportError_TransientFailsOverWithoutEviction(t *testing.T) {
	repo := &transportTempUnschedRepoStub{}
	s := NewRuntime(Dependencies{ProviderState: repo}, Options{})
	c := newTransportErrorFixture(t)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 149, Name: "acc", Platform: capability.PlatformAnthropic}}

	err := s.transportError(context.Background(), c, provider,
		errors.New(`Post "http://upstream/v1/messages?beta=true": EOF`), forwardcore.Notice{})

	var failoverErr *forwardcore.UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("expected *UpstreamFailoverError, got %T: %v", err, err)
	}
	if failoverErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("StatusCode = %d, want 502", failoverErr.StatusCode)
	}
	if string(failoverErr.ResponseBody) != string(gatewayTransportFailoverBody) {
		t.Fatalf("ResponseBody = %s, want legacy 502 body", failoverErr.ResponseBody)
	}
	if !failoverErr.ShouldRetryNextProvider() {
		t.Fatal("transient transport error must allow retrying the next provider")
	}
	if repo.calls != 0 {
		t.Fatalf("SetTempUnschedulable called %d times for a transient error, want 0", repo.calls)
	}
	if c.Writer.Written() {
		t.Fatal("handler owns the response; service must not write on transport failover")
	}
}

// TestHandleUpstreamTransportError_PersistentEvictsProvider supplies connection
// refusal before output with a persistence recorder. Failover is returned and
// one exclusion write records the provider, expected cooldown window, and
// transport diagnostic prefix.
func TestHandleUpstreamTransportError_PersistentEvictsProvider(t *testing.T) {
	repo := &transportTempUnschedRepoStub{}
	s := NewRuntime(Dependencies{ProviderState: repo}, Options{})
	c := newTransportErrorFixture(t)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 149, Name: "acc", Platform: capability.PlatformAnthropic}}

	before := time.Now()
	err := s.transportError(context.Background(), c, provider,
		errors.New(`dial tcp 1.2.3.4:443: connect: connection refused`), forwardcore.Notice{})

	if _, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); !ok {
		t.Fatalf("expected *UpstreamFailoverError, got %T: %v", err, err)
	}
	if repo.calls != 1 {
		t.Fatalf("SetTempUnschedulable called %d times for a persistent error, want 1", repo.calls)
	}
	if repo.lastID != provider.Record.ID {
		t.Fatalf("unscheduled provider = %d, want %d", repo.lastID, provider.Record.ID)
	}
	wantUntil := before.Add(gatewayTransportErrorTempUnschedDuration)
	if repo.lastUntil.Before(wantUntil.Add(-time.Minute)) || repo.lastUntil.After(wantUntil.Add(time.Minute)) {
		t.Fatalf("until = %v, want ~%v", repo.lastUntil, wantUntil)
	}
	if !strings.HasPrefix(repo.lastReason, "upstream transport error (proxy/network): ") {
		t.Fatalf("reason = %q, want transport-error prefix", repo.lastReason)
	}
}

// TestHandleUpstreamTransportError_ClientCanceledNoFailover supplies
// context.Canceled to transport handling with an available persistence adapter.
// The cancellation error remains inspectable and no provider exclusion or typed
// failover signal is produced.
func TestHandleUpstreamTransportError_ClientCanceledNoFailover(t *testing.T) {
	repo := &transportTempUnschedRepoStub{}
	s := NewRuntime(Dependencies{ProviderState: repo}, Options{})
	c := newTransportErrorFixture(t)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 149, Name: "acc", Platform: capability.PlatformAnthropic}}

	inErr := context.Canceled
	err := s.transportError(context.Background(), c, provider, inErr, forwardcore.Notice{})

	if _, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); ok {
		t.Fatal("canceled client must not fail over to another provider")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled passthrough", err)
	}
	if repo.calls != 0 {
		t.Fatalf("SetTempUnschedulable called %d times on client cancel, want 0", repo.calls)
	}
}

// TestHandleUpstreamTransportError_UpstreamDeadlineStillFailsOver supplies a
// deadline error while the request context remains live. The result is typed
// failover and the recorder receives no persistent transport exclusion write.
func TestHandleUpstreamTransportError_UpstreamDeadlineStillFailsOver(t *testing.T) {
	repo := &transportTempUnschedRepoStub{}
	s := NewRuntime(Dependencies{ProviderState: repo}, Options{})
	c := newTransportErrorFixture(t)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 149, Name: "acc", Platform: capability.PlatformAnthropic}}

	err := s.transportError(context.Background(), c, provider,
		context.DeadlineExceeded, forwardcore.Notice{})

	if _, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); !ok {
		t.Fatalf("upstream deadline with live request context must fail over, got %T: %v", err, err)
	}
	if repo.calls != 0 {
		t.Fatalf("SetTempUnschedulable called %d times for upstream deadline, want 0", repo.calls)
	}
}
