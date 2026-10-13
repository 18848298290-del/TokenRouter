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

// A date-only administrator filter uses the injected calendar when its
// requested timezone is invalid. The selected New York date includes a
// daylight-saving reversal, so the range must follow the next local midnight
// instead of a fixed daily duration.

// TestAffiliateRecordFilterInjectedCalendar parses the New York
// daylight-saving reversal date with an invalid requested timezone.
//
// It checks calendar midnight, inclusive end, the 25-hour span, unchanged
// page, and capped page size.
func TestAffiliateRecordFilterInjectedCalendar(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	// Select the daylight-saving reversal date and an invalid user zone so the
	// injected calendar determines the inclusive filter interval.
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
