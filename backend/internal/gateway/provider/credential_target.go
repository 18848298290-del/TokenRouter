package provider

import (
	"context"
	"net/http"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// ExecutionProviderReader retrieves an execution target by ID.
type ExecutionProviderReader interface {
	GetByID(context.Context, int64) (*ExecutionProvider, error)
}

func CredentialProvider(ctx context.Context, repo ExecutionProviderReader, value *ExecutionProvider) (*ExecutionProvider, error) {
	input := ExecutionRecord(value)
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

// CredentialChatGPTHeaders resolves the credential provider before applying ChatGPT headers.
func CredentialChatGPTHeaders(ctx context.Context, reader ExecutionProviderReader, headers http.Header, value *ExecutionProvider) error {
	resolved, err := CredentialProvider(ctx, reader, value)
	if err != nil {
		return err
	}
	provideradapter.SetChatGPTAccountHeaders(headers, resolved.View())
	return nil
}
