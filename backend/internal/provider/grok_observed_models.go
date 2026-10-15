package provider

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// grok_observed_models.go fetches and persists observed Grok model lists
// through the probe runtime.
// Grok model discovery keeps the last saved model list until a nonempty
// replacement is fetched. A parseable fetch timestamp younger than six hours
// skips work, including a future timestamp accepted by that age comparison;
// missing token input or an empty result also leaves saved models available.
// Background scheduling clones supported provider data and lets the shared
// runtime own deduplication, cancellation, and completion.

// Grok model observation keys and durations govern saved discovery data,
// freshness reuse, and the runtime budget for background fetch attempts.
const (
	GrokObservedModelsExtraKey = "grok_observed_models"
	GrokObservedModelsTTL      = 6 * time.Hour
	GrokObservedModelsTimeout  = 15 * time.Second
)

// GrokObservedModelsSnapshot records fetched model IDs and discovery
// observation metadata.
type GrokObservedModelsSnapshot struct {
	// Models is borrowed while SyncGrokObservedModels serializes its fetched list.
	// JSON decoding then supplies the map values passed to UpdateExtra rather than
	// retaining that original slice.
	Models []string `json:"models"` // reported upstream model identities
	// FetchedAt controls SyncGrokObservedModels freshness only when parseable. An
	// age below six hours skips work, including timestamps in the future accepted
	// by that comparison.
	FetchedAt string `json:"fetched_at"`       // RFC3339
	Source    string `json:"source,omitempty"` // upstream discovery source label
}

// GrokModelsOptions supplies shared scheduling, model fetch, token acquisition,
// and persistence.
type GrokModelsOptions struct {
	// Runtime tracks keyed discovery admission and cancellation. The production
	// factory shares it with quota probes; owners supply a usable runtime and
	// stop its registered work during service shutdown.
	Runtime *ProbeRuntime // shared background operation owner
	// Available is required by ScheduleGrokObservedModels after provider-kind
	// checks. Direct SyncGrokObservedModels does not repeat this predicate, so its
	// caller supplies reached fetch and write dependencies.
	Available func() bool // scheduling availability predicate
	// Token is attempted when stored access text is blank. Its error is discarded,
	// and missing or unsuccessful acquisition leaves the current model observation
	// unchanged.
	Token func(context.Context, *Record) (string, error)           // optional access-token acquisition callback
	Fetch func(context.Context, *Record, string) ([]string, error) // upstream model identity fetcher
	// UpdateExtra receives a provider ID and decoded model patch after a final
	// cancellation check. This helper adds no credential-identity comparison to
	// that supplied write.
	UpdateExtra func(context.Context, int64, map[string]any) error // provider model snapshot writer
	// Debug is called when background synchronization returns an error. Scheduling
	// owners supply a usable callback on that path so diagnostics do not interrupt
	// runtime completion.
	Debug func(string, ...any) // background failure diagnostic callback
}

// ScheduleGrokObservedModels clones supported provider data and requests one
// keyed runtime discovery task for an available Grok OAuth row. Invalid records
// skip; runtime admission owns duplicate and stopped-work handling, and
// background failures reach Debug.
func ScheduleGrokObservedModels(o GrokModelsOptions, provider *Record) {
	if provider == nil || !provider.IsGrokOAuth() || !o.Available() {
		return
	}
	// Capture supported provider containers before asynchronous discovery so
	// later owner edits do not mutate those inputs; unsupported values retain
	// CloneRecord's documented sharing.
	acc := CloneRecord(provider)
	o.Runtime.Schedule("models:"+strconv.FormatInt(acc.ID, 10), GrokObservedModelsTimeout, func(ctx context.Context) {
		if err := SyncGrokObservedModels(ctx, o, acc); err != nil {
			o.Debug("grok_observed_models_sync_failed", "provider_id", acc.ID, "error", err)
		}
	})
}

// SyncGrokObservedModels fetches and stores a nonempty discovery replacement
// for a supplied provider. Nil, a parseably fresh timestamp, unavailable token
// text, or an empty result skips successfully while retaining existing models.
// Token callback failures are discarded.
//
// Fetch errors are returned. A nonempty list is serialized and decoded into a
// fresh map before the cancellation check and ID-based UpdateExtra call. The
// helper supplies no full credential CAS, and a dependency that ignores
// cancellation can extend work beyond the context deadline.
func SyncGrokObservedModels(ctx context.Context, o GrokModelsOptions, provider *Record) error {
	if provider == nil {
		return nil
	}
	// Freshness requires a nonempty saved list and a usable timestamp.
	// Unparseable time permits replacement discovery, with empty fetch results
	// still leaving the saved catalogue available to later readers.
	if snap := ParseGrokObservedModels(provider.Extra); snap != nil {
		if t, err := time.Parse(time.RFC3339, snap.FetchedAt); err == nil && time.Since(t) < GrokObservedModelsTTL {
			return nil
		}
	}
	token := strings.TrimSpace(provider.GetGrokAccessToken())
	if token == "" && o.Token != nil {
		// Failed acquisition contributes no usable token to this best-effort
		// discovery path; retain the prior model list when stored text also stays
		// empty.
		if at, err := o.Token(ctx, provider); err == nil {
			token = strings.TrimSpace(at)
		}
	}
	if token == "" {
		return nil
	}
	ids, err := o.Fetch(ctx, provider, token)
	if err != nil {
		return err
	}
	// An empty discovery response supplies no replacement model catalogue.
	// Leave the previous observation stored so an incomplete upstream result
	// cannot erase it.
	if len(ids) == 0 {
		return nil
	}
	snap := GrokObservedModelsSnapshot{Models: ids, FetchedAt: time.Now().UTC().Format(time.RFC3339), Source: "upstream_v1_models"}
	// Encode the typed snapshot and decode a fresh Extra patch so the writer
	// receives detached model containers using the persisted JSON field names.
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return o.UpdateExtra(ctx, provider.ID, map[string]any{GrokObservedModelsExtraKey: asMap})
}

// ParseGrokObservedModels JSON-decodes saved discovery data and returns it when
// the model list is nonempty. Missing values, unsupported encoding, invalid
// shape, or empty models return nil; timestamp validity and freshness are
// interpreted separately.
func ParseGrokObservedModels(extra map[string]any) *GrokObservedModelsSnapshot {
	if extra == nil {
		return nil
	}
	raw, ok := extra[GrokObservedModelsExtraKey]
	if !ok || raw == nil {
		return nil
	}
	// Extra may hold typed or map-shaped discovery values. Treat conversion
	// failures as absent discovery, leaving freshness and replacement decisions
	// to the sync caller.
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var snap GrokObservedModelsSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil
	}
	if len(snap.Models) == 0 {
		return nil
	}
	return &snap
}
