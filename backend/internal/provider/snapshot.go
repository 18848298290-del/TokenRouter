package provider

import (
	"slices"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// ProviderSnapshot holds the identity, eligibility, and runtime data used to select candidates.
// Callers perform model rewriting and credential lookup separately.
type ProviderSnapshot struct {
	// ModelPolicy is populated at model matching time so protocol prechecks do not read dynamic defaults.
	ModelPolicy      ModelRoutingSnapshot `json:"-"`
	ID               int64
	ParentProviderID *int64
	Platform         string
	Type             string
	AuthMode         string
	EnabledProtocols []capability.ProtocolID
	Status           string
	Schedulable      bool
	Concurrency      int
	Priority         int
	ExpiresAt        *time.Time
}

// RoutingSnapshot returns an independent copy with protocols resolved by the provider configuration.
func (r *Record) RoutingSnapshot() ProviderSnapshot {
	if r == nil {
		return ProviderSnapshot{}
	}
	snapshot := ProviderSnapshot{
		ID: r.ID, Platform: r.Platform, Type: r.Type, AuthMode: ProtocolAuthMode(r),
		EnabledProtocols: slices.Clone(r.UpstreamProtocols()), Status: r.Status, Schedulable: r.Schedulable,
		Concurrency: r.Concurrency, Priority: r.Priority,
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

// Protocols returns resolved capabilities with an independent protocol slice.
func (s ProviderSnapshot) Protocols() capability.ProviderProtocols {
	return capability.ProviderProtocols{Platform: s.Platform, Type: s.Type, AuthMode: s.AuthMode, Enabled: slices.Clone(s.EnabledProtocols)}
}
