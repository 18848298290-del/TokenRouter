package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// sparkShadowAllowedCredentialKeys lists writable Spark shadow credential fields: model mappings.
// Validation and sanitization share this allowlist.
var sparkShadowAllowedCredentialKeys = map[string]struct{}{
	"model_mapping":         {},
	"compact_model_mapping": {},
}

// ResolveCredentialRecord resolves a shadow to its parent for credential and token access.
// Ordinary providers, including nil, are returned unchanged.
// Shadows require an existing, non-shadow OpenAI OAuth parent; lookup or validation failures return an error.
// Credential access, quota queries, and usage probes share this parent validation.
func ResolveCredentialRecord(ctx context.Context, read func(context.Context, int64) (*Record, error), provider *Record) (*Record, error) {
	if provider == nil || !provider.IsCredentialShadow() {
		return provider, nil
	}
	parent, err := read(ctx, *provider.ParentProviderID)
	if err != nil {
		return nil, fmt.Errorf("resolve spark shadow parent %d: %w", *provider.ParentProviderID, err)
	}
	if parent == nil {
		return nil, fmt.Errorf("spark shadow parent %d not found", *provider.ParentProviderID)
	}
	// Reject shadow chains that bypassed creation validation through manual writes or corrupted data.
	// Credential resolution follows exactly one parent link.
	if parent.IsCredentialShadow() {
		return nil, fmt.Errorf("spark shadow parent %d is itself a shadow", parent.ID)
	}
	if !parent.IsOpenAIOAuth() {
		return nil, fmt.Errorf("spark shadow parent %d is not OpenAI OAuth", parent.ID)
	}
	return parent, nil
}

// CreateShadow creates one Spark-dimension shadow for an OpenAI OAuth parent.
// The created Credentials map contains model_mapping; authentication tokens remain on the parent.
func (s *Admin) CreateShadow(ctx context.Context, parentID int64, opts ShadowOptions) (*Record, error) {
	// Load the parent and validate its platform and type.
	parent, err := s.providerRepo.GetByID(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("get parent provider: %w", err)
	}
	if !parent.IsOpenAIOAuth() {
		return nil, apperror.New(apperror.CategoryBadRequest, "SPARK_SHADOW_INVALID_PARENT",
			"spark shadow requires an OpenAI OAuth parent provider")
	}
	// The parent must own credentials because ResolveCredentialRecord follows one parent link.
	if parent.IsCredentialShadow() {
		return nil, apperror.New(apperror.CategoryBadRequest, "SPARK_SHADOW_PARENT_IS_SHADOW",
			"spark shadow parent must be a real provider, not another spark shadow")
	}

	// Reject an existing shadow before attempting creation.
	shadows, err := s.providerRepo.ListShadowsByParent(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("check existing spark shadows: %w", err)
	}
	if len(shadows) > 0 {
		return nil, apperror.New(apperror.CategoryConflict, "SPARK_SHADOW_ALREADY_EXISTS",
			"parent provider already has a spark shadow provider")
	}

	// Validate supplied groups when a group reader is configured; otherwise inherit the parent's groups.
	// An ungrouped parent leaves the shadow ungrouped; group names do not select defaults.
	groupIDs := opts.GroupIDs
	if len(groupIDs) > 0 {
		if s.options.Groups != nil {
			if err := s.ValidateGroupIDs(ctx, groupIDs); err != nil {
				return nil, err
			}
		}
	} else if len(parent.GroupIDs) > 0 {
		groupIDs = append([]int64(nil), parent.GroupIDs...)
	}

	// Create the shadow with model configuration and no authentication tokens.
	// A blank name defaults to "<parent name> (Spark)" and is limited to 100 runes.
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = parent.Name + " (Spark)"
	}
	if runes := []rune(name); len(runes) > 100 {
		name = string(runes[:100])
	}
	// Nonpositive concurrency inherits the parent value; zero means unlimited to the limiter.
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = parent.Concurrency
	}
	// Nonpositive priority inherits the parent value, including zero from name-only requests.
	// Lower values schedule first; inheriting avoids replacing the parent priority with an omitted zero.
	// The proxy follows the parent; omitted groups and concurrency also inherit parent values.
	priority := opts.Priority
	if priority <= 0 {
		priority = parent.Priority
	}
	shadow := &Record{
		Name:             name,
		Platform:         PlatformOpenAI,
		Type:             ProviderTypeOAuth,
		Status:           StatusActive,
		Credentials:      map[string]any{"model_mapping": s.options.ShadowModels()},
		ParentProviderID: &parentID,
		QuotaDimension:   QuotaDimensionSpark,
		ProxyID:          parent.ProxyID,
		Priority:         priority,
		Concurrency:      concurrency,
		Schedulable:      true,
	}

	// Creation assigns shadow.ID. A unique index rejects concurrent creation after the precheck.
	// A reread confirming the competing shadow converts that failure into a structured 409.
	if err := s.providerRepo.Create(ctx, shadow); err != nil {
		if existing, qerr := s.providerRepo.ListShadowsByParent(ctx, parentID); qerr == nil && len(existing) > 0 {
			return nil, apperror.New(apperror.CategoryConflict, "SPARK_SHADOW_ALREADY_EXISTS",
				"parent provider already has a spark shadow provider")
		}
		return nil, fmt.Errorf("create spark shadow: %w", err)
	}

	// Creation and group binding commit separately; on binding failure, delete the shadow so creation can be retried.
	// Cleanup ignores request cancellation. A process crash can still leave a partially created record.
	if len(groupIDs) > 0 {
		if err := s.providerRepo.BindGroups(ctx, shadow.ID, groupIDs); err != nil {
			if delErr := s.providerRepo.Delete(context.WithoutCancel(ctx), shadow.ID); delErr != nil {
				s.options.Error("spark_shadow_bind_groups_rollback_failed",
					"shadow_id", shadow.ID, "parent_id", parentID, "delete_err", delErr)
			}
			return nil, fmt.Errorf("bind groups for spark shadow: %w", err)
		}
		shadow.GroupIDs = groupIDs
	}

	return shadow, nil
}

// propagateProxyToShadows writes proxyID to each Spark shadow of parentID.
// Writes run synchronously and stop at the first error; earlier shadow updates may already be committed.
// WriteConfiguration applies ConfigProxyID and handles configuration publication.
// A provider with no shadows produces no writes.
func (s *Admin) propagateProxyToShadows(ctx context.Context, parentID int64, proxyID *int64) error {
	return PropagateProviderProxyToShadows(ctx, s.providerRepo, parentID, proxyID)
}

// PropagateProviderProxyToShadows copies a parent's proxy selection to its Spark shadows.
// Administrative edits and CRS synchronization share this update path.
func PropagateProviderProxyToShadows(ctx context.Context, repo ShadowProxyStore, parentID int64, proxyID *int64) error {
	shadows, err := repo.ListShadowsByParent(ctx, parentID)
	if err != nil {
		return fmt.Errorf("list spark shadows for proxy propagation: %w", err)
	}
	for _, shadow := range shadows {
		shadow.ProxyID = proxyID
		if err := WriteConfiguration(ctx, repo, shadow, ConfigurationChange{Fields: ConfigProxyID}); err != nil {
			return fmt.Errorf("update spark shadow %d proxy: %w", shadow.ID, err)
		}
	}
	return nil
}

func (s *Admin) RevertProviderProxyFallback(ctx context.Context, id int64) error {
	if err := s.providerRepo.RevertProxyFallback(ctx, id); err != nil {
		return err
	}
	// Reload the actual ProxyID after reverting, then propagate it to the shadows.
	provider, err := s.providerRepo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get provider after proxy revert: %w", err)
	}
	return s.propagateProxyToShadows(ctx, id, provider.ProxyID)
}

func (s *Admin) ValidateGroupIDs(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if s.options.Groups == nil {
		return errors.New("group repository not configured")
	}
	return s.options.Groups.ValidateGroups(ctx, ids)
}

func IsAllowedSparkShadowCredentialsUpdate(credentials map[string]any) bool {
	if credentials == nil {
		return true
	}
	for key := range credentials {
		if _, ok := sparkShadowAllowedCredentialKeys[key]; !ok {
			return false
		}
	}
	return true
}

func SanitizeSparkShadowCredentials(credentials map[string]any) map[string]any {
	if len(credentials) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(sparkShadowAllowedCredentialKeys))
	for key := range sparkShadowAllowedCredentialKeys {
		if value, ok := credentials[key]; ok && value != nil {
			out[key] = value
		}
	}
	return out
}

// ParentHealthyForShadow checks whether a Spark shadow can use its parent credentials for scheduling.
// Nil and ordinary providers pass; lookup supplies the parent from a snapshot or storage.
// The parent must be active OpenAI OAuth, pass the configured expiry check, and have no active temporary cooldown.
// Credential and transport cooldowns apply because the shadow shares the parent token and proxy.
// Parent rate-limit, overload, and manual scheduling flags do not govern this check.
// Spark has its own usage window; a missing or unusable parent excludes the shadow.
func ParentHealthyForShadow(provider *Record, lookup func(int64) *Record) bool {
	if provider == nil || !provider.IsShadow() {
		return true
	}
	parent := lookup(*provider.ParentProviderID)
	if parent == nil {
		return false
	}
	return parent.IsOpenAIOAuth() && parent.IsCredentialUsableForShadow()
}
