package assertion

import "fmt"

// type.go asserts concrete fixture types and reports mismatches with a panic.
// Test fixtures often carry payloads through an any value before an assertion
// needs their concrete type. MustType returns the asserted value directly and
// gives mismatches a diagnostic panic using %T for the expected zero value and
// the supplied value.

// MustType asserts value as T and returns the resulting fixture value. A
// mismatch panics with expected and actual type formatting; when T is an
// interface, its zero value formats as nil in that diagnostic.
func MustType[T any](value any) T {
	result, ok := value.(T)
	if !ok {
		var expected T
		panic(fmt.Sprintf("expected %T, got %T", expected, value))
	}
	return result
}
