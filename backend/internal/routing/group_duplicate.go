package routing

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

const (
	MaxGroupNameRunes            = 100
	DuplicateGroupInactiveStatus = "inactive"
)

func DuplicateGroupOperationID(sourceID int64, actorScope, operationKey string) string {
	operationKey = strings.TrimSpace(operationKey)
	if operationKey == "" {
		return ""
	}
	actorScope = strings.TrimSpace(actorScope)
	if actorScope == "" {
		actorScope = "admin:0"
	}
	payload := "admin.groups.duplicate\x00" + actorScope + "\x00" + strconv.FormatInt(sourceID, 10) + "\x00" + operationKey
	digest := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", digest)
}

func DuplicateGroupName(sourceName string, copyNumber int) string {
	if copyNumber < 1 {
		copyNumber = 1
	}
	suffix := " (Copy)"
	if copyNumber > 1 {
		suffix = fmt.Sprintf(" (Copy %d)", copyNumber)
	}
	baseRunes := []rune(strings.TrimSpace(sourceName))
	maxBaseRunes := max(MaxGroupNameRunes-len([]rune(suffix)), 0)
	if len(baseRunes) > maxBaseRunes {
		baseRunes = baseRunes[:maxBaseRunes]
	}
	return string(baseRunes) + suffix
}

func CloneGroupValuePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func CloneGroupModelRouting(value map[string][]int64) map[string][]int64 {
	if value == nil {
		return nil
	}
	cloned := make(map[string][]int64, len(value))
	for model, providerIDs := range value {
		cloned[model] = append([]int64(nil), providerIDs...)
	}
	return cloned
}

func CloneGroupForDuplicate(source *Group, operationID string) *Group {
	return &Group{
		Name:        DuplicateGroupName(source.Name, 1),
		Description: source.Description,

		SchedulerType:                   source.SchedulerType,
		AdvancedSchedulerOverrides:      policy.CloneGroupAdvancedSchedulerOverrides(source.AdvancedSchedulerOverrides),
		DisplayBrand:                    source.DisplayBrand,
		RateMultiplier:                  source.RateMultiplier,
		RoutingPolicy:                   source.RoutingPolicy.Clone(),
		IsExclusive:                     source.IsExclusive,
		Status:                          DuplicateGroupInactiveStatus,
		DuplicateOperationID:            operationID,
		SessionIsolationEnabled:         source.SessionIsolationEnabled,
		AllowImageGeneration:            source.AllowImageGeneration,
		AllowBatchImageGeneration:       source.AllowBatchImageGeneration,
		ClaudeCodeOnly:                  source.ClaudeCodeOnly,
		FallbackGroupID:                 CloneGroupValuePointer(source.FallbackGroupID),
		FallbackGroupIDOnInvalidRequest: CloneGroupValuePointer(source.FallbackGroupIDOnInvalidRequest),
		UnavailableFallbackGroupID:      CloneGroupValuePointer(source.UnavailableFallbackGroupID),
		ModelRouting:                    CloneGroupModelRouting(source.ModelRouting),
		ModelRoutingEnabled:             source.ModelRoutingEnabled,
		MCPXMLInject:                    source.MCPXMLInject,
		SupportedModelScopes:            append([]string(nil), source.SupportedModelScopes...),
		SortOrder:                       source.SortOrder,
		AllowedProtocols:                CloneGroupClientProtocols(source.AllowedProtocols),
		ProtocolFallbacks:               protocol.CloneFallbacks(source.ProtocolFallbacks),
		ResponsesImagePolicy:            source.ResponsesImagePolicy,
		AllowMessagesDispatch:           source.AllowsClientProtocol(protocol.ProtocolAnthropicMessages),
		AllowLive:                       source.AllowLive,
		ForceOpenAIFast:                 source.ForceOpenAIFast,
		OpenAIFastPolicy:                source.EffectiveOpenAIFastPolicy(),
		RequireOAuthOnly:                source.RequireOAuthOnly,
		RequirePrivacySet:               source.RequirePrivacySet,
		DefaultMappedModel:              source.DefaultMappedModel,
		ModelsListConfig: GroupModelsListConfig{
			Enabled: source.ModelsListConfig.Enabled,
			Models:  append([]string(nil), source.ModelsListConfig.Models...),
		},
		AvailabilityProbeConfig:     source.AvailabilityProbeConfig,
		RPMLimit:                    source.RPMLimit,
		MaxReasoningEffort:          source.MaxReasoningEffort,
		MaxReasoningEffortOverLimit: source.MaxReasoningEffortOverLimit,
		ReasoningEffortMappings:     append([]ReasoningEffortMapping(nil), source.ReasoningEffortMappings...),
	}
}

// CloneGroupClientProtocols returns an independent, non-nil protocol slice.
func CloneGroupClientProtocols(protocols []protocol.ProtocolID) []protocol.ProtocolID {
	return append([]protocol.ProtocolID{}, protocols...)
}

// RecoverDuplicateGroup looks up a committed copy by actor, source group, and idempotency key.
func (s *GroupAdmin) RecoverDuplicateGroup(ctx context.Context, id int64, actorScope, operationKey string) (*Group, error) {
	operationID := DuplicateGroupOperationID(id, actorScope, operationKey)
	if operationID == "" {
		return nil, nil
	}
	if s.groupDuplicateRepo == nil {
		return nil, errors.New("group duplicate repository is not configured")
	}
	group, err := s.groupDuplicateRepo.FindByDuplicateOperationID(ctx, operationID)
	if err != nil {
		return nil, fmt.Errorf("find duplicate group operation: %w", err)
	}
	if group == nil {
		return nil, nil
	}
	hydrated, err := s.groupRepo.GetByID(ctx, group.ID)
	if err != nil {
		return nil, fmt.Errorf("load recovered duplicate group: %w", err)
	}
	return hydrated, nil
}

// DuplicateGroup creates a disabled configuration copy while retaining provider priorities.
// The repository commits the group, bindings, and outbox together so a binding failure rolls back the copy.
func (s *GroupAdmin) DuplicateGroup(ctx context.Context, id int64, actorScope, operationKey string) (*Group, error) {
	existing, err := s.RecoverDuplicateGroup(ctx, id, actorScope, operationKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	source, err := s.groupRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.groupDuplicateRepo == nil {
		return nil, errors.New("group duplicate repository is not configured")
	}

	duplicate := CloneGroupForDuplicate(source, DuplicateGroupOperationID(id, actorScope, operationKey))
	SanitizeGroupReasoningEffortPolicy(duplicate)
	for copyNumber := 1; ; copyNumber++ {
		duplicate.Name = DuplicateGroupName(source.Name, copyNumber)
		duplicate.ID = 0
		duplicate.CreatedAt = time.Time{}
		duplicate.UpdatedAt = time.Time{}
		if err := s.groupDuplicateRepo.CreateFromSource(ctx, duplicate, source.ID); err == nil {
			hydrated, loadErr := s.groupRepo.GetByID(ctx, duplicate.ID)
			if loadErr != nil {
				return nil, fmt.Errorf("load duplicate group: %w", loadErr)
			}
			return hydrated, nil
		} else if !errors.Is(err, ErrGroupExists) {
			return nil, fmt.Errorf("create duplicate group: %w", err)
		}

		// A uniqueness conflict can be a generated name or an operation ID. Try recovering the operation first;
		// if it is absent, retry with the next copy name.
		recovered, recoverErr := s.RecoverDuplicateGroup(ctx, id, actorScope, operationKey)
		if recoverErr != nil {
			return nil, recoverErr
		}
		if recovered != nil {
			return recovered, nil
		}
	}
}
