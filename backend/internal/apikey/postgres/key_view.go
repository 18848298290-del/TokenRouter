package postgres

import (
	"context"
	"database/sql"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	keycore "github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/identity/contact"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
)

// Key repository reads construct identity and group views from the Ent
// relationships selected by each query. Unselected fields stay at their entity
// zero values, and several returned slices and pointers retain entity storage
// until authentication snapshot conversion performs its own copying.

// SQLExecutor supplies the SQL operations used by Key count and presentation
// reads.
type SQLExecutor interface {
	// ExecContext executes the supplied SQL operation with caller arguments and
	// context, returning its result or failure. Key callers inspect affected-row
	// counts when ownership or deletion eligibility must be established.
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	// QueryContext returns rows for the supplied SQL and caller arguments.
	// Successful callers scan, inspect iteration errors and close the rows so the
	// shared database connection can be reused by later Key operations.
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// userEntityToKeyView maps a selected Ent user into its Key identity view,
// retaining nil input. Stored scalar pointers remain shared with the entity;
// notification recipient JSON is parsed into the user's recipient list when
// nonempty.
func userEntityToKeyView(u *dbent.User) *keycore.User {
	if u == nil {
		return nil
	}
	out := &keycore.User{
		ID:                         u.ID,
		Email:                      u.Email,
		Username:                   u.Username,
		Notes:                      u.Notes,
		PasswordHash:               u.PasswordHash,
		Role:                       u.Role,
		Balance:                    u.Balance,
		FrozenBalance:              u.FrozenBalance,
		Concurrency:                u.Concurrency,
		Status:                     u.Status,
		SignupSource:               u.SignupSource,
		LastLoginAt:                u.LastLoginAt,
		LastActiveAt:               u.LastActiveAt,
		TotpSecretEncrypted:        u.TotpSecretEncrypted,
		TotpEnabled:                u.TotpEnabled,
		TotpEnabledAt:              u.TotpEnabledAt,
		BalanceNotifyEnabled:       u.BalanceNotifyEnabled,
		BalanceNotifyThresholdType: u.BalanceNotifyThresholdType,
		BalanceNotifyThreshold:     u.BalanceNotifyThreshold,
		TotalRecharged:             u.TotalRecharged,
		RPMLimit:                   u.RpmLimit,
		APIKeyLimit:                u.APIKeyLimit,
		CreatedAt:                  u.CreatedAt,
		UpdatedAt:                  u.UpdatedAt,
		DeletedAt:                  u.DeletedAt,
	}
	// The saved recipient value accepts legacy strings and contact records;
	// parse either representation before deduction notices inspect the returned user view.
	if u.BalanceNotifyExtraEmails != "" && u.BalanceNotifyExtraEmails != "[]" {
		out.BalanceNotifyExtraEmails = contact.ParseNotifyEmails(u.BalanceNotifyExtraEmails)
	}
	return out
}

// groupEntityToKeyView maps selected Ent group fields into a hydrated routing
// view, retaining nil input. It decodes routing policy and copies advanced
// scheduler overrides; other entity collections and scalar pointers can remain
// shared, and unselected entity fields retain their zero values.
func groupEntityToKeyView(g *dbent.Group) *routing.Group {
	if g == nil {
		return nil
	}
	routingPolicy := routing.DecodeGroupRoutingPolicy(g.RoutingPolicy)
	return &routing.Group{
		ID:                              g.ID,
		Name:                            g.Name,
		Localization:                    g.Localization,
		Description:                     KeyDerefString(g.Description),
		SchedulerType:                   keycore.GroupSchedulerType(g.SchedulerType),
		AdvancedSchedulerOverrides:      accessview.CloneGroupAdvancedSchedulerOverrides(g.AdvancedSchedulerOverrides),
		DisplayBrand:                    g.DisplayBrand,
		RateMultiplier:                  g.RateMultiplier,
		IsExclusive:                     g.IsExclusive,
		Status:                          g.Status,
		Hydrated:                        true,
		DuplicateOperationID:            KeyDerefString(g.DuplicateOperationID),
		SessionIsolationEnabled:         g.SessionIsolationEnabled,
		AllowImageGeneration:            g.AllowImageGeneration,
		AllowBatchImageGeneration:       g.AllowBatchImageGeneration,
		RoutingPolicy:                   routingPolicy,
		ClaudeCodeOnly:                  g.ClaudeCodeOnly,
		FallbackGroupID:                 g.FallbackGroupID,
		FallbackGroupIDOnInvalidRequest: g.FallbackGroupIDOnInvalidRequest,
		UnavailableFallbackGroupID:      g.UnavailableFallbackGroupID,
		ModelRouting:                    g.ModelRouting,
		ModelRoutingEnabled:             g.ModelRoutingEnabled,
		MCPXMLInject:                    g.McpXMLInject,
		SupportedModelScopes:            g.SupportedModelScopes,
		SortOrder:                       g.SortOrder,
		AllowedProtocols:                g.AllowedProtocols,
		ProtocolFallbacks:               g.ProtocolFallbacks,
		ResponsesImagePolicy:            g.ResponsesImagePolicy,
		AllowMessagesDispatch:           g.AllowMessagesDispatch,
		AllowLive:                       g.AllowLive,
		ForceOpenAIFast:                 g.ForceOpenaiFast,
		OpenAIFastPolicy:                g.OpenaiFastPolicy,
		RequireOAuthOnly:                g.RequireOauthOnly,
		RequirePrivacySet:               g.RequirePrivacySet,
		DefaultMappedModel:              g.DefaultMappedModel,
		ModelsListConfig:                g.ModelsListConfig,
		AvailabilityProbeConfig:         g.AvailabilityProbeConfig,
		RPMLimit:                        g.RpmLimit,
		MaxReasoningEffort:              g.MaxReasoningEffort,
		MaxReasoningEffortOverLimit:     g.MaxReasoningEffortOverLimit,
		ReasoningEffortMappings:         g.ReasoningEffortMappings,
		CreatedAt:                       g.CreatedAt,
		UpdatedAt:                       g.UpdatedAt,
	}
}
