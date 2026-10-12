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

// OpenAIProbePolicy applies request identity policy to probes and reads dynamic settings on demand.
type OpenAIProbePolicy struct {
	Available               bool
	ForceCLI                bool
	Read                    func(context.Context, int64) (*provider.Record, error)
	AllowClaudeCode         func(context.Context) bool
	BrowserUserAgent        func(context.Context) string
	DefaultBrowserUserAgent string
	Routers                 interface {
		MatchUserAgent(int64, string) egress.TLSFingerprintRouterMatchResult
	}
	Profiles       *egressprovider.TLSProfiles
	ManualProfiles *egressprovider.TLSProfiles
	Detect         func(func() (string, string), *provider.Record, []string, egress.TLSFingerprintRouterMatchResult) provider.CodexClientRestrictionDetectionResult
}

// Prepare checks automatic probe client eligibility before execution obtains credentials.
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
		if userAgent == "" {
			userAgent = openai.CodexCanonicalUserAgent()
		}
	}
	if run.Headers == nil {
		run.Headers = make(http.Header)
	}
	run.Headers.Set("User-Agent", userAgent)
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
		result = provider.DetectCodexClient(provider.CodexClientOptions{ForceCLI: p.ForceCLI, OfficialUserAgent: openai.IsCodexOfficialClientRequestStrict, OfficialOriginator: openai.IsCodexOfficialClientOriginator, AllowedClients: openai.MatchAllowedClients}, readClient, value, allowed, match.Matched)
	}
	if result.Enabled && !result.Matched {
		return fmt.Errorf("OpenAI automatic probe rejected: policy=%s reason=%s", result.Policy, result.Reason)
	}
	run.SetAutomaticRoute(match)
	return nil
}

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

// ApplyUserAgent uses gateway UA precedence and modifies the request headers.
func (p *OpenAIProbePolicy) ApplyUserAgent(ctx context.Context, value *provider.Record, req *http.Request, passthrough bool, match egress.TLSFingerprintRouterMatchResult) {
	if req == nil {
		return
	}
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
	wasBrowser := value != nil && value.Type == provider.ProviderTypeOAuth && openai.IsBrowserUserAgent(req.Header.Get("user-agent"))
	p.applyBrowserUserAgent(ctx, value, req)
	if passthrough && value != nil && value.Type == provider.ProviderTypeOAuth && !wasBrowser && !openai.IsCodexOfficialClientRequest(req.Header.Get("user-agent")) {
		req.Header.Set("user-agent", openai.CodexCanonicalUserAgent())
	}
}

// applyBrowserUserAgent replaces browser UAs with the configured or default probe UA.
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
