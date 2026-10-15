package promotion

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/identity/contact"
)

// promo_code.go represents registration promotion controls and evaluates their
// current eligibility.
// A registration Promo grant is eligible only while its code is active, its
// deadline has not passed, and its positive total-use limit has remaining
// capacity. A deadline equal to the supplied time still permits use, and a
// nonpositive MaxUses leaves the count limit open.
//
// Stored usage records retain the granted amount independently of later code
// edits. Update inputs distinguish omitted fields from provided pointers,
// with a zero expiration time requesting removal of the deadline. Read models
// can carry optional user and usage details; their slices and pointers follow
// the storage and response converters' sharing rules.

// Promo code status values determine whether the eligibility checks can admit
// a registration balance grant.
const (
	PromoCodeStatusActive   = "active"   // permits other eligibility checks
	PromoCodeStatusDisabled = "disabled" // rejects registration code use
)

// PromoCode represents a registration balance grant and its stored
// eligibility controls.
type PromoCode struct {
	ID          int64   // stored promotion identifier
	Code        string  // registration promotion text
	BonusAmount float64 // USD
	// MaxUses and UsedCount control global redemption capacity. A positive
	// maximum rejects further grants once the stored count reaches it; the
	// mutation transaction increments UsedCount with each receipt.
	MaxUses   int    // positive total-use ceiling
	UsedCount int    // completed promotion grant count
	Status    string // saved code availability status
	// ExpiresAt limits grants after its timestamp. Nil leaves the deadline open,
	// and entity and HTTP conversions retain this pointer rather than copying
	// the time.
	ExpiresAt *time.Time // optional eligibility deadline
	Notes     string     // administrator promotion note
	CreatedAt time.Time  // promotion creation timestamp
	UpdatedAt time.Time  // latest promotion mutation timestamp

	// UsageRecords contains receipts supplied by a loading caller. The
	// repository's ordinary code conversion leaves it unset; consumers manage
	// any attached slice's mutable storage.
	UsageRecords []PromoCodeUsage // optional loaded grant receipts
}

// PromoCodeUsage records one user's grant amount and its receipt timestamp.
type PromoCodeUsage struct {
	ID          int64     // stored grant receipt identifier
	PromoCodeID int64     // granting promotion identifier
	UserID      int64     // credited account identifier
	BonusAmount float64   // USD
	UsedAt      time.Time // grant receipt timestamp

	// PromoCode holds optional code details attached by a loader. Ordinary usage
	// conversion leaves it nil and does not fetch the granting code.
	PromoCode *PromoCode // optional loaded code
	// User holds optional recipient details from an eagerly loaded user edge.
	// Its account pointers retain their converter-defined sharing with the
	// underlying entity.
	User *UserView // optional loaded recipient
}

// CreatePromoCodeInput carries administrator values used to create an active
// registration promotion.
type CreatePromoCodeInput struct {
	// Code is trimmed and uppercased by PromoService.Create. An empty trimmed
	// value requests a random hexadecimal code before persistence.
	Code        string  // requested promotion text
	BonusAmount float64 // USD
	MaxUses     int     // positive total-use ceiling
	// ExpiresAt is passed into the created code as the supplied pointer. Nil
	// leaves expiration unset; service creation does not copy or validate that
	// time.
	ExpiresAt *time.Time // optional eligibility deadline
	Notes     string     // administrator promotion note
}

// UpdatePromoCodeInput carries optional administrator edits to an existing
// registration promotion.
type UpdatePromoCodeInput struct {
	// Code, BonusAmount, MaxUses, and Status use nil to retain their
	// corresponding stored value. Provided pointers request replacements,
	// including zero values for amount and the usage limit.
	Code        *string  // optional replacement code
	BonusAmount *float64 // optional USD grant
	MaxUses     *int     // optional total-use ceiling
	Status      *string  // optional availability status
	// ExpiresAt nil leaves the stored deadline unchanged. A zero pointed-to time
	// clears it and makes PromoService.Update replace this input field with nil.
	ExpiresAt *time.Time // optional deadline edit
	// Notes nil leaves saved text unchanged. A supplied pointer replaces the
	// note, including an empty string supplied to clear its text.
	Notes *string // optional administrator note
}

// UserView carries account details included with loaded promotion usage
// receipts.
type UserView struct {
	ID            int64   // recipient account identifier
	Email         string  // recipient email address
	Username      string  // recipient display name
	Role          string  // global account role
	Balance       float64 // USD
	FrozenBalance float64 // USD
	Concurrency   int     // account concurrency allowance
	Status        string  // account lifecycle status
	// AllowedGroups holds account group grants when a caller supplies them. The
	// PostgreSQL promotion user converter leaves this slice unset; shallow HTTP
	// conversion retains supplied storage.
	AllowedGroups []int64 // exclusive group grants
	// DisabledPublicGroups carries account group denials when supplied.
	// PostgreSQL promotion user loading leaves this slice unset, while shallow
	// HTTP conversion shares its backing storage.
	DisabledPublicGroups []int64 // denied public group identifiers
	// LastActiveAt retains the account's timestamp pointer through entity and
	// HTTP conversion. Copy the pointed-to time before independently modifying a
	// response.
	LastActiveAt *time.Time // optional account activity time
	CreatedAt    time.Time  // account creation timestamp
	UpdatedAt    time.Time  // account update timestamp
	// DeletedAt retains the account's deletion pointer through conversion. Nil
	// means no deletion timestamp is present; response edits require a separate
	// time copy.
	DeletedAt *time.Time // optional deletion timestamp
	// BalanceNotifyEnabled gates alerts. BalanceNotifyThresholdType and
	// BalanceNotifyThreshold select a fixed amount or percentage threshold.
	// Positive percentage thresholds use positive TotalRecharged as their base;
	// with a nonpositive recharge total, billing treats that threshold as a fixed
	// amount. BalanceNotifyExtraEmails supplies additional recipients.
	BalanceNotifyEnabled       bool   // balance notification switch
	BalanceNotifyThresholdType string // fixed or percentage interpretation
	// BalanceNotifyThreshold retains the account pointer through conversion.
	// Billing interprets it using BalanceNotifyThresholdType and uses global
	// policy when it is nil.
	BalanceNotifyThreshold *float64 // optional notification threshold
	// BalanceNotifyExtraEmails contains parsed recipient entries. HTTP user
	// conversion allocates its own recipient DTO slice, so response entry
	// mutations do not change this slice.
	// additional notification recipients
	BalanceNotifyExtraEmails []contact.Entry
	TotalRecharged           float64 // USD
	RPMLimit                 int     // account requests per minute
	// zero leaves key count unlimited
	APIKeyLimit int
}

// CanUseAt reports whether the saved code controls allow a grant at now.
//
// Active status is required, while a nil deadline leaves the time window open
// and equality with a deadline still passes. A positive MaxUses compares
// UsedCount with the ceiling; nonpositive limits leave count-based admission
// open. This check observes a snapshot and reserves no usage, so redemption
// rechecks the locked record before recording a grant.
func (p *PromoCode) CanUseAt(now time.Time) bool {
	if p.Status != PromoCodeStatusActive {
		return false
	}
	if p.ExpiresAt != nil && now.After(*p.ExpiresAt) {
		return false
	}
	if p.MaxUses > 0 && p.UsedCount >= p.MaxUses {
		return false
	}
	return true
}

// IsExpired reports whether the code's optional deadline precedes the current
// system time.
//
// It delegates to IsExpiredAt; nil deadlines and equality with the observed
// time both return false.
func (p *PromoCode) IsExpired() bool { return p.IsExpiredAt(time.Now()) }

// IsExpiredAt reports whether the optional expiration timestamp is earlier
// than now.
//
// A nil deadline or exact equality returns false, allowing callers to
// distinguish the deadline from activation and usage checks.
func (p *PromoCode) IsExpiredAt(now time.Time) bool {
	return p.ExpiresAt != nil && now.After(*p.ExpiresAt)
}
