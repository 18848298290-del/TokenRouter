package messageforward

import (
	"context"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	claude "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// OAuth metadata combines provider identity with a session UUID, and the
// session seed must remain stable as later conversation messages arrive. Parsed
// requests use the client IP, normalized user agent, and API key discriminator;
// body-based conversion uses the fingerprint client ID instead. Both paths
// select the first user text as the conversation component.
//
// An existing metadata.user_id suppresses generated metadata. Otherwise
// provider identity precedes fingerprint identity, with a generated client ID
// filling a remaining gap. The account UUID and fingerprint CLI version then
// select the encoded metadata format. Changing the first user text or
// discriminator changes the session seed even when the provider stays fixed.

// metadataUserID derives OAuth metadata for a parsed request lacking
// metadata.user_id. Nil requests or providers return empty, and provider client
// identity precedes the fingerprint client ID.
//
// The session seed uses the provider ID, request session discriminator, and
// first user text. Appending later turns leaves those inputs stable, while
// changing the opening text changes the derived UUID. The fingerprint CLI
// version controls metadata encoding, and a missing account UUID remains
// available to the delegated format helper as an empty value.
func metadataUserID(parsed *requeststate.ParsedRequest, provider *gatewayprovider.ExecutionProvider, fp *claude.Fingerprint) string {
	if parsed == nil || provider == nil {
		return ""
	}
	if parsed.MetadataUserID != "" {
		return ""
	}

	userID := strings.TrimSpace(provider.View().GetClaudeUserID())
	if userID == "" && fp != nil {
		userID = fp.ClientID
	}
	if userID == "" {
		// When both provider metadata and the fingerprint omit a client
		// identity, generate one so FormatMetadataUserID can still
		// encode a usable identity component.
		userID = claude.GenerateClientID()
	}

	// Derive the metadata UUID from the opening user text and client
	// discriminator so later appended turns retain this conversation
	// identity rather than a turn-specific routing hash.
	var firstUserText string
	if parsed.Body != nil {
		firstUserText = claude.ExtractFirstUserText(parsed.Body.Bytes())
	}
	seed := claude.BuildStableSessionSeed(provider.Record.ID, sessionContextDiscriminator(parsed.SessionContext), firstUserText)
	sessionID := upstream.GenerateSessionUUID(seed)

	// The fingerprint CLI version selects the metadata encoding expected by
	// that client, while the account UUID supplies the separate provider
	// account identity.
	var uaVersion string
	if fp != nil {
		uaVersion = claude.ExtractCLIVersion(fp.UserAgent)
	}
	providerUUID := strings.TrimSpace(provider.View().GetExtraString("account_uuid"))
	return claude.FormatMetadataUserID(userID, providerUUID, sessionID, uaVersion)
}

// metadataUserIDFromBody derives OAuth metadata from a body lacking
// metadata.user_id. A missing provider returns empty, and the provider client
// identity precedes a fingerprint client ID or generated identity.
//
// Body conversion uses the fingerprint client ID as its client discriminator,
// together with the provider ID and first user text. Later messages keep the
// seed stable while those inputs are unchanged. The account UUID and
// fingerprint CLI version choose metadata encoding; ctx is accepted but unused
// by this derivation.
func metadataUserIDFromBody(
	ctx context.Context,
	provider *gatewayprovider.ExecutionProvider,
	fp *claude.Fingerprint,
	body []byte,
) string {
	_ = ctx
	if provider == nil {
		return ""
	}
	if existing := gjson.GetBytes(body, "metadata.user_id").String(); existing != "" {
		return ""
	}

	userID := strings.TrimSpace(provider.View().GetClaudeUserID())
	if userID == "" && fp != nil {
		userID = fp.ClientID
	}
	if userID == "" {
		// A missing provider and fingerprint identity selects a
		// generated client ID so body conversion can still assemble the
		// required metadata identity component.
		userID = claude.GenerateClientID()
	}

	// Use the fingerprint client ID and opening text for body conversion so
	// appended messages keep their metadata UUID when those seed inputs
	// remain unchanged.
	var clientDiscriminator string
	if fp != nil {
		clientDiscriminator = fp.ClientID
	}
	seed := claude.BuildStableSessionSeed(provider.Record.ID, clientDiscriminator, claude.ExtractFirstUserText(body))
	sessionID := upstream.GenerateSessionUUID(seed)

	var uaVersion string
	if fp != nil {
		uaVersion = claude.ExtractCLIVersion(fp.UserAgent)
	}
	providerUUID := strings.TrimSpace(provider.View().GetExtraString("account_uuid"))
	return claude.FormatMetadataUserID(userID, providerUUID, sessionID, uaVersion)
}

// sessionContextDiscriminator joins client IP, normalized user agent, and API
// key ID for parsed-request session seeds. A nil session context returns empty,
// leaving the other seed components authoritative.
func sessionContextDiscriminator(sc *requeststate.SessionContext) string {
	if sc == nil {
		return ""
	}
	return sc.ClientIP + ":" + requeststate.NormalizeSessionUserAgent(sc.UserAgent) + ":" + strconv.FormatInt(sc.APIKeyID, 10)
}
