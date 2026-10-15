package contract

import "time"

// risk.go defines moderation policy and event values consumed by risk
// notifications.
// Moderation notification inputs describe a recorded violation or session-risk
// warning after its business action has been decided. The delivery adapter
// formats identity, timestamps, category, and counters into template variables;
// policy thresholds supply the corresponding notice's displayed ban threshold.

type (
	// RiskPolicy supplies the ban-count thresholds displayed in moderation and session-risk notices.
	RiskPolicy struct {
		// BanThreshold: content violation count threshold.
		// CyberBanThreshold: session violation count threshold.
		BanThreshold, CyberBanThreshold int
	}
	// RiskLog carries the recorded content-moderation observations used to compose a user notice.
	RiskLog struct {
		// ID identifies the recorded event for delivery deduplication; nonpositive
		// IDs produce no source identifier when the notification request is assembled.
		ID int64
		// UserID supplies an optional account identifier for language lookup. An
		// absent pointer becomes zero, and app's locale reader uses email for
		// nonpositive identifiers passed by the delivery adapter.
		UserID *int64
		// UserEmail: recipient email address.
		// GroupName: request group name.
		// HighestCategory: highest scoring category.
		UserEmail, GroupName, HighestCategory string
		HighestScore                          float64 // recorded moderation score
		ViolationCount                        int     // recorded violation count
		AutoBanned                            bool    // recorded automatic-ban outcome
		// CreatedAt supplies notice time when nonzero; the variable builder uses
		// the current UTC time when this record has no stored timestamp.
		CreatedAt time.Time
	}
)

// RiskWarning carries the session-risk observation supplied to the account-disabled notice.
type RiskWarning struct {
	// ID supplies the session-warning source identifier for delivery matching;
	// nonpositive IDs leave that identifier absent in the assembled notification request.
	ID int64
	// UserID supplies an optional account identifier for language lookup. An
	// absent pointer becomes zero, and app's account reader uses recipient email
	// when the delivered identifier is nonpositive.
	UserID *int64
	// UserEmail: recipient email address.
	// GroupName: request group name.
	// ProviderName: warning provider name.
	UserEmail, GroupName, ProviderName string
	ViolationCount                     int // recorded warning count
	// CreatedAt supplies the displayed trigger time when nonzero; a missing
	// timestamp leaves the variable builder's current UTC time in the notice.
	CreatedAt time.Time
}
