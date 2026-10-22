package mixins

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"

	"github.com/TokenFlux/TokenRouter/ent/intercept"
)

// soft_delete.go implements Ent query filtering and delete-to-update
// conversion for entities with a deleted_at timestamp. Ordinary Ent queries
// hide rows whose deletion timestamp is set, while maintenance callers can use
// SkipSoftDelete to inspect deleted entities or remove them physically. Query
// traversal and mutation hooks apply the same active-row condition across
// schemas that include the mixin.
//
// Delete builders are created for deletion, so changing a mutation's operation
// also requires dispatching it through the generated client as an update. The
// helper obtains that client through reflection because generated Ent code
// imports the mixins package. The converted mutation retains its existing row
// predicates and adds the active-row condition before assigning the timestamp.

// SoftDeleteMixin implements retained deletion records and default active-row queries.
//
// Example:
//
//	func (User) Mixin() []ent.Mixin {
//	    return []ent.Mixin{
//	        mixins.SoftDeleteMixin{},
//	    }
//	}
type SoftDeleteMixin struct {
	mixin.Schema // reusable schema definition
}

// softDeleteKey marks contexts that bypass soft deletion.
// Its private type prevents collisions with keys from other packages.
type softDeleteKey struct{}

// Fields defines the nullable timestamp distinguishing active from deleted rows.
// Query filtering uses NULL as the active state; a retained deletion stores
// the application time in this PostgreSQL time-zone-aware column.
func (SoftDeleteMixin) Fields() []ent.Field {
	return []ent.Field{
		// Retained deletion timestamp.
		field.Time("deleted_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{
				dialect.Postgres: "timestamptz",
			}),
	}
}

// SkipSoftDelete derives a context that bypasses query filtering and delete conversion.
// Ent operations using the derived context can read retained rows, and delete
// operations remove their matching rows physically.
//
// Example:
//
//	// Include soft-deleted users.
//	users, err := client.User.Query().All(mixins.SkipSoftDelete(ctx))
//
//	// Physically delete the row.
//	client.User.DeleteOneID(id).Exec(mixins.SkipSoftDelete(ctx))
func SkipSoftDelete(parent context.Context) context.Context {
	return context.WithValue(parent, softDeleteKey{}, true)
}

// Interceptors adds a filter for active rows to Ent query traversals.
// The filter requires deleted_at IS NULL unless the context
// was created with SkipSoftDelete.
func (d SoftDeleteMixin) Interceptors() []ent.Interceptor {
	return []ent.Interceptor{
		intercept.TraverseFunc(func(ctx context.Context, q intercept.Query) error {
			if skip, _ := ctx.Value(softDeleteKey{}).(bool); skip {
				return nil
			}
			// Traversal applies the active-row condition to each participating entity
			// query, including edges followed by a larger query over mixed schemas.
			d.applyPredicate(q)
			return nil
		}),
	}
}

// Hooks converts delete mutations into updates that set the deletion timestamp.
// Non-delete operations and SkipSoftDelete contexts continue through the supplied
// mutator. Converted deletes require a generated mutation exposing SetOp,
// SetDeletedAt, and WhereP; an unexpected mutation type returns an error.
//
// The hook adds the active-row predicate before setting application time, so
// already deleted rows retain their timestamp. It redispatches through the
// mutation's generated client because the original builder still represents
// a delete operation, while the modified mutation needs an update builder.
func (d SoftDeleteMixin) Hooks() []ent.Hook {
	return []ent.Hook{
		func(next ent.Mutator) ent.Mutator {
			return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
				if m.Op() != ent.OpDelete && m.Op() != ent.OpDeleteOne {
					return next.Mutate(ctx, m)
				}
				if skip, _ := ctx.Value(softDeleteKey{}).(bool); skip {
					return next.Mutate(ctx, m)
				}
				// Schemas using this mixin generate the timestamp setter and SQL
				// predicate method needed to execute deletion as an active-row update.
				mx, ok := m.(interface {
					// SetOp selects the operation executed by the generated mutation client.
					SetOp(ent.Op)
					// SetDeletedAt supplies the timestamp stored by the converted update mutation.
					SetDeletedAt(time.Time)
					// WhereP appends SQL predicates restricting the rows changed by this mutation.
					WhereP(...func(*sql.Selector))
				})
				if !ok {
					return nil, fmt.Errorf("unexpected mutation type %T", m)
				}
				// Keep an existing deletion time when another delete targets the same
				// retained row; the active-row predicate excludes that row from this update.
				d.applyPredicate(mx)
				mx.SetOp(ent.OpUpdate)
				mx.SetDeletedAt(time.Now())
				return mutateWithClient(ctx, m)
			})
		},
	}
}

// applyPredicate restricts a query or mutation to rows with deleted_at IS NULL.
func (d SoftDeleteMixin) applyPredicate(w interface {
	// WhereP appends selector predicates used to restrict matching database rows.
	WhereP(...func(*sql.Selector))
},
) {
	w.WhereP(
		sql.FieldIsNull(d.Fields()[0].Descriptor().Name),
	)
}

// mutateWithClient dispatches a converted mutation through its generated client.
// Callers pass a non-nil context and mutation exposing Client(). The client
// returned by that mutation retains its driver, including an active transaction.
// A non-nil mutation error is propagated before checking the returned value. A
// nil result interface is rejected; an interface containing a typed nil value is
// returned.
//
// Reflection obtains Client() because the generated package imports mixins.
// Generated clients implement ent.Mutator and dispatch through that interface.
// Custom client signatures use reflected dispatch with their declared methods;
// missing methods or unexpected argument and result counts return errors, while
// incompatible argument types or unusable client values can panic.
func mutateWithClient(ctx context.Context, m ent.Mutation) (ent.Value, error) {
	clientMethod := reflect.ValueOf(m).MethodByName("Client")
	if !clientMethod.IsValid() || clientMethod.Type().NumIn() != 0 || clientMethod.Type().NumOut() != 1 {
		return nil, fmt.Errorf("soft delete: mutation client method not found for %T", m)
	}
	client := clientMethod.Call(nil)[0]
	// The operation changed after the delete builder was constructed. Dispatching
	// through Client.Mutate selects an update builder for that same mutation.
	var value ent.Value
	var err error
	// The declared client type must expose Mutate. Its dynamic value can also
	// implement ent.Mutator when Client() returns any, which hides the method.
	mutator, ok := reflect.TypeAssert[ent.Mutator](client)
	if ok && ctx != nil && client.Type().Implements(reflect.TypeFor[ent.Mutator]()) {
		value, err = mutator.Mutate(ctx, m)
	} else {
		// Custom signatures can accept these arguments while returning concrete
		// result types. Keep their declared method visibility and reflected checks,
		// including the rejection of a nil context before the method is invoked.
		value, err = mutateReflectedClient(ctx, m, client)
	}
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("soft delete: mutation client returned nil for %T", m)
	}
	return value, nil
}

// mutateReflectedClient invokes a custom client's declared Mutate method.
// Callers validate the returned value after handling its error, and the method
// types must accept ctx and m with two results.
//
// The second result is checked for nil before conversion to error. A nil
// concrete pointer therefore means success, while an error interface containing
// that pointer remains non-nil. Non-nil second results that do not implement
// error produce diagnostics; argument mismatches and non-nilable second results
// can panic.
func mutateReflectedClient(ctx context.Context, m ent.Mutation, client reflect.Value) (ent.Value, error) {
	mutateMethod := client.MethodByName("Mutate")
	if !mutateMethod.IsValid() {
		return nil, fmt.Errorf("soft delete: mutation client missing Mutate for %T", m)
	}
	if mutateMethod.Type().NumIn() != 2 || mutateMethod.Type().NumOut() != 2 {
		return nil, fmt.Errorf("soft delete: mutation client signature mismatch for %T", m)
	}

	results := mutateMethod.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(m)})
	value := results[0].Interface()
	var err error
	if !results[1].IsNil() {
		errValue := results[1].Interface()
		typedErr, ok := errValue.(error)
		if !ok {
			return nil, fmt.Errorf("soft delete: unexpected error type %T for %T", errValue, m)
		}
		err = typedErr
	}
	return value, err
}
