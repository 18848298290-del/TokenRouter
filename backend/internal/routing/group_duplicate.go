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

// A duplication operation has two identities: its actor-scoped recovery digest
// and its generated administrative display name. Repeated keyed requests can
// recover a committed copy, while name collisions alone advance the numbered
// copy suffix.
//
// The configuration copy begins inactive and omits persistent identity and
// derived counts, then the duplicate repository writes its source memberships
// and scheduling event in one transaction. Reloading returns saved management
// data after that write. Collection cloning separates editable routing and
// protocol collections according to their supported shapes. The probe retry
// pointer remains shared by the value copy, and localized content is not
// assigned by the clone constructor. Name truncation reserves suffix space and
// counts Unicode runes.

// Duplication reserves bounded administrative names and an inactive saved state
// so copied groups require activation before serving ordinary routing requests.
const (
	MaxGroupNameRunes            = 100
	DuplicateGroupInactiveStatus = "inactive"
)

// DuplicateGroupOperationID builds a stable lookup identity from the source,
// actor scope, and operation key. A blank trimmed key disables recovery, and a
// blank actor scope becomes admin:0.
//
// The fixed duplicate-operation tag, trimmed actor scope, decimal source ID,
// and trimmed key are joined with NUL separators before SHA-256 hashing.
// Callers use a stable authenticated-actor scope so repeated keys recover
// within that administrator and source group. The digest is an identity lookup
// value rather than a generated group name.
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

// DuplicateGroupName creates a numbered copy name within MaxGroupNameRunes.
// Nonpositive copy numbers become one, and the source name is trimmed before
// adding the English copy suffix.
//
// Suffix space is reserved before truncating the base so the requested copy
// number survives the length limit. Truncation uses runes, keeping multibyte
// characters intact even when the administrative name is long. A blank source
// still yields the suffix, and callers check saved-name uniqueness through the
// duplicate repository.
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
		// Reserve the whole numbered suffix and truncate only the rune slice, keeping
		// a multibyte source name valid within the shared administrative length
		// ceiling.
		baseRunes = baseRunes[:maxBaseRunes]
	}
	return string(baseRunes) + suffix
}

// CloneGroupValuePointer allocates pointer storage for a shallow copy of one
// value and returns nil for nil input. Mutable members inside T remain shared;
// duplication uses this helper for scalar fallback IDs.
func CloneGroupValuePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// CloneGroupModelRouting copies the preference map and each provider-ID slice
// for a duplicate group. Nil input returns nil, and empty slices can become nil
// because append copies their elements into a nil destination.
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

// CloneGroupForDuplicate builds an inactive configuration copy carrying the
// supplied operation identity. Supply a non-nil source; persistent identity,
// timestamps, hydration, and provider counts start unset, and Localization
// starts with its zero value.
//
// Routing rules, protocol collections, preference lists, scheduler overrides,
// fallback pointers, and effort mappings receive copied storage.
// RoutingPolicy.Clone copies its supported feature-map and slice shapes; other
// dynamic feature values retain their supplied references.
// AvailabilityProbeConfig is assigned by value, so its MaxRetries pointer stays
// shared until a caller copies it before editing. Display order is retained
// from the source, and the copy name starts at the first suffix until
// persistence resolves conflicts.
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
		// Keep the value-copied config's retry count read-only while both groups
		// reference it. Direct callers of this clone helper copy MaxRetries before
		// editing either group's count.
		AvailabilityProbeConfig:     source.AvailabilityProbeConfig,
		RPMLimit:                    source.RPMLimit,
		MaxReasoningEffort:          source.MaxReasoningEffort,
		MaxReasoningEffortOverLimit: source.MaxReasoningEffortOverLimit,
		ReasoningEffortMappings:     append([]ReasoningEffortMapping(nil), source.ReasoningEffortMappings...),
	}
}

// CloneGroupClientProtocols returns an independent non-nil slice of client
// protocol IDs. Nil or empty input produces an empty selection, allowing a
// duplicate to keep closed admission rather than imply an omitted
// configuration.
func CloneGroupClientProtocols(protocols []protocol.ProtocolID) []protocol.ProtocolID {
	return append([]protocol.ProtocolID{}, protocols...)
}

// RecoverDuplicateGroup resolves a saved copy by source ID, actor scope, and
// operation key. Blank keys or absent saved operations return nil without an
// error; a matched copy is reloaded through GetByID to include management
// counts.
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

// DuplicateGroup returns a recovered operation or creates an inactive group
// from the current source configuration. A stable actor scope and key let later
// calls return an existing committed copy before loading the source again.
//
// The duplicate repository saves the copy, source memberships, and scheduling
// outbox together, then GetByID reloads the response. Uniqueness conflicts may
// identify either a saved operation or a generated name, so recovery is tried
// before advancing the suffix. Other errors return immediately; name conflicts
// have no fixed retry limit, and repository calls receive ctx.
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
	// Reuse one normalized configuration while resetting persistence-populated
	// identity and timestamps before each generated-name attempt; a failed write
	// can have populated those working fields.
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

		// Both name and operation-ID uniqueness use ErrGroupExists. Recover a saved
		// keyed copy first so the next suffix is tried only for an unresolved name
		// collision.
		recovered, recoverErr := s.RecoverDuplicateGroup(ctx, id, actorScope, operationKey)
		if recoverErr != nil {
			return nil, recoverErr
		}
		if recovered != nil {
			return recovered, nil
		}
	}
}
