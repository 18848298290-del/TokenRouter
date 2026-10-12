package apperror

const (
	// CategoryOK identifies successful completion.
	CategoryOK              Category = 200
	CategoryBadRequest      Category = 400
	CategoryUnauthorized    Category = 401
	CategoryForbidden       Category = 403
	CategoryNotFound        Category = 404
	CategoryConflict        Category = 409
	CategoryTooManyRequests Category = 429
	// CategoryClientClosed identifies a request canceled by the client.
	CategoryClientClosed       Category = 499
	CategoryInternalServer     Category = 500
	CategoryBadGateway         Category = 502
	CategoryServiceUnavailable Category = 503
	CategoryGatewayTimeout     Category = 504
)

// Category identifies an application error using stable numeric codes.
type Category int32

// BadRequest creates an invalid-request error.
func BadRequest(reason, message string) *ApplicationError {
	return New(CategoryBadRequest, reason, message)
}

// IsBadRequest reports whether err has CategoryBadRequest, including through wrappers.
func IsBadRequest(err error) bool {
	return CategoryOf(err) == CategoryBadRequest
}

// TooManyRequests creates a rate-limit error.
func TooManyRequests(reason, message string) *ApplicationError {
	return New(CategoryTooManyRequests, reason, message)
}

// Unauthorized creates an authentication error.
func Unauthorized(reason, message string) *ApplicationError {
	return New(CategoryUnauthorized, reason, message)
}

// Forbidden creates an access-denied error.
func Forbidden(reason, message string) *ApplicationError {
	return New(CategoryForbidden, reason, message)
}

// NotFound creates a missing-resource error.
func NotFound(reason, message string) *ApplicationError {
	return New(CategoryNotFound, reason, message)
}

// IsNotFound reports whether err has CategoryNotFound, including through wrappers.
func IsNotFound(err error) bool {
	return CategoryOf(err) == CategoryNotFound
}

// Conflict creates a conflicting-state error.
func Conflict(reason, message string) *ApplicationError {
	return New(CategoryConflict, reason, message)
}

// InternalServer creates an internal failure error.
func InternalServer(reason, message string) *ApplicationError {
	return New(CategoryInternalServer, reason, message)
}

// ServiceUnavailable creates a temporary service-unavailability error.
func ServiceUnavailable(reason, message string) *ApplicationError {
	return New(CategoryServiceUnavailable, reason, message)
}

// GatewayTimeout creates an upstream timeout error.
func GatewayTimeout(reason, message string) *ApplicationError {
	return New(CategoryGatewayTimeout, reason, message)
}
