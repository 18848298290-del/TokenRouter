package apperror

import (
	"errors"
	"fmt"
	"maps"
)

// Application failures carry a stable category and reason alongside the text
// exposed to clients. Unknown failures receive a generic message while their
// cause remains available to internal logging through the error chain.

// UnknownCode, UnknownReason, and UnknownMessage supply the public status for
// errors that have no ApplicationError in their chain.
const (
	UnknownCode    = 500              // unclassified failure category
	UnknownReason  = ""               // absent application reason
	UnknownMessage = "internal error" // public generic failure text
)

// Status contains the client-visible error description serialized by response
// adapters.
type Status struct {
	Code    int32  `json:"code"`             // stable numeric category
	Reason  string `json:"reason,omitempty"` // application reason identifier
	Message string `json:"message"`          // public response text
	// Metadata is mutable response data. Clone and the With methods copy its map;
	// callers coordinate any concurrent access to each resulting map.
	Metadata map[string]string `json:"metadata,omitempty"` // serialized string detail map
}

// ApplicationError carries a status and an optional diagnostic cause for
// error-chain inspection. Protocol adapters use Status for responses, while
// errors.Is compares category and reason independently of message text.
type ApplicationError struct {
	Status       // public response description
	cause  error // shared diagnostic cause
}

// Newf creates an application error whose message uses fmt.Sprintf formatting.
func Newf(code Category, reason, format string, a ...any) *ApplicationError {
	return New(code, reason, fmt.Sprintf(format, a...))
}

// Error formats the status and diagnostic cause for internal error reporting. A
// nil receiver returns the literal <nil>; the formatted text can include
// metadata and underlying error details.
func (e *ApplicationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.cause == nil {
		return fmt.Sprintf("error: code=%d reason=%q message=%q metadata=%v", e.Code, e.Reason, e.Message, e.Metadata)
	}
	return fmt.Sprintf("error: code=%d reason=%q message=%q metadata=%v cause=%v", e.Code, e.Reason, e.Message, e.Metadata, e.cause)
}

// Unwrap returns the shared diagnostic cause for standard error-chain
// traversal. The receiver must be nonnil, and a missing cause ends traversal by
// returning a nil error.
func (e *ApplicationError) Unwrap() error {
	return e.cause
}

// Is reports whether the target chain contains an ApplicationError with
// matching Code and Reason. Message and metadata are excluded from this
// comparison; the receiver and matching target must be nonnil.
func (e *ApplicationError) Is(err error) bool {
	if se := new(ApplicationError); errors.As(err, &se) {
		return se.Code == e.Code && se.Reason == e.Reason
	}
	return false
}

// WithCause returns a status copy with cause attached for error-chain
// inspection. Metadata is copied by Clone, while cause remains shared; the
// receiver must be nonnil before attaching the supplied error.
func (e *ApplicationError) WithCause(cause error) *ApplicationError {
	err := Clone(e)
	err.cause = cause
	return err
}

// WithMetadata returns an error copy with an independently allocated string
// metadata map. Nil md removes metadata from the copy, the existing cause
// remains shared, and the receiver must be nonnil.
func (e *ApplicationError) WithMetadata(md map[string]string) *ApplicationError {
	err := Clone(e)
	if md == nil {
		err.Metadata = nil
		return err
	}
	err.Metadata = make(map[string]string, len(md))
	maps.Copy(err.Metadata, md)
	return err
}

// New creates an application error from category, reason, and public message.
func New(code Category, reason, message string) *ApplicationError {
	return &ApplicationError{
		Status: Status{
			Code:    int32(code),
			Message: message,
			Reason:  reason,
		},
	}
}

// CategoryOf resolves an application category through wrappers, using
// CategoryOK for a nil error. Unknown errors become CategoryInternalServer; a
// chain containing a typed nil ApplicationError panics when Code is accessed.
func CategoryOf(err error) Category {
	if err == nil {
		return CategoryOK
	}
	return Category(FromError(err).Code)
}

// Reason returns an application reason found through the supplied error chain.
// Nil and unrecognized errors produce UnknownReason; a typed nil
// ApplicationError in the chain panics when Reason is accessed.
func Reason(err error) string {
	if err == nil {
		return UnknownReason
	}
	return FromError(err).Reason
}

// Message returns the public application message found through the supplied
// error chain. Nil produces an empty string and unrecognized errors produce
// UnknownMessage; a typed nil ApplicationError panics when Message is accessed.
func Message(err error) string {
	if err == nil {
		return ""
	}
	return FromError(err).Message
}

// Clone copies the status and its string metadata map, retaining the shared
// cause. Nil input returns nil, and mutations to a nonnil copied map do not
// affect the input map.
func Clone(err *ApplicationError) *ApplicationError {
	if err == nil {
		return nil
	}
	var metadata map[string]string
	if err.Metadata != nil {
		metadata = make(map[string]string, len(err.Metadata))
		maps.Copy(metadata, err.Metadata)
	}
	return &ApplicationError{
		cause: err.cause,
		Status: Status{
			Code:     err.Code,
			Reason:   err.Reason,
			Message:  err.Message,
			Metadata: metadata,
		},
	}
}

// FromError returns the ApplicationError located by errors.As, including a
// typed nil pointer when present. Nil input returns nil; otherwise an
// unrecognized error becomes a generic internal error with that error attached
// as its cause.
func FromError(err error) *ApplicationError {
	if err == nil {
		return nil
	}
	if se := new(ApplicationError); errors.As(err, &se) {
		return se
	}

	// Unknown error text can contain internal details. Expose the generic status
	// while retaining the supplied error as a cause for internal diagnostics.
	return New(UnknownCode, UnknownReason, UnknownMessage).WithCause(err)
}
