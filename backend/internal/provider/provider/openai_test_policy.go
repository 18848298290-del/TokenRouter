package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Automatic OpenAI probes apply the same client restrictions and outbound
// identity priorities as gateway requests. Preparation reads the chosen
// User-Agent, supplies credential-owner defaults for OAuth, pairs the
// originator, and records the TLS router match before credential execution
// begins.
//
// Router-provided outbound agents win over record agents and forced CLI or
// browser replacement. Browser agents can use a dynamic configured value or the
// supplied default, while passthrough normalization depends on whether the
// earlier identity was a browser. TLS selection uses the recorded preparation
// flag to choose automatic or manual profile services, keeping the route
// decision and fingerprint source aligned for a probe. Callers configure
// required record readers and matching callbacks for the branches their
// providers can reach.

// OpenAIProbePolicy applies client admission and outbound identity choices to
// provider probes.
type OpenAIProbePolicy struct {
	// Available permits automatic preparation. A false value rejects automatic
	// probes while manual preparation still returns without evaluating policy.
	Available bool
	// ForceCLI supplies the built-in detector's forced-client rule and canonical
	// outbound agent. A nonblank matched router agent wins before forced
	// replacement.
	ForceCLI bool
	// Read resolves a shadow's credential owner when an automatic OAuth probe has
	// no supplied agent. Lookup errors abort preparation.
	Read func(context.Context, int64) (*provider.Record, error)
	// AllowClaudeCode optionally reads dynamic allowance when the record requires
	// Codex CLI. An absent callback adds no Claude Code client exemption.
	AllowClaudeCode func(context.Context) bool
	// BrowserUserAgent optionally reads the current browser replacement. Nonblank
	// values win over DefaultBrowserUserAgent when an OAuth request presents a
	// browser agent.
	BrowserUserAgent        func(context.Context) string
	DefaultBrowserUserAgent string // configured browser replacement default
	// Routers optionally matches the selected automatic agent against the record's
	// router ID. Missing services or nonpositive IDs leave the match unset.
	Routers interface {
		// MatchUserAgent returns the selected router's match and outbound identity or
		// fingerprint settings for an agent. Preparation retains the result for later
		// request and TLS policy.
		MatchUserAgent(int64, string) egress.TLSFingerprintRouterMatchResult
	}
	// Profiles and ManualProfiles resolve request fingerprints for prepared
	// automatic routes and other runs respectively. A nil selected service leaves
	// the probe without a profile.
	Profiles       *egressprovider.TLSProfiles // automatic probe fingerprint resolver
	ManualProfiles *egressprovider.TLSProfiles // manual probe fingerprint resolver
	// Detect optionally replaces built-in client admission. A nil callback uses
	// ForceCLI and the platform's official-agent, originator, and allowed-client
	// classifiers.
	Detect func(func() (string, string), *provider.Record, []string, egress.TLSFingerprintRouterMatchResult) provider.CodexClientRestrictionDetectionResult
}

// Prepare validates an automatic probe's client identity before executor
// credential lookup. Manual runs return immediately, while an unavailable
// automatic policy returns a configuration error. An empty OAuth agent is
// obtained from the credential owner or the canonical Codex value.
//
// Preparation replaces originator according to the final inbound agent, obtains
// its router match, and evaluates configured client restrictions with dynamic
// Claude Code allowance when applicable. A rejected identity returns before
// route publication. An accepted run publishes that match, including an
// unmatched result, so later request and TLS policy share the same preparation
// state.
func (p *OpenAIProbePolicy) Prepare(run *TestRun, value *provider.Record) error {
	if !run.Automatic {
		return nil
	}
	if !p.Available {
		return errors.New("OpenAI automatic probe routing is not configured")
	}
	userAgent := strings.TrimSpace(run.GetHeader("User-Agent"))
	if userAgent == "" && value.IsOpenAIOAuth() {
		credential := value
		if value.IsCredentialShadow() {
			resolved, err := provider.ResolveCredentialRecord(run.Context, p.Read, value)
			if err != nil {
				return err
			}
			credential = resolved
		}
		userAgent = strings.TrimSpace(credential.GetOpenAIUserAgent())
		// An OAuth record lacking a credential-owner agent still needs a recognized
		// probe identity before client restriction checks, so select canonical Codex
		// after that lookup.
		if userAgent == "" {
			userAgent = openai.CodexCanonicalUserAgent()
		}
	}
	if run.Headers == nil {
		run.Headers = make(http.Header)
	}
	run.Headers.Set("User-Agent", userAgent)
	// The selected probe agent can differ from the prepared originator. Rebuild
	// that companion header from the agent before evaluating client identity and
	// TLS routing.
	run.Headers.Del("originator")
	if originator, _, ok := openai.PairCodexClientIdentity(userAgent); ok {
		run.Headers.Set("originator", originator)
	}
	var match egress.TLSFingerprintRouterMatchResult
	if p.Routers != nil && value.GetTLSFingerprintRouterID() > 0 {
		match = p.Routers.MatchUserAgent(value.GetTLSFingerprintRouterID(), userAgent)
	}
	var allowed []string
	if value.IsCodexCLIOnlyEnabled() && p.AllowClaudeCode != nil && p.AllowClaudeCode(run.Context) {
		allowed = []string{openai.AllowedClientClaudeCode}
	}
	readClient := func() (string, string) { return run.GetHeader("User-Agent"), run.GetHeader("originator") }
	var result provider.CodexClientRestrictionDetectionResult
	if p.Detect != nil {
		result = p.Detect(readClient, value, allowed, match)
	} else {
		// The built-in path uses the same official and allowed-client predicates as
		// gateway admission when callers do not inject a specialized detector.
		result = provider.DetectCodexClient(provider.CodexClientOptions{ForceCLI: p.ForceCLI, OfficialUserAgent: openai.IsCodexOfficialClientRequestStrict, OfficialOriginator: openai.IsCodexOfficialClientOriginator, AllowedClients: openai.MatchAllowedClients}, readClient, value, allowed, match.Matched)
	}
	if result.Enabled && !result.Matched {
		return fmt.Errorf("OpenAI automatic probe rejected: policy=%s reason=%s", result.Policy, result.Reason)
	}
	run.SetAutomaticRoute(match)
	return nil
}

// ApplyTestRouting uses the run's prepared automatic route to set outbound
// identity. Unprepared runs retain ordinary routing except an automatic run's
// supplied agent; OAuth originator resolution and User-Agent precedence use the
// stored router match.
func (p *OpenAIProbePolicy) ApplyTestRouting(run *TestRun, value *provider.Record, req *http.Request, oauth bool) {
	match, automatic := run.AutomaticRoute()
	if !automatic {
		if run.Automatic {
			if agent := strings.TrimSpace(run.GetHeader("User-Agent")); agent != "" {
				req.Header.Set("User-Agent", agent)
			}
		}
		return
	}
	if agent := strings.TrimSpace(run.GetHeader("User-Agent")); agent != "" {
		req.Header.Set("User-Agent", agent)
	}
	if oauth {
		req.Header.Set("originator", openai.ResolveUpstreamOriginator(func() string { return run.GetHeader("originator") }, openai.IsCodexOfficialClientByHeaders(run.GetHeader("User-Agent"), run.GetHeader("originator")), match.Matched, match.UpstreamOriginator))
	}
	p.ApplyUserAgent(req.Context(), value, req, false, match)
}

// ApplyUserAgent chooses the outbound agent in router, record, forced-CLI, and
// browser-processing order. Nil requests are skipped; a nonempty matched router
// override returns immediately and therefore takes precedence over the
// remaining choices.
//
// Without that override, record configuration can replace the current agent
// before forced CLI selection. Browser detection captures the earlier value,
// then dynamic browser replacement runs. Passthrough OAuth requests that were
// not browsers and still lack a recognized official agent receive canonical
// Codex identity, so browser-derived replacement retains its separate handling.
func (p *OpenAIProbePolicy) ApplyUserAgent(ctx context.Context, value *provider.Record, req *http.Request, passthrough bool, match egress.TLSFingerprintRouterMatchResult) {
	if req == nil {
		return
	}
	// A matched router's nonblank outbound agent is a complete selection.
	// Returning here keeps record, forced-CLI, and browser choices from
	// overwriting that route policy.
	if match.Matched {
		if agent := strings.TrimSpace(match.UpstreamUserAgent); agent != "" {
			req.Header.Set("user-agent", agent)
			return
		}
	}
	if value != nil {
		if agent := strings.TrimSpace(value.GetOpenAIUserAgent()); agent != "" {
			req.Header.Set("user-agent", agent)
		}
	}
	if p.ForceCLI {
		req.Header.Set("user-agent", openai.CodexCanonicalUserAgent())
		return
	}
	// Capture browser identity before replacement, because a configured browser
	// agent can look nonofficial afterward and still belongs to the browser path
	// during passthrough normalization.
	wasBrowser := value != nil && value.Type == provider.ProviderTypeOAuth && openai.IsBrowserUserAgent(req.Header.Get("user-agent"))
	p.applyBrowserUserAgent(ctx, value, req)
	// Passthrough OAuth clients outside the browser path need recognized Codex
	// identity after other choices. Canonicalize remaining nonofficial agents
	// while keeping browser-derived replacements in their own path.
	if passthrough && value != nil && value.Type == provider.ProviderTypeOAuth && !wasBrowser && !openai.IsCodexOfficialClientRequest(req.Header.Get("user-agent")) {
		req.Header.Set("user-agent", openai.CodexCanonicalUserAgent())
	}
}

// applyBrowserUserAgent replaces browser agents on OAuth records with the
// supplied default or a nonblank dynamic setting. Nil requests, nil records,
// other authentication types, and nonbrowser agents leave headers unchanged.
func (p *OpenAIProbePolicy) applyBrowserUserAgent(ctx context.Context, value *provider.Record, req *http.Request) {
	if req == nil || value == nil || !value.IsOAuth() || !openai.IsBrowserUserAgent(req.Header.Get("user-agent")) {
		return
	}
	agent := p.DefaultBrowserUserAgent
	if p.BrowserUserAgent != nil {
		if configured := strings.TrimSpace(p.BrowserUserAgent(ctx)); configured != "" {
			agent = configured
		}
	}
	req.Header.Set("user-agent", agent)
}

// ResolveTestTLS resolves request fingerprinting from the run's recorded
// preparation state. Prepared automatic routes use Profiles, other runs use
// ManualProfiles, and a nil selected service returns nil; callers supply nonnil
// run and record values.
func (p *OpenAIProbePolicy) ResolveTestTLS(run *TestRun, value *provider.Record) *tlsfingerprint.Profile {
	match, automatic := run.AutomaticRoute()
	profiles := p.ManualProfiles
	if automatic {
		profiles = p.Profiles
	}
	if profiles == nil {
		return nil
	}
	return profiles.ResolveRequestTLS(egress.TLSSelection{Enabled: value.IsTLSFingerprintEnabled(), DirectProfileID: value.GetTLSFingerprintProfileID(), RouterMatched: match.Matched, RouterID: match.RouterID, RouterProfileID: match.TLSFingerprintProfileID})
}
