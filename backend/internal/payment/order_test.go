package payment

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestGenerateOutTradeNo generates one merchant order number around
// two clock observations. It checks the prefix, date and random-suffix
// shape, and accepts either surrounding date if generation crosses
// midnight.
func TestGenerateOutTradeNo(t *testing.T) {
	t.Parallel()

	// Observe the date before and after generation so a midnight crossing
	// can accept either observed day while still checking the complete
	// merchant-number format.
	before := time.Now().Format("20060102")
	got := GenerateOutTradeNo()
	after := time.Now().Format("20060102")

	require.Regexp(t, `^tr_[0-9]{8}[a-zA-Z0-9]{8}$`, got)
	require.Contains(t, []string{before, after}, got[3:11])
}
