package live

import (
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
)

// model_state.go tracks current Live model aliases and restores client-facing
// model metadata in server frames.
// A Live client can change its declared model through sideband frames while
// server events arrive on another forwarding direction. Response restoration
// needs one current client label and its internal model set, so the state
// updates those values together and snapshots them under a shared mutex.
//
// Each locally rewritten model change replaces the earlier internal set.
// A blank update retains the previous client name but still replaces the set.
// Parsed restoration checks selected protocol metadata paths and changes only
// values present in that current set. A field rewrite failure keeps earlier
// successful changes and leaves that field as received, allowing the control
// payload to continue downstream.

// liveSidebandModelState coordinates the current client model and internal
// restoration names.
type liveSidebandModelState struct {
	// mu protects clientModel and internalModels as one current restoration state.
	// Client-to-upstream updates replace the set, while the opposite direction
	// obtains a coherent pair through snapshot before restoring a server event.
	mu             sync.RWMutex        // current restoration state lock
	clientModel    string              // latest locally rewritten client model
	internalModels map[string]struct{} // current internal model restoration set
}

// firstModel returns first when its trimmed form is nonblank, otherwise
// fallback. The chosen string retains its supplied whitespace; callers that
// require canonical names trim the returned value themselves.
func firstModel(first, fallback string) string {
	if strings.TrimSpace(first) != "" {
		return first
	}
	return fallback
}

// newLiveSidebandModelState initializes restoration from RequestedModel or the
// creation Model and registers its internal creation names. Nil records
// produce empty writable state; update trims the selected client identity.
func newLiveSidebandModelState(record *session.LiveCallRecord) *liveSidebandModelState {
	state := &liveSidebandModelState{internalModels: make(map[string]struct{})}
	if record == nil {
		return state
	}
	state.update(firstModel(record.RequestedModel, record.Model), record.Model, record.UpstreamModel)
	return state
}

// update replaces current internal names while retaining a nonblank supplied
// client label. Nil state is ignored, and each internal name is trimmed,
// deduplicated, and excluded when equal to the chosen client label.
//
// The client label and rebuilt set change under the same write lock so
// server-side snapshots do not pair a new label with the earlier internal
// names. Blank client input retains the existing label but still clears and
// rebuilds its set. The state describes the latest local rewrite before its
// upstream write, not an association with acknowledgement or an earlier turn.
func (s *liveSidebandModelState) update(clientModel string, internalModels ...string) {
	if s == nil {
		return
	}
	clientModel = strings.TrimSpace(clientModel)
	// Replace client identity and internal membership under one lock, so the
	// opposite forwarding direction snapshots a consistent restoration pair
	// instead of mixed update stages.
	s.mu.Lock()
	if clientModel != "" {
		s.clientModel = clientModel
	}
	s.internalModels = make(map[string]struct{}, len(internalModels))
	for _, model := range internalModels {
		model = strings.TrimSpace(model)
		if model == "" || model == s.clientModel {
			continue
		}
		s.internalModels[model] = struct{}{}
	}
	s.mu.Unlock()
}

// snapshot returns the current client label and an independently allocated
// internal-name slice under the read lock. Nil state returns empty results;
// names retain map iteration order and callers need no sorting for
// restoration.
func (s *liveSidebandModelState) snapshot() (string, []string) {
	if s == nil {
		return "", nil
	}
	s.mu.RLock()
	clientModel := s.clientModel
	models := make([]string, 0, len(s.internalModels))
	for model := range s.internalModels {
		models = append(models, model)
	}
	s.mu.RUnlock()
	return clientModel, models
}

// RestoreServerPayload restores the client label in recognized Live metadata
// paths when their trimmed string value matches an internal name. Empty
// restoration input and invalid JSON return the supplied payload.
//
// The matcher considers model, version, response.model, and session.model
// paths, leaving other strings outside its decisions. Each successful field
// update feeds the next lookup. A mutation failure leaves that field unchanged
// while retaining previous successful updates, and no error is reported to the
// controller; the downstream frame therefore receives the best completed
// restoration.
func RestoreServerPayload(payload []byte, clientModel string, internalModels []string) []byte {
	clientModel = strings.TrimSpace(clientModel)
	if clientModel == "" || len(internalModels) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	internal := make(map[string]struct{}, len(internalModels))
	for _, model := range internalModels {
		if model = strings.TrimSpace(model); model != "" && model != clientModel {
			internal[model] = struct{}{}
		}
	}
	rewritten := payload
	for _, modelPath := range []string{"model", "modelVersion", "model_version", "response.model", "session.model"} {
		value := gjson.GetBytes(rewritten, modelPath)
		if value.Type != gjson.String {
			continue
		}
		if _, ok := internal[strings.TrimSpace(value.String())]; !ok {
			continue
		}
		// Retain prior successful metadata updates when one field mutation fails;
		// this best-effort restoration should still forward the server payload
		// through the established control connection.
		if next, err := sjson.SetBytes(rewritten, modelPath, clientModel); err == nil {
			rewritten = next
		}
	}
	return rewritten
}
