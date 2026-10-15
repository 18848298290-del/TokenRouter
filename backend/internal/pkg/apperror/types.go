package apperror

// types.go defines application error categories and their constructors and
// predicates.
// Application categories use HTTP-shaped numeric values so response adapters
// and domain predicates can share error classifications. Constructors attach
// the caller's reason and public message, and wrapper-aware predicates compare
// the resolved category.

// These category values identify response outcomes and remain the numeric codes
// inspected by CategoryOf, error predicates, and protocol response adapters.
const (
	CategoryOK                 Category = 200 // successful operation category
	CategoryBadRequest         Category = 400 // invalid request category
	CategoryUnauthorized       Category = 401 // missing authentication category
	CategoryForbidden          Category = 403 // denied access category
	CategoryNotFound           Category = 404 // missing resource category
	CategoryConflict           Category = 409 // conflicting state category
	CategoryTooManyRequests    Category = 429 // request throttling category
	CategoryClientClosed       Category = 499 // client cancellation category
	CategoryInternalServer     Category = 500 // internal failure category
	CategoryBadGateway         Category = 502 // upstream failure category
	CategoryServiceUnavailable Category = 503 // temporary unavailability category
	CategoryGatewayTimeout     Category = 504 // upstream timeout category
)

// Category identifies an application error using stable numeric codes.
type Category int32

// BadRequest creates an invalid-request error with the supplied reason and
// message.
func BadRequest(reason, message string) *ApplicationError {
	return New(CategoryBadRequest, reason, message)
}

// IsBadRequest resolves wrappers and reports whether err has the
// invalid-request category. Nil errors resolve to success, and unknown errors
// resolve to an internal failure category.
func IsBadRequest(err error) bool {
	return CategoryOf(err) == CategoryBadRequest
}

// TooManyRequests creates a throttling error with the supplied reason and
// message.
func TooManyRequests(reason, message string) *ApplicationError {
	return New(CategoryTooManyRequests, reason, message)
}

// Unauthorized creates an authentication error with the supplied reason and
// message.
func Unauthorized(reason, message string) *ApplicationError {
	return New(CategoryUnauthorized, reason, message)
}

// Forbidden creates an access-denied error with the supplied reason and
// message.
func Forbidden(reason, message string) *ApplicationError {
	return New(CategoryForbidden, reason, message)
}

// NotFound creates a missing-resource error with the supplied reason and
// message.
func NotFound(reason, message string) *ApplicationError {
	return New(CategoryNotFound, reason, message)
}

// IsNotFound resolves wrappers and reports whether err has the missing-resource
// category. Nil errors resolve to success, and unknown errors resolve to an
// internal failure category.
func IsNotFound(err error) bool {
	return CategoryOf(err) == CategoryNotFound
}

// Conflict creates a state-conflict error with the supplied reason and message.
func Conflict(reason, message string) *ApplicationError {
	return New(CategoryConflict, reason, message)
}

// InternalServer creates an internal-failure error with the supplied reason and
// message.
func InternalServer(reason, message string) *ApplicationError {
	return New(CategoryInternalServer, reason, message)
}

// ServiceUnavailable creates a temporary-unavailability error with the supplied
// reason and message.
func ServiceUnavailable(reason, message string) *ApplicationError {
	return New(CategoryServiceUnavailable, reason, message)
}

// GatewayTimeout creates an upstream-timeout error with the supplied reason and
// message.
func GatewayTimeout(reason, message string) *ApplicationError {
	return New(CategoryGatewayTimeout, reason, message)
}
