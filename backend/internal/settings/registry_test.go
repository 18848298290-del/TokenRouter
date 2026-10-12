package settings

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// registryParticipant constructs one module's ownership declarations with an empty
// preparer for registry tests. Callers replace that callback when testing mutation
// or failures, and construction alone performs no preparation.
func registryParticipant(module, field, key string) Participant {
	return Participant{Module: module, Fields: []string{field}, Keys: []string{key}, Prepare: func(context.Context, Fields, map[string]string) (PreparedChange, error) { return PreparedChange{}, nil }}
}

// TestRegistryRejectsDuplicateOwnershipAtConstruction adds a second declaration
// that duplicates a module, request field, or storage key. Each constructor call
// returns an error, protecting exclusive ownership before any participant prepares data.
func TestRegistryRejectsDuplicateOwnershipAtConstruction(t *testing.T) {
	for _, tc := range []struct {
		name   string      // duplicated ownership category label
		second Participant // conflicting second module registration
	}{
		{"field", registryParticipant("second", "field", "other")},
		{"key", registryParticipant("second", "other", "key")},
		{"module", registryParticipant("first", "other", "other")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewRegistry(registryParticipant("first", "field", "key"), tc.second); err == nil {
				t.Fatal("duplicate ownership accepted")
			}
		})
	}
}

// TestRegistryPreparesInOrderWithoutSharingMutableInput mutates constructor slices,
// participant inputs, and the returned source map. The registry preserves declared
// ownership and call order while retaining the caller's JSON/current values and
// a detached prepared write map.
func TestRegistryPreparesInOrderWithoutSharingMutableInput(t *testing.T) {
	input := Fields{"first": json.RawMessage(`{"enabled":true}`), "second": json.RawMessage(`false`), "secret": json.RawMessage(`"not-owned"`)}
	current := map[string]string{"one": "old-one", "two": "old-two", "unowned": "secret"}
	order := []string{}
	prepared := map[string]string{"one": "new-one"}
	first := registryParticipant("first", "first", "one")
	first.Prepare = func(_ context.Context, fields Fields, values map[string]string) (PreparedChange, error) {
		order = append(order, "first")
		if len(fields) != 1 || len(values) != 1 {
			t.Fatal("participant received another owner's data")
		}
		fields["first"][0] = '['
		values["one"] = "changed"
		return PreparedChange{Values: prepared}, nil
	}
	second := registryParticipant("second", "second", "two")
	second.Prepare = func(_ context.Context, fields Fields, values map[string]string) (PreparedChange, error) {
		order = append(order, "second")
		if string(fields["second"]) != "false" || values["two"] != "old-two" {
			t.Fatal("participant input changed")
		}
		return PreparedChange{Values: map[string]string{"two": "new-two"}}, nil
	}
	registry, err := NewRegistry(first, second)
	if err != nil {
		t.Fatal(err)
	}
	// Change the supplied declaration slices after construction so preparation
	// must use the registry's retained copies rather than those modified ownership names.
	first.Fields[0] = "secret"
	first.Keys[0] = "unowned"
	changes, err := registry.Prepare(context.Background(), input, current)
	if err != nil {
		t.Fatal(err)
	}
	prepared["one"] = "late-mutation"
	if !reflect.DeepEqual(order, []string{"first", "second"}) || string(input["first"]) != `{"enabled":true}` || current["one"] != "old-one" || changes[0].Values["one"] != "new-one" {
		t.Fatal("static registration, ordering or snapshot isolation changed")
	}
}

// TestRegistryRejectsUnownedWritesAndStopsAfterPrepareFailure supplies an unowned
// write, a preparer error, and a cancelled context. Each case stops preparation
// before later participant calls, and the expected error remains observable.
func TestRegistryRejectsUnownedWritesAndStopsAfterPrepareFailure(t *testing.T) {
	first := registryParticipant("first", "first", "one")
	first.Prepare = func(context.Context, Fields, map[string]string) (PreparedChange, error) {
		return PreparedChange{Values: map[string]string{"two": "forbidden"}}, nil
	}
	secondCalls := 0
	second := registryParticipant("second", "second", "two")
	second.Prepare = func(context.Context, Fields, map[string]string) (PreparedChange, error) {
		secondCalls++
		return PreparedChange{}, nil
	}
	registry, err := NewRegistry(first, second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Prepare(context.Background(), nil, nil); err == nil || secondCalls != 0 {
		t.Fatal("unowned write did not stop preparation")
	}
	expected := errors.New("validation failed")
	first.Prepare = func(context.Context, Fields, map[string]string) (PreparedChange, error) {
		return PreparedChange{}, expected
	}
	registry, err = NewRegistry(first, second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Prepare(context.Background(), nil, nil); !errors.Is(err, expected) || secondCalls != 0 {
		t.Fatal("validation failure did not stop preparation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = registry.Prepare(ctx, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled preparation proceeded")
	}
}
