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

// metadataUserID derives OAuth metadata from the provider and client session when the request has none.
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
		// Generate a valid client ID when provider metadata and the fingerprint omit one.
		userID = claude.GenerateClientID()
	}

	// The provider, client discriminator, and first user text keep session_id stable as messages are appended.
	// GenerateSessionHash serves sticky routing and can change between turns.
	var firstUserText string
	if parsed.Body != nil {
		firstUserText = claude.ExtractFirstUserText(parsed.Body.Bytes())
	}
	seed := claude.BuildStableSessionSeed(provider.Record.ID, sessionContextDiscriminator(parsed.SessionContext), firstUserText)
	sessionID := upstream.GenerateSessionUUID(seed)

	// Select the metadata format using the fingerprint's CLI version.
	var uaVersion string
	if fp != nil {
		uaVersion = claude.ExtractCLIVersion(fp.UserAgent)
	}
	providerUUID := strings.TrimSpace(provider.View().GetExtraString("account_uuid"))
	return claude.FormatMetadataUserID(userID, providerUUID, sessionID, uaVersion)
}

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
		userID = claude.GenerateClientID()
	}

	// Use a session seed so later body changes do not produce a different session_id.
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

// sessionContextDiscriminator combines client IP, normalized UA, and API key ID
// so different clients with the same opening message have distinct session seeds.
func sessionContextDiscriminator(sc *requeststate.SessionContext) string {
	if sc == nil {
		return ""
	}
	return sc.ClientIP + ":" + requeststate.NormalizeSessionUserAgent(sc.UserAgent) + ":" + strconv.FormatInt(sc.APIKeyID, 10)
}
