package apikey

import (
	"slices"
)

// clonePointer copies scalar and time values while retaining nil.
func clonePointer[T any](v *T) *T {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

// cloneModelRouting copies the map and provider slices, retaining the distinction between nil and empty.
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

func cloneModelsList(v GroupModelsListConfig) GroupModelsListConfig {
	v.Models = slices.Clone(v.Models)
	return v
}

// cloneMembership copies member data and window pointers so request changes stay outside the cache.
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
