package querycache

import (
	"maps"
	"reflect"
)

// Cached results can contain pointers inside maps, slices, interfaces, and
// exported struct fields. Clone recursively rebuilds those positions so
// supported mutable values can be changed by a caller independently of the
// stored snapshot. It starts a struct copy by value, then replaces exported
// fields with recursively copied values.
//
// Map keys, unexported fields, arrays, and other unsupported kinds retain their
// value or reference relationships. Inputs need to be acyclic because the
// traversal records no visited objects, and shared references can become
// separate copies. Pointer reconstruction uses reflect.New of the element type,
// which can change a named pointer's dynamic type; generic type assertion then
// decides whether the result fits T.

// ShallowMap returns a new top-level map while retaining each nested value as
// supplied. Nil input produces an allocated empty map, and callers copy mutable
// nested values when independent ownership is needed.
func ShallowMap(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	maps.Copy(out, values)
	return out
}

// Clone recursively copies supported pointer, map, slice, interface, and
// exported struct-field positions. Inputs must be acyclic, and callers treat
// references in map keys, unexported fields, arrays, and unsupported kinds as
// immutable while sharing snapshots.
//
// Traversal does not retain an alias registry, so repeated references can
// become distinct copies. Reflective pointer allocation uses the element type
// and can replace a named pointer with an unnamed pointer. Clone panics if the
// rebuilt result cannot be asserted as T; with T equal to any, a changed
// dynamic pointer type can be returned.
func Clone[T any](value T) T {
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return value
	}
	result, ok := reflect.TypeAssert[T](cloneValue(v))
	if !ok {
		panic("querycache: cloned value changed type")
	}
	return result
}

// cloneValue rebuilds supported reference containers and exported struct fields
// for Clone. Nil references retain their nil values; maps retain their keys,
// while arrays and unsupported kinds are returned as supplied.
//
// Structs are first copied by value so inaccessible fields remain intact before
// exported fields are replaced recursively. Pointer allocation derives from the
// element type, and traversal has no cycle or alias tracking. The caller
// supplies an acyclic value whose shared unsupported references remain
// immutable when the result is used.
func cloneValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		// Allocation from the element type creates an ordinary pointer. A named
		// pointer can therefore change dynamic type before Clone's final type assertion.
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(cloneValue(v.Elem()))
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(cloneValue(v.Elem()))
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		it := v.MapRange()
		for it.Next() {
			out.SetMapIndex(it.Key(), cloneValue(it.Value()))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(cloneValue(v.Index(i)))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		// Copy the entire struct first because unexported fields cannot be set
		// individually through reflection. Their existing reference values remain shared.
		out.Set(v)
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				out.Field(i).Set(cloneValue(v.Field(i)))
			}
		}
		return out
	default:
		return v
	}
}
