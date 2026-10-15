package provider

import (
	"context"
	"net/http"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// credential_target.go resolves the credential-owning execution target and
// applies its ChatGPT account headers.
// Spark execution targets carry routing configuration while their OpenAI OAuth
// parent owns authentication credentials. Credential resolution keeps an
// ordinary target intact and reads a parent for a shadow, allowing header
// construction to use the account that supplies its token.
//
// The resolver converts records for provider-level validation but retains the
// loaded execution target beside that conversion. Comparing the resolved
// record with the input distinguishes ordinary targets from parent lookups.
// Returning the retained target keeps header construction attached to the same
// parent object loaded by the repository.

// ExecutionProviderReader loads execution targets for credential-parent
// resolution by provider ID.
type ExecutionProviderReader interface {
	// GetByID loads the execution target for a credential parent lookup.
	GetByID(context.Context, int64) (*ExecutionProvider, error)
}

// CredentialProvider returns the credential-owning execution target, reading
// one parent for a Spark shadow. Ordinary targets, including nil, return
// unchanged; the reader is required for shadows, whose parent must be a
// non-shadow OpenAI OAuth provider.
func CredentialProvider(ctx context.Context, repo ExecutionProviderReader, value *ExecutionProvider) (*ExecutionProvider, error) {
	input := ExecutionRecord(value)
	// The core validates the parent record, while retaining the loaded target lets
	// header callers use the same credential owner returned by the repository.
	var parent *ExecutionProvider
	resolved, err := providercore.ResolveCredentialRecord(ctx, func(ctx context.Context, id int64) (*providercore.Record, error) {
		var readErr error
		parent, readErr = repo.GetByID(ctx, id)
		return ExecutionRecord(parent), readErr
	}, input)
	if err != nil {
		return nil, err
	}
	if resolved == input {
		return value, nil
	}
	return parent, nil
}

// CredentialChatGPTHeaders resolves the credential owner and writes its
// ChatGPT account headers into headers. Parent lookup or validation errors
// stop header application; callers provide a writable header map and a reader
// when the target is a shadow.
func CredentialChatGPTHeaders(ctx context.Context, reader ExecutionProviderReader, headers http.Header, value *ExecutionProvider) error {
	resolved, err := CredentialProvider(ctx, reader, value)
	if err != nil {
		return err
	}
	provideradapter.SetChatGPTAccountHeaders(headers, resolved.View())
	return nil
}
