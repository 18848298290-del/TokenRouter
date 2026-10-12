package provider

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync"
)

const OpenAIAuthModeAgentIdentity = "agentIdentity"

type OpenAITaskOptions struct {
	Read          func(context.Context, int64) (*Record, error)
	Register      func(context.Context, *Record) (string, error)
	Persist       func(context.Context, *Record, map[string]any) error
	Invalidate    func(int64)
	FallbackMutex *sync.Mutex
}

// OpenAITaskCoordinator serializes task registration per provider within this process.
type OpenAITaskCoordinator struct{ locks sync.Map }

func (r *Record) IsOpenAIAgentIdentity() bool {
	if r == nil || !r.IsOpenAIOAuth() {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.GetCredential(OpenAIAuthModeCredentialKey)), OpenAIAuthModeAgentIdentity)
}

// openAITaskCopyCredentials shallow-copies credentials for task updates.
func openAITaskCopyCredentials(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	result := make(map[string]any, len(values))
	maps.Copy(result, values)
	return result
}

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
		actual, _ := s.locks.LoadOrStore(credProvider.ID, candidate)
		loadedTaskMu, ok := actual.(*sync.Mutex)
		if !ok {
			return errors.New("agent identity task lock has invalid type")
		}
		sharedTaskMu = loadedTaskMu
	}
	sharedTaskMu.Lock()
	defer sharedTaskMu.Unlock()
	// Re-read under the shared lock and reuse a task that differs from the one being replaced.
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
