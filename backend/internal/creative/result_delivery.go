package creative

import (
	"context"
	"errors"
	"time"
)

const (
	OutputDeliveryPending = "DELIVERY_PENDING"
	OutputDeliveryLost    = "DELIVERY_LOST"
)

// ProviderOutput contains a provider result; image bytes pass through worker memory and temporary storage.
type ProviderOutput struct {
	Index        int
	Success      bool
	Bytes        []byte
	Mime         string
	ErrorCode    string
	ErrorMessage string
}

// ProviderOutcomeStore records provider success, output metadata, and recovery work in one transaction.
type ProviderOutcomeStore interface {
	RecordProviderOutcome(context.Context, string, int64, []CreativeRunOutput, time.Time) error
	CompleteProviderOutcome(context.Context, string, float64, bool, time.Time) error
}

// ResultDelivery records provider outcomes and saves generated images for client retrieval.
type ResultDelivery struct {
	Now      func() time.Time
	Repo     CreativeRunRepository
	Outcomes ProviderOutcomeStore
	Store    CreativeTransientStore
	TTL      time.Duration
}

// Record persists the outcome before bounded output-save retries; exhausted saves are marked as delivery loss.
func (d ResultDelivery) Record(ctx context.Context, id string, providerID int64, outputs []ProviderOutput) error {
	if d.Outcomes == nil {
		return errors.New("creative platform outcome store is not configured")
	}
	now := d.now()
	expires := now.Add(d.TTL)
	metadata := make([]CreativeRunOutput, 0, len(outputs))
	for _, output := range outputs {
		status := CreativeRunOutputStatusFailed
		code, message := output.ErrorCode, output.ErrorMessage
		if output.Success {
			if len(output.Bytes) == 0 {
				return ErrCreativeTransientFailed.WithCause(errors.New("creative output is empty"))
			}
			status = CreativeRunOutputStatusSucceeded
			code = OutputDeliveryPending
			message = ""
		}
		mime := output.Mime
		size := int64(len(output.Bytes))
		metadata = append(metadata, CreativeRunOutput{RunID: id, OutputIndex: output.Index, Status: status, MimeType: &mime, ByteSize: &size, TransientExpiresAt: &expires, ErrorCode: &code, ErrorMessage: &message})
	}
	if err := d.Outcomes.RecordProviderOutcome(ctx, id, providerID, metadata, now); err != nil {
		return err
	}
	saveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, output := range outputs {
		if !output.Success {
			continue
		}
		var saved bool
		for attempt := range 3 {
			if saveCtx.Err() != nil {
				break
			}
			if d.Store != nil && d.Store.SaveOutput(saveCtx, id, output.Index, output.Bytes, d.TTL) == nil {
				saved = true
				break
			}
			if attempt < 2 {
				timer := time.NewTimer(time.Second)
				select {
				case <-saveCtx.Done():
					timer.Stop()
				case <-timer.C:
				}
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code := ""
		if !saved {
			code = OutputDeliveryLost
		}
		if err := d.Repo.UpdateCreativeRunOutput(ctx, id, output.Index, CreativeRunOutputStatusSucceeded, output.Mime, int64(len(output.Bytes)), &expires, code, ""); err != nil {
			return err
		}
	}
	return nil
}

// Lost reports marked or confirmed missing output; storage read failures remain errors for retry.
func (d ResultDelivery) Lost(ctx context.Context, id string, outputs []*CreativeRunOutput) (bool, error) {
	lost := false
	for _, output := range outputs {
		if output == nil || output.Status != CreativeRunOutputStatusSucceeded {
			continue
		}
		if output.ErrorCode != nil && *output.ErrorCode == OutputDeliveryLost {
			lost = true
			continue
		}
		if d.Store == nil {
			return false, ErrCreativeTransientUnavailable
		}
		_, err := d.Store.LoadOutput(ctx, id, output.OutputIndex)
		if errors.Is(err, ErrCreativeTransientNotFound) {
			lost = true
			continue
		}
		if err != nil {
			return false, err
		}
	}
	return lost, nil
}

// now returns the injected clock's time or the system time when no clock is configured.
func (d ResultDelivery) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}
