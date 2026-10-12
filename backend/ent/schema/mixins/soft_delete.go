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

// SoftDeleteMixin implements soft deletion using deleted_at.
//
// Default behavior:
//   - Deletes set deleted_at to the current application time.
//   - Ent queries filter for deleted_at IS NULL.
//   - SkipSoftDelete allows queries to include deleted rows and deletes to remove rows physically.
//
// Implementation:
//   - An Ent traversal interceptor adds the deleted_at IS NULL predicate.
//   - A mutation hook converts delete operations to updates of deleted_at.
//
// Example:
//
//	func (User) Mixin() []ent.Mixin {
//	    return []ent.Mixin{
//	        mixins.SoftDeleteMixin{},
//	    }
//	}
type SoftDeleteMixin struct {
	mixin.Schema
}

// softDeleteKey marks contexts that bypass soft deletion.
// Its private type prevents collisions with keys from other packages.
type softDeleteKey struct{}

// Fields defines the nullable deletion timestamp.
// deleted_at uses:
//   - PostgreSQL TIMESTAMPTZ for deletion time.
//   - Optional and Nillable so omitted values are NULL.
//   - NULL for active rows and a timestamp for deleted rows.
func (SoftDeleteMixin) Fields() []ent.Field {
	return []ent.Field{
		field.Time("deleted_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{
				dialect.Postgres: "timestamptz",
			}),
	}
}

// SkipSoftDelete derives a context that bypasses query filtering and delete conversion.
//
// Use it to:
//   - Include soft-deleted rows in queries.
//   - Delete rows physically.
//   - Access deleted rows for restoration.
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
			// The generated query wrapper supplies WhereP for adding the predicate across entity types.
			d.applyPredicate(q)
			return nil
		}),
	}
}

// Hooks converts Ent delete operations to timestamp updates.
// The hook uses time.Now from the application process for deleted_at.
// Rows remain in the table until deleted with SkipSoftDelete.
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
				// The generated mutation must support updating deleted_at and adding SQL predicates.
				mx, ok := m.(interface {
					SetOp(ent.Op)
					SetDeletedAt(time.Time)
					WhereP(...func(*sql.Selector))
				})
				if !ok {
					return nil, fmt.Errorf("unexpected mutation type %T", m)
				}
				// Exclude rows that have already been soft-deleted.
				d.applyPredicate(mx)
				mx.SetOp(ent.OpUpdate)
				mx.SetDeletedAt(time.Now())
				return mutateWithClient(ctx, m, next)
			})
		},
	}
}

// applyPredicate restricts a query or mutation to rows with deleted_at IS NULL.
func (d SoftDeleteMixin) applyPredicate(w interface{ WhereP(...func(*sql.Selector)) }) {
	w.WhereP(
		sql.FieldIsNull(d.Fields()[0].Descriptor().Name),
	)
}

// mutateWithClient dispatches the converted update through the generated client.
// Reflection avoids importing the generated ent package, which imports these mixins.
func mutateWithClient(ctx context.Context, m ent.Mutation, fallback ent.Mutator) (ent.Value, error) {
	clientMethod := reflect.ValueOf(m).MethodByName("Client")
	if !clientMethod.IsValid() || clientMethod.Type().NumIn() != 0 || clientMethod.Type().NumOut() != 1 {
		return nil, fmt.Errorf("soft delete: mutation client method not found for %T", m)
	}
	client := clientMethod.Call(nil)[0]
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
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("soft delete: mutation client returned nil for %T", m)
	}
	v, ok := value.(ent.Value)
	if !ok {
		return nil, fmt.Errorf("soft delete: unexpected value type %T for %T", value, m)
	}
	return v, nil
}
