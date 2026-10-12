package requeststate

import (
	"encoding/json"
	"maps"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
)

// Tool lowering needs one mapping for the output still being produced and
// another for a later WebSocket session update. Replacing session names leaves
// the active-turn slot intact, so restoration can continue using the current
// turn's names while future declarations change.
//
// The mutex synchronizes reference publication and retrieval, while consumers
// keep published maps immutable after retrieval. Client mappings and namespace
// maps are retained; Codex-name publication clones its string map, and bridge
// publication clones declaration bytes while sharing its client mapping.
// Getters return those captured references, so callers replace whole maps for a
// later turn and keep retrieved storage read-only. Synchronizing a getter
// cannot protect later mutation by its consumer.

// ResponseTools holds separate request, active-turn, session, and HTTP-bridge
// restoration state. Its lock protects slot access; callers treat the maps and
// bytes returned from those slots as immutable while restoration uses them.
type ResponseTools struct {
	// mu protects openai, grok, namespaces, activeNames, sessionNames, bridgeState,
	// and bridgePresent during publication and retrieval. Published map contents
	// stay immutable after a getter releases the lock.
	mu                        sync.RWMutex                             // mapping reference publication lock
	openai, grok              bridge.ResponsesClientToolMapping        // OpenAI lowered-tool mapping and Grok lowered-tool mapping
	namespaces                map[string]bridge.ResponsesNamespaceName // shared flattened namespace reverse map
	activeNames, sessionNames map[string]string                        // active-turn name restoration and future-session name restoration
	bridgeState               WSBridgeTools                            // captured next HTTP-bridge tool state
	bridgePresent             bool                                     // bridge state publication presence flag
}

// WSBridgeTools carries lowered tool declarations and client mappings into an
// HTTP bridge turn.
type WSBridgeTools struct {
	// ClientMapping and LoweredTools describe the same lowered declarations.
	// SetBridge copies LoweredTools bytes but retains the mapping's internal maps;
	// Bridge returns both shared references to the next turn.
	ClientMapping bridge.ResponsesClientToolMapping // shared lowered client-tool mapping
	LoweredTools  json.RawMessage                   // captured tool declaration JSON bytes
}

// ClientMapping returns the selected OpenAI or Grok mapping value under a read
// lock. Nil state yields an empty mapping, and its internal maps remain shared;
// callers keep their contents immutable while restoration uses them.
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

// SetClientMapping publishes the supplied mapping to the OpenAI or Grok slot
// and ignores nil state. Its internal maps are retained, so callers keep them
// unchanged after publication and synchronize any replacement through this
// setter.
func (s *ResponseTools) SetClientMapping(grok bool, mapping bridge.ResponsesClientToolMapping) {
	if s == nil {
		return
	}
	// Replace the selected platform's complete mapping under the publication lock
	// so a concurrent getter captures one consistent set of tool-kind and
	// namespace evidence.
	s.mu.Lock()
	defer s.mu.Unlock()
	if grok {
		s.grok = mapping
	} else {
		s.openai = mapping
	}
}

// Namespaces returns the captured namespace-name map under a read lock, or nil
// for nil state. The map remains shared after unlocking; callers read it
// immutably and publish a replacement to change names.
func (s *ResponseTools) Namespaces() map[string]bridge.ResponsesNamespaceName {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.namespaces
}

// SetNamespaces publishes the supplied namespace map and ignores nil state. It
// retains that map rather than cloning it; callers leave its contents unchanged
// while request output may still read its names.
func (s *ResponseTools) SetNamespaces(names map[string]bridge.ResponsesNamespaceName) {
	if s == nil {
		return
	}
	// Replace the namespace reference through the same lock used by retrieval;
	// output readers then capture either the earlier or the newly published
	// immutable name map.
	s.mu.Lock()
	s.namespaces = names
	s.mu.Unlock()
}

// CodexNames returns active-turn names or future-session names according to
// session. Nil state returns nil, and the captured map stays shared after
// unlocking, so callers treat its string entries as immutable.
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

// SetCodexNames publishes a copied string map to either active-turn or
// future-session storage. Nil state is ignored, and nil input produces an empty
// map rather than an uninitialized slot.
//
// The copy is prepared before taking the publication lock, so callers keep
// reverse stable during copying. Session publication changes only sessionNames;
// active output can continue reading its existing activeNames map. A later
// active-turn publication replaces that separate slot. Retrieval shares each
// published map, requiring consumers to treat its entries as immutable after
// the setter returns.
func (s *ResponseTools) SetCodexNames(session bool, reverse map[string]string) {
	if s == nil {
		return
	}
	// Publish a stable name set so later caller edits cannot change an active
	// turn while session-name updates proceed through their separate slot.
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

// Bridge returns captured HTTP-bridge declarations and their presence flag
// under a read lock. Nil state returns absent zero values; returned declaration
// bytes and mapping internals are shared and stay read-only for the consuming
// turn.
func (s *ResponseTools) Bridge() (WSBridgeTools, bool) {
	if s == nil {
		return WSBridgeTools{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bridgeState, s.bridgePresent
}

// SetBridge publishes supplied HTTP-bridge state and marks it present, ignoring
// a nil receiver. LoweredTools bytes are copied before publication; empty
// storage can become nil, while ClientMapping internal maps remain shared and
// caller-immutable.
//
// The captured state and presence flag change under one lock, so Bridge
// observes a complete publication. A zero WSBridgeTools value is still present
// after this call; callers use that presence to distinguish published empty
// declarations from an uninitialized bridge slot. They leave returned byte
// storage and mapping contents unchanged while the next turn reads them.
func (s *ResponseTools) SetBridge(state WSBridgeTools) {
	if s == nil {
		return
	}
	// Bridge turns retain declaration bytes after this request update finishes.
	// Copy those bytes before publication while retaining the read-only client mapping.
	state.LoweredTools = append(json.RawMessage(nil), state.LoweredTools...)
	// Pair state replacement with its presence flag before unlocking so the next
	// bridge turn cannot observe newly present state with the preceding declarations.
	s.mu.Lock()
	s.bridgeState, s.bridgePresent = state, true
	s.mu.Unlock()
}
