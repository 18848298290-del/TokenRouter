package session

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// Messages converted to Responses can retain a response ID and opaque turn state
// for their provider, API Key, and prompt-cache identity. Continuation can also be
// disabled after an unsupported upstream response. All three values occupy one
// binding with one deadline, so a write refreshes the lifetime of the other
// retained values as well. Disabled continuation still permits turn-state lookup.
//
// Reads and updates use separate concurrent map operations. Read-modify-write
// updates can lose another writer's change, and cleanup can delete a replacement
// installed after the loaded value was read. Updates also carry existing fields
// without testing their prior deadline. Merged same-session state requires caller coordination;
// this process-local store performs no background expiration sweep.

// CompatResponses holds process-local response continuity for provider/key/prompt identities.
// Individual map operations support concurrent access; callers coordinate updates
// for one key because merging and publication are separate operations.
type CompatResponses struct {
	bindings sync.Map // key to shared continuity binding
	// TTL supplies each write's shared lifetime; nil selects one hour and
	// concurrent writers require a callback safe for their overlapping calls.
	TTL func() time.Duration
}

// compatResponseBinding holds response continuity values under one shared validity deadline.
type compatResponseBinding struct {
	// ResponseID, TurnState, and ContinuationDisabled share ExpiresAt, so updating
	// one field renews every retained value; the two identity strings are stored trimmed.
	//
	// ResponseID names the continuation target. CompatResponses.Response removes
	// an enabled binding when this ID is blank, discarding its retained TurnState too.
	ResponseID string // most recently bound response ID
	// TurnState carries opaque upstream state. CompatResponses.TurnState retains
	// a blank-state binding, and ContinuationDisabled does not suppress that
	// method's nonblank-state lookup.
	TurnState string // opaque upstream turn state
	// ContinuationDisabled suppresses response-ID lookup and later response binding;
	// retained turn state remains available through its independent lookup method.
	ContinuationDisabled bool
	// ExpiresAt accepts the exact deadline and expires strictly afterward;
	// a zero value keeps this loaded binding unbounded during reads.
	ExpiresAt time.Time
}

// ttl returns the configured continuation lifetime or a one-hour default.
// Nil receivers and absent callbacks select the default; callback durations pass
// through unchanged, including zero or negative values.
func (s *CompatResponses) ttl() time.Duration {
	if s != nil && s.TTL != nil {
		return s.TTL()
	}
	return time.Hour
}

// CompatResponseKey joins provider ID, API Key ID, and trimmed prompt identity with
// NUL separators. A whitespace-only prompt returns empty; numeric IDs are included
// as supplied, including nonpositive values. The key separates same-prompt sessions
// sent through different provider/key pairs.
//
// Callers use this same key for response IDs, turn state, and disabled flags.
// Decimal IDs precede the prompt, so delimiters keep their components distinct
// when the prompt contains ordinary punctuation. The function constructs a storage
// identity; it neither validates authenticated ownership nor hashes the prompt.
func CompatResponseKey(providerID, apiKeyID int64, prompt string) string {
	key := strings.TrimSpace(prompt)
	if key == "" {
		return ""
	}
	return strings.Join([]string{strconv.FormatInt(providerID, 10), strconv.FormatInt(apiKeyID, 10), key}, "\x00")
}

// Response returns the trimmed response ID for a live, enabled binding, or empty
// when unavailable. Nil stores, empty keys, and missing records return empty.
// Malformed or expired values are removed; the exact deadline remains valid and
// a zero deadline is unbounded.
//
// A disabled binding is retained and returns empty. A blank response ID causes
// the entire binding to be deleted, including any retained turn state, so this
// read can retire a turn-only record. Load and Delete are separate operations;
// cleanup can remove a concurrently replaced binding. Callers coordinate access
// when another update for the same session may be in progress.
func (s *CompatResponses) Response(key string) string {
	if s == nil {
		return ""
	}
	if key == "" {
		return ""
	}
	raw, ok := s.bindings.Load(key)
	if !ok {
		return ""
	}
	binding, ok := raw.(compatResponseBinding)
	if !ok {
		// Malformed values provide no usable response fields. Remove the whole
		// binding so later reads see absence rather than retaining undecodable continuity.
		s.bindings.Delete(key)
		return ""
	}
	if !binding.ExpiresAt.IsZero() && time.Now().After(binding.ExpiresAt) {
		// All fields share this expired deadline. Remove the binding so later
		// response or turn-state lookup cannot reuse this loaded expired continuity.
		s.bindings.Delete(key)
		return ""
	}
	if binding.ContinuationDisabled {
		return ""
	}
	if strings.TrimSpace(binding.ResponseID) == "" {
		// A turn-only enabled binding supplies no response continuation ID. This
		// cleanup removes its retained turn state too, requiring subsequent output to bind state again.
		s.bindings.Delete(key)
		return ""
	}
	return strings.TrimSpace(binding.ResponseID)
}

// BindResponse installs a trimmed response ID and refreshes the binding lifetime.
// Nil stores, empty keys, and blank IDs are ignored. A loaded existing turn state
// is retained even if that entry's previous deadline has passed.
//
// If the loaded binding has continuation disabled, the response ID remains empty
// and the existing fields receive a new deadline. This keeps the upstream
// unsupported-continuation decision across later successful responses. The load,
// merge, and store are separate operations, so callers coordinate same-key writers
// to prevent one update from overwriting another's fields.
func (s *CompatResponses) BindResponse(key, responseID string) {
	if s == nil {
		return
	}
	id := strings.TrimSpace(responseID)
	if key == "" || id == "" {
		return
	}
	binding := compatResponseBinding{
		ResponseID: id,
		ExpiresAt:  time.Now().Add(s.ttl()),
	}
	if raw, ok := s.bindings.Load(key); ok {
		if existing, ok := raw.(compatResponseBinding); ok {
			// Retain a loaded unsupported-continuation decision even when a later
			// response provides an ID; renewing the shared deadline retains turn state too.
			if existing.ContinuationDisabled {
				existing.ResponseID = ""
				existing.ExpiresAt = time.Now().Add(s.ttl())
				s.bindings.Store(key, existing)
				return
			}
			binding.TurnState = existing.TurnState
		}
	}
	// Store the new response and loaded turn state together so either lookup
	// uses the refreshed deadline; same-key updates can overwrite an intervening merge.
	s.bindings.Store(key, binding)
}

// DeleteResponse clears the response ID while retaining turn state or a disabled
// flag. Nil stores, empty keys, and missing bindings are ignored. Invalid stored
// values, or a binding with neither remaining state nor disable flag, are deleted.
//
// A surviving binding receives a fresh shared deadline. Existing expiry is not
// checked before merging, so retained fields can be renewed after their previous
// deadline passed. Read, mutation, and publication are separate map operations;
// callers coordinate same-key updates to prevent lost fields or removal of a
// replacement installed during cleanup.
func (s *CompatResponses) DeleteResponse(key string) {
	if s == nil {
		return
	}
	if key == "" {
		return
	}
	raw, ok := s.bindings.Load(key)
	if !ok {
		return
	}
	binding, ok := raw.(compatResponseBinding)
	if !ok {
		// An undecodable binding cannot retain turn state or a disable flag after
		// response removal, so discard the complete value for this session identity.
		s.bindings.Delete(key)
		return
	}
	binding.ResponseID = ""
	if strings.TrimSpace(binding.TurnState) == "" && !binding.ContinuationDisabled {
		// Clearing the response leaves no turn state or disabling evidence here.
		// Remove the empty binding instead of renewing an entry with no continuity value.
		s.bindings.Delete(key)
		return
	}
	binding.ExpiresAt = time.Now().Add(s.ttl())
	// Renew retained turn state or disabling evidence after clearing response
	// identity, so deleting the response alone leaves those other continuity decisions available.
	s.bindings.Store(key, binding)
}

// Disable stores a continuation-disabled binding with no response ID and a fresh
// deadline. Nil stores and empty keys are ignored. Existing turn state is retained
// from a loaded binding even if its old deadline has passed.
//
// The disable flag affects Response lookup and future BindResponse writes, while
// TurnState can still return the retained opaque value. Loading old state and
// publishing the merged binding are separate operations, so a concurrent response
// or turn-state update can be overwritten. Callers coordinate writes for one
// provider/key/prompt identity.
func (s *CompatResponses) Disable(key string) {
	if s == nil {
		return
	}
	if key == "" {
		return
	}
	binding := compatResponseBinding{
		ContinuationDisabled: true,
		ExpiresAt:            time.Now().Add(s.ttl()),
	}
	if raw, ok := s.bindings.Load(key); ok {
		if existing, ok := raw.(compatResponseBinding); ok {
			binding.TurnState = existing.TurnState
		}
	}
	// Publish the disabled decision with retained turn state and no response ID.
	// Future response binding keeps that decision while opaque state lookup remains available.
	s.bindings.Store(key, binding)
}

// Disabled reports whether a live binding carries the continuation-disable flag.
// Nil stores, empty keys, and missing entries return false. Invalid or expired
// entries are removed; a zero deadline is unbounded and the exact expiration
// instant still counts as live.
//
// A false result means this store has no current disabling evidence, allowing
// the caller's ordinary continuation policy to proceed. Lookup does not renew
// lifetime. Loading and deleting are separate map operations, so expiration
// cleanup can delete a replacement installed after the old value was read.
func (s *CompatResponses) Disabled(key string) bool {
	if s == nil {
		return false
	}
	if key == "" {
		return false
	}
	raw, ok := s.bindings.Load(key)
	if !ok {
		return false
	}
	binding, ok := raw.(compatResponseBinding)
	if !ok {
		// An undecodable value supplies no current disable decision to the caller.
		// Discard it so the next lookup sees absent continuation policy evidence.
		s.bindings.Delete(key)
		return false
	}
	if !binding.ExpiresAt.IsZero() && time.Now().After(binding.ExpiresAt) {
		// The disabling evidence has reached the end of its shared lifetime.
		// Delete the binding so subsequent reads can apply ordinary continuation policy.
		s.bindings.Delete(key)
		return false
	}
	return binding.ContinuationDisabled
}

// TurnState returns a trimmed opaque state from a live binding, regardless of its
// continuation-disable flag. Nil stores, empty keys, and missing, malformed, or
// blank-state entries return empty. Malformed and blank-state entries are retained
// on this path.
//
// A nonblank state's expired binding is deleted; its exact deadline remains live
// and zero means no deadline. Turn-state lookup does not refresh lifetime, while
// other field writes can refresh the shared deadline. Load and expiration cleanup
// are separate operations, so cleanup can remove a concurrently replaced binding;
// callers coordinate same-session updates when they require merged state.
func (s *CompatResponses) TurnState(key string) string {
	if s == nil {
		return ""
	}
	if key == "" {
		return ""
	}
	raw, ok := s.bindings.Load(key)
	if !ok {
		return ""
	}
	binding, ok := raw.(compatResponseBinding)
	if !ok || strings.TrimSpace(binding.TurnState) == "" {
		return ""
	}
	if !binding.ExpiresAt.IsZero() && time.Now().After(binding.ExpiresAt) {
		// Turn state has outlived the shared binding deadline. Remove every loaded
		// field together so response-ID lookup cannot reuse that same expired continuity.
		s.bindings.Delete(key)
		return ""
	}
	return strings.TrimSpace(binding.TurnState)
}

// BindTurnState stores a trimmed opaque value and refreshes the binding's shared
// lifetime. Nil stores, empty keys, and blank state are ignored. Loaded response
// ID and continuation-disable state are retained even if the old deadline passed.
//
// One deadline governs all retained fields, so this update can renew existing
// response continuity or a previous unsupported-continuation decision. The load,
// merge, and store do not form a compare-and-swap operation; another same-key
// writer can lose its update. Callers coordinate changes for a provider/key/prompt
// identity when retaining all fields matters.
func (s *CompatResponses) BindTurnState(key, turnState string) {
	if s == nil {
		return
	}
	state := strings.TrimSpace(turnState)
	if key == "" || state == "" {
		return
	}
	binding := compatResponseBinding{
		TurnState: state,
		ExpiresAt: time.Now().Add(s.ttl()),
	}
	if raw, ok := s.bindings.Load(key); ok {
		if existing, ok := raw.(compatResponseBinding); ok {
			binding.ResponseID = existing.ResponseID
			binding.ContinuationDisabled = existing.ContinuationDisabled
		}
	}
	// Store turn state with the loaded response and disable flag under one
	// deadline; same-key writers can overwrite an intervening change to either retained field.
	s.bindings.Store(key, binding)
}
