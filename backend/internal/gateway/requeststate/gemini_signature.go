package requeststate

import "bytes"

// GeminiSignatureState requests cleanup on provider changes or once when signature ownership is unknown.
type GeminiSignatureState struct {
	BoundProviderID int64
	cleanedUnknown  bool
}
type SignatureChange struct {
	Clean              bool
	Missing            bool
	PreviousProviderID int64
}

func (s *GeminiSignatureState) Select(providerID int64, hasSessionKey bool, body []byte) SignatureChange {
	change := SignatureChange{PreviousProviderID: s.BoundProviderID}
	if s.BoundProviderID > 0 && s.BoundProviderID != providerID {
		change.Clean = true
		s.BoundProviderID = providerID
	} else if hasSessionKey && s.BoundProviderID == 0 && !s.cleanedUnknown && bytes.Contains(body, []byte(`"thoughtSignature"`)) {
		change.Clean = true
		change.Missing = true
		s.cleanedUnknown = true
		s.BoundProviderID = providerID
	} else if s.BoundProviderID == 0 {
		s.BoundProviderID = providerID
	}
	return change
}
