package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// TestAffiliateRecordFilterInjectedCalendar includes the final nanosecond of the end date in the calendar's
// time zone.
func TestAffiliateRecordFilterInjectedCalendar(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/?start_at=2026-11-01&end_at=2026-11-01&timezone=invalid-zone", nil)
	filter := parseAffiliateRecordFilter(c, 2, 200, timezone.NewCalendar(loc))
	require.Equal(t, 2, filter.Page)
	require.Equal(t, 100, filter.PageSize)
	require.NotNil(t, filter.StartAt)
	require.NotNil(t, filter.EndAt)
	require.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, loc), *filter.StartAt)
	require.Equal(t, time.Date(2026, 11, 2, 0, 0, 0, 0, loc).Add(-time.Nanosecond), *filter.EndAt)
	require.Equal(t, 25*time.Hour-time.Nanosecond, filter.EndAt.Sub(*filter.StartAt))
}
