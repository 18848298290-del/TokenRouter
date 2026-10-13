package usage

import "errors"

// A canceled usage write can have an unknown database outcome once processing
// has begun. Persistence markers distinguish a confirmed unwritten submission
// from a best-effort result that was dropped or abandoned while retaining the
// underlying error chain for inspection.

// Persistence dispositions classify write failures for errors.As inspection
// and the usage helper predicates; unknown leaves the database outcome unresolved.
const (
	usageLogCreateDispositionUnknown      usageLogCreateDisposition = iota // unresolved database write outcome
	usageLogCreateDispositionNotPersisted                                  // confirmed unwritten usage submission
	usageLogCreateDispositionDropped                                       // abandoned best-effort submission result
)

// usageLogCreateDisposition identifies the caller-visible outcome category of a
// failed usage write.
type usageLogCreateDisposition int

// UsageLogCreateError wraps a write failure with its persistence disposition.
// The disposition distinguishes a confirmed unwritten record from a dropped
// best-effort submission.
type UsageLogCreateError struct {
	err         error                     // underlying usage write error
	disposition usageLogCreateDisposition // persistence outcome classification marker
}

// Error returns the underlying failure text, or a generic usage-write message
// when the receiver has no error. Nil receivers are accepted so formatting a
// missing wrapper stays defined.
func (e *UsageLogCreateError) Error() string {
	if e == nil || e.err == nil {
		return "usage log create error"
	}
	return e.err.Error()
}

// Unwrap returns the underlying error for errors.Is and errors.As traversal.
// Nil receivers return nil, allowing callers to inspect absent wrappers through
// the same error-chain interface.
func (e *UsageLogCreateError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// MarkUsageLogCreateNotPersisted attaches the confirmed-unwritten marker to a
// nonnil error. Callers use it only after establishing that this submission did
// not reach persistence; nil input returns nil.
func MarkUsageLogCreateNotPersisted(err error) error {
	if err == nil {
		return nil
	}
	return &UsageLogCreateError{
		err:         err,
		disposition: usageLogCreateDispositionNotPersisted,
	}
}

// MarkUsageLogCreateDropped marks a nonnil best-effort submission error as
// dropped or abandoned. The marker alone does not establish the database
// outcome after enqueueing; nil input returns nil.
func MarkUsageLogCreateDropped(err error) error {
	if err == nil {
		return nil
	}
	return &UsageLogCreateError{
		err:         err,
		disposition: usageLogCreateDispositionDropped,
	}
}

// IsUsageLogCreateNotPersisted reports whether the first UsageLogCreateError
// found in the chain has the confirmed-unwritten disposition. Nil errors and
// chains with no such wrapper return false. Callers supply nonnil wrapper
// pointers in error chains; a typed-nil wrapper reached by errors.As panics.
func IsUsageLogCreateNotPersisted(err error) bool {
	if err == nil {
		return false
	}
	var target *UsageLogCreateError
	if !errors.As(err, &target) {
		return false
	}
	return target.disposition == usageLogCreateDispositionNotPersisted
}

// IsUsageLogCreateDropped reports whether the first UsageLogCreateError found
// in the chain has the dropped disposition. Nil errors and chains with no such
// wrapper return false. Callers supply nonnil wrapper pointers in error chains;
// a typed-nil wrapper reached by errors.As panics.
func IsUsageLogCreateDropped(err error) bool {
	if err == nil {
		return false
	}
	var target *UsageLogCreateError
	if !errors.As(err, &target) {
		return false
	}
	return target.disposition == usageLogCreateDispositionDropped
}
