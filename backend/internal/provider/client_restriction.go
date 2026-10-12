package provider

// OpenAI OAuth providers select unrestricted, Codex, or TLS-router client access.
// Unrestricted access bypasses matching; restricted policies first honor the
// configured ForceCLI override. TLS-router policy then checks a bound router and
// the caller's match result, while Codex policy reads client headers on demand.
//
// Header matching tries the official user agent, official originator, provider
// exceptions, and global exceptions in that order. The first match determines
// the reason returned to callers, so provider exceptions take precedence over
// matching global exceptions. The detector recognizes supplied metadata through
// injected predicates; request authentication and computing a trustworthy router
// match belong to its callers.

// Client restriction reasons identify the first matching policy branch or the
// missing evidence that rejects a restricted request, for caller diagnostics.
const (
	// CodexClientRestrictionReasonDisabled means the provider allows any client.
	CodexClientRestrictionReasonDisabled = "openai_oauth_client_policy_any"
	// CodexClientRestrictionReasonMatchedUA identifies an allowed official client user agent.
	CodexClientRestrictionReasonMatchedUA = "official_client_user_agent_matched"
	// CodexClientRestrictionReasonMatchedOriginator identifies an allowed official originator.
	CodexClientRestrictionReasonMatchedOriginator = "official_client_originator_matched"
	// CodexClientRestrictionReasonMatchedAllowedClient identifies a provider-level
	// named client exception.
	CodexClientRestrictionReasonMatchedAllowedClient = "allowed_client_matched"
	// CodexClientRestrictionReasonMatchedGlobalAllowedClient identifies a global
	// named client exception.
	CodexClientRestrictionReasonMatchedGlobalAllowedClient = "global_allowed_client_matched"
	// CodexClientRestrictionReasonNotMatchedUA means no allowed client identity matched.
	CodexClientRestrictionReasonNotMatchedUA = "official_client_user_agent_not_matched"
	// CodexClientRestrictionReasonForceCodexCLI identifies the ForceCodexCLI override.
	CodexClientRestrictionReasonForceCodexCLI = "force_codex_cli_enabled"
	// CodexClientRestrictionReasonMatchedTLSRouter identifies a match to the provider's TLS router.
	CodexClientRestrictionReasonMatchedTLSRouter = "tls_router_matched"
	// CodexClientRestrictionReasonNotMatchedTLSRouter means the provider's TLS
	// router did not match.
	CodexClientRestrictionReasonNotMatchedTLSRouter = "tls_router_not_matched"
	// CodexClientRestrictionReasonTLSRouterMissing means the policy requires a
	// router but none is bound.
	CodexClientRestrictionReasonTLSRouterMissing = "tls_router_missing"
)

// CodexClientRestrictionDetectionResult reports the client policy decision and its reason.
type CodexClientRestrictionDetectionResult struct {
	// Enabled selects whether callers enforce Matched. Reason identifies the first
	// matching or rejecting branch, and Policy names the resolved provider setting.
	// Unrestricted results have both Enabled and Matched false.
	Enabled bool   // restriction requires caller enforcement
	Matched bool   // restricted request passes selection
	Reason  string // selected branch diagnostic identifier
	Policy  string // resolved provider client policy
}

// CodexClientOptions supplies platform identity checks and the CLI override.
type CodexClientOptions struct {
	// ForceCLI accepts any request whose provider policy is restricted, including
	// TLS-router policy. It is checked before router binding or metadata reads.
	ForceCLI bool
	// OfficialUserAgent and OfficialOriginator must be callable when Codex metadata
	// matching reaches them. A matching user agent skips originator and exception
	// checks; the predicates define which supplied header spellings qualify.
	OfficialUserAgent  func(string) bool // official user-agent matching predicate
	OfficialOriginator func(string) bool // official originator matching predicate
	// AllowedClients is required when a reached provider or global exception list
	// is nonempty. It receives the same headers used for official identity checks.
	AllowedClients func(string, string, []string) bool
}

// ClientRestrictionDetector receives provider and router state and reads HTTP
// client metadata on demand.
type ClientRestrictionDetector interface {
	// DetectClient resolves provider policy and returns enforcement and match flags.
	// Metadata is read only when needed. The router flag must describe the selected
	// provider's bound router; callers enforce enabled, unmatched results themselves.
	DetectClient(func() (string, string), *Record, []string, bool) CodexClientRestrictionDetectionResult
}

// CodexClientDetector uses configured identity predicates to implement provider
// client-policy detection for HTTP execution and automatic provider probes.
type CodexClientDetector struct {
	Options CodexClientOptions /* configured identity matching callbacks */
}

// DetectCodexClient returns the provider's client restriction decision and its
// first applicable reason. Nil providers and unrestricted policies disable
// enforcement. Restricted policies honor ForceCLI before router or header checks,
// including acceptance of TLS-router policy through that override.
//
// TLS policy requires a positive bound router ID and trusts routerMatched from
// the caller. Codex policy invokes readClient for user agent and originator, then
// tries official predicates, provider exceptions, and global exceptions in order.
// Provide callable readers and predicates for the branches reached; bypass and
// TLS branches can use a nil reader. The detector compares metadata rather than
// authenticating the client, and callers reject enabled, unmatched decisions.
func DetectCodexClient(options CodexClientOptions, readClient func() (string, string), provider *Record, globalAllowedClients []string, routerMatched bool) CodexClientRestrictionDetectionResult {
	policy := OpenAIOAuthClientPolicyAny
	if provider != nil {
		policy = provider.GetOpenAIOAuthClientPolicy()
	}
	if provider == nil || policy == OpenAIOAuthClientPolicyAny {
		return CodexClientRestrictionDetectionResult{
			Enabled: false,
			Matched: false,
			Reason:  CodexClientRestrictionReasonDisabled,
			Policy:  policy,
		}
	}

	// The configured force override applies to every restricted policy before
	// router requirements. Probe and HTTP callers therefore share the same acceptance order.
	if options.ForceCLI {
		return CodexClientRestrictionDetectionResult{
			Enabled: true,
			Matched: true,
			Reason:  CodexClientRestrictionReasonForceCodexCLI,
			Policy:  policy,
		}
	}

	if policy == OpenAIOAuthClientPolicyTLSRouterMatchedOnly {
		if provider.GetTLSFingerprintRouterID() <= 0 {
			return CodexClientRestrictionDetectionResult{
				Enabled: true,
				Matched: false,
				Reason:  CodexClientRestrictionReasonTLSRouterMissing,
				Policy:  policy,
			}
		}
		if routerMatched {
			return CodexClientRestrictionDetectionResult{
				Enabled: true,
				Matched: true,
				Reason:  CodexClientRestrictionReasonMatchedTLSRouter,
				Policy:  policy,
			}
		}
		return CodexClientRestrictionDetectionResult{
			Enabled: true,
			Matched: false,
			Reason:  CodexClientRestrictionReasonNotMatchedTLSRouter,
			Policy:  policy,
		}
	}

	// Read metadata after policy bypass and TLS decisions, allowing callers to
	// supply lazy request headers or probe headers without constructing an HTTP request.
	userAgent, originator := readClient()
	if options.OfficialUserAgent(userAgent) {
		return CodexClientRestrictionDetectionResult{
			Enabled: true,
			Matched: true,
			Reason:  CodexClientRestrictionReasonMatchedUA,
			Policy:  policy,
		}
	}
	if options.OfficialOriginator(originator) {
		return CodexClientRestrictionDetectionResult{
			Enabled: true,
			Matched: true,
			Reason:  CodexClientRestrictionReasonMatchedOriginator,
			Policy:  policy,
		}
	}

	// A provider exception takes precedence over the global list when both match.
	// This branch records the provider-specific reason after official identity checks miss.
	if allowed := provider.GetCodexCLIOnlyAllowedClients(); len(allowed) > 0 &&
		options.AllowedClients(userAgent, originator, allowed) {
		return CodexClientRestrictionDetectionResult{
			Enabled: true,
			Matched: true,
			Reason:  CodexClientRestrictionReasonMatchedAllowedClient,
			Policy:  policy,
		}
	}

	// Provider exceptions may be absent or unmatched. Global exceptions then admit
	// a recognized named client and report the global source of that allowance.
	if len(globalAllowedClients) > 0 &&
		options.AllowedClients(userAgent, originator, globalAllowedClients) {
		return CodexClientRestrictionDetectionResult{
			Enabled: true,
			Matched: true,
			Reason:  CodexClientRestrictionReasonMatchedGlobalAllowedClient,
			Policy:  policy,
		}
	}

	return CodexClientRestrictionDetectionResult{
		Enabled: true,
		Matched: false,
		Reason:  CodexClientRestrictionReasonNotMatchedUA,
		Policy:  policy,
	}
}

// DetectClient returns a restriction decision using the detector's configured
// options. The detector must be nonnil; reader and predicate requirements follow
// the reached DetectCodexClient branches, and matched describes this provider's
// configured router result.
func (d *CodexClientDetector) DetectClient(read func() (string, string), value *Record, allowed []string, matched bool) CodexClientRestrictionDetectionResult {
	return DetectCodexClient(d.Options, read, value, allowed, matched)
}
