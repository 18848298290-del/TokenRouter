package testkit

import (
	"context"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// tokenRepositoryReader adapts provider fixtures for production credential refresh.
type tokenRepositoryReader struct {
	source gatewayprovider.ExecutionProviderStore
}

func (r tokenRepositoryReader) GetByID(ctx context.Context, id int64) (*acctcore.Record, error) {
	v, err := r.source.GetByID(ctx, id)
	return gatewayprovider.ExecutionRecord(v), err
}

func TokenRepository(repo gatewayprovider.ExecutionProviderStore) acctcore.RefreshRepository {
	if repo == nil {
		return nil
	}
	reader := tokenRepositoryReader{repo}
	writer, hasWriter := repo.(acctcore.CredentialRefreshWriter)
	grok, hasGrok := repo.(acctcore.GrokRefreshSuccessWriter)
	if hasWriter && hasGrok {
		return struct {
			acctcore.RefreshRepository
			acctcore.CredentialRefreshWriter
			acctcore.GrokRefreshSuccessWriter
		}{reader, writer, grok}
	}
	if hasWriter {
		return struct {
			acctcore.RefreshRepository
			acctcore.CredentialRefreshWriter
		}{reader, writer}
	}
	if hasGrok {
		return struct {
			acctcore.RefreshRepository
			acctcore.GrokRefreshSuccessWriter
		}{reader, grok}
	}
	return reader
}
