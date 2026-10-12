package provider

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync"
)

// Agent Identity requests sign with a registered task ID instead of an OAuth
// access token. Several application consumers share one coordinator, so a
// provider's task registration is serialized within the process and reread
// after acquiring that provider's mutex when storage reads are available.
//
// An existing task is reused unless the caller asks to replace that same
// observed task ID; a different newly stored ID wins the second check.
// Eligible credential shadows resolve to their parent before lock selection.
// Registration produces a new ID, Persist stores and installs the updated
// credentials, and connection invalidation follows a successful return.
// Optional reread errors leave the captured record usable, and this local
// mutex does not serialize other processes or administrator writes. Callers
// provide stable records and coordinate those separate writers.

// The Agent Identity mode selects task registration and signed request headers
// for OpenAI OAuth records whose current credential mode matches this value.
const OpenAIAuthModeAgentIdentity = "agentIdentity"

// OpenAITaskOptions supplies provider lookup, task registration, credential
// persistence and connection invalidation callbacks.
type OpenAITaskOptions struct {
	// Read is required for credential shadows and optional for regular records.
	// Positive provider IDs are reread under their selected mutex when this callback exists.
	Read func(context.Context, int64) (*Record, error)
	// Register performs the upstream task exchange when no reusable task survives
	// the locked reread. Ensure stores its returned ID as supplied, so the callback
	// validates its usability. A persistence failure can leave the upstream task created.
	Register func(context.Context, *Record) (string, error)
	// Persist stores the new credentials and installs them into the passed record.
	// Ensure copies from that record after success before invalidating its connections.
	Persist func(context.Context, *Record, map[string]any) error
	// Invalidate is optional and discards connections after successful persistence.
	// It receives the resolved credential owner's ID, including for a shadow request.
	Invalidate func(int64)
	// FallbackMutex is required whenever work reaches locking. Nonpositive IDs
	// use it directly; positive IDs select a mutex published by the shared coordinator.
	FallbackMutex *sync.Mutex
}

// OpenAITaskCoordinator publishes provider mutexes shared by task-registration
// consumers within one process.
type OpenAITaskCoordinator struct {
	// locks publishes one mutex per positive credential-owner ID through LoadOrStore.
	// Keep the coordinator shared and do not copy it after its first use.
	locks sync.Map
}

// IsOpenAIAgentIdentity reports whether a nonnil OpenAI OAuth record selects
// Agent Identity authentication through its trimmed, case-insensitive current
// mode key. Other records and other mode spellings return false; legacy PAT
// mode handling is separate.
func (r *Record) IsOpenAIAgentIdentity() bool {
	if r == nil || !r.IsOpenAIOAuth() {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.GetCredential(OpenAIAuthModeCredentialKey)), OpenAIAuthModeAgentIdentity)
}

// openAITaskCopyCredentials returns a shallow top-level copy for task-ID
// publication, retaining nil input as nil. Nested credential values remain
// shared, so callers keep them stable while updating the copied map.
func openAITaskCopyCredentials(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	result := make(map[string]any, len(values))
	maps.Copy(result, values)
	return result
}

// Ensure registers or replaces a task for an Agent Identity record and
// persists its credentials before optional connection invalidation. Nil or
// ineligible records return nil. A shadow already classified as Agent Identity
// requires a valid parent resolved through Read.
//
// A nonempty task is reused when expectedTaskID is empty or differs from that
// task. The stored task ID is trimmed; expectedTaskID is compared as supplied.
// Work reaching lock selection requires FallbackMutex even for persisted
// providers; positive IDs select the coordinator's shared provider mutex, and
// other IDs use the supplied mutex. A configured reread under that lock can
// install fresher credentials and avoid replacing a different task; read or
// resolution errors retain the captured observation.
//
// Register and Persist are required for a new task. Register validates that
// its returned ID is usable for signing; Ensure stores the value as supplied.
// Persist must install the supplied credentials into its record as well as
// perform the intended write because request copy-back reads that record after
// success. The mutex
// coordinates this process's consumers, and callers synchronize access to
// request records and independent writers. Registration can succeed before
// persistence fails; that upstream side effect is not rolled back.
func (s *OpenAITaskCoordinator) Ensure(ctx context.Context, options OpenAITaskOptions, value *Record, expectedTaskID string) error {
	taskMu := options.FallbackMutex
	if value == nil || !value.IsOpenAIAgentIdentity() {
		return nil
	}
	credProvider := value
	if value.IsCredentialShadow() {
		resolved, err := ResolveCredentialRecord(ctx, options.Read, value)
		if err != nil {
			return err
		}
		credProvider = resolved
	}
	if credProvider == nil || !credProvider.IsOpenAIAgentIdentity() {
		return errors.New("agent identity credentials are unavailable")
	}
	currentTaskID := strings.TrimSpace(credProvider.GetCredential("task_id"))
	if currentTaskID != "" && (expectedTaskID == "" || currentTaskID != expectedTaskID) {
		return nil
	}
	if taskMu == nil {
		return errors.New("agent identity task lock is unavailable")
	}
	sharedTaskMu := taskMu
	if credProvider.ID > 0 {
		candidate := &sync.Mutex{}
		// Publish one mutex for this credential-owner ID before entering registration,
		// so independent consumers sharing the coordinator select the same local lock.
		actual, _ := s.locks.LoadOrStore(credProvider.ID, candidate)
		loadedTaskMu, ok := actual.(*sync.Mutex)
		if !ok {
			return errors.New("agent identity task lock has invalid type")
		}
		sharedTaskMu = loadedTaskMu
	}
	sharedTaskMu.Lock()
	defer sharedTaskMu.Unlock()
	// Recheck after waiting for another local consumer's registration. A different
	// persisted task wins replacement, while read failures retain the captured credentials.
	if options.Read != nil && credProvider.ID > 0 {
		if refreshed, refreshErr := options.Read(ctx, credProvider.ID); refreshErr == nil && refreshed != nil {
			if refreshed.IsCredentialShadow() {
				if resolved, resolveErr := ResolveCredentialRecord(ctx, options.Read, refreshed); resolveErr == nil && resolved != nil {
					refreshed = resolved
				}
			}
			if refreshed.IsOpenAIAgentIdentity() {
				credProvider = refreshed
				if !value.IsCredentialShadow() {
					value.Credentials = openAITaskCopyCredentials(credProvider.Credentials)
				}
			}
		}
	}
	currentTaskID = strings.TrimSpace(credProvider.GetCredential("task_id"))
	if currentTaskID != "" && (expectedTaskID == "" || currentTaskID != expectedTaskID) {
		return nil
	}
	// The locked check found no reusable task. Register upstream first, then ask
	// Persist to install and store that ID before invalidating existing connections.
	newTaskID, err := options.Register(ctx, credProvider)
	if err != nil {
		return err
	}
	credentials := make(map[string]any, len(credProvider.Credentials)+1)
	maps.Copy(credentials, credProvider.Credentials)
	credentials["task_id"] = newTaskID
	if err := options.Persist(ctx, credProvider, credentials); err != nil {
		return err
	}
	if !value.IsCredentialShadow() && value != credProvider {
		value.Credentials = openAITaskCopyCredentials(credProvider.Credentials)
	}
	if options.Invalidate != nil {
		options.Invalidate(credProvider.ID)
	}
	return nil
}
