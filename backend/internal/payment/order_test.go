package payment

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestGenerateOutTradeNo checks the merchant order number format and date.
func TestGenerateOutTradeNo(t *testing.T) {
	t.Parallel()

	before := time.Now().Format("20060102")
	got := GenerateOutTradeNo()
	after := time.Now().Format("20060102")

	require.Regexp(t, `^tr_[0-9]{8}[a-zA-Z0-9]{8}$`, got)
	require.Contains(t, []string{before, after}, got[3:11])
}
