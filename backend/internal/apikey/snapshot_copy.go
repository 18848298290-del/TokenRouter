package apikey

import (
	"slices"
)

// Authentication snapshots reuse parsed configuration across requests, so
// selected mutable values need fresh storage during conversion. These copies
// retain nil input and isolate scalar pointers, model candidate slices, model
// lists, and membership time pointers at the points that construct request
// views.

// clonePointer copies the pointed-to value into newly allocated storage,
// retaining nil input. Nested references inside a composite value remain
// shared; current authentication callers use scalar values and time.Time
// pointers.
func clonePointer[T any](v *T) *T {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

// cloneModelRouting copies the model map and each provider-ID slice for
// snapshot conversion. Nil maps remain nil, and slices retain nil versus empty
// values so callers can modify copied candidate lists independently.
func cloneModelRouting(v map[string][]int64) map[string][]int64 {
	if v == nil {
		return nil
	}
	out := make(map[string][]int64, len(v))
	for k, ids := range v {
		out[k] = slices.Clone(ids)
	}
	return out
}

// cloneModelsList copies the model list slice within a configuration value. The
// other configuration fields are copied by value, and a nil Models slice
// remains nil for the resulting snapshot.
func cloneModelsList(v GroupModelsListConfig) GroupModelsListConfig {
	v.Models = slices.Clone(v.Models)
	return v
}

// cloneMembership copies membership data and allocates its window-start and
// last-active time pointers. Nil input remains nil; all current membership
// fields are scalar values or those copied pointers, so requests can update
// these values independently.
func cloneMembership(v *TeamMembership) *TeamMembership {
	if v == nil {
		return nil
	}
	out := *v
	out.DailyWindowStart = clonePointer(v.DailyWindowStart)
	out.WeeklyWindowStart = clonePointer(v.WeeklyWindowStart)
	out.MonthlyWindowStart = clonePointer(v.MonthlyWindowStart)
	out.LastActiveAt = clonePointer(v.LastActiveAt)
	return &out
}
