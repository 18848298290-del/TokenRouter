package provider

import (
	"context"
)

// management_create.go creates provider batches and schedules privacy
// maintenance for successful OAuth records.
// Batch provider creation reports outcomes in input order and keeps successful
// call results when another entry fails validation or storage. Negative billing
// multipliers and invalid request-ID header settings become per-entry failures,
// while the administrative store owns each provider's remaining validation,
// persistence, and transaction completion.
//
// Successful OAuth records are copied before being collected for OpenAI or
// Antigravity privacy work. The optional AfterCreate callback receives the
// created record during the loop, and privacy batches are registered after all
// entries have been processed. Those later actions have separate service
// lifetimes; background privacy results do not change the creation counts
// already returned to the caller.

// ManagementCreationPrivacy supplies platform privacy changes for copied
// batch-created OAuth records.
type ManagementCreationPrivacy interface {
	// ForceAntigravityPrivacy applies Antigravity privacy to a copied OAuth
	// provider and returns the resulting mode. The privacy service owns request
	// cancellation and waiting for operations registered through this call.
	ForceAntigravityPrivacy(context.Context, *Record) string
	// ForceOpenAIPrivacy applies OpenAI privacy to a copied OAuth provider and
	// returns the resulting mode. The privacy service owns request cancellation
	// and waiting for operations registered through this call.
	ForceOpenAIPrivacy(context.Context, *Record) string
}

// ManagementCreationOptions binds postcreation callbacks and tracked batch
// privacy work.
type ManagementCreationOptions struct {
	// Privacy supplies the operations used by scheduled OAuth privacy batches.
	// Configure it when Background can accept OpenAI or Antigravity creation work.
	Privacy ManagementCreationPrivacy
	// Background registers the collected privacy batch after creation. Nil skips
	// scheduling, and the returned admission boolean does not affect creation
	// outcomes.
	Background func(string, func()) bool
	// AfterCreate receives each successfully created record before its success
	// entry is appended. Nil skips this hook; privacy copies are collected
	// beforehand.
	AfterCreate func(*Record)
	// Error logs a recovered background privacy panic when supplied. Nil skips
	// this logging call while the task still terminates after recovery.
	Error func(string, ...any)
}

// ManagementCreationItem records one input provider's creation outcome for HTTP
// presentation.
type ManagementCreationItem struct {
	Name    string // submitted provider display name
	ID      int64  // successfully created provider identifier
	Success bool   // provider creation succeeded flag
	Error   string // failed provider creation message
}

// ManagementCreationResult summarizes creation counts and outcomes in submitted
// input order.
type ManagementCreationResult struct {
	// Success and Failed count the submitted creation outcomes stored in Results.
	// Results retains input order, including failed entries, and batch privacy work
	// starts after this aggregate is built.
	Success, // successful provider creation count
	Failed int // failed provider creation count
	Results []ManagementCreationItem // input-ordered provider creation outcomes
}

// Create processes providers sequentially and returns one outcome for every
// submitted input. Validation and creation failures increment Failed and leave
// later entries eligible for processing. Managed RPM sanitization can modify
// the Extra maps carried by the input slice, and the supplied administrative
// creation path can normalize shared credential maps.
//
// Successful OAuth records are copied for privacy before AfterCreate receives
// the created record. OpenAI and Antigravity privacy batches are registered
// after the loop, using optional background submission. Their failures cannot
// remove completed provider writes or alter the ordered creation outcomes
// returned by this method.
func (s *ManagementBatch) Create(ctx context.Context, inputs []CreateProviderInput) (*ManagementCreationResult, error) {
	result := &ManagementCreationResult{Results: make([]ManagementCreationItem, 0, len(inputs))}
	var antigravity, openai []*Record
	for _, input := range inputs {
		if input.RateMultiplier != nil && *input.RateMultiplier < 0 {
			result.Failed++
			result.Results = append(result.Results, ManagementCreationItem{Name: input.Name, Error: "rate_multiplier must be >= 0"})
			continue
		}
		SanitizeManagedBaseRPM(input.Extra)
		if err := ValidateUpstreamRequestIDHeaderExtra(input.Extra); err != nil {
			// Header configuration failed before this provider was submitted to
			// storage. Record that entry's error while keeping later creation
			// inputs eligible to run.
			result.Failed++
			result.Results = append(result.Results, ManagementCreationItem{Name: input.Name, Error: err.Error()})
			continue
		}
		value, err := s.store.CreateProvider(ctx, &input)
		if err != nil {
			// The administrative creation path may fail after a provider write.
			// Record its error as this entry's outcome and continue without
			// undoing completed writes.
			result.Failed++
			result.Results = append(result.Results, ManagementCreationItem{Name: input.Name, Error: err.Error()})
			continue
		}
		// The postcreation hook receives the live returned record. Capture
		// supported mutable values first so later privacy work owns a separate
		// record snapshot.
		if value.Type == ProviderTypeOAuth {
			switch value.Platform {
			case PlatformAntigravity:
				antigravity = append(antigravity, CloneRecord(value))
			case PlatformOpenAI:
				openai = append(openai, CloneRecord(value))
			}
		}
		if s.creation.AfterCreate != nil {
			s.creation.AfterCreate(value)
		}
		result.Success++
		result.Results = append(result.Results, ManagementCreationItem{Name: input.Name, ID: value.ID, Success: true})
	}
	// Register the privacy batches after all creation outcomes are known. Their
	// asynchronous service requests do not participate in these completed
	// provider writes.
	s.scheduleCreationPrivacy(antigravity, PlatformAntigravity)
	s.scheduleCreationPrivacy(openai, PlatformOpenAI)
	return result, nil
}

// scheduleCreationPrivacy submits a platform privacy batch when records and a
// background callback are present. It reads the supplied records later, so
// Create provides copies; the configured privacy service manages request
// cancellation and shutdown.
func (s *ManagementBatch) scheduleCreationPrivacy(values []*Record, platform string) {
	if len(values) == 0 || s.creation.Background == nil {
		return
	}
	// Background work reads the copies collected during Create. PrivacyService
	// registers each request separately so its shutdown wait covers requests
	// that have started.
	s.creation.Background("handler/admin/provider_handler.go:BatchCreate", func() {
		defer func() {
			// Creation has completed before this privacy task starts. Recover a
			// panic for diagnostics while retaining successful provider writes
			// and their already reported outcomes.
			if r := recover(); r != nil {
				event := "batch_create_openai_privacy_panic"
				if platform == PlatformAntigravity {
					event = "batch_create_antigravity_privacy_panic"
				}
				if s.creation.Error != nil {
					s.creation.Error(event, "recover", r)
				}
			}
		}()
		for _, value := range values {
			if platform == PlatformAntigravity {
				s.creation.Privacy.ForceAntigravityPrivacy(context.Background(), value)
			} else {
				s.creation.Privacy.ForceOpenAIPrivacy(context.Background(), value)
			}
		}
	})
}
