package provider

import (
	"maps"
)

// model_routing_snapshot.go captures a private model mapping for subsequent
// request resolution.
// A request's model mapping stays fixed while its provider configuration can
// be edited elsewhere. Capturing the mapping detaches its string entries from
// the source map, and resolution delegates exact and wildcard matching to the
// common one-hop mapping rule.

// ModelRoutingSnapshot retains copied model aliases for subsequent request matching.
type ModelRoutingSnapshot struct {
	// mapping contains constructor-copied string entries and is read by Resolve.
	// A zero snapshot has a nil mapping and resolves requests as unmatched.
	mapping map[string]string // retained model alias mapping
}

// NewModelRoutingSnapshot copies a mapping for subsequent request matching.
// Nil and empty inputs retain their collection shape, and later source-map edits
// cannot change the captured string entries used by Resolve. Keep the input map
// stable until copying finishes.
func NewModelRoutingSnapshot(mapping map[string]string) ModelRoutingSnapshot {
	return ModelRoutingSnapshot{mapping: maps.Clone(mapping)}
}

// Resolve applies the captured one-hop model mapping and reports a match.
// Exact and wildcard precedence follows ResolveMappedModel. A miss returns
// requested unchanged with false. A match returns its stored target with true,
// including empty and identity targets, so callers use the flag to distinguish
// a match from an unchanged request.
func (s ModelRoutingSnapshot) Resolve(requested string) (string, bool) {
	return ResolveMappedModel(s.mapping, requested)
}
