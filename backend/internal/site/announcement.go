package site

import (
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

const (
	AnnouncementStatusDraft    = "draft"
	AnnouncementStatusActive   = "active"
	AnnouncementStatusArchived = "archived"

	AnnouncementNotifyModeSilent = "silent"
	AnnouncementNotifyModePopup  = "popup"

	AnnouncementConditionTypeSubscription = "subscription"
	AnnouncementConditionTypeBalance      = "balance"

	AnnouncementOperatorIn  = "in"
	AnnouncementOperatorGT  = "gt"
	AnnouncementOperatorGTE = "gte"
	AnnouncementOperatorLT  = "lt"
	AnnouncementOperatorLTE = "lte"
	AnnouncementOperatorEQ  = "eq"
)

var (
	ErrAnnouncementNotFound      = infraerrors.NotFound("ANNOUNCEMENT_NOT_FOUND", "announcement not found")
	ErrAnnouncementInvalidTarget = infraerrors.BadRequest("ANNOUNCEMENT_INVALID_TARGET", "invalid announcement targeting rules")
)

type AnnouncementTargeting struct {
	// AnyOf matches when any condition group matches.
	AnyOf []AnnouncementConditionGroup `json:"any_of,omitempty"`
}

type AnnouncementConditionGroup struct {
	// AllOf matches when every condition in the group matches.
	AllOf []AnnouncementCondition `json:"all_of,omitempty"`
}

type AnnouncementCondition struct {
	// Type: subscription | balance
	Type string `json:"type"`

	// Operator:
	// - subscription: in
	// - balance: gt/gte/lt/lte/eq
	Operator string `json:"operator"`

	// plan IDs matched by a subscription condition
	PlanIDs []int64 `json:"plan_ids,omitempty"`

	// comparison threshold for a balance condition
	Value float64 `json:"value,omitempty"`
}

// AnnouncementCopy stores the title and body as one language version.
type AnnouncementCopy struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// AnnouncementLocalization contains localized announcement content.
type AnnouncementLocalization locale.Content[AnnouncementCopy]

type Announcement struct {
	Localization AnnouncementLocalization
	Resolution   locale.Resolution
	ID           int64
	Title        string
	Content      string
	Status       string
	NotifyMode   string
	Targeting    AnnouncementTargeting
	StartsAt     *time.Time
	EndsAt       *time.Time
	CreatedBy    *int64
	UpdatedBy    *int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (t AnnouncementTargeting) Matches(balance float64, activePlanIDs map[int64]struct{}) bool {
	// No targeting rules means the announcement is visible to all users.
	if len(t.AnyOf) == 0 {
		return true
	}

	for _, group := range t.AnyOf {
		if len(group.AllOf) == 0 {
			// An empty group must not make an OR expression match every user.
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

func (t AnnouncementTargeting) NormalizeAndValidate() (AnnouncementTargeting, error) {
	normalized := AnnouncementTargeting{AnyOf: make([]AnnouncementConditionGroup, 0, len(t.AnyOf))}

	// Empty targeting is valid and matches all users.
	if len(t.AnyOf) == 0 {
		return normalized, nil
	}

	if len(t.AnyOf) > 50 {
		return AnnouncementTargeting{}, ErrAnnouncementInvalidTarget
	}

	for _, g := range t.AnyOf {
		if len(g.AllOf) == 0 {
			return AnnouncementTargeting{}, ErrAnnouncementInvalidTarget
		}
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
		// The end time is exclusive.
		return false
	}
	return true
}

// Resolve selects a title and body from the same content version for the requested language.
func (c AnnouncementLocalization) Resolve(code string) (AnnouncementCopy, locale.Resolution) {
	return locale.Content[AnnouncementCopy](c).Resolve(code)
}
