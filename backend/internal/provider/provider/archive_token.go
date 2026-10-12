package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// DecodeArchiveIDToken decodes imported OpenAI identity hints. The caller is responsible for authentication.
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
