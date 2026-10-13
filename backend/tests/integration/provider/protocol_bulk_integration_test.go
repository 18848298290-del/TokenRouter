//go:build integration

package provider_test

import (
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Bulk edits can assign different upstream protocol sets to providers in the
// same request. PostgreSQL assertions check those per-provider patches
// alongside a shared API-key update, then verify an empty protocol set and
// cleanup of the retired text-route field.

// TestUnifiedProtocolBulkUpdate edits two protocol sets with a shared API-key
// patch. Reads check both protocol sets, the first API key, and the second
// custom Extra; a later patch empties the first set and removes the retired
// text-route field.
func (s *ProviderRepoSuite) TestUnifiedProtocolBulkUpdate() {
	first := &provider.Record{Name: "protocol-one", Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Credentials: map[string]any{"api_key": "before", "upstream_protocols": []string{"anthropic_messages"}}, Extra: map[string]any{"keep": true}}
	second := &provider.Record{Name: "protocol-two", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Credentials: map[string]any{"api_key": "before", "upstream_protocols": []string{"openai_responses"}}, Extra: map[string]any{"keep": true}}
	s.Require().NoError(s.repo.Create(s.ctx, first))
	s.Require().NoError(s.repo.Create(s.ctx, second))
	count, err := s.repo.BulkUpdate(s.ctx, []int64{first.ID, second.ID}, provider.ProviderBulkUpdate{Credentials: map[string]any{"api_key": "after"}, ProtocolUpdates: map[int64]map[string]any{first.ID: {"upstream_protocols": []string{"openai_chat_completions"}, "api_base_urls": map[string]any{"chat_completions": "https://relay.example"}}, second.ID: {"upstream_protocols": []string{"openai_embeddings"}}}})
	s.Require().NoError(err)
	s.Require().Equal(int64(2), count)
	got, err := s.repo.GetByID(s.ctx, first.ID)
	s.Require().NoError(err)
	s.Require().Equal("after", got.GetCredential("api_key"))
	s.Require().Equal([]protocol.ProtocolID{protocol.ProtocolOpenAIChatCompletions}, got.UpstreamProtocols())
	got, err = s.repo.GetByID(s.ctx, second.ID)
	s.Require().NoError(err)
	s.Require().Equal([]protocol.ProtocolID{"openai_embeddings"}, got.UpstreamProtocols())
	s.Require().Equal(true, got.Extra["keep"])
	_, err = s.repo.BulkUpdate(s.ctx, []int64{first.ID}, provider.ProviderBulkUpdate{ProtocolUpdates: map[int64]map[string]any{first.ID: {"upstream_protocols": []string{}}}, Extra: map[string]any{"openai_text_route_mode": "force_responses"}})
	s.Require().NoError(err)
	got, err = s.repo.GetByID(s.ctx, first.ID)
	s.Require().NoError(err)
	s.Require().Empty(got.UpstreamProtocols())
	s.Require().NotContains(got.Extra, "openai_text_route_mode")
}
