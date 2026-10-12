package provider

const (
	// CodexClientRestrictionReasonDisabled means the provider allows any client.
	CodexClientRestrictionReasonDisabled = "openai_oauth_client_policy_any"
	// CodexClientRestrictionReasonMatchedUA identifies an allowed official client user agent.
	CodexClientRestrictionReasonMatchedUA = "official_client_user_agent_matched"
	// CodexClientRestrictionReasonMatchedOriginator identifies an allowed official originator.
	CodexClientRestrictionReasonMatchedOriginator = "official_client_originator_matched"
	// CodexClientRestrictionReasonMatchedAllowedClient identifies a provider-level named client exception.
	CodexClientRestrictionReasonMatchedAllowedClient = "allowed_client_matched"
	// CodexClientRestrictionReasonMatchedGlobalAllowedClient identifies a global named client exception.
	CodexClientRestrictionReasonMatchedGlobalAllowedClient = "global_allowed_client_matched"
	// CodexClientRestrictionReasonNotMatchedUA means no allowed client identity matched.
	CodexClientRestrictionReasonNotMatchedUA = "official_client_user_agent_not_matched"
	// CodexClientRestrictionReasonForceCodexCLI identifies the ForceCodexCLI override.
	CodexClientRestrictionReasonForceCodexCLI = "force_codex_cli_enabled"
	// CodexClientRestrictionReasonMatchedTLSRouter identifies a match to the provider's TLS router.
	CodexClientRestrictionReasonMatchedTLSRouter = "tls_router_matched"
	// CodexClientRestrictionReasonNotMatchedTLSRouter means the provider's TLS router did not match.
	CodexClientRestrictionReasonNotMatchedTLSRouter = "tls_router_not_matched"
	// CodexClientRestrictionReasonTLSRouterMissing means the policy requires a router but none is bound.
	CodexClientRestrictionReasonTLSRouterMissing = "tls_router_missing"
)

// CodexClientRestrictionDetectionResult reports the client policy decision and its reason.
type CodexClientRestrictionDetectionResult struct {
	Enabled bool
	Matched bool
	Reason  string
	Policy  string
}

// CodexClientOptions supplies platform identity checks and the CLI override.
type CodexClientOptions struct {
	ForceCLI           bool
	OfficialUserAgent  func(string) bool
	OfficialOriginator func(string) bool
	AllowedClients     func(string, string, []string) bool
}

// ClientRestrictionDetector receives provider and router state and reads HTTP client metadata on demand.
type ClientRestrictionDetector interface {
	DetectClient(func() (string, string), *Record, []string, bool) CodexClientRestrictionDetectionResult
}

// CodexClientDetector binds platform options to DetectCodexClient.
type CodexClientDetector struct{ Options CodexClientOptions }

// DetectCodexClient reads client metadata only when the policy needs it, so probes need no HTTP request.
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

	// After official-client checks, try provider-level named client exceptions.
	if allowed := provider.GetCodexCLIOnlyAllowedClients(); len(allowed) > 0 &&
		options.AllowedClients(userAgent, originator, allowed) {
		return CodexClientRestrictionDetectionResult{
			Enabled: true,
			Matched: true,
			Reason:  CodexClientRestrictionReasonMatchedAllowedClient,
			Policy:  policy,
		}
	}

	// Then try named client exceptions from global settings.
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

func (d *CodexClientDetector) DetectClient(read func() (string, string), value *Record, allowed []string, matched bool) CodexClientRestrictionDetectionResult {
	return DetectCodexClient(d.Options, read, value, allowed, matched)
}
