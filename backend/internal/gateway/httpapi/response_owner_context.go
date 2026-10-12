package httpapi

import "github.com/gin-gonic/gin"

const responseOwnerContextKey = "openai_http_response_owner"

// HTTPResponseOwner identifies the downstream user and key bound to response continuation.
type HTTPResponseOwner struct{ UserID, APIKeyID int64 }

// SetHTTPResponseOwner stores positive authenticated user and key IDs in the request context.
func SetHTTPResponseOwner(c *gin.Context, userID, keyID int64) {
	if c == nil || userID <= 0 || keyID <= 0 {
		return
	}
	c.Set(responseOwnerContextKey, HTTPResponseOwner{UserID: userID, APIKeyID: keyID})
}

// ResponseOwnerFromContext returns the authenticated response owner and whether both IDs are valid.
func ResponseOwnerFromContext(c *gin.Context) (HTTPResponseOwner, bool) {
	value, ok := c.Get(responseOwnerContextKey)
	if !ok {
		return HTTPResponseOwner{}, false
	}
	owner, ok := value.(HTTPResponseOwner)
	return owner, ok && owner.UserID > 0 && owner.APIKeyID > 0
}
