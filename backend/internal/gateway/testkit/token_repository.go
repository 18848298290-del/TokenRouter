package testkit

import (
	"context"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// Refresh tests can supply an execution-provider store that also implements
// credential write capabilities. Wrapping just the read interface would hide
// those optional methods from the production refresh coordinator, even though
// the fixture can perform them.
//
// The adapter captures credential and Grok-success writer interfaces during
// construction and embeds exactly the capabilities the source exposes
// alongside converted record reads. Runtime type assertions therefore reach
// the same supported writes through the wrapper. A nil source remains a nil
// refresh repository, and record conversion preserves missing results and
// accompanying read errors. The fixture retains the source interfaces for the
// duration of its refresh test.

// tokenRepositoryReader adapts provider fixtures for production credential refresh.
type tokenRepositoryReader struct {
	source gatewayprovider.ExecutionProviderStore // retained execution-provider fixture reader
}

// GetByID converts the execution-store result through the production record
// copier and retains its error. A missing provider stays nil, so refresh
// callers can apply their normal absent-record handling.
func (r tokenRepositoryReader) GetByID(ctx context.Context, id int64) (*acctcore.Record, error) {
	v, err := r.source.GetByID(ctx, id)
	return gatewayprovider.ExecutionRecord(v), err
}

// TokenRepository adapts execution-provider reads while keeping optional
// refresh writers discoverable by production type assertions. A nil source
// returns nil, and the source is retained for later fixture operations.
//
// CredentialRefreshWriter and GrokRefreshSuccessWriter are detected separately
// when this wrapper is built. The returned anonymous wrapper embeds whichever
// of those capabilities exists alongside the converted read interface; a
// read-only source returns the base reader. This prevents adaptation from
// silently stripping the fixture's supported persistence methods during
// credential refresh.
func TokenRepository(repo gatewayprovider.ExecutionProviderStore) acctcore.RefreshRepository {
	if repo == nil {
		return nil
	}
	reader := tokenRepositoryReader{repo}
	writer, hasWriter := repo.(acctcore.CredentialRefreshWriter)
	grok, hasGrok := repo.(acctcore.GrokRefreshSuccessWriter)
	if hasWriter && hasGrok {
		return struct {
			acctcore.RefreshRepository        // converted provider record reads
			acctcore.CredentialRefreshWriter  // source credential persistence capability
			acctcore.GrokRefreshSuccessWriter // source Grok-success persistence capability
		}{reader, writer, grok}
	}
	if hasWriter {
		return struct {
			acctcore.RefreshRepository       // converted provider record reads
			acctcore.CredentialRefreshWriter // source credential persistence capability
		}{reader, writer}
	}
	if hasGrok {
		return struct {
			acctcore.RefreshRepository        // converted provider record reads
			acctcore.GrokRefreshSuccessWriter // source Grok-success persistence capability
		}{reader, grok}
	}
	return reader
}
