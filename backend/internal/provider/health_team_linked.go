package provider

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const (
	openAITeamLinkedErrorDedupTTL      = 60 * time.Second
	openAITeamLinkedErrorFanoutTimeout = 30 * time.Second
	OpenAITeamLinkedErrorBlockReason   = "team_linked_error"
)

// TeamLinkedStore lists providers by platform and writes each provider's error state separately.
type TeamLinkedStore interface {
	ListByPlatform(context.Context, string) ([]Record, error)
	SetError(context.Context, int64, string) error
}

// TeamLinkedOptions supplies runtime blocking, logging, and the clock.
type TeamLinkedOptions struct {
	Now   func() time.Time
	Warn  func(string, ...any)
	Block func(*Record, time.Time, string)
}

// TeamLinkedHealth owns process-local deduplication for workspace-wide error propagation.
type TeamLinkedHealth struct {
	providerRepo           TeamLinkedStore
	options                TeamLinkedOptions
	openaiTeamLinkedMu     sync.Mutex
	openaiTeamLinkedRecent map[string]time.Time
}

func NewTeamLinkedHealth(store TeamLinkedStore, options TeamLinkedOptions) *TeamLinkedHealth {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Warn == nil {
		options.Warn = func(string, ...any) {}
	}
	if options.Block == nil {
		options.Block = func(*Record, time.Time, string) {}
	}
	return &TeamLinkedHealth{providerRepo: store, options: options}
}

// HandleWorkspaceDeactivated marks other providers with the same ChatGPT account ID as errored.
// It blocks matching non-shadow records returned by the store before writing their state.
// The triggering provider is handled by the ordinary 402 path.
func (s *TeamLinkedHealth) HandleWorkspaceDeactivated(ctx context.Context, provider *Record, deactivated bool) {
	if s == nil || s.providerRepo == nil || !deactivated || provider == nil || !provider.IsOpenAIOAuthLike() {
		return
	}
	teamID := strings.TrimSpace(provider.GetChatGPTAccountID())
	if teamID == "" {
		return
	}
	if !s.markOpenAITeamLinkedFired(teamID) {
		return
	}
	// Persistence has a separate budget because the failed request may already be canceled.
	baseCtx := context.Background()
	if ctx != nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	opCtx, cancel := context.WithTimeout(baseCtx, openAITeamLinkedErrorFanoutTimeout)
	defer cancel()

	providers, err := s.providerRepo.ListByPlatform(opCtx, capability.PlatformOpenAI)
	if err != nil {
		s.options.Warn("openai_team_linked_error_list_failed", "trigger_provider_id", provider.ID, "error", err)
		return
	}
	var targets []*Record
	for i := range providers {
		acc := &providers[i]
		if acc.ID == provider.ID || acc.IsShadow() || strings.TrimSpace(acc.GetChatGPTAccountID()) != teamID {
			continue
		}
		targets = append(targets, acc)
	}
	if len(targets) == 0 {
		return
	}
	// Block all matching providers locally before writing their states one at a time.
	for _, acc := range targets {
		s.options.Block(acc, time.Time{}, OpenAITeamLinkedErrorBlockReason)
	}
	errorMsg := fmt.Sprintf("Workspace deactivated (402): team-linked error triggered by provider #%d", provider.ID)
	marked := 0
	for _, acc := range targets {
		// Continue after an individual write failure. The runtime block is already installed,
		// and another failure after the deduplication TTL can retry the persistent update.
		if err := s.providerRepo.SetError(opCtx, acc.ID, errorMsg); err != nil {
			s.options.Warn("openai_team_linked_error_set_error_failed", "provider_id", acc.ID, "error", err)
			continue
		}
		marked++
	}
	s.options.Warn("openai_team_linked_error_fanout",
		"trigger_provider_id", provider.ID,
		"chatgpt_account_id", teamID,
		"affected", marked,
		"targets", len(targets),
	)
}

// markOpenAITeamLinkedFired admits one propagation per team per TTL within this process.
func (s *TeamLinkedHealth) markOpenAITeamLinkedFired(teamID string) bool {
	now := s.options.Now()
	s.openaiTeamLinkedMu.Lock()
	defer s.openaiTeamLinkedMu.Unlock()
	if expiry, ok := s.openaiTeamLinkedRecent[teamID]; ok && expiry.After(now) {
		return false
	}
	if s.openaiTeamLinkedRecent == nil {
		s.openaiTeamLinkedRecent = make(map[string]time.Time)
	}
	for k, v := range s.openaiTeamLinkedRecent {
		if !v.After(now) {
			delete(s.openaiTeamLinkedRecent, k)
		}
	}
	s.openaiTeamLinkedRecent[teamID] = now.Add(openAITeamLinkedErrorDedupTTL)
	return true
}
