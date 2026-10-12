package requeststate

import "bytes"

// Gemini thought signatures carry provider-specific history into later
// requests. Selecting another provider requests cleanup, while a session with
// unknown ownership requests cleanup once when signature bytes are present.
//
// The state belongs to one serial provider-selection sequence. Select records
// the new provider before forwarding, and the HTTP Gemini attempt adapter
// replaces signatures when Clean is true. Missing identifies the unknown-owner
// path for its diagnostic event; it is separate from an ordinary provider
// switch. Detection uses the quoted thoughtSignature byte spelling, so this
// helper requests cleanup without parsing or modifying the request itself.

// GeminiSignatureState tracks signature ownership across serial selections for
// one request.
type GeminiSignatureState struct {
	// BoundProviderID records the selected signature owner. cleanedUnknown records
	// whether unbound session history already requested cleanup; both values are
	// updated by the same serial selection sequence.
	BoundProviderID int64 // current signature provider identifier
	cleanedUnknown  bool  // unknown-owner cleanup already requested
}

// SignatureChange tells the Gemini attempt adapter which cleanup decision to
// apply.
type SignatureChange struct {
	// Clean requests signature replacement and Missing identifies its unknown-owner
	// path. PreviousProviderID retains the binding before Select changed it, so
	// the attempt adapter can report the reason for cleanup.
	Clean              bool  // request body signature cleanup
	Missing            bool  // cleanup from unknown ownership
	PreviousProviderID int64 // provider binding before selection
}

// Select updates the provider binding and reports whether the caller should
// clean thought signatures. Callers use a nonnil state and serialize selections
// before the Gemini attempt adapter applies any requested body cleanup.
//
// A different positive bound provider requests cleanup immediately. An unbound
// session with quoted signature bytes requests one unknown-owner cleanup and
// sets Missing; subsequent selections retain that decision even if the body
// still contains signatures. PreviousProviderID captures the binding before
// this selection, allowing diagnostics to distinguish missing ownership from
// provider switching.
func (s *GeminiSignatureState) Select(providerID int64, hasSessionKey bool, body []byte) SignatureChange {
	change := SignatureChange{PreviousProviderID: s.BoundProviderID}
	// A known provider switch invalidates ownership of carried signature history;
	// request cleanup before the new provider receives this attempt's body.
	if s.BoundProviderID > 0 && s.BoundProviderID != providerID {
		change.Clean = true
		s.BoundProviderID = providerID
	} else if hasSessionKey && s.BoundProviderID == 0 && !s.cleanedUnknown && bytes.Contains(body, []byte(`"thoughtSignature"`)) {
		// Unknown session ownership cannot justify forwarding carried signatures.
		// Request this initial cleanup once and bind subsequent selections to
		// the chosen provider.
		change.Clean = true
		change.Missing = true
		s.cleanedUnknown = true
		s.BoundProviderID = providerID
	} else if s.BoundProviderID == 0 {
		s.BoundProviderID = providerID
	}
	return change
}
