package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// archive_token.go decodes imported OpenAI ID Tokens into account metadata
// hints. Imported OpenAI ID Tokens can contain account hints even after
// expiry. Archive decoding reads those claims to fill metadata during import;
// signature and expiry verification stay with authentication callers, so
// decoding success does not authorize an imported token.

// DecodeArchiveIDToken extracts archive identity hints from the JWT payload
// without authenticating it. Malformed tokens return an error; absent user
// information returns nil hints, and callers separately validate any credential
// used for authentication.
func DecodeArchiveIDToken(token string) (*provider.ArchiveIdentityHints, error) {
	claims, err := openai.DecodeIDToken(token)
	if err != nil {
		return nil, err
	}
	info := claims.GetUserInfo()
	if info == nil {
		return nil, nil
	}
	return &provider.ArchiveIdentityHints{Email: info.Email, PlanType: info.PlanType, ChatGPTAccountID: info.ChatGPTAccountID, ChatGPTUserID: info.ChatGPTUserID, OrganizationID: info.OrganizationID}, nil
}
