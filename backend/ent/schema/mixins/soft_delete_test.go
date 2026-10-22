package mixins

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"entgo.io/ent"
	"github.com/stretchr/testify/require"
)

// soft_delete_test.go characterizes client dispatch for converted delete
// mutations. The fixtures expose generated-style and custom method signatures
// so dispatch can retain client ownership, error ordering, and nil distinctions
// while reporting malformed client methods consistently.

// softDeleteClientMutation exposes a selected client through the generated method shape.
type softDeleteClientMutation[T any] struct {
	ent.Mutation     // unused generated mutation methods
	client       T   // selected mutation client
	clientCalls  int // client lookup count
}

// softDeleteCustomClient exposes independently typed arguments and results for reflected dispatch.
type softDeleteCustomClient[C, M, V, E any] func(C, M) (V, E)

// softDeleteMissingClient represents a mutation without a generated client accessor.
type softDeleteMissingClient struct {
	ent.Mutation // unused generated mutation methods
}

// softDeleteClientArgument represents an accessor with an unsupported argument count.
type softDeleteClientArgument struct {
	ent.Mutation // unused generated mutation methods
}

// softDeleteClientResults represents an accessor with an unsupported result count.
type softDeleteClientResults struct {
	ent.Mutation // unused generated mutation methods
}

// softDeleteMutateArgument represents a client with an unsupported mutation argument count.
type softDeleteMutateArgument struct{}

// softDeleteMutateResults represents a client with an unsupported mutation result count.
type softDeleteMutateResults struct{}

// softDeleteStubError allows concrete and interface error results to contain nil pointers.
type softDeleteStubError struct{}

// softDeleteNilClient accepts a nil receiver to characterize client pointer handling.
type softDeleteNilClient struct{}

// TestMutateWithClient_Ownership invokes the client selected by one mutation.
// It protects single dispatch and the identity of the context, mutation, and
// returned object that belong to the caller.
func TestMutateWithClient_Ownership(t *testing.T) {
	ctx := context.WithValue(context.Background(), softDeleteKey{}, "owner")
	want := new(int)
	calls := 0
	mutation := &softDeleteClientMutation[ent.MutateFunc]{}
	mutation.client = func(gotCtx context.Context, gotMutation ent.Mutation) (ent.Value, error) {
		calls++
		require.Same(t, ctx, gotCtx)
		require.Same(t, mutation, gotMutation)
		return want, nil
	}

	got, err := mutateWithClient(ctx, mutation)

	require.NoError(t, err)
	require.Same(t, want, got)
	require.Equal(t, 1, calls)
	require.Equal(t, 1, mutation.clientCalls)
}

// TestMutateWithClient_Results supplies nil values and errors through ent.Mutator.
// Mutation errors take precedence over missing values, and typed nil interfaces
// retain their dynamic type when returned to the caller.
func TestMutateWithClient_Results(t *testing.T) {
	mutationErr := errors.New("mutation failed")
	var typedNilValue *int
	var typedNilError *softDeleteStubError
	tests := []struct {
		name  string    // named result condition
		value ent.Value // configured mutation value
		err   error     // configured mutation error
	}{
		{name: "nil value"},
		{name: "typed nil value", value: typedNilValue},
		{name: "error before nil value", err: mutationErr},
		{name: "error discards value", value: 3, err: mutationErr},
		{name: "typed nil error", err: typedNilError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutation := &softDeleteClientMutation[ent.Mutator]{
				client: ent.MutateFunc(func(context.Context, ent.Mutation) (ent.Value, error) {
					return tt.value, tt.err
				}),
			}
			got, err := mutateWithClient(context.Background(), mutation)
			switch {
			case tt.err != nil:
				require.True(t, err == tt.err, "mutation error identity must be retained")
				require.Nil(t, got)
			case tt.value == nil:
				require.EqualError(t, err, fmt.Sprintf("soft delete: mutation client returned nil for %T", mutation))
				require.Nil(t, got)
			default:
				require.NoError(t, err)
				require.True(t, got != nil, "a typed nil value retains its dynamic type")
				require.Equal(t, tt.value, got)
			}
		})
	}
}

// TestMutateWithClient_CustomSignatures supplies callable methods with concrete
// results or broader arguments. It protects concrete nil-error handling and the
// result identity accepted by the reflected client dispatch.
func TestMutateWithClient_CustomSignatures(t *testing.T) {
	ctx := context.Background()
	t.Run("concrete result and nil concrete error", func(t *testing.T) {
		var mutation ent.Mutation
		client := softDeleteCustomClient[any, any, int, *softDeleteStubError](func(gotCtx, gotMutation any) (int, *softDeleteStubError) {
			require.Equal(t, ctx, gotCtx)
			require.Same(t, mutation, gotMutation)
			return 5, nil
		})
		mutation = mutationWithSoftDeleteClient(client)
		got, err := mutateWithClient(ctx, mutation)
		require.NoError(t, err)
		require.Equal(t, 5, got)
	})
	t.Run("concrete error before nil value", func(t *testing.T) {
		wantErr := &softDeleteStubError{}
		client := softDeleteCustomClient[context.Context, ent.Mutation, ent.Value, *softDeleteStubError](func(context.Context, ent.Mutation) (ent.Value, *softDeleteStubError) {
			return nil, wantErr
		})
		got, err := mutateWithClient(ctx, mutationWithSoftDeleteClient(client))
		require.Same(t, wantErr, err)
		require.Nil(t, got)
	})
	t.Run("nil concrete result", func(t *testing.T) {
		client := softDeleteCustomClient[context.Context, ent.Mutation, *int, error](func(context.Context, ent.Mutation) (*int, error) {
			return nil, nil
		})
		got, err := mutateWithClient(ctx, mutationWithSoftDeleteClient(client))
		require.NoError(t, err)
		require.True(t, got != nil, "the concrete result type survives a nil pointer")
		require.IsType(t, (*int)(nil), got)
	})
	t.Run("nil non-error result", func(t *testing.T) {
		client := softDeleteCustomClient[context.Context, ent.Mutation, int, *int](func(context.Context, ent.Mutation) (int, *int) {
			return 9, nil
		})
		got, err := mutateWithClient(ctx, mutationWithSoftDeleteClient(client))
		require.NoError(t, err)
		require.Equal(t, 9, got)
	})
	t.Run("nil concrete client", func(t *testing.T) {
		got, err := mutateWithClient(ctx, &softDeleteClientMutation[*softDeleteNilClient]{})
		require.NoError(t, err)
		require.Equal(t, "nil receiver", got)
	})
}

// TestMutateWithClient_Diagnostics supplies missing methods and unsupported
// method shapes. It protects diagnostic selection, including a client hidden
// behind an interface that does not expose Mutate.
func TestMutateWithClient_Diagnostics(t *testing.T) {
	tests := []struct {
		name     string       // named method condition
		mutation ent.Mutation // supplied mutation fixture
		message  string       // expected diagnostic prefix
	}{
		{name: "missing client", mutation: &softDeleteMissingClient{}, message: "mutation client method not found"},
		{name: "client argument count", mutation: &softDeleteClientArgument{}, message: "mutation client method not found"},
		{name: "client result count", mutation: &softDeleteClientResults{}, message: "mutation client method not found"},
		{name: "missing mutate", mutation: &softDeleteClientMutation[int]{}, message: "mutation client missing Mutate"},
		{name: "hidden mutate", mutation: &softDeleteClientMutation[any]{client: ent.MutateFunc(nil)}, message: "mutation client missing Mutate"},
		{name: "nil interface client", mutation: &softDeleteClientMutation[any]{}, message: "mutation client missing Mutate"},
		{name: "mutate argument count", mutation: &softDeleteClientMutation[softDeleteMutateArgument]{}, message: "mutation client signature mismatch"},
		{name: "mutate result count", mutation: &softDeleteClientMutation[softDeleteMutateResults]{}, message: "mutation client signature mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mutateWithClient(context.Background(), tt.mutation)
			require.Nil(t, got)
			require.EqualError(t, err, fmt.Sprintf("soft delete: %s for %T", tt.message, tt.mutation))
		})
	}
	t.Run("unexpected error before nil value", func(t *testing.T) {
		client := softDeleteCustomClient[context.Context, ent.Mutation, ent.Value, *int](func(context.Context, ent.Mutation) (ent.Value, *int) {
			return nil, new(int)
		})
		mutation := mutationWithSoftDeleteClient(client)
		got, err := mutateWithClient(context.Background(), mutation)
		require.Nil(t, got)
		require.EqualError(t, err, fmt.Sprintf("soft delete: unexpected error type *int for %T", mutation))
	})
}

// TestMutateWithClient_InvalidTypesPanic supplies types outside the dispatch
// calling rules. Unusable method arguments, scalar error results, and nil
// interfaces exposing Mutate panic before a value can reach the caller.
func TestMutateWithClient_InvalidTypesPanic(t *testing.T) {
	t.Run("incompatible argument type", func(t *testing.T) {
		client := softDeleteCustomClient[int, ent.Mutation, ent.Value, error](func(int, ent.Mutation) (ent.Value, error) {
			t.Fatal("incompatible arguments must be rejected before calling Mutate")
			return nil, nil
		})
		require.Panics(t, func() {
			_, _ = mutateWithClient(context.Background(), mutationWithSoftDeleteClient(client))
		})
	})
	t.Run("scalar error result", func(t *testing.T) {
		client := softDeleteCustomClient[context.Context, ent.Mutation, ent.Value, int](func(context.Context, ent.Mutation) (ent.Value, int) {
			return nil, 0
		})
		require.Panics(t, func() {
			_, _ = mutateWithClient(context.Background(), mutationWithSoftDeleteClient(client))
		})
	})
	t.Run("nil mutator interface", func(t *testing.T) {
		require.Panics(t, func() {
			_, _ = mutateWithClient(context.Background(), &softDeleteClientMutation[ent.Mutator]{})
		})
	})
	t.Run("nil context", func(t *testing.T) {
		client := ent.MutateFunc(func(context.Context, ent.Mutation) (ent.Value, error) {
			t.Fatal("a nil context must be rejected before calling Mutate")
			return nil, nil
		})
		require.Panics(t, func() {
			// A nil context violates the helper's calling rules and exercises the
			// reflection argument check before the supplied client can perform work.
			//nolint:staticcheck // Exercise the documented non-nil context requirement.
			_, _ = mutateWithClient(nil, mutationWithSoftDeleteClient(client))
		})
	})
}

// mutationWithSoftDeleteClient wraps a client with its concrete accessor result type.
func mutationWithSoftDeleteClient[T any](client T) *softDeleteClientMutation[T] {
	return &softDeleteClientMutation[T]{client: client}
}

// Client returns the fixture client and records each accessor invocation.
func (m *softDeleteClientMutation[T]) Client() T {
	m.clientCalls++
	return m.client
}

// Mutate delegates the supplied arguments to the custom method fixture.
func (c softDeleteCustomClient[C, M, V, E]) Mutate(ctx C, m M) (V, E) {
	return c(ctx, m)
}

// Client accepts an extra argument to exercise accessor shape rejection.
func (*softDeleteClientArgument) Client(int) int {
	return 0
}

// Client returns extra results to exercise accessor shape rejection.
func (*softDeleteClientResults) Client() (int, error) {
	return 0, nil
}

// Mutate omits arguments to exercise mutation method shape rejection.
func (softDeleteMutateArgument) Mutate() (ent.Value, error) {
	return nil, nil
}

// Mutate omits an error result to exercise method shape rejection.
func (softDeleteMutateResults) Mutate(context.Context, ent.Mutation) ent.Value {
	return nil
}

// Error reports the fixture error for concrete and interface results.
// It also accepts a nil receiver, allowing failed assertions to format typed
// nil errors while comparing their dynamic type.
func (*softDeleteStubError) Error() string {
	return "stub mutation failed"
}

// Mutate accepts a nil client receiver and returns its identifying value.
// The dispatch helper must invoke this receiver so the method can decide how
// to handle its own nil state.
func (*softDeleteNilClient) Mutate(context.Context, ent.Mutation) (ent.Value, error) {
	return "nil receiver", nil
}
