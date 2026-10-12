package apperror

import (
	"errors"
	"fmt"
	"maps"
)

const (
	UnknownCode    = 500
	UnknownReason  = ""
	UnknownMessage = "internal error"
)

// Status contains the serialized error code, reason, message, and metadata.
type Status struct {
	Code     int32             `json:"code"`
	Reason   string            `json:"reason,omitempty"`
	Message  string            `json:"message"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ApplicationError pairs an application error category and reason with an optional cause.
// Adapters map it to protocol responses; Code values remain stable for comparisons and serialization.
type ApplicationError struct {
	Status
	cause error
}

// Newf creates an application error with a formatted message.
func Newf(code Category, reason, format string, a ...any) *ApplicationError {
	return New(code, reason, fmt.Sprintf(format, a...))
}

func (e *ApplicationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.cause == nil {
		return fmt.Sprintf("error: code=%d reason=%q message=%q metadata=%v", e.Code, e.Reason, e.Message, e.Metadata)
	}
	return fmt.Sprintf("error: code=%d reason=%q message=%q metadata=%v cause=%v", e.Code, e.Reason, e.Message, e.Metadata, e.cause)
}

// Unwrap returns the underlying cause.
func (e *ApplicationError) Unwrap() error {
	return e.cause
}

// Is compares Code and Reason with an ApplicationError found in the target chain.
func (e *ApplicationError) Is(err error) bool {
	if se := new(ApplicationError); errors.As(err, &se) {
		return se.Code == e.Code && se.Reason == e.Reason
	}
	return false
}

// WithCause returns a copy with the supplied cause.
func (e *ApplicationError) WithCause(cause error) *ApplicationError {
	err := Clone(e)
	err.cause = cause
	return err
}

// WithMetadata returns a copy with an independent metadata map.
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

// New creates an application error with the supplied category, reason, and message.
func New(code Category, reason, message string) *ApplicationError {
	return &ApplicationError{
		Status: Status{
			Code:    int32(code),
			Message: message,
			Reason:  reason,
		},
	}
}

// CategoryOf returns the category through wrappers, CategoryOK for nil,
// or CategoryInternalServer for an unrecognized error.
func CategoryOf(err error) Category {
	if err == nil {
		return CategoryOK
	}
	return Category(FromError(err).Code)
}

// Reason returns the reason for a particular error.
// It supports wrapped errors.
func Reason(err error) string {
	if err == nil {
		return UnknownReason
	}
	return FromError(err).Reason
}

// Message returns the message for a particular error.
// It supports wrapped errors.
func Message(err error) string {
	if err == nil {
		return ""
	}
	return FromError(err).Message
}

// Clone copies the error and metadata map; the cause remains shared.
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

// FromError returns an ApplicationError found through wrappers, nil for nil,
// or a generic internal error wrapping an unrecognized error.
func FromError(err error) *ApplicationError {
	if err == nil {
		return nil
	}
	if se := new(ApplicationError); errors.As(err, &se) {
		return se
	}

	// Fall back to a generic internal error.
	return New(UnknownCode, UnknownReason, UnknownMessage).WithCause(err)
}
