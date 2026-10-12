package site

import (
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// Announcement visibility combines publication state, a half-open schedule,
// and targeting based on account balance and active subscription plans. A
// targeting expression is an OR of groups, with each nonempty group requiring
// all its conditions. An absent expression reaches every user, while an empty
// group inside a configured expression cannot independently grant visibility.
//
// Validation preserves that distinction by accepting no groups and rejecting
// configured empty groups, unknown operators, and invalid plan identifiers.
// Runtime matching rejects unknown types and operators and empty subscription
// plan collections from already stored data.
// Schedule checks accept the start instant and exclude the end instant, so
// expiration and user-facing reads agree on the last eligible moment.

const (
	// Publication states govern editing, scheduled user visibility, and archival;
	// active announcements still need their schedule and targeting checks to pass.
	AnnouncementStatusDraft    = "draft"    // unpublished editing state
	AnnouncementStatusActive   = "active"   // scheduled publication state
	AnnouncementStatusArchived = "archived" // hidden historical state

	// Notification modes accompany user-visible announcements and let the frontend
	// choose ordinary list presentation or an automatic popup presentation request.
	AnnouncementNotifyModeSilent = "silent" // ordinary list presentation
	AnnouncementNotifyModePopup  = "popup"  // requested popup presentation

	// Condition types select the user observation consumed by a targeting rule:
	// active plan membership for subscription conditions or the current account balance.
	AnnouncementConditionTypeSubscription = "subscription" // active plan membership
	AnnouncementConditionTypeBalance      = "balance"      // account balance comparison

	// Targeting operators define plan membership and exact floating-point balance
	// comparisons; each condition type accepts its own subset during validation.
	AnnouncementOperatorIn  = "in"  // contains active plan
	AnnouncementOperatorGT  = "gt"  // strictly greater balance
	AnnouncementOperatorGTE = "gte" // greater or equal balance
	AnnouncementOperatorLT  = "lt"  // strictly lower balance
	AnnouncementOperatorLTE = "lte" // lower or equal balance
	AnnouncementOperatorEQ  = "eq"  // exactly equal balance
)

var (
	// Announcement errors distinguish inaccessible records from targeting rules
	// that cannot be saved, allowing HTTP callers to report the domain failure.
	ErrAnnouncementNotFound      = infraerrors.NotFound("ANNOUNCEMENT_NOT_FOUND", "announcement not found")
	ErrAnnouncementInvalidTarget = infraerrors.BadRequest("ANNOUNCEMENT_INVALID_TARGET", "invalid announcement targeting rules")
)

// AnnouncementTargeting represents the OR groups used to select eligible announcement readers.
type AnnouncementTargeting struct {
	// AnyOf reaches every user when empty; when configured, at least one
	// nonempty group must satisfy all its conditions for Matches to return true.
	AnyOf []AnnouncementConditionGroup `json:"any_of,omitempty"`
}

// AnnouncementConditionGroup combines targeting conditions that must all match one user.
type AnnouncementConditionGroup struct {
	// AllOf requires every condition to match; empty groups are rejected when
	// saving and ignored during matching so they cannot grant unconditional access.
	AllOf []AnnouncementCondition `json:"all_of,omitempty"`
}

// AnnouncementCondition tests either active plan membership or a user's account balance.
type AnnouncementCondition struct {
	Type string `json:"type"` // subscription or balance

	// Operator is in for subscription membership, or a supported numeric
	// comparator for balance conditions; other combinations fail validation and runtime matching.
	Operator string `json:"operator"`

	// PlanIDs contains the subscription plans whose membership can satisfy this
	// condition. Matching ignores this list for balance conditions; normalization requires positive IDs.
	PlanIDs []int64 `json:"plan_ids,omitempty"`

	// Value is compared with the supplied balance for balance conditions, using
	// that balance's units and exact floating-point operators; subscription conditions ignore it.
	Value float64 `json:"value,omitempty"`
}

// AnnouncementCopy stores the title and body as one language version.
type AnnouncementCopy struct {
	// Title and Content form one localized announcement copy, so resolving a
	// language selects the heading and body from the same content version together.
	Title   string `json:"title"`   // displayed announcement heading
	Content string `json:"content"` // displayed announcement body
}

// AnnouncementLocalization holds revisioned source copy and translations for one announcement.
type AnnouncementLocalization locale.Content[AnnouncementCopy]

// Announcement records publication, targeting, localized copy, schedule, and administrator audit data.
type Announcement struct {
	// Localization stores revisioned copy; ListForUser resolves it when Revision
	// is positive, while Update initializes missing revision state from Title and Content.
	Localization AnnouncementLocalization
	// Resolution records the selected copy's language on localized user reads;
	// it remains zero-valued when ListForUser uses an unversioned source record.
	Resolution locale.Resolution
	ID         int64                 // persistent announcement identifier
	Title      string                // source or resolved heading
	Content    string                // source or resolved body
	Status     string                // stored publication state
	NotifyMode string                // frontend presentation choice
	Targeting  AnnouncementTargeting // eligible reader expression
	// StartsAt and EndsAt define a half-open publication interval. A nil start
	// permits immediate visibility, and a nil end leaves the interval open-ended.
	StartsAt *time.Time // inclusive start instant
	EndsAt   *time.Time // exclusive end instant
	// CreatedBy identifies the creating administrator, while UpdatedBy identifies
	// the latest editing administrator. Service input records these optional IDs
	// when the supplied actor identifier is positive.
	CreatedBy *int64    // creating administrator identifier
	UpdatedBy *int64    // editing administrator identifier
	CreatedAt time.Time // persisted creation timestamp
	// UpdatedAt is both persisted update time and the PostgreSQL update predicate;
	// a stale value causes concurrent edits to return a localization conflict.
	UpdatedAt time.Time
}

// Matches reports whether any configured group grants visibility for the supplied observations.
// No groups means unrestricted targeting. A configured empty group is skipped,
// and every condition in a remaining group must match before that group wins.
//
// Groups combine with OR, while conditions combine with AND. For example, a
// low-balance group can match independently of another group requiring both a
// larger balance and plan membership. The activePlanIDs set and balance belong
// to the same user observation assembled by the announcement service.
func (t AnnouncementTargeting) Matches(balance float64, activePlanIDs map[int64]struct{}) bool {
	if len(t.AnyOf) == 0 {
		return true
	}

	for _, group := range t.AnyOf {
		if len(group.AllOf) == 0 {
			// Stored expressions can contain empty groups even though saving rejects
			// them. Skip that group so missing conditions do not grant every user visibility.
			continue
		}
		allMatched := true
		for _, cond := range group.AllOf {
			if !cond.Matches(balance, activePlanIDs) {
				allMatched = false
				break
			}
		}
		if allMatched {
			return true
		}
	}

	return false
}

// Matches reports whether one condition accepts the supplied balance or active plans.
// Subscription rules require in and at least one matching plan; empty plan
// sets fail. Balance rules use exact comparisons, and unknown types or
// operators fail instead of granting visibility.
func (c AnnouncementCondition) Matches(balance float64, activePlanIDs map[int64]struct{}) bool {
	switch c.Type {
	case AnnouncementConditionTypeSubscription:
		if c.Operator != AnnouncementOperatorIn {
			return false
		}
		if len(c.PlanIDs) == 0 {
			return false
		}
		if len(activePlanIDs) == 0 {
			return false
		}
		for _, planID := range c.PlanIDs {
			if _, ok := activePlanIDs[planID]; ok {
				return true
			}
		}
		return false

	case AnnouncementConditionTypeBalance:
		switch c.Operator {
		case AnnouncementOperatorGT:
			return balance > c.Value
		case AnnouncementOperatorGTE:
			return balance >= c.Value
		case AnnouncementOperatorLT:
			return balance < c.Value
		case AnnouncementOperatorLTE:
			return balance <= c.Value
		case AnnouncementOperatorEQ:
			return balance == c.Value
		default:
			return false
		}

	default:
		return false
	}
}

// NormalizeAndValidate returns a separately allocated targeting expression suitable for storage.
// It trims condition types and operators, requires positive plan IDs, and
// permits at most fifty groups and fifty conditions per group. The caller's
// group and plan slices remain separate from the normalized result.
//
// No groups is a valid unrestricted expression. Once groups are supplied, an
// empty group is invalid because runtime matching would otherwise have to
// interpret an incomplete AND expression inside the OR. Condition-specific
// validation then limits subscription and balance operators to their supported meanings.
func (t AnnouncementTargeting) NormalizeAndValidate() (AnnouncementTargeting, error) {
	normalized := AnnouncementTargeting{AnyOf: make([]AnnouncementConditionGroup, 0, len(t.AnyOf))}

	if len(t.AnyOf) == 0 {
		return normalized, nil
	}

	// Limit saved expressions to fifty alternative targeting groups.
	if len(t.AnyOf) > 50 {
		return AnnouncementTargeting{}, ErrAnnouncementInvalidTarget
	}

	for _, g := range t.AnyOf {
		if len(g.AllOf) == 0 {
			return AnnouncementTargeting{}, ErrAnnouncementInvalidTarget
		}
		// Limit each saved alternative to fifty conjunctive conditions.
		if len(g.AllOf) > 50 {
			return AnnouncementTargeting{}, ErrAnnouncementInvalidTarget
		}

		group := AnnouncementConditionGroup{AllOf: make([]AnnouncementCondition, 0, len(g.AllOf))}
		for _, c := range g.AllOf {
			cond := AnnouncementCondition{
				Type:     strings.TrimSpace(c.Type),
				Operator: strings.TrimSpace(c.Operator),
				Value:    c.Value,
			}
			for _, planID := range c.PlanIDs {
				if planID <= 0 {
					return AnnouncementTargeting{}, ErrAnnouncementInvalidTarget
				}
				cond.PlanIDs = append(cond.PlanIDs, planID)
			}

			if err := cond.validate(); err != nil {
				return AnnouncementTargeting{}, err
			}
			group.AllOf = append(group.AllOf, cond)
		}

		normalized.AnyOf = append(normalized.AnyOf, group)
	}

	return normalized, nil
}

// validate checks the condition-specific operator and required subscription plan collection.
// Type and Operator are expected to have been trimmed by NormalizeAndValidate.
// Positive plan identifiers are checked there before this type-specific validation runs.
func (c AnnouncementCondition) validate() error {
	switch c.Type {
	case AnnouncementConditionTypeSubscription:
		if c.Operator != AnnouncementOperatorIn {
			return ErrAnnouncementInvalidTarget
		}
		if len(c.PlanIDs) == 0 {
			return ErrAnnouncementInvalidTarget
		}
		return nil

	case AnnouncementConditionTypeBalance:
		switch c.Operator {
		case AnnouncementOperatorGT, AnnouncementOperatorGTE, AnnouncementOperatorLT, AnnouncementOperatorLTE, AnnouncementOperatorEQ:
			return nil
		default:
			return ErrAnnouncementInvalidTarget
		}

	default:
		return ErrAnnouncementInvalidTarget
	}
}

// IsActiveAt reports whether a published announcement's schedule includes now.
// Nil announcements and non-active statuses are ineligible. A nil schedule
// endpoint leaves that side of the interval unbounded.
//
// The start instant is included and the end instant is excluded: a record
// ending exactly at now is already inactive. This same end-time rule is used
// by the archival service, while targeting is evaluated separately for the
// user whose balance and active subscriptions are being read.
func (a *Announcement) IsActiveAt(now time.Time) bool {
	if a == nil {
		return false
	}
	if a.Status != AnnouncementStatusActive {
		return false
	}
	if a.StartsAt != nil && now.Before(*a.StartsAt) {
		return false
	}
	if a.EndsAt != nil && !now.Before(*a.EndsAt) {
		return false
	}
	return true
}

// Resolve selects an announcement heading and body together for the requested language.
// locale.Content determines translation freshness and language fallback, returning
// resolution metadata that the user response can report alongside that selected copy.
func (c AnnouncementLocalization) Resolve(code string) (AnnouncementCopy, locale.Resolution) {
	return locale.Content[AnnouncementCopy](c).Resolve(code)
}
