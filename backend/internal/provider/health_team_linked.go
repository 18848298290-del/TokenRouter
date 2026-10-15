package provider

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// health_team_linked.go propagates OpenAI workspace deactivation to matching
// account records with process-local deduplication.
// OpenAI workspace deactivation affects other non-shadow OpenAI records with
// the same saved ChatGPT account ID. Propagation claims a process-local team key before
// querying, so a failed lookup still delays another attempt until the
// deduplication interval expires.
//
// Returned teammates receive runtime blocks as a group before individual
// persistent error writes begin. An individual write failure leaves that block
// installed and does not stop later teammates. Persistence uses a context
// detached from request cancellation with its own deadline; the synchronous
// block callback has no context parameter and must return for persistence to
// proceed.

// openAITeamLinkedErrorDedupTTL and openAITeamLinkedErrorFanoutTimeout limit
// repeated team propagation and its detached store calls.
// OpenAITeamLinkedErrorBlockReason identifies runtime blocks installed for
// teammates.
const (
	openAITeamLinkedErrorDedupTTL      = 60 * time.Second    // team propagation interval
	openAITeamLinkedErrorFanoutTimeout = 30 * time.Second    // detached persistence budget
	OpenAITeamLinkedErrorBlockReason   = "team_linked_error" // teammate runtime reason
)

// TeamLinkedStore provides platform records and separate writes of provider
// error state.
type TeamLinkedStore interface {
	// ListByPlatform returns the platform records available for teammate
	// filtering.
	ListByPlatform(context.Context, string) ([]Record, error)
	// SetError records the supplied diagnostic for one provider's persistent error
	// state.
	SetError(context.Context, int64, string) error
}

// TeamLinkedOptions supplies the clock and callbacks for teammate propagation.
type TeamLinkedOptions struct {
	// Now timestamps deduplication entries before team admission acquires its
	// mutex. Concurrent handlers can call it together, so supply a clock safe for
	// those calls; NewTeamLinkedHealth uses time.Now when it is absent.
	Now func() time.Time
	// Warn receives query, write, and completion diagnostics; NewTeamLinkedHealth
	// replaces an absent callback with a no-op.
	Warn func(string, ...any)
	// Block receives each matched record before any persistent write, using a zero
	// deadline. NewTeamLinkedHealth supplies a no-op when absent.
	Block func(*Record, time.Time, string)
}

// TeamLinkedHealth coordinates teammate propagation with deduplication shared
// by its callers.
type TeamLinkedHealth struct {
	// providerRepo supplies propagation targets and error writes. A missing store
	// makes HandleWorkspaceDeactivated return before claiming a team key.
	providerRepo TeamLinkedStore
	options      TeamLinkedOptions // normalized propagation callbacks
	// openaiTeamLinkedMu protects all access to openaiTeamLinkedRecent. Each
	// admitted team receives an expiry before external work, and subsequent calls
	// share that process-local claim.
	openaiTeamLinkedMu     sync.Mutex           // team admission lock
	openaiTeamLinkedRecent map[string]time.Time // team admission expiries
}

// NewTeamLinkedHealth creates an idle propagation service using store and
// options. Missing Now uses time.Now, and missing Warn or Block becomes a
// no-op; construction performs no provider queries.
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

// HandleWorkspaceDeactivated propagates a confirmed workspace deactivation to
// other non-shadow records with the same trimmed ChatGPT account ID. Nil services
// or stores, ineligible triggers, missing IDs, and a live deduplication claim
// skip propagation. The triggering provider's ordinary 402 handling
// remains with its caller.
//
// The team claim precedes the store query and survives query or write failures.
// Runtime callbacks run for every matched target before separate error writes,
// and failed writes are logged while later targets continue. Store calls use a
// detached thirty-second context; Block receives that target's record
// synchronously and has no cancellation parameter.
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
	// Detach request cancellation while retaining context values, then bound the
	// store work independently so a canceled triggering request can still
	// propagate teammate errors.
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
	// The store writes can fail independently. Install every target's runtime
	// block first so failure on an early target still leaves later teammates
	// blocked locally.
	for _, acc := range targets {
		s.options.Block(acc, time.Time{}, OpenAITeamLinkedErrorBlockReason)
	}
	errorMsg := fmt.Sprintf("Workspace deactivated (402): team-linked error triggered by provider #%d", provider.ID)
	marked := 0
	for _, acc := range targets {
		// A failed write leaves its target's runtime block installed. Continue with
		// other targets; a later trigger can retry this team after deduplication
		// expires.
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

// markOpenAITeamLinkedFired reports whether this team can begin another
// propagation. It serializes claim checks and expiry updates, pruning expired
// teams only when a new claim is admitted.
//
// A live expiry rejects another claim for the same case-sensitive ID. Admission
// records the next expiry before releasing the lock, so concurrent handlers
// share one claim even while its caller is still querying or writing providers.
// Failures outside this helper do not roll back that claim.
func (s *TeamLinkedHealth) markOpenAITeamLinkedFired(teamID string) bool {
	now := s.options.Now()
	// Keep claim inspection, pruning and installation under one lock so concurrent
	// team observations cannot both admit propagation before either installs its
	// expiry.
	s.openaiTeamLinkedMu.Lock()
	defer s.openaiTeamLinkedMu.Unlock()
	if expiry, ok := s.openaiTeamLinkedRecent[teamID]; ok && expiry.After(now) {
		return false
	}
	if s.openaiTeamLinkedRecent == nil {
		s.openaiTeamLinkedRecent = make(map[string]time.Time)
	}
	// Cleanup is driven by admitted propagations. Repeated attempts within a live
	// team's TTL return earlier and therefore do not sweep other teams.
	for k, v := range s.openaiTeamLinkedRecent {
		if !v.After(now) {
			delete(s.openaiTeamLinkedRecent, k)
		}
	}
	s.openaiTeamLinkedRecent[teamID] = now.Add(openAITeamLinkedErrorDedupTTL)
	return true
}
