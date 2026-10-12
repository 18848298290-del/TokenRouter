package session

import (
	"context"
	"time"
)

// Cyber blocking first checks an explicitly declared session, then checks visible
// transcript prefixes under an active source scope. Runtime settings and storage
// availability are tested before the lazy explicit-key callback, so a disabled
// feature does not cause extra payload parsing. Storage failures are logged and
// return no block to the gateway caller.
//
// Transcript lookup is bounded, but an active scope with more candidates than the
// limit returns an overflow block marker. Accepting a truncated candidate list
// could lose a previously blocked prefix when the caller appends more history.
// Scope activation therefore precedes transcript derivation, and the overflow
// result bypasses the bounded store lookup.

// CyberLookup carries borrowed request data for synchronous Cyber block lookup.
type CyberLookup struct {
	APIKeyID int64 // authenticated API Key identifier
	// Body retains request bytes during this synchronous lookup; callers keep
	// the storage stable while transcript derivation reads its visible history.
	Body []byte
	// ClientIP and UserAgent combine with APIKeyID for transcript scope lookup
	// after no explicit block is found. CyberSessionScopeKey trims the IP and
	// normalizes User-Agent; Find checks scope activity before deriving
	// transcript keys.
	ClientIP, UserAgent string // resolved source IP address and supplied User-Agent header
	// ExplicitKey is invoked after runtime and store checks; nil supplies no
	// explicit candidate, allowing scope-gated transcript matching to continue.
	ExplicitKey func() string
}

// CyberBlocks applies current runtime settings before optional session-block storage operations.
type CyberBlocks struct {
	store CyberSessionBlockStore // optional session block storage
	// settings supplies current enabled state and TTL; nil disables the feature
	// while a configured callback receives the operation's caller context.
	settings func(context.Context) (bool, time.Duration)
	// log receives formatted diagnostics synchronously; nil suppresses messages
	// after storage failure while the gateway lookup still returns no block.
	log func(string, ...any)
}

// NewCyberBlocks binds session-block storage, runtime settings, and an optional logger.
// Nil settings disable blocking through Runtime; nil storage skips writes and
// lookup, and each callback runs synchronously in the caller's operation.
func NewCyberBlocks(store CyberSessionBlockStore, settings func(context.Context) (bool, time.Duration), log func(string, ...any)) *CyberBlocks {
	return &CyberBlocks{store: store, settings: settings, log: log}
}

// Runtime returns the enabled flag and lifetime from the settings callback.
// A nil receiver or missing callback returns disabled with a one-hour lifetime;
// otherwise the supplied context and callback results pass through unchanged.
func (s *CyberBlocks) Runtime(ctx context.Context) (bool, time.Duration) {
	if s == nil || s.settings == nil {
		return false, time.Hour
	}
	return s.settings(ctx)
}

// logf sends diagnostics synchronously through the optional Cyber logger.
// A nil receiver or logger skips the message, allowing disabled or unavailable
// blocking paths to return without a logging dependency.
func (s *CyberBlocks) logf(format string, args ...any) {
	if s != nil && s.log != nil {
		s.log(format, args...)
	}
}

// MarkCyberSessionBlocked writes keys under the current enabled setting and TTL.
// A nil receiver, empty key list, disabled feature, or absent store skips writing;
// storage errors are logged and do not return to the caller.
func (s *CyberBlocks) MarkCyberSessionBlocked(ctx context.Context, scopeKey string, keys []string) {
	if s == nil || len(keys) == 0 {
		return
	}
	enabled, ttl := s.Runtime(ctx)
	if !enabled {
		return
	}
	store := s.store
	if store == nil {
		return
	}
	// Blocking persistence is advisory here: report failures through the logger
	// while leaving the caller free to finish its upstream policy handling.
	if err := store.SetCyberSessionBlocked(ctx, scopeKey, keys, ttl); err != nil {
		s.logf("cyber session block write failed: err=%v", err)
	}
}

// Find returns the first known explicit or transcript block, or empty when
// blocking is disabled, storage is absent, or a storage read fails. ExplicitKey
// runs lazily after availability checks. Its known block returns immediately;
// an explicit read error ends lookup before any scope or transcript work.
//
// Without an explicit hit, an active API Key/IP/User-Agent scope enables cumulative
// transcript checks. Truncated candidates return the overflow marker immediately,
// while an inactive scope or empty candidate list returns empty. Store errors are
// logged and allow the request, including context-related errors returned by the
// store. Callers provide stable borrowed bytes until this synchronous lookup ends.
func (s *CyberBlocks) Find(ctx context.Context, input CyberLookup) string {
	apiKeyID, body, clientIP, userAgent := input.APIKeyID, input.Body, input.ClientIP, input.UserAgent
	enabled, _ := s.Runtime(ctx)
	if !enabled {
		return ""
	}
	store := s.store
	if store == nil {
		return ""
	}
	explicitKey := ""
	if input.ExplicitKey != nil {
		explicitKey = input.ExplicitKey()
	}
	if explicitKey != "" {
		key, err := store.FindCyberSessionBlocked(ctx, []string{explicitKey})
		if err != nil {
			// A failed read supplies no confirmed block; log the storage failure
			// and return empty so this optional session check allows the request.
			s.logf("cyber explicit session read failed: err=%v", err)
			return ""
		}
		if key != "" {
			return key
		}
	}
	// With no explicit hit, use transcript evidence within an active source
	// scope so a shared visible prefix alone does not trigger this lookup path.
	scopeKey := CyberSessionScopeKey(apiKeyID, clientIP, userAgent)
	active, err := store.IsCyberSessionScopeActive(ctx, scopeKey)
	if err != nil {
		// A failed read supplies no confirmed block; log the storage failure
		// and return empty so this optional session check allows the request.
		s.logf("cyber session scope read failed: err=%v", err)
		return ""
	}
	if !active {
		return ""
	}
	transcript := deriveOpenAICyberTranscriptBlockKeys(apiKeyID, body)
	if transcript.lookupKeysTruncated {
		// An active scope enables conservative overflow rejection. Appended items
		// can evict an earlier blocked prefix from the bounded candidate set, so
		// reject before lookup instead of treating the truncated set as permission.
		return cyberSessionTranscriptLookupOverflowBlockKey
	}
	keys := transcript.lookupKeys
	if len(keys) == 0 {
		return ""
	}
	key, err := store.FindCyberSessionBlocked(ctx, keys)
	if err != nil {
		// A failed read supplies no confirmed block; log the storage failure
		// and return empty so this optional session check allows the request.
		s.logf("cyber session block batch read failed: err=%v", err)
		return ""
	}
	return key
}
