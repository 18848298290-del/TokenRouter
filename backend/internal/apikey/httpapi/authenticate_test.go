package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// TestRecordAccessOwnershipDoesNotAuthenticate records loaded Key, actor and
// team identifiers into telemetry before admission. It checks those
// observations are populated while authenticated principal and subject context
// remain absent.
func TestRecordAccessOwnershipDoesNotAuthenticate(t *testing.T) {
	var latest telemetry.RequestRecord
	ctx := telemetry.WithRequestCapture(context.Background(), telemetry.RequestRecord{RequestID: "request", StartedAt: time.Now(), State: "running"}, func(record telemetry.RequestRecord) { latest = record })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	teamID := int64(9)
	recordAccessOwnership(c, &apikey.AccessSnapshot{ActorUserID: 42, KeyID: 8, TeamID: &teamID})
	require.Equal(t, int64(42), latest.UserID)
	require.Equal(t, int64(8), latest.APIKeyID)
	require.Equal(t, teamID, latest.TeamID)
	_, authenticated := authctx.GetPrincipal(c)
	require.False(t, authenticated)
	_, subject := authctx.GetAuthSubjectFromContext(c)
	require.False(t, subject)
}
