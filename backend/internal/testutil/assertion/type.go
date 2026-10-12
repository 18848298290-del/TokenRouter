package assertion

import "fmt"

// MustType asserts that value has type T.
// A mismatch panics with the expected and actual types.
func MustType[T any](value any) T {
	result, ok := value.(T)
	if !ok {
		var expected T
		panic(fmt.Sprintf("expected %T, got %T", expected, value))
	}
	return result
}
