package ollama

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// Ollama settings markup can express usage through text percentages, CSS
// tracks, and several reset-time elements. The stored fixture and focused HTML
// cases check these representations together with model attribution,
// optional-field omission, sign-in rejection, and plan or balance source
// precedence.

// ollamaUsageFixture reads the stored settings-page markup and fails the test
// if reading fails. The bytes are returned for parser and fetch fixtures that
// share the same expected usage observations.
func ollamaUsageFixture(t *testing.T) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/ollama_settings_usage.html")
	require.NoError(t, e)
	return b
}

// TestParseOllamaCloudUsageHTMLFixture checks the stored page's plan, two
// window percentages and reset times, wallet text, and sorted model counts. It
// also checks sign-in rejection before usage recognition and rejection of
// unrelated settings HTML.
func TestParseOllamaCloudUsageHTMLFixture(t *testing.T) {
	data, err := ParseOllamaCloudUsageHTML(ollamaUsageFixture(t))
	require.NoError(t, err)
	require.Equal(t, "max", data.Plan)
	require.NotNil(t, data.FiveHour)
	require.Equal(t, 5.6, data.FiveHour.UsedPercent)
	require.NotNil(t, data.FiveHour.ResetAt)
	require.Equal(t, time.Date(2026, time.July, 23, 3, 0, 0, 0, time.UTC), *data.FiveHour.ResetAt)
	require.NotNil(t, data.SevenDay)
	require.Equal(t, 14.2, data.SevenDay.UsedPercent)
	require.NotNil(t, data.SevenDay.ResetAt)
	require.Equal(t, time.Date(2026, time.July, 29, 0, 0, 0, 0, time.UTC), *data.SevenDay.ResetAt)
	require.Equal(t, "$0", data.Balance)
	require.Equal(t, []usageview.OllamaCloudUsageModel{
		{Model: "gpt-oss:120b-cloud", Window: usageview.OllamaCloudUsageModelWindowFiveHour, Requests: 2},
		{Model: "qwen3-coder:480b-cloud", Window: usageview.OllamaCloudUsageModelWindowFiveHour, Requests: 3},
		{Model: "gpt-oss:120b-cloud", Window: usageview.OllamaCloudUsageModelWindowSevenDay, Requests: 12},
		{Model: "qwen3-coder:480b-cloud", Window: usageview.OllamaCloudUsageModelWindowSevenDay, Requests: 13},
	}, data.Models)

	_, err = ParseOllamaCloudUsageHTML([]byte(`<html><body><main>Sign in to Ollama</main></body></html>`))
	require.ErrorIs(t, err, ErrUnauthorizedHTML)
	_, err = ParseOllamaCloudUsageHTML([]byte(`<html><body><p>5 hour usage 42% used</p><form>Sign in to Ollama</form></body></html>`))
	require.ErrorIs(t, err, ErrUnauthorizedHTML)
	_, err = ParseOllamaCloudUsageHTML([]byte(`<html><body><main>unrelated settings</main></body></html>`))
	require.Error(t, err)
}

// TestParseOllamaCloudUsageHTMLMissingOptionalFieldsAndCSSWidthFallback
// supplies a track with no text percentage and repeated model identity. It
// checks summed CSS usage, omitted optional fields, comma-separated request
// parsing, and retention of the first duplicate's request count.
func TestParseOllamaCloudUsageHTMLMissingOptionalFieldsAndCSSWidthFallback(t *testing.T) {
	data, err := ParseOllamaCloudUsageHTML([]byte(`
		<section>
			<p>5 hour usage</p>
			<div data-usage-track>
				<div data-usage-segment style="width: 23.5%"><span data-model="model-a" data-requests="1,234"></span></div>
				<div data-usage-segment data-model="model-a" data-requests="9,999" style="width: 0%"></div>
			</div>
		</section>`))
	require.NoError(t, err)
	require.Equal(t, 23.5, data.FiveHour.UsedPercent)
	require.Nil(t, data.FiveHour.ResetAt)
	require.Empty(t, data.Plan)
	require.Nil(t, data.SevenDay)
	require.Empty(t, data.Balance)
	require.Equal(t, []usageview.OllamaCloudUsageModel{{
		Model: "model-a", Window: usageview.OllamaCloudUsageModelWindowFiveHour, Requests: 1234,
	}}, data.Models)
}

// TestParseOllamaCloudUsageHTMLResetElementVariants places reset markup beside
// the usage row in three element forms. It checks that the parser reaches the
// sibling reset block and retains the same UTC instant.
func TestParseOllamaCloudUsageHTMLResetElementVariants(t *testing.T) {
	const want = "2026-07-23T03:00:00Z"
	for name, element := range map[string]string{
		"time datetime":  `<time datetime="` + want + `">2 hours.</time>`,
		"custom element": `<local-time data-time="` + want + `">2 hours.</local-time>`,
		"class token":    `<span class="text-xs local-time tabular-nums" data-time="` + want + `">2 hours.</span>`,
	} {
		t.Run(name, func(t *testing.T) {
			data, err := ParseOllamaCloudUsageHTML([]byte(
				`<div><div><span>Session usage</span><span>1% used</span></div><div>Resets in ` + element + `</div></div>`,
			))
			require.NoError(t, err)
			require.NotNil(t, data.FiveHour)
			require.NotNil(t, data.FiveHour.ResetAt)
			require.Equal(t, want, data.FiveHour.ResetAt.Format(time.RFC3339))
		})
	}
}

// TestParseOllamaCloudUsageHTMLPlanAndBalanceFallbacks supplies competing plan
// labels and a credits sentence, then a subscription-only fragment. It checks
// Cloud usage precedence, compact balance text, and the alternate subscription
// label.
func TestParseOllamaCloudUsageHTMLPlanAndBalanceFallbacks(t *testing.T) {
	data, err := ParseOllamaCloudUsageHTML([]byte(`
		<section>
			<h2><span>Cloud usage</span><span>max</span></h2>
			<div><span>Plan</span><span>Pro</span></div>
			<p>Credits currently available: USD $9.50</p>
		</section>`))
	require.NoError(t, err)
	require.Equal(t, "max", data.Plan)
	require.Equal(t, "USD$9.50", data.Balance)

	data, err = ParseOllamaCloudUsageHTML([]byte(`<div><span>Subscription</span><span>Pro</span></div>`))
	require.NoError(t, err)
	require.Equal(t, "Pro", data.Plan)
}
