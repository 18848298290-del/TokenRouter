package usageview

import "time"

// Ollama settings HTML can expose percent windows, model request counts, and a
// textual account balance together. Parsed display values keep reset text
// beside any parsed timestamp, while fetch observations separately report
// failure and retry evidence for provider-owned snapshot maintenance.

// These window identifiers bind model request counts to the same five-hour and
// seven-day windows used for usage display.
const (
	OllamaCloudUsageModelWindowFiveHour OllamaCloudUsageModelWindow = "five_hour" // five-hour usage window
	OllamaCloudUsageModelWindowSevenDay OllamaCloudUsageModelWindow = "seven_day" // seven-day usage window
)

// OllamaCloudUsageWindow holds one parsed settings-page percentage and
// optional reset evidence.
type OllamaCloudUsageWindow struct {
	UsedPercent float64 `json:"used_percent"` // percent
	// ResetAt and ResetText retain distinct settings-page observations. The parser
	// prefers a time element for ResetAt and attempts text parsing when no timestamp
	// exists, while ResetText remains available for display even when parsing fails.
	ResetAt   *time.Time `json:"reset_at,omitempty"`   // parsed usage reset instant
	ResetText string     `json:"reset_text,omitempty"` // reported usage reset text
}

// OllamaCloudUsageModelWindow identifies the usage window owning a model
// request count.
type OllamaCloudUsageModelWindow string

// OllamaCloudUsageModel records one model and request count from a
// settings-page window.
type OllamaCloudUsageModel struct {
	Model    string                      `json:"model"`    // settings-page model display name
	Window   OllamaCloudUsageModelWindow `json:"window"`   // owning usage window identifier
	Requests int64                       `json:"requests"` // reported model request count
}

// OllamaCloudUsageData holds the display values extracted from the cloud
// settings page.
type OllamaCloudUsageData struct {
	Plan string `json:"plan,omitempty"` // settings-page subscription plan label
	// FiveHour and SevenDay retain independently recognized settings-page windows.
	// Either can be nil when its percentage block is absent or unparseable, while
	// the other parsed window and the remaining display data stay available.
	FiveHour *OllamaCloudUsageWindow `json:"five_hour,omitempty"` // parsed five-hour usage window
	SevenDay *OllamaCloudUsageWindow `json:"seven_day,omitempty"` // parsed seven-day usage window
	Balance  string                  `json:"balance,omitempty"`   // reported account balance text
	Models   []OllamaCloudUsageModel `json:"models,omitempty"`    // parsed model request observations
}

// OllamaUsageObservation separates fetch results from provider-owned retry and
// snapshot-maintenance decisions.
type OllamaUsageObservation struct {
	// Data supplies a successful parse. The provider service handles a nonempty
	// Failure through its failure path and retains its previous successful Data.
	Data       *OllamaCloudUsageData // parsed current display data
	HTTPStatus int                   // observed settings HTTP status
	Failure    string                // sanitized fetch failure label
	// RetryAfter supplies the positive upstream delay when recognized. The
	// provider scheduler combines it with refresh interval and accumulated failures.
	RetryAfter time.Duration // requested upstream retry delay
	// Unauthorized records either HTTP authentication rejection or recognized
	// sign-in HTML. The provider service uses it to select unauthorized snapshot status.
	Unauthorized bool // observed session authentication failure
}
