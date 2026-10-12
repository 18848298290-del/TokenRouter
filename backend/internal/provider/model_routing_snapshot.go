package provider

import (
	"maps"
)

// ModelRoutingSnapshot owns a copy of the mapping used for model matching.
// Resolve applies at most one mapping hop.
type ModelRoutingSnapshot struct {
	mapping map[string]string
}

// NewModelRoutingSnapshot copies the mapping for subsequent request matching.
func NewModelRoutingSnapshot(mapping map[string]string) ModelRoutingSnapshot {
	return ModelRoutingSnapshot{mapping: maps.Clone(mapping)}
}

func (s ModelRoutingSnapshot) Resolve(requested string) (string, bool) {
	return ResolveMappedModel(s.mapping, requested)
}
