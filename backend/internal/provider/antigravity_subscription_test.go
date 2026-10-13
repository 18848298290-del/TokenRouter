package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

// TestNormalizeAntigravitySubscription_PaidTierWithIneligible supplies a Pro
// tier with an eligibility warning. Normalization must retain Pro, report
// abnormal status, and return the warning text.
func TestNormalizeAntigravitySubscription_PaidTierWithIneligible(t *testing.T) {
	resp := &google.AntigravityLoadCodeAssistResponse{
		PaidTier: &google.AntigravityPaidTierInfo{ID: "g1-pro-tier"},
		IneligibleTiers: []*google.AntigravityIneligibleTier{
			{ReasonMessage: "location validation required"},
		},
	}

	result := NormalizeAntigravitySubscription(resp)

	assert.Equal(t, "Pro", result.PlanType, "paid tier should preserve Pro even with ineligible tiers")
	assert.Equal(t, "abnormal", result.SubscriptionStatus)
	assert.Equal(t, "location validation required", result.SubscriptionError)
}

// TestNormalizeAntigravitySubscription_FreeTierWithIneligible supplies a Free
// tier and warning. Normalization must report the Abnormal plan and
// subscription status for that discovered state.
func TestNormalizeAntigravitySubscription_FreeTierWithIneligible(t *testing.T) {
	resp := &google.AntigravityLoadCodeAssistResponse{
		PaidTier: &google.AntigravityPaidTierInfo{ID: "free-tier"},
		IneligibleTiers: []*google.AntigravityIneligibleTier{
			{ReasonMessage: "some warning"},
		},
	}

	result := NormalizeAntigravitySubscription(resp)

	assert.Equal(t, "Abnormal", result.PlanType, "free tier with ineligible should be Abnormal")
	assert.Equal(t, "abnormal", result.SubscriptionStatus)
}

// TestNormalizeAntigravitySubscription_NoIneligible supplies paid Ultra
// discovery without eligibility warnings. It checks normalization returns
// Ultra and leaves subscription status empty for that configured response.
func TestNormalizeAntigravitySubscription_NoIneligible(t *testing.T) {
	resp := &google.AntigravityLoadCodeAssistResponse{
		PaidTier: &google.AntigravityPaidTierInfo{ID: "g1-ultra-tier"},
	}

	result := NormalizeAntigravitySubscription(resp)

	assert.Equal(t, "Ultra", result.PlanType)
	assert.Empty(t, result.SubscriptionStatus)
}

// TestNormalizeAntigravitySubscription_NilResponse supplies no discovery
// response to the normalizer. The returned plan must be Free so absent
// discovery has the defined display default.
func TestNormalizeAntigravitySubscription_NilResponse(t *testing.T) {
	result := NormalizeAntigravitySubscription(nil)
	assert.Equal(t, "Free", result.PlanType)
}

// TestNormalizeAntigravitySubscription_NoTierWithIneligible supplies an
// eligibility warning without paid/current tier metadata. It checks
// normalization returns the Abnormal plan label and abnormal subscription
// status for this discovery response.
func TestNormalizeAntigravitySubscription_NoTierWithIneligible(t *testing.T) {
	resp := &google.AntigravityLoadCodeAssistResponse{
		IneligibleTiers: []*google.AntigravityIneligibleTier{
			{ReasonMessage: "unknown issue"},
		},
	}

	result := NormalizeAntigravitySubscription(resp)

	assert.Equal(t, "Abnormal", result.PlanType, "no tier + ineligible should be Abnormal")
	assert.Equal(t, "abnormal", result.SubscriptionStatus)
}
