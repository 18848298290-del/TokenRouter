package provider

import (
	"slices"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// snapshot.go builds provider identity and protocol snapshots for candidate
// routing checks.
// Candidate protocol checks need provider identity and eligibility while
// credentials stay in the execution record. RoutingSnapshot captures those
// fields, resolves configured protocols, and detaches its protocol slice and
// optional identity/expiry pointers for later candidate checks. The diagnostic
// response-header name is copied for execution observation before dispatch.

// ProviderSnapshot holds provider identity, eligibility, and runtime selection data.
type ProviderSnapshot struct {
	// ModelPolicy is filled separately before CandidatePlan.ResolveModel applies
	// its captured aliases. RoutingSnapshot leaves this field as a zero mapping.
	ModelPolicy ModelRoutingSnapshot `json:"-"`
	ID          int64                // provider record identifier
	// ParentProviderID retains optional parent identity. RoutingSnapshot copies
	// its value so editing this pointer does not change the source record's parent.
	ParentProviderID *int64 // optional credential parent identifier
	Platform         string // configured upstream platform identity
	Type             string // configured provider authentication type
	AuthMode         string // resolved protocol authentication mode
	// EnabledProtocols is resolved and copied by RoutingSnapshot. Protocols
	// copies it again, allowing each returned capability slice to be edited separately.
	EnabledProtocols []capability.ProtocolID // resolved upstream protocol choices
	Status           string                  // stored provider lifecycle status
	Schedulable      bool                    // configured candidate scheduling flag
	Concurrency      int                     // configured request concurrency limit
	RequestIDHeader  string                  `json:"-"` // selected diagnostic response header
	Priority         int                     // configured candidate ordering priority
	// ExpiresAt retains the optional stored expiry. RoutingSnapshot copies that
	// value so changing the snapshot's timestamp leaves the source record unchanged.
	ExpiresAt *time.Time // optional provider expiry time
}

// RoutingSnapshot copies selection fields with protocols resolved from the
// record's configuration. Nil records return a zero snapshot; parent, expiry,
// and protocol values are detached, while ModelPolicy is left for model matching.
// Keep the source record stable during capture so its identity and protocol
// fields describe the same configuration.
func (r *Record) RoutingSnapshot() ProviderSnapshot {
	if r == nil {
		return ProviderSnapshot{}
	}
	snapshot := ProviderSnapshot{
		ID: r.ID, Platform: r.Platform, Type: r.Type, AuthMode: ProtocolAuthMode(r),
		EnabledProtocols: slices.Clone(r.UpstreamProtocols()), Status: r.Status, Schedulable: r.Schedulable,
		Concurrency: r.Concurrency, Priority: r.Priority, RequestIDHeader: UpstreamRequestIDHeaderName(r),
	}
	if r.ParentProviderID != nil {
		id := *r.ParentProviderID
		snapshot.ParentProviderID = &id
	}
	if r.ExpiresAt != nil {
		at := *r.ExpiresAt
		snapshot.ExpiresAt = &at
	}
	return snapshot
}

// Protocols returns resolved provider capabilities with a copied Enabled
// slice. Callers can change the returned protocol choices independently, and
// nil versus empty choices retain their meaning for capability resolution.
func (s ProviderSnapshot) Protocols() capability.ProviderProtocols {
	return capability.ProviderProtocols{Platform: s.Platform, Type: s.Type, AuthMode: s.AuthMode, Enabled: slices.Clone(s.EnabledProtocols)}
}
