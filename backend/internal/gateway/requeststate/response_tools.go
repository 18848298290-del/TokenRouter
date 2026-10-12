package requeststate

import (
	"encoding/json"
	"maps"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
)

// ResponseTools holds request and active-turn tool restoration state separately from session updates.
// Callers treat published mappings as read-only and replace them for later turns; active output retains its mapping.
type ResponseTools struct {
	mu                        sync.RWMutex
	openai, grok              bridge.ResponsesClientToolMapping
	namespaces                map[string]bridge.ResponsesNamespaceName
	activeNames, sessionNames map[string]string
	bridgeState               WSBridgeTools
	bridgePresent             bool
}

// WSBridgeTools holds tool declarations and lowered mappings inherited by the next HTTP bridge turn.
type WSBridgeTools struct {
	ClientMapping bridge.ResponsesClientToolMapping
	LoweredTools  json.RawMessage
}

func (s *ResponseTools) ClientMapping(grok bool) bridge.ResponsesClientToolMapping {
	if s == nil {
		return bridge.ResponsesClientToolMapping{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if grok {
		return s.grok
	}
	return s.openai
}

func (s *ResponseTools) SetClientMapping(grok bool, mapping bridge.ResponsesClientToolMapping) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if grok {
		s.grok = mapping
	} else {
		s.openai = mapping
	}
}

func (s *ResponseTools) Namespaces() map[string]bridge.ResponsesNamespaceName {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.namespaces
}

func (s *ResponseTools) SetNamespaces(names map[string]bridge.ResponsesNamespaceName) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.namespaces = names
	s.mu.Unlock()
}

func (s *ResponseTools) CodexNames(session bool) map[string]string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if session {
		return s.sessionNames
	}
	return s.activeNames
}

func (s *ResponseTools) SetCodexNames(session bool, reverse map[string]string) {
	if s == nil {
		return
	}
	copyMap := make(map[string]string, len(reverse))
	maps.Copy(copyMap, reverse)
	s.mu.Lock()
	defer s.mu.Unlock()
	if session {
		s.sessionNames = copyMap
	} else {
		s.activeNames = copyMap
	}
}

func (s *ResponseTools) Bridge() (WSBridgeTools, bool) {
	if s == nil {
		return WSBridgeTools{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bridgeState, s.bridgePresent
}

func (s *ResponseTools) SetBridge(state WSBridgeTools) {
	if s == nil {
		return
	}
	state.LoweredTools = append(json.RawMessage(nil), state.LoweredTools...)
	s.mu.Lock()
	s.bridgeState, s.bridgePresent = state, true
	s.mu.Unlock()
}
