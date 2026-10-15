package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// registry.go validates module setting ownership and prepares isolated
// participant changes.
// A combined request can touch settings owned by several modules, and each
// preparer needs its submitted fields and saved values before storage changes.
// Registration declares module names, flat request fields, and writable setting
// keys, rejecting overlaps during assembly. The registry keeps that declared
// order for preparation and stops before later participants after an error.
//
// Each participant receives copies of its owned JSON bytes and current-value
// map, so mutation during preparation leaves other inputs available for later
// participants. Returned write maps are cloned after ownership checking. Expectations
// and application closures remain the participant's supplied references, and the
// update session later combines them with the complete batch and post-commit work.

// Fields maps flat request names to JSON bytes, retaining null separately from omission.
type Fields map[string]json.RawMessage

// Participant declares one module's request fields, setting keys, and preparation
// callback. Its callback validates and computes changes for the update session;
// callback implementations leave persistence and runtime publication to Commit
// and the returned post-commit application work.
type Participant struct {
	Module string // unique settings module owner
	// Fields and Keys declare request and storage ownership for this participant.
	// NewRegistry copies their slice storage and rejects names owned by another
	// registration; Prepare restricts its input and returned Values to these lists.
	Fields []string // owned flat request names
	Keys   []string // owned persisted setting keys
	// Prepare receives copied inputs and returns changes for later persistence.
	// Its owner keeps returned Expected maps, pointed strings, and captured Apply
	// dependencies stable until Commit finishes; Registry clones the Values map.
	Prepare func(context.Context, Fields, map[string]string) (PreparedChange, error)
}

// Registry holds validated module registrations in their configured preparation order.
type Registry struct {
	participants []Participant // declared settings preparation order
}

// NewRegistry validates module, request-field, and setting-key ownership and
// returns an ordered registration list. Names must be nonempty and unique in
// their category, and each module supplies a Prepare callback. Declared field
// and key slices are copied, while callbacks and their captured dependencies remain shared.
func NewRegistry(participants ...Participant) (*Registry, error) {
	fields, keys, modules := map[string]string{}, map[string]string{}, map[string]bool{}
	result := &Registry{participants: make([]Participant, len(participants))}
	for i, p := range participants {
		if p.Module == "" || p.Prepare == nil || modules[p.Module] {
			return nil, fmt.Errorf("invalid or duplicate settings participant: %q", p.Module)
		}
		modules[p.Module] = true
		for _, pair := range []struct {
			values []string          // names declared by this registration
			owners map[string]string // owners already registered for names
			kind   string            // ownership category for error reporting
		}{{p.Fields, fields, "field"}, {p.Keys, keys, "key"}} {
			for _, value := range pair.values {
				if prior, exists := pair.owners[value]; value == "" || exists {
					return nil, fmt.Errorf("duplicate or empty settings %s %q: %s/%s", pair.kind, value, prior, p.Module)
				}
				pair.owners[value] = p.Module
			}
		}
		// Copy declaration slices before retaining the participant so later assembly
		// edits to constructor inputs cannot redirect this registry's request or storage ownership.
		p.Fields = slices.Clone(p.Fields)
		p.Keys = slices.Clone(p.Keys)
		result.participants[i] = p
	}
	return result, nil
}

// Prepare invokes participants in registration order with their copied owned
// request bytes and current values. It checks context cancellation before each
// participant and stops on preparation failure, a conflicting returned owner,
// or a returned write key outside that participant's declaration.
//
// Returned Values maps are cloned so later participant mutation cannot change
// those writes. Expected keys bypass the Values ownership check, and their maps
// and pointed strings remain shared alongside Apply closures. Participants follow
// Participant's precommit preparation rule. Changes are retained when they have
// writes or an Apply callback; expectation-only results are discarded. Callers
// pass the complete result to their update session.
func (r *Registry) Prepare(ctx context.Context, input Fields, current map[string]string) ([]PreparedChange, error) {
	changes := make([]PreparedChange, 0, len(r.participants))
	for _, p := range r.participants {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Isolate owned request bytes and saved values before calling the preparer
		// so an early module cannot mutate another participant's inputs or the caller's snapshot.
		fields := Fields{}
		values := map[string]string{}
		allowed := map[string]bool{}
		for _, field := range p.Fields {
			if value, ok := input[field]; ok {
				fields[field] = slices.Clone(value)
			}
		}
		for _, key := range p.Keys {
			allowed[key] = true
			if value, ok := current[key]; ok {
				values[key] = value
			}
		}
		change, err := p.Prepare(ctx, fields, values)
		if err != nil {
			return nil, err
		}
		if change.Module != "" && change.Module != p.Module {
			return nil, fmt.Errorf("settings participant %s returned owner %s", p.Module, change.Module)
		}
		change.Module = p.Module
		for key := range change.Values {
			if !allowed[key] {
				return nil, fmt.Errorf("settings participant %s returned unowned key %s", p.Module, key)
			}
		}
		// Retain a detached write map after ownership validation so a later change
		// to the participant's map cannot alter the batch supplied to Commit.
		change.Values = maps.Clone(change.Values)
		if len(change.Values) > 0 || change.Apply != nil {
			changes = append(changes, change)
		}
	}
	return changes, nil
}

// OwnedKeys returns a new slice containing all declared storage keys in
// registration order. A registry with no keys returns nil, and the caller can
// modify the slice without changing registration ownership.
func (r *Registry) OwnedKeys() []string {
	var keys []string
	for _, p := range r.participants {
		keys = append(keys, p.Keys...)
	}
	return keys
}
