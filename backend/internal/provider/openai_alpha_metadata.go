package provider

import (
	"context"
	"fmt"
	"maps"
	"strings"
)

// OpenAIAlphaMetadataPorts applies metadata to the request copy before persisting it.
type OpenAIAlphaMetadataPorts struct {
	Apply   func(map[string]any)
	Persist func(context.Context, map[string]any) error
}

// EnsureAlphaSearchMetadata validates PAT metadata when the ChatGPT account ID is missing and merges the
// result.
func (s *OpenAIAuthorization) EnsureAlphaSearchMetadata(ctx context.Context, record *Record, token, proxyURL string, ports OpenAIAlphaMetadataPorts) error {
	if record == nil || !record.IsOpenAIPersonalAccessToken() || strings.TrimSpace(record.GetChatGPTAccountID()) != "" {
		return nil
	}
	ctx, done, err := s.activity.begin(ctx, ErrProbeStopped)
	if err != nil {
		return err
	}
	defer done()
	info, err := s.Options.ValidatePAT(ctx, token, proxyURL)
	if err != nil {
		return fmt.Errorf("validate Codex PAT metadata for alpha/search: %w", err)
	}
	credentials := maps.Clone(record.Credentials)
	if credentials == nil {
		credentials = make(map[string]any)
	}
	maps.Copy(credentials, BuildOpenAIProviderCredentials(info))
	credentials = NormalizeOpenAIPersonalAccessTokenCredentials(record, info, credentials)
	record.Credentials = maps.Clone(credentials)
	ports.Apply(maps.Clone(credentials))
	if ports.Persist != nil {
		if err := ports.Persist(ctx, credentials); err != nil {
			return fmt.Errorf("persist Codex PAT metadata for alpha/search: %w", err)
		}
	}
	return nil
}
