package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// A Spark shadow uses its parent's OpenAI OAuth credentials and proxy while
// carrying its own Spark quota dimension and scheduling controls. Credential
// resolution follows one parent link and rejects a shadow parent, so stored
// chains cannot select another shadow as the credential owner.
//
// Creating a shadow validates the parent and groups before saving, then binds
// groups as a separate write. Binding failure triggers compensating deletion
// with request cancellation removed; a crash or failed cleanup can still leave
// the created row. The database uniqueness constraint handles competing
// creations after the precheck. Proxy propagation updates shadows sequentially
// and retains earlier writes if a later shadow fails. Parent health checks
// share configured expiration and temporary cooldown, while Spark scheduling
// evaluates its own independent quota and scheduling switch.

// sparkShadowAllowedCredentialKeys lists writable Spark shadow credential
// fields: model mappings.
// Validation and sanitization share this allowlist.
var sparkShadowAllowedCredentialKeys = map[string]struct{}{
	"model_mapping":         {},
	"compact_model_mapping": {},
}

// ResolveCredentialRecord returns the credential owner for a provider. Nil and
// ordinary records pass through unchanged; shadows call read for the parent ID
// and require a nonshadow OpenAI OAuth record.
//
// Resolution follows one link so a corrupted or manually written shadow chain
// cannot redirect authentication through another shadow. Lookup errors, absent
// parents, and incompatible parent types return errors before credential
// access. Callers retain ownership of the returned record; this helper returns
// the reader's object directly and does not copy it.
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
	// Reject shadow chains that bypassed creation validation through manual writes
	// or corrupted data.
	// Credential resolution follows exactly one parent link.
	if parent.IsCredentialShadow() {
		return nil, fmt.Errorf("spark shadow parent %d is itself a shadow", parent.ID)
	}
	if !parent.IsOpenAIOAuth() {
		return nil, fmt.Errorf("spark shadow parent %d is not OpenAI OAuth", parent.ID)
	}
	return parent, nil
}

// CreateShadow creates a Spark shadow attached to an OpenAI OAuth parent.
// Supplied groups are validated when a group reader is available; omitted
// groups inherit the parent's existing IDs, and nonpositive concurrency or
// priority inherits the parent value.
//
// A blank name derives from the parent, and names are capped at one hundred
// runes. The record holds model mappings while credential operations resolve to
// its parent. Creation and group binding are separate writes: a bind error
// attempts deletion with cancellation removed and returns the binding failure.
// Failed cleanup is logged, and the operations do not form one atomic
// transaction.
//
// An existing shadow returns a conflict. If creation fails after the precheck,
// a reread finding a shadow also returns that conflict, covering the
// competing-create path enforced by storage uniqueness.
func (s *Admin) CreateShadow(ctx context.Context, parentID int64, opts ShadowOptions) (*Record, error) {
	parent, err := s.providerRepo.GetByID(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("get parent provider: %w", err)
	}
	if !parent.IsOpenAIOAuth() {
		return nil, apperror.New(apperror.CategoryBadRequest, "SPARK_SHADOW_INVALID_PARENT",
			"spark shadow requires an OpenAI OAuth parent provider")
	}
	// The parent must own credentials because ResolveCredentialRecord follows one
	// parent link.
	if parent.IsCredentialShadow() {
		return nil, apperror.New(apperror.CategoryBadRequest, "SPARK_SHADOW_PARENT_IS_SHADOW",
			"spark shadow parent must be a real provider, not another spark shadow")
	}

	shadows, err := s.providerRepo.ListShadowsByParent(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("check existing spark shadows: %w", err)
	}
	if len(shadows) > 0 {
		return nil, apperror.New(apperror.CategoryConflict, "SPARK_SHADOW_ALREADY_EXISTS",
			"parent provider already has a spark shadow provider")
	}

	// Nonempty supplied groups take precedence and are validated when a reader
	// is configured. An omitted or empty selection inherits the parent's IDs;
	// an ungrouped parent stays ungrouped because names do not select defaults.
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

	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = parent.Name + " (Spark)"
	}
	// The persisted display name has a one-hundred-rune limit; truncating by
	// runes keeps a multibyte character intact at the resulting name's end.
	if runes := []rune(name); len(runes) > 100 {
		name = string(runes[:100])
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = parent.Concurrency
	}
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

	// Creation assigns shadow.ID. A unique index rejects concurrent creation after
	// the precheck.
	// A reread confirming the competing shadow converts that failure into a
	// structured 409.
	if err := s.providerRepo.Create(ctx, shadow); err != nil {
		if existing, qerr := s.providerRepo.ListShadowsByParent(ctx, parentID); qerr == nil && len(existing) > 0 {
			return nil, apperror.New(apperror.CategoryConflict, "SPARK_SHADOW_ALREADY_EXISTS",
				"parent provider already has a spark shadow provider")
		}
		return nil, fmt.Errorf("create spark shadow: %w", err)
	}

	// Creation and group binding commit separately; on binding failure, delete the
	// shadow so creation can be retried.
	// Cleanup ignores request cancellation. A process crash can still leave a
	// partially created record.
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

// propagateProxyToShadows sends the parent's proxy selection through the shared
// shadow update path. That path writes synchronously and stops on the first
// error, so earlier shadow updates can already be committed.
func (s *Admin) propagateProxyToShadows(ctx context.Context, parentID int64, proxyID *int64) error {
	return PropagateProviderProxyToShadows(ctx, s.providerRepo, parentID, proxyID)
}

// PropagateProviderProxyToShadows applies a parent's proxy selection to each
// returned shadow. Administration and CRS use ConfigProxyID through
// WriteConfiguration; nil clears the selected proxy.
//
// Updates run sequentially, stopping at the first failure and returning its
// shadow ID. Earlier successful updates remain committed, and the supplied
// proxy pointer is assigned to each reader-owned record before writing. A
// provider with no returned shadows causes no writes.
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

// RevertProviderProxyFallback restores the parent's stored proxy selection
// and propagates the reread selection to its shadows. A successful storage
// revert is followed by a fresh parent read so propagation uses the result of
// that write.
//
// Restoration and shadow updates use separate storage operations. A parent
// lookup failure can be returned after restoration succeeds, and propagation
// can fail after earlier shadow updates succeed. This function supplies no
// compensation for those writes. Callers account for storage's transaction
// completion rules and that partial progress before retrying.
func (s *Admin) RevertProviderProxyFallback(ctx context.Context, id int64) error {
	if err := s.providerRepo.RevertProxyFallback(ctx, id); err != nil {
		return err
	}
	// The revert can select the parent's previous proxy. Reread that committed
	// selection before propagating it so shadows follow the resulting stored
	// proxy rather than the pre-revert snapshot.
	provider, err := s.providerRepo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get provider after proxy revert: %w", err)
	}
	return s.propagateProxyToShadows(ctx, id, provider.ProxyID)
}

// ValidateGroupIDs delegates a nonempty selection to the configured group
// reader. Empty selections succeed immediately, while a nonempty selection with
// no group reader returns a configuration error.
func (s *Admin) ValidateGroupIDs(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if s.options.Groups == nil {
		return errors.New("group repository not configured")
	}
	return s.options.Groups.ValidateGroups(ctx, ids)
}

// IsAllowedSparkShadowCredentialsUpdate checks whether every submitted key is
// an allowed shadow model-mapping key. Nil and empty maps pass, and this key
// check leaves value validation to the owning update path.
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

// SanitizeSparkShadowCredentials returns a top-level map containing nonnil
// allowed mapping values. Nil or empty input yields an empty map; retained
// nested values remain shared with the caller's input.
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

// ParentHealthyForShadow reports whether a shadow's parent passes the shared
// credential-health checks. Nil and ordinary records pass immediately; shadows
// require lookup to return an OpenAI OAuth parent whose check succeeds.
//
// That check requires active status, an unexpired ExpiresAt when
// AutoPauseOnExpired is enabled, and an elapsed temporary cooldown. The parent's manual scheduling switch, global
// rate-limit reset, and overload window are omitted here because Spark uses its
// own scheduling and quota state. Callers evaluate the shadow's eligibility
// separately after this parent check.
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
