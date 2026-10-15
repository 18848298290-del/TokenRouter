package httpapi

import "github.com/gin-gonic/gin"

// response_owner_context.go stores and retrieves the authenticated user and
// API Key identifiers used for Responses ownership checks.
// Responses continuation authorization needs both an authenticated user ID and
// API Key ID from the HTTP request. They are retained as a value pair so later
// ownership checks can reject missing or invalid identity independently of
// upstream response metadata.

// responseOwnerContextKey stores authenticated HTTP identity for continuation
// checks, which require both positive identifiers from the same request.
const responseOwnerContextKey = "openai_http_response_owner"

// HTTPResponseOwner identifies the downstream user and key bound to response continuation.
type HTTPResponseOwner struct {
	// UserID and APIKeyID are accepted together only when both are positive,
	// linking a response continuation to its authenticated user and Key.
	// UserID identifies the authenticated user; APIKeyID identifies the credential.
	UserID, APIKeyID int64
}

// SetHTTPResponseOwner stores positive authenticated user and Key IDs as a
// request value. Nil contexts or nonpositive IDs skip the update, leaving any
// existing binding unchanged; callers bind identity after authentication.
func SetHTTPResponseOwner(c *gin.Context, userID, keyID int64) {
	if c == nil || userID <= 0 || keyID <= 0 {
		return
	}
	c.Set(responseOwnerContextKey, HTTPResponseOwner{UserID: userID, APIKeyID: keyID})
}

// ResponseOwnerFromContext returns the stored user/Key identity and its
// validity flag. It requires a Gin context; absent values, another stored type
// or nonpositive IDs return false so continuation authorization can reject
// missing ownership.
func ResponseOwnerFromContext(c *gin.Context) (HTTPResponseOwner, bool) {
	value, ok := c.Get(responseOwnerContextKey)
	if !ok {
		return HTTPResponseOwner{}, false
	}
	owner, ok := value.(HTTPResponseOwner)
	return owner, ok && owner.UserID > 0 && owner.APIKeyID > 0
}
