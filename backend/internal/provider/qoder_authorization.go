package provider

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Qoder device authorization keeps a prepared site flow until its browser proof
// can be completed. Concurrent polls for the same state share one completion
// claim; successful token info remains replayable until the session expires,
// while pending or failed attempts release the claim for a later poll.
//
// The session mutex coordinates claims, cached result publication, and expiry
// cleanup. Preparation and platform completion run outside that mutex with
// tracked operation contexts, and the stored flow retains its creation-time
// identity and proxy. Store cleanup stops before authorization activity is
// cancelled and drained. Callback parsing fills missing proof but stored-flow
// completion does not pass the pasted code to the platform callback, and
// callback code disagreement is not a validation condition.

// QoderOAuthSessionTTL bounds stored device authorization lifetime, while
// QoderOAuthPollInterval advertises the client polling cadence for its pending
// browser flow.
const (
	QoderOAuthSessionTTL   = 10 * time.Minute // authorization session lifetime duration
	QoderOAuthPollInterval = 2                // advertised poll interval in seconds
)

// QoderAuthURLResult carries the browser URL and session proof for device
// authorization.
type QoderAuthURLResult struct {
	AuthURL   string `json:"auth_url"`   // browser device authorization URL
	SessionID string `json:"session_id"` // authorization session lookup ID
	State     string `json:"state"`      // session completion proof value
	ExpiresIn int64  `json:"expires_in"` // seconds
	Interval  int    `json:"interval"`   // seconds
	Site      string `json:"site"`       // selected authorization site identifier
}

// QoderExchangeCodeInput contains stored-session proof and optional pasted
// callback input.
type QoderExchangeCodeInput struct {
	SessionID   string // authorization session lookup ID
	State       string // submitted session proof value
	Code        string // parsed callback code text
	CallbackURL string // optional pasted callback text
	// ProxyID is accepted at completion for client compatibility;
	// completeSession ignores it because the prepared Flow already contains
	// the
	// creation-time proxy.
	ProxyID *int64
}

// QoderTokenInfo represents site token, machine, user, and optional
// organization observations.
type QoderTokenInfo struct {
	// SecurityOauthToken, MachineID, UID, and AID form direct-token
	// identity.
	// ValidateQoderCredentials requires a machine ID and at least one
	// user/account identifier when accepting a security token.
	SecurityOauthToken string `json:"security_oauth_token"`        // issued COSY security token
	RefreshToken       string `json:"refresh_token,omitempty"`     // issued token refresh credential
	MachineID          string `json:"machine_id"`                  // browser flow machine identifier
	MachineToken       string `json:"machine_token,omitempty"`     // site machine authentication token
	MachineType        string `json:"machine_type,omitempty"`      // site machine type label
	UID                string `json:"uid,omitempty"`               // COSY user identity string
	AID                string `json:"aid,omitempty"`               // alternate COSY account identifier
	OrganizationID     string `json:"organization_id,omitempty"`   // selected organization identity string
	OrganizationName   string `json:"organization_name,omitempty"` // selected organization display name
	Name               string `json:"name,omitempty"`              // reported account display name
	UserType           string `json:"user_type,omitempty"`         // reported account entitlement type
	Site               string `json:"site"`                        // selected token exchange site
	RefreshMode        string `json:"refresh_mode"`                // site token refresh route
	ExpiresAt          string `json:"expires_at,omitempty"`        // UTC RFC3339
	// Extra contains optional authorization warnings;
	// BuildProviderCredentials
	// assigns this map directly when nonempty, so callers keep shared
	// nested
	// warning data stable.
	Extra map[string]any `json:"extra,omitempty"`
}

// QoderPollResult reports pending browser authorization or its completed shared
// token result.
type QoderPollResult struct {
	Status string `json:"status"` // pending or completed authorization
	// TokenInfo carries a supplied completed result and can be a cached
	// shared pointer; callers copy mutable values before modifying a
	// replayed
	// result.
	TokenInfo *QoderTokenInfo `json:"token_info,omitempty"`
}

// QoderAuthorizationSession stores one device flow and its completion
// claim/replay state.
type QoderAuthorizationSession[F any] struct {
	// State, Flow, and CreatedAt remain stable after publication so
	// completion
	// reads the preparation-time proof, site identity, and lifetime
	// reference
	// outside its mutable completion-state transitions.
	State string // expected browser session proof
	// Flow retains the site-specific preparation result used by Complete;
	// callers keep its machine, proof, and proxy inputs stable during
	// completion.
	Flow F // prepared site flow value
	// CreatedAt supplies the age checked by Get and BeginCompletion. Future
	// instants remain accepted until their elapsed age exceeds the session
	// lifetime.
	CreatedAt time.Time // authorization lifetime reference timestamp
	// Completing, CompleteCh, and CompletedTokenInfo are updated under the
	// store mutex. One claimant runs Complete, waiters observe CompleteCh,
	// and
	// successful token info supplies later replay.
	Completing bool // completion claim currently held
	// CompleteCh wakes completion waiters when FinishCompletion releases a
	// claim; the store closes it under its mutex before clearing the field.
	CompleteCh chan struct{} // current completion release notification
	// CompletedTokenInfo retains a successful result until session expiry;
	// cached returns share this pointer, so consumers coordinate any
	// subsequent
	// mutation.
	CompletedTokenInfo *QoderTokenInfo // cached successful authorization result
}

// QoderAuthorizationStore retains expiring flow pointers and serializes
// completion claims per session.
type QoderAuthorizationStore[F any] struct {
	runtimeMu sync.Mutex // cleanup lifecycle state lock
	// runtimeStarted, runtimeStopped, and runtimeWG share runtimeMu while
	// cleanup is registered or stopped. Start registers runtimeWG while
	// holding
	// the lock, and Stop permanently closes future startup before waiting.
	runtimeStarted bool           // cleanup worker started once
	runtimeStopped bool           // cleanup startup permanently disabled
	runtimeWG      sync.WaitGroup // actual cleanup worker completion

	mu sync.RWMutex // session and completion state lock
	// sessions owns lookup and completion claims under mu; Set and Get
	// retain/return session pointers, so external access respects the
	// store-owned mutable fields.
	sessions map[string]*QoderAuthorizationSession[F]
	// stopCh is closed once by Stop to end cleanup. Session lookup and
	// completion methods remain callable after this cleanup signal has
	// closed.
	stopCh chan struct{} // cleanup shutdown notification channel
}

// QoderAuthorization combines stored device flows with tracked preparation and
// completion callbacks.
type QoderAuthorization[F any] struct {
	Store *QoderAuthorizationStore[F] // retained authorization session store
	// Prepare supplies a fresh session ID, nonnil session, and public
	// result
	// for the selected site/proxy. It receives the tracked operation
	// context
	// and should honor cancellation.
	Prepare func(context.Context, string, *int64) (string, *QoderAuthorizationSession[F], *QoderAuthURLResult, error)
	// Complete finishes the stored Flow or reports pending. A successful
	// completed outcome supplies nonnil token info, and error/pending
	// outcomes
	// release the claim for another poll.
	Complete func(context.Context, F) (*QoderTokenInfo, bool, error)
	// activity registers URL preparation and completion until they return;
	// StopContext cancels these operations after the store cleanup loop has
	// finished stopping.
	activity operationActivity
}

// NewQoderAuthorizationStore allocates an empty flow map and cleanup stop
// channel without launching a worker. Its owner calls Start to begin expiry
// cleanup and Stop to wait for that worker.
func NewQoderAuthorizationStore[F any]() *QoderAuthorizationStore[F] {
	store := &QoderAuthorizationStore[F]{
		sessions: make(map[string]*QoderAuthorizationSession[F]),
		stopCh:   make(chan struct{}),
	}

	return store
}

// Set stores the supplied nonnil session pointer under its ID, replacing a
// prior entry. Callers keep proof, creation time, and flow inputs stable;
// completion fields belong to store methods under the session mutex.
func (s *QoderAuthorizationStore[F]) Set(sessionID string, session *QoderAuthorizationSession[F]) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = session
}

// Get returns the stored session pointer when its age is no greater than the
// ten-minute lifetime. Missing or older entries return false without deletion;
// callers coordinate access to mutable completion fields with the store.
func (s *QoderAuthorizationStore[F]) Get(sessionID string) (*QoderAuthorizationSession[F], bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, false
	}
	// The lifetime also bounds replayed authorization results.
	if time.Since(session.CreatedAt) > QoderOAuthSessionTTL {
		return nil, false
	}
	return session, true
}

// BeginCompletion validates a trimmed session ID and state, then returns cached
// token info, a waiter channel, or an exclusive completion claim. Callers
// receiving a claim invoke FinishCompletion on success, error, or pending
// outcomes. Returned pointers stay shared with the store.
//
// The session mutex protects expiry removal, cached-result precedence, and
// claimant publication. Completed results are replayed while the session
// remains valid; a concurrent claimant gets the existing notification channel
// instead of starting another exchange. Expired entries are removed, and a
// missing or mismatched proof returns an error before completion can be
// claimed.
func (s *QoderAuthorizationStore[F]) BeginCompletion(sessionID, state string) (*QoderAuthorizationSession[F], *QoderTokenInfo, <-chan struct{}, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil, nil, errors.New("qoder oauth session_id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	// Validate live session age before cached replay or claim; remove an
	// expired flow so it cannot authorize a later poll.
	if !ok || time.Since(session.CreatedAt) > QoderOAuthSessionTTL {
		if ok {
			delete(s.sessions, sessionID)
		}
		return nil, nil, nil, errors.New("qoder oauth session not found or expired")
	}
	if strings.TrimSpace(state) == "" || strings.TrimSpace(state) != session.State {
		return nil, nil, nil, errors.New("qoder oauth state is invalid")
	}
	if session.CompletedTokenInfo != nil {
		return session, session.CompletedTokenInfo, nil, nil
	}
	// An existing claim can come from stored fixture state with no channel;
	// create its notification channel so later waiters can observe release.
	if session.Completing {
		if session.CompleteCh == nil {
			session.CompleteCh = make(chan struct{})
		}
		return nil, nil, session.CompleteCh, nil
	}
	// Claim completion under the session mutex before invoking the platform
	// callback, so concurrent polls can wait for its result and release
	// notification.
	session.Completing = true
	session.CompleteCh = make(chan struct{})
	return session, nil, nil, nil
}

// FinishCompletion releases the current session's claim and wakes its waiters,
// retaining tokenInfo when nonnil. Nil leaves any existing cached result
// unchanged; a missing session ID returns without work. Callers pass the ID
// received with their claim.
//
// Cached publication and Completing reset occur under the mutex before the
// channel closes, so awakened polls can recheck the completed result or claim
// another attempt. The method looks up by ID at finish time and does not
// compare the claimed pointer against a replacement session; owners keep IDs
// unique for outstanding flows.
func (s *QoderAuthorizationStore[F]) FinishCompletion(sessionID string, tokenInfo *QoderTokenInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[strings.TrimSpace(sessionID)]
	if !ok {
		return
	}
	if tokenInfo != nil {
		session.CompletedTokenInfo = tokenInfo
	}
	session.Completing = false
	if session.CompleteCh != nil {
		// Wake waiters after cached token state and claim release are
		// visible
		// under the mutex; they recheck the session before claiming
		// another
		// attempt.
		close(session.CompleteCh)
		session.CompleteCh = nil
	}
}

// Start registers one session-cleanup worker unless this store has already
// started or permanently stopped. Callers construct the store before use;
// repeated starts return without launching additional expiry workers.
//
// The lifecycle mutex covers startup flags and runtimeWG registration before
// the worker is launched. Stop uses the same mutex before waiting, so it
// either prevents admission or includes that worker in its completion barrier.
// Stored-session operations use their separate session mutex throughout this
// cleanup lifecycle.
func (s *QoderAuthorizationStore[F]) Start() {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeStarted || s.runtimeStopped {
		return
	}
	s.runtimeStarted = true
	// Register cleanup while startup and stop share the lifecycle lock,
	// preventing Stop from waiting before this worker has been counted.
	s.runtimeWG.Go(func() { ; s.cleanup() })
}

// Stop permanently prevents cleanup startup, signals any registered worker
// once, and waits for its actual exit. Calls before Start and repeated calls
// are accepted for a constructed store.
//
// The lifecycle mutex protects the stopped flag and single channel closure.
// Waiting occurs after that mutex is released, allowing a registered worker
// to finish its session-map access and return. Session methods use the separate
// store mutex and remain callable after cleanup stops; this barrier drains the
// cleanup worker and leaves platform authorization activity to its own owner.
func (s *QoderAuthorizationStore[F]) Stop() {
	s.runtimeMu.Lock()
	if !s.runtimeStopped {
		s.runtimeStopped = true
		// Close the cleanup signal once while holding lifecycle state;
		// subsequent stops join the same registered worker without
		// closing its
		// channel again.
		close(s.stopCh)
	}
	s.runtimeMu.Unlock()
	s.runtimeWG.Wait()
}

// cleanup removes sessions older than the authorization lifetime on one-minute
// ticks until stopCh closes. Expiry deletion holds the session mutex, so
// completion claims and cleanup observe serialized map changes.
func (s *QoderAuthorizationStore[F]) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			// Expired flows can disappear while platform completion
			// is running;
			// its later FinishCompletion then treats the removed ID
			// as missing
			// instead of caching that late result.
			s.mu.Lock()
			for id, session := range s.sessions {
				if time.Since(session.CreatedAt) > QoderOAuthSessionTTL {
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
		}
	}
}

// GenerateAuthURLForSite registers preparation with shutdown tracking, calls
// Prepare for the requested site/proxy, and stores the resulting flow. Callback
// errors stop publication; callbacks that ignore cancellation can still return
// a session for the final Set.
func (s *QoderAuthorization[F]) GenerateAuthURLForSite(ctx context.Context, site string, proxyID *int64) (*QoderAuthURLResult, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("qoder authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	id, session, result, err := s.Prepare(operation, site, proxyID)
	if err != nil {
		return nil, err
	}
	// Publish the prepared session after the callback returns;
	// cancellation-sensitive preparation is responsible for preventing a
	// late
	// successful flow from reaching this store.
	s.Store.Set(id, session)
	return result, nil
}

// Start launches the retained store cleanup worker through its one-start
// lifecycle. Repeated calls and calls after store shutdown return with no new
// worker; authorization exchanges register separately when requested.
func (s *QoderAuthorization[F]) Start() { s.Store.Start() }

// StopContext first stops and waits for session cleanup, then cancels and waits
// for tracked preparation/completion within ctx. The activity tracker retains
// its first result; the earlier store wait itself has no caller timeout.
func (s *QoderAuthorization[F]) StopContext(ctx context.Context) error {
	s.Store.Stop()
	return s.activity.stop(ctx, "qoder authorization")
}

// ExchangeCode normalizes callback proof in the supplied input and completes
// its stored flow. Nil input or mismatched callback state is rejected; pending
// completion is returned as an error, and parsed code is retained in input but
// not passed to Complete.
func (s *QoderAuthorization[F]) ExchangeCode(ctx context.Context, input *QoderExchangeCodeInput) (*QoderTokenInfo, error) {
	if input == nil {
		return nil, errors.New("qoder oauth input is required")
	}
	if err := normalizeQoderExchangeInput(input); err != nil {
		return nil, err
	}
	tokenInfo, pending, err := s.completeSession(ctx, input.SessionID, input.State, input.ProxyID)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, errors.New("qoder authorization is still pending; finish authorization in the browser and try again")
	}
	return tokenInfo, nil
}

// Poll returns pending or completed state for a valid stored-session proof. It
// can wait for another claimant with caller cancellation, and completed
// TokenInfo may be replayed from the store; proxyID is accepted but ignored by
// completion.
func (s *QoderAuthorization[F]) Poll(ctx context.Context, sessionID, state string, proxyID *int64) (*QoderPollResult, error) {
	tokenInfo, pending, err := s.completeSession(ctx, sessionID, state, proxyID)
	if err != nil {
		return nil, err
	}
	if pending {
		return &QoderPollResult{Status: "pending"}, nil
	}
	return &QoderPollResult{
		Status:    "completed",
		TokenInfo: tokenInfo,
	}, nil
}

// completeSession runs one tracked completion or waits for another claimant's
// cached result. The supplied state must match a live session, and the stored
// Flow supplies preparation-time proxy and identity. Caller or shutdown
// cancellation can end a waiting operation.
//
// BeginCompletion chooses replay, wait, or a claim under the store lock.
// Waiters reenter that decision when notified. The claimant invokes Complete
// outside the lock and always calls FinishCompletion for returned success,
// pending, or error outcomes. Pending/error outcomes wake another attempt
// without caching success. When the session remains stored at completion,
// successful pointers are shared for replay until expiry; removal during the
// callback leaves its returned result outside the store.
func (s *QoderAuthorization[F]) completeSession(ctx context.Context, sessionID, state string, proxyID *int64) (*QoderTokenInfo, bool, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("qoder authorization is stopped"))
	if err != nil {
		return nil, false, err
	}
	defer done()
	ctx = operation
	// Complete the stored Flow with its preparation-time proxy so later
	// poll
	// arguments cannot redirect this authorization exchange to another
	// proxy.
	_ = proxyID
	for {
		session, cachedTokenInfo, waitCh, err := s.Store.BeginCompletion(sessionID, state)
		if err != nil {
			return nil, false, err
		}
		if cachedTokenInfo != nil {
			return cachedTokenInfo, false, nil
		}
		// An existing claimant owns the exchange; wait for its release
		// or this
		// operation's cancellation, then reread cached/pending state
		// before
		// another claim.
		if waitCh != nil {
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			case <-waitCh:
				continue
			}
		}
		tokenInfo, pending, err := s.Complete(ctx, session.Flow)
		if err != nil || pending {
			// A pending or failed exchange leaves no cached
			// success; release
			// and wake waiters so a later poll can attempt this
			// stored flow
			// again.
			s.Store.FinishCompletion(sessionID, nil)
			return nil, pending, err
		}
		s.Store.FinishCompletion(sessionID, tokenInfo)
		return tokenInfo, false, nil
	}
}

// normalizeQoderExchangeInput fills missing state/code in the caller's input
// from its pasted callback. Nil input returns an error, and nonblank request
// values remain as supplied. A callback state that conflicts with the trimmed
// request state is rejected.
//
// ParseQoderCallback accepts URL, fragment, query, and bare-code forms, so
// filling proof before comparison keeps those representations tied to the
// submitted state. Callback code disagreement is not checked here. This
// function mutates input even when a later state comparison fails, and
// ExchangeCode uses the normalized state to select the stored flow.
func normalizeQoderExchangeInput(input *QoderExchangeCodeInput) error {
	if input == nil {
		return errors.New("qoder oauth input is required")
	}
	// Parse the callback once so missing state and code come from the same
	// representation; compare the resulting state after filling those
	// missing
	// values, which remain populated if that comparison fails.
	callbackState, callbackCode := ParseQoderCallback(input.CallbackURL)
	if strings.TrimSpace(input.State) == "" && callbackState != "" {
		input.State = callbackState
	}
	if strings.TrimSpace(input.Code) == "" && callbackCode != "" {
		input.Code = callbackCode
	}
	if callbackState != "" && strings.TrimSpace(input.State) != "" && callbackState != strings.TrimSpace(input.State) {
		return errors.New("qoder oauth callback state does not match request state")
	}
	return nil
}

// ParseQoderCallback extracts trimmed state/code from URL query or fragment
// fields, then accepts query text or a bare code when neither field was found.
// Blank input returns two empty strings. Query values win over matching
// fragment keys; valid fragment values fill missing query values.
//
// Malformed fragment parsing leaves URL query data available. A query-looking
// path is retried as a raw key/value string, while remaining text becomes a
// code with no state. Callers still validate stored-session proof separately,
// and normalizeQoderExchangeInput rejects callback state disagreement before
// completion can use that flow.
func ParseQoderCallback(raw string) (state string, code string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	// A malformed URL can still be a pasted code or query; retain its raw
	// text
	// for the following compatibility parsing paths.
	u, err := url.Parse(raw)
	if err == nil && u != nil {
		values := u.Query()
		// Query state/code take precedence over fragment values; merge
		// missing
		// values only so a pasted callback can carry either browser
		// representation.
		if u.Fragment != "" {
			// Invalid fragment syntax leaves query proof available;
			// discarding
			// that optional parse failure retains the independently
			// parsed URL
			// query values.
			if fragmentValues, fragmentErr := url.ParseQuery(u.Fragment); fragmentErr == nil {
				for key, vals := range fragmentValues {
					if len(vals) > 0 && values.Get(key) == "" {
						values.Set(key, vals[0])
					}
				}
			}
		}
		state = strings.TrimSpace(values.Get("state"))
		code = strings.TrimSpace(values.Get("code"))
		if state != "" || code != "" {
			return state, code
		}
	}
	// A pasted query string may have parsed as an ordinary URL path above;
	// retry it as key/value input before accepting a bare code.
	if strings.Contains(raw, "=") {
		values, err := url.ParseQuery(strings.TrimPrefix(raw, "?"))
		if err == nil {
			return strings.TrimSpace(values.Get("state")), strings.TrimSpace(values.Get("code"))
		}
	}
	// Keep unmatched callback text as the code input for compatibility;
	// state
	// remains absent and completion still requires a matching stored
	// session
	// proof.
	return "", raw
}

// BuildProviderCredentials returns nonempty token and identity fields as a
// fresh credential map. Nil tokenInfo produces an empty map, and a nonempty
// Extra map is assigned directly under extra; callers keep those nested warning
// values stable while sharing them.
func (s *QoderAuthorization[F]) BuildProviderCredentials(tokenInfo *QoderTokenInfo) map[string]any {
	credentials := map[string]any{}
	if tokenInfo == nil {
		return credentials
	}
	if tokenInfo.SecurityOauthToken != "" {
		credentials["security_oauth_token"] = tokenInfo.SecurityOauthToken
	}
	if tokenInfo.RefreshToken != "" {
		credentials["refresh_token"] = tokenInfo.RefreshToken
	}
	if tokenInfo.MachineID != "" {
		credentials["machine_id"] = tokenInfo.MachineID
	}
	if tokenInfo.MachineToken != "" {
		credentials["machine_token"] = tokenInfo.MachineToken
	}
	if tokenInfo.MachineType != "" {
		credentials["machine_type"] = tokenInfo.MachineType
	}
	if tokenInfo.UID != "" {
		credentials["uid"] = tokenInfo.UID
	}
	if tokenInfo.AID != "" {
		credentials["aid"] = tokenInfo.AID
	}
	if tokenInfo.OrganizationID != "" {
		credentials["organization_id"] = tokenInfo.OrganizationID
	}
	if tokenInfo.OrganizationName != "" {
		credentials["organization_name"] = tokenInfo.OrganizationName
	}
	if tokenInfo.Name != "" {
		credentials["name"] = tokenInfo.Name
	}
	if tokenInfo.UserType != "" {
		credentials["user_type"] = tokenInfo.UserType
	}
	if tokenInfo.Site != "" {
		credentials["site"] = tokenInfo.Site
	}
	if tokenInfo.RefreshMode != "" {
		credentials["refresh_mode"] = tokenInfo.RefreshMode
	}
	if tokenInfo.ExpiresAt != "" {
		credentials["expires_at"] = tokenInfo.ExpiresAt
	}
	if len(tokenInfo.Extra) > 0 {
		credentials["extra"] = tokenInfo.Extra
	}
	return credentials
}
