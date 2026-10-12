package provider

import (
	"context"
	"fmt"
	"maps"
	"strings"
)

// Alpha Search authentication needs a ChatGPT account identity that older PAT
// imports may lack. A PAT without that identity is validated through the
// authorization callback, and returned token metadata is merged with its
// existing credential configuration before OAuth-only fields are removed.
//
// The request record and required Apply callback receive separate top-level
// maps before optional persistence runs. Nested values from the existing
// credentials can remain shared, and a persistence failure leaves the already
// applied request metadata in place. Authorization activity tracking connects
// validation and storage work to service shutdown. This sequence lets the
// request use discovered account metadata, while callers retain ownership of
// concurrent access and of any durable write performed by Persist.

// OpenAIAlphaMetadataPorts supplies request-local application and optional
// storage for discovered PAT credentials.
type OpenAIAlphaMetadataPorts struct {
	// Apply is required when metadata validation runs and receives a separate
	// top-level credential copy before persistence, retaining any nested borrowed values.
	Apply func(map[string]any)
	// Persist is optional and runs after request-local application. Its error
	// leaves those local updates in place; the callback controls durable write behavior.
	Persist func(context.Context, map[string]any) error
}

// EnsureAlphaSearchMetadata fills missing ChatGPT account identity for an
// OpenAI PAT, returning nil when the record is absent, ineligible or already
// identified. Supply a nonnil context, callable validation and Apply
// callbacks, and exclusive access to the mutable record during this operation.
//
// Validation errors propagate before mutation. Successful metadata overwrites
// matching credentials in a shallow copy, then PAT normalization removes
// OAuth-only fields. The record and Apply receive separate top-level copies
// before optional Persist is called with the tracked context. Nested retained
// values remain shared; storage errors are wrapped and returned after local
// application, and an absent Persist leaves the update local.
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
	// Merge discovered identity with retained configuration before removing OAuth-only
	// state, so a legacy PAT gains account metadata without gaining refresh behavior.
	credentials := maps.Clone(record.Credentials)
	if credentials == nil {
		credentials = make(map[string]any)
	}
	maps.Copy(credentials, BuildOpenAIProviderCredentials(info))
	credentials = NormalizeOpenAIPersonalAccessTokenCredentials(record, info, credentials)
	// Install request-visible metadata before storage. A later Persist error leaves
	// both local recipients updated, and nested retained values remain borrowed.
	record.Credentials = maps.Clone(credentials)
	ports.Apply(maps.Clone(credentials))
	if ports.Persist != nil {
		if err := ports.Persist(ctx, credentials); err != nil {
			return fmt.Errorf("persist Codex PAT metadata for alpha/search: %w", err)
		}
	}
	return nil
}
