package ops

import "time"

// alert_models.go defines stored alert rules, observed events, scope silences,
// and event-list filters.
// Alert evaluation stores the observed value and threshold in an event so
// administrators can inspect the firing comparison after editing its rule.
// Consecutive evaluations and the latest fire time control event creation,
// while saved exact-scope silences can suppress it; runtime mail silencing is
// represented in separate settings.

// Alert status values distinguish active firing events, ordinary resolution,
// and administrator-requested resolution in persisted events and the dashboard
// response.
const (
	OpsAlertStatusFiring         = "firing"          // active firing event state
	OpsAlertStatusResolved       = "resolved"        // ordinary resolved event state
	OpsAlertStatusManualResolved = "manual_resolved" // administrator requested resolution state
)

// OpsAlertRule carries a persisted indicator comparison and its evaluation/mail
// preferences.
type OpsAlertRule struct {
	ID          int64  `json:"id"`          // persisted alert rule identity
	Name        string `json:"name"`        // administrator rule display name
	Description string `json:"description"` // administrator rule explanation text

	Enabled  bool   `json:"enabled"`  // include rule in evaluation
	Severity string `json:"severity"` // configured P0-P3 notification severity

	// MetricType selects an evaluator observation, Operator compares its numeric
	// value with Threshold, and unsupported indicators become unavailable for the
	// cycle. Request, resource, and group-ratio metrics use percentage units;
	// count metrics use their observed counts. The evaluator does not cap supplied
	// ratios or measurements at one hundred.
	MetricType string  `json:"metric_type"` // evaluator indicator selector name
	Operator   string  `json:"operator"`    // configured numeric comparison operator
	Threshold  float64 `json:"threshold"`   // indicator comparison threshold value

	// WindowMinutes selects the prior completed-minute request window.
	// SustainedMinutes is converted into a required consecutive evaluation count,
	// while CooldownMinutes compares against the latest event fire time before
	// another event is created.
	WindowMinutes    int `json:"window_minutes"`    // minutes
	SustainedMinutes int `json:"sustained_minutes"` // minutes
	CooldownMinutes  int `json:"cooldown_minutes"`  // minutes

	NotifyEmail bool `json:"notify_email"` // request firing email attempts

	// Filters carries optional platform, group_id, and region values read by
	// parseOpsAlertRuleScope. Platform and group constrain supported metrics;
	// region participates in scoped silence lookup.
	Filters map[string]any `json:"filters,omitempty"`

	// LastTriggeredAt carries optional metadata read from the stored rule row. The
	// evaluator obtains the event-creation cooldown timestamp from the latest
	// event FiredAt.
	LastTriggeredAt *time.Time `json:"last_triggered_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"` // stored rule creation time
	UpdatedAt       time.Time  `json:"updated_at"` // stored rule update time
}

// OpsAlertEvent carries one persisted firing or resolved observation and its
// notification marker.
type OpsAlertEvent struct {
	ID       int64  `json:"id"`       // persisted alert event identity
	RuleID   int64  `json:"rule_id"`  // owning persisted rule identity
	Severity string `json:"severity"` // recorded firing severity label
	Status   string `json:"status"`   // stored event lifecycle status

	Title       string `json:"title"`       // administrator event display title
	Description string `json:"description"` // recorded event explanation text

	// MetricValue and ThresholdValue retain the observed value and rule cutoff
	// from event creation when available. Later rule edits do not alter these
	// recorded comparison values.
	MetricValue *float64 `json:"metric_value,omitempty"` // optional observed indicator value
	// optional recorded comparison threshold
	ThresholdValue *float64 `json:"threshold_value,omitempty"`

	Dimensions map[string]any `json:"dimensions,omitempty"` // recorded platform/group scope values

	FiredAt    time.Time  `json:"fired_at"`              // recorded event firing time
	ResolvedAt *time.Time `json:"resolved_at,omitempty"` // optional event resolution time

	// EmailSent is the stored marker updated after any evaluator mail callback
	// returns nil. The marker write is attempted separately and can fail after the
	// callback has accepted its request.
	EmailSent bool      `json:"email_sent"`
	CreatedAt time.Time `json:"created_at"` // stored event creation time
}

// OpsAlertSilence carries an exact saved rule/platform/group/region scope and
// its suppression deadline.
type OpsAlertSilence struct {
	ID int64 `json:"id"` // persisted scoped silence identity

	// RuleID, Platform, GroupID, and Region identify one exact scope in repository
	// silence lookup. Nil group or region matches a saved SQL NULL scope; it does
	// not act as a wildcard for other groups or regions.
	RuleID   int64   `json:"rule_id"`            // selected alert rule identity
	Platform string  `json:"platform"`           // selected provider platform scope
	GroupID  *int64  `json:"group_id,omitempty"` // optional exact group scope
	Region   *string `json:"region,omitempty"`   // optional exact region scope

	Until  time.Time `json:"until"`  // exclusive silence end timestamp
	Reason string    `json:"reason"` // administrator silence explanation text

	CreatedBy *int64    `json:"created_by,omitempty"` // optional creating administrator identity
	CreatedAt time.Time `json:"created_at"`           // stored silence creation time
}

// OpsAlertEventFilter selects persisted event rows and an optional descending
// cursor.
type OpsAlertEventFilter struct {
	// Limit bounds ListAlertEvents results. The repository defaults nonpositive
	// values to one hundred and caps values above five hundred before applying the
	// descending cursor.
	Limit int

	// BeforeFiredAt and BeforeID are applied together when the time is nonzero and
	// the ID is positive. Events sort by firing time descending, then ID
	// descending, and the cursor excludes that row and its newer neighbors.
	BeforeFiredAt *time.Time // cursor event firing time
	BeforeID      *int64     // cursor event row identity

	Status   string // exact event lifecycle filter
	Severity string // exact event severity filter
	// EmailSent filters the stored mail marker when supplied. A pointer to false
	// selects unmarked events, while nil leaves both marker states eligible.
	EmailSent *bool

	// StartTime and EndTime constrain firing timestamps independently. Nil or
	// zero-valued endpoints omit their respective comparison; supplied nonzero
	// times select an inclusive start and exclusive end.
	StartTime *time.Time // optional inclusive firing start
	EndTime   *time.Time // optional exclusive firing end

	Platform string // exact JSON platform filter
	// GroupID selects the string representation of a positive group identity saved
	// in event dimensions. Nil or nonpositive values omit this JSON filter.
	GroupID *int64
}
