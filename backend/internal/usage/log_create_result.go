package usage

import "errors"

const (
	usageLogCreateDispositionUnknown usageLogCreateDisposition = iota
	usageLogCreateDispositionNotPersisted
	usageLogCreateDispositionDropped
)

type usageLogCreateDisposition int

// UsageLogCreateError wraps a write failure with its persistence disposition.
// The disposition distinguishes a confirmed unwritten record from a dropped best-effort submission.
type UsageLogCreateError struct {
	err         error
	disposition usageLogCreateDisposition
}

func (e *UsageLogCreateError) Error() string {
	if e == nil || e.err == nil {
		return "usage log create error"
	}
	return e.err.Error()
}

func (e *UsageLogCreateError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func MarkUsageLogCreateNotPersisted(err error) error {
	if err == nil {
		return nil
	}
	return &UsageLogCreateError{
		err:         err,
		disposition: usageLogCreateDispositionNotPersisted,
	}
}

func MarkUsageLogCreateDropped(err error) error {
	if err == nil {
		return nil
	}
	return &UsageLogCreateError{
		err:         err,
		disposition: usageLogCreateDispositionDropped,
	}
}

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
