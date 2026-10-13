package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpsAlertRuleValidation supplies the four core rule members and an empty
// payload to the validator. It checks the accepted name, missing-member
// rejection, and classification of selected percentage versus queue-count
// indicators.
func TestOpsAlertRuleValidation(t *testing.T) {
	raw := map[string]json.RawMessage{
		"name":        json.RawMessage(`"High error rate"`),
		"metric_type": json.RawMessage(`"error_rate"`),
		"operator":    json.RawMessage(`">"`),
		"threshold":   json.RawMessage(`90`),
	}

	validated, err := validateOpsAlertRulePayload(raw)
	require.NoError(t, err)
	require.Equal(t, "High error rate", validated.Name)

	_, err = validateOpsAlertRulePayload(map[string]json.RawMessage{})
	require.Error(t, err)

	require.True(t, isPercentOrRateMetric("error_rate"))
	require.True(t, isPercentOrRateMetric("disk_usage_percent"))
	require.False(t, isPercentOrRateMetric("concurrency_queue_depth"))
}
