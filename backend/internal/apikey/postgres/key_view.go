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

type SQLExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

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
	// Accept both string-array and NotifyEmailEntry-array notification JSON.
	if u.BalanceNotifyExtraEmails != "" && u.BalanceNotifyExtraEmails != "[]" {
		out.BalanceNotifyExtraEmails = contact.ParseNotifyEmails(u.BalanceNotifyExtraEmails)
	}
	return out
}

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
