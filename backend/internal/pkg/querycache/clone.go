package querycache

import (
	"maps"
	"reflect"
)

// ShallowMap copies top-level entries while sharing nested values.
// A nil input returns an allocated empty map.
func ShallowMap(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	maps.Copy(out, values)
	return out
}

// Clone recursively copies pointers, maps, slices, and exported struct fields.
// Inputs must be acyclic. Map keys, unexported fields, and other kinds remain shared
// or copied by value, so callers must treat references in those positions as immutable.
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

// cloneValue recursively copies supported reference types and exported struct fields.
func cloneValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
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
