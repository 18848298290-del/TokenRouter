package usageview

import "time"

const (
	OllamaCloudUsageModelWindowFiveHour OllamaCloudUsageModelWindow = "five_hour"
	OllamaCloudUsageModelWindowSevenDay OllamaCloudUsageModelWindow = "seven_day"
)

// OllamaCloudUsageWindow contains usage and reset information from an official usage window.
type OllamaCloudUsageWindow struct {
	UsedPercent float64    `json:"used_percent"`
	ResetAt     *time.Time `json:"reset_at,omitempty"`
	ResetText   string     `json:"reset_text,omitempty"`
}

// OllamaCloudUsageModelWindow identifies the window containing a model request count.
type OllamaCloudUsageModelWindow string

// OllamaCloudUsageModel contains the model and request count shown for an Ollama usage window.
type OllamaCloudUsageModel struct {
	Model    string                      `json:"model"`
	Window   OllamaCloudUsageModelWindow `json:"window"`
	Requests int64                       `json:"requests"`
}

// OllamaCloudUsageData contains parsed display values from the usage page.
type OllamaCloudUsageData struct {
	Plan     string                  `json:"plan,omitempty"`
	FiveHour *OllamaCloudUsageWindow `json:"five_hour,omitempty"`
	SevenDay *OllamaCloudUsageWindow `json:"seven_day,omitempty"`
	Balance  string                  `json:"balance,omitempty"`
	Models   []OllamaCloudUsageModel `json:"models,omitempty"`
}

// OllamaUsageObservation holds HTTP results for provider retry, failure-count, and storage decisions.
type OllamaUsageObservation struct {
	Data         *OllamaCloudUsageData
	HTTPStatus   int
	Failure      string
	RetryAfter   time.Duration
	Unauthorized bool
}
