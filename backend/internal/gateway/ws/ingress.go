package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// ingress.go executes sequential Responses turns with connection affinity,
// replay history, and recovery.
// A Responses connection must retain the correct request history and upstream
// connection while later turns refer to earlier response IDs. The first
// normalized payload fixes HTTP-bridge versus pooled execution for this Run
// invocation; both paths keep the client model and requested effort with each
// forwarded turn.
//
// HTTP bridging expands ordinary replay history and a separate
// replacement-provider history from immutable item bodies. Pooled execution
// holds one lease, preferring response affinity and store-disabled session
// affinity at startup, then evaluates strict continuation before reusing it.
// Missing-response recovery and pre-output transport retry can replay a turn
// without reacquiring admission, while idle ping failure has its own
// context-coverage checks. Replay mutation is published only after required
// continuation and input edits succeed. Completion precedes affinity writes,
// and lease cleanup marks unfinished relay work broken while retaining
// independently marked transport failures.

// payloadString returns the trimmed gjson string form at key for ingress
// decisions. Missing values produce empty; this helper does not validate the
// complete document, and nonstring values can have nonempty string forms.
func payloadString(body []byte, key string) string {
	return strings.TrimSpace(gjson.GetBytes(body, key).String())
}

// Run executes the first normalized turn and every later client request through
// HTTP bridge or one held provider pool lease. State, Port, and Codec are
// required; callers serialize the session and retain immutable request bytes
// referenced by replay histories.
//
// The initial payload determines the execution path. Bridge completion updates
// separate normal and provider-failover histories; a later classified provider
// failure carries a replay-safe current request for entry replacement. Pooled
// turns preserve strict connection affinity when store is disabled and
// continuation is present. A missing-response error or pre-output transport
// failure can use its bounded recovery path while skipping repeated admission
// callbacks. Idle ping recovery separately requires sufficient tool-call
// context before dropping continuation. AfterTurn observes final local outcomes
// before affinity writes; deferred cleanup releases any held lease.
func (s *IngressSession) Run(ctx context.Context, firstMessage []byte) error {
	p, codec, o, state := s.Port, s.Codec, s.Options, s.State
	refresh := func() {
		if s.Refresh == nil {
			return
		}
		value := s.Refresh()
		o.PreviousRecovery = value.IngressPreviousResponseRecoveryEnabled
		// Convert snapshot TTL seconds into durations for subsequent affinity writes.
		o.ResponseStickyTTL = time.Duration(value.StickyResponseIDTTLSeconds) * time.Second
		o.SessionStickyTTL = time.Duration(value.StickySessionTTLSeconds) * time.Second
	}
	hooks, stateStore, groupID := s.Hooks, s.Store, s.Options.GroupID
	debugEnabled := o.Debug
	storeDisabledConnMode := o.StoreDisabledMode
	firstPayload, err := p.Parse(firstMessage, false, 1)
	if err != nil {
		return err
	}
	state.SessionHash = p.GenerateHash(firstPayload.RawForHash)
	// Load cached turn metadata only when no current value was supplied, letting
	// an existing handshake value remain the session's selected continuation
	// header.
	if state.TurnState == "" && stateStore != nil && state.SessionHash != "" {
		if value, ok := stateStore.GetSessionTurnState(groupID, state.SessionHash); ok {
			state.TurnState = value
		}
	}
	// Recompute startup connection preference from response affinity, then
	// store-disabled session affinity when no continuation is supplied, so
	// unrelated caller preference does not select this lease.
	state.PreferredConnID = ""
	if stateStore != nil && firstPayload.PreviousResponseID != "" {
		if value, ok := stateStore.GetResponseConn(firstPayload.PreviousResponseID); ok {
			state.PreferredConnID = value
		}
	}
	state.StoreDisabled = p.StoreDisabled(firstPayload.PayloadRaw)
	if stateStore != nil && state.StoreDisabled && firstPayload.PreviousResponseID == "" && state.SessionHash != "" {
		if value, ok := stateStore.GetSessionConn(groupID, state.SessionHash); ok {
			state.PreferredConnID = value
		}
	}
	if p.ShouldBridge(firstPayload) {
		p.Log(fmt.Sprintf(
			"ingress_ws_http_bridge_start provider_id=%d provider_type=%s payload_bytes=%d threshold_bytes=%d has_session_hash=%v store_disabled=%v",
			o.ProviderID,
			o.ProviderType,
			firstPayload.PayloadBytes,
			o.BridgeThreshold,
			state.SessionHash != "",
			state.StoreDisabled,
		))
		currentBridgePayload := firstPayload
		// Keep the first payload as the Grok session seed while later turns select
		// their routing model, so cache identity can separate model-specific history.
		grokCacheSeedPayload := firstPayload.PayloadRaw
		var bridgeReplayInput []json.RawMessage
		bridgeReplayInputExists := false
		var bridgeProviderFailoverInput []json.RawMessage
		bridgeProviderFailoverInputExists := false
		for turn := 1; ; turn++ {
			refresh()
			turnStartedAt := time.Now()
			if hooks != nil && hooks.TurnStarted != nil {
				hooks.TurnStarted(turn, turnStartedAt)
			}
			if turn > 1 && hooks != nil && hooks.BeforeRequest != nil {
				updatedPayload, err := hooks.BeforeRequest(turn, currentBridgePayload.PayloadRaw, currentBridgePayload.OriginalModel, currentBridgePayload.PreviousResponseID)
				if err != nil {
					return err
				}
				if len(updatedPayload) > 0 {
					currentBridgePayload.PayloadRaw = updatedPayload
					currentBridgePayload.PayloadBytes = len(updatedPayload)
					currentBridgePayload.PreviousResponseID = strings.TrimSpace(gjson.GetBytes(updatedPayload, "previous_response_id").String())
				}
			}
			if hooks != nil && hooks.BeforeTurn != nil {
				if err := hooks.BeforeTurn(turn); err != nil {
					return err
				}
			}
			p.SetRequestState(state.TurnState, state.SessionHash)
			// Strip known-invalid ciphertext from current and retained input together,
			// so replay prefix matching compares the same surviving history items.
			if invalidDigests := p.InvalidDigests(groupID, state.SessionHash); len(invalidDigests) > 0 {
				strippedPayload, strippedCount := p.StripInvalid(
					currentBridgePayload.PayloadRaw, invalidDigests, "ingress_ws_http_bridge_invalid_encrypted_lineage_strip", o.ProviderID, turn,
				)
				if strippedCount > 0 {
					currentBridgePayload.PayloadRaw = strippedPayload
					currentBridgePayload.PayloadBytes = len(strippedPayload)
				}
				if bridgeReplayInputExists {
					bridgeReplayInput, _ = codec.StripItems(bridgeReplayInput, invalidDigests)
				}
				if bridgeProviderFailoverInputExists {
					bridgeProviderFailoverInput, _ = codec.StripItems(bridgeProviderFailoverInput, invalidDigests)
				}
			}
			bridgePayloadRaw := currentBridgePayload.PayloadRaw

			toolOutputCoverage := wire.AnalyzeToolCallOutputContextCoverageBytes(currentBridgePayload.PayloadRaw)
			needsBridgeReplay := currentBridgePayload.PreviousResponseID != "" ||
				(toolOutputCoverage.HasFunctionCallOutput && !toolOutputCoverage.ContextCoversAllCallIDs)
			// Build ordinary and replacement-provider histories from the same parsed
			// current items. Their arrays or item bytes can be shared, so later history
			// extension uses Combine rather than in-place append.
			bridgeCurrentItems, bridgeCurrentItemsExist, extractErr := codec.Extract(
				currentBridgePayload.PayloadRaw,
			)
			if extractErr != nil {
				return fmt.Errorf("build websocket http bridge replay input: %w", extractErr)
			}
			turnReplayInput, turnReplayInputExists := codec.BuildFromItems(
				bridgeReplayInput,
				bridgeReplayInputExists,
				bridgeCurrentItems,
				bridgeCurrentItemsExist,
				needsBridgeReplay,
			)
			turnProviderFailoverInput, turnProviderFailoverInputExists := codec.BuildFromItems(
				bridgeProviderFailoverInput,
				bridgeProviderFailoverInputExists,
				bridgeCurrentItems,
				bridgeCurrentItemsExist,
				needsBridgeReplay,
			)
			// A continuation or uncovered tool output needs the retained full input on
			// the HTTP path; install that sequence only when replay input presence is
			// known.
			if needsBridgeReplay && turnReplayInputExists {
				updatedPayload, setInputErr := codec.SetInput(
					currentBridgePayload.PayloadRaw,
					turnReplayInput,
					true,
				)
				if setInputErr != nil {
					return fmt.Errorf("set websocket http bridge replay input: %w", setInputErr)
				}
				bridgePayloadRaw = updatedPayload

				p.Log(fmt.Sprintf(
					"ingress_ws_http_bridge_replay_input provider_id=%d turn=%d input_items=%d previous_response_id_present=%v has_tool_output=%v",
					o.ProviderID,
					turn,
					len(turnReplayInput),
					currentBridgePayload.PreviousResponseID != "",
					codec.HasOutput(currentBridgePayload.PayloadRaw),
				))
			}
			grokCacheIdentity := ""
			// Use the first request as the stable Grok session seed while deriving each
			// turn's cache identity with its current routing model, keeping
			// model-specific histories distinct.
			if o.Platform == "grok" {
				grokCacheIdentity, err = p.BridgeIdentity(grokCacheSeedPayload, currentBridgePayload.RoutingModel, turn)
				if err != nil {
					return fmt.Errorf("resolve Grok websocket cache identity: %w", err)
				}
			}
			result, bridgeErr := p.Bridge(ctx, currentBridgePayload, bridgePayloadRaw, grokCacheIdentity, turn)

			// Attach the client's pre-policy effort to the bridge result before
			// completion observes it, since HTTP execution received the already
			// normalized request body.
			if result != nil {
				result.RequestedReasoningEffort = currentBridgePayload.RequestedReasoningEffort
			}
			if hooks != nil && hooks.AfterTurn != nil {
				hooks.AfterTurn(TurnCapture{
					Turn:               turn,
					StartedAt:          turnStartedAt,
					RequestBody:        append([]byte(nil), bridgePayloadRaw...),
					OriginalModel:      currentBridgePayload.OriginalModel,
					PreviousResponseID: currentBridgePayload.PreviousResponseID,
					Result:             result,
					Err:                bridgeErr,
					PayloadSource:      "http_bridge",
				})
			}
			if bridgeErr != nil {
				// A later bridge failure cannot reuse the initial request for provider
				// replacement. Build a self-contained current payload and retain an empty
				// unsafe result for entry to reject.
				if turn > 1 && p.IsFailover(bridgeErr) {
					retryPayload, retrySafe, retryPayloadErr := codec.RetryPayload(
						currentBridgePayload.ProviderIdentitySourceRaw,
						turnProviderFailoverInput,
						turnProviderFailoverInputExists,
						currentBridgePayload.OriginalModel,
					)
					if retryPayloadErr != nil {
						return fmt.Errorf("build websocket current-turn failover payload: %w", retryPayloadErr)
					}
					if !retrySafe {
						retryPayload = nil
					}
					return NewCurrentTurnFailoverError(bridgeErr, retryPayload)
				}
				return bridgeErr
			}
			if result == nil {
				return errors.New("websocket http bridge turn result is nil")
			}
			// Both histories can share bridgeCurrentItems' array. Extend them through
			// Combine so appending one output sequence cannot overwrite the other
			// history's stored item references.
			bridgeReplayInput = turnReplayInput
			bridgeReplayInputExists = turnReplayInputExists
			if result.WSReplayInputExists {
				bridgeReplayInput = codec.Combine(bridgeReplayInput, result.WSReplayInput)
				bridgeReplayInputExists = true
			}
			bridgeProviderFailoverInput = turnProviderFailoverInput
			bridgeProviderFailoverInputExists = turnProviderFailoverInputExists
			if len(result.WSProviderFailoverReplayInput) > 0 {
				bridgeProviderFailoverInput = codec.Combine(
					bridgeProviderFailoverInput,
					result.WSProviderFailoverReplayInput,
				)
				bridgeProviderFailoverInputExists = true
			}
			// Publish successful bridge continuation metadata after its completion
			// callback, then write session and response affinity independently; failed
			// persistent affinity writes remain observable through BindWarning.
			if bridgeTurnState := strings.TrimSpace(result.ResponseTurnState); bridgeTurnState != "" {
				state.TurnState = bridgeTurnState
				if stateStore != nil && state.SessionHash != "" {
					stateStore.BindSessionTurnState(groupID, state.SessionHash, bridgeTurnState, o.SessionStickyTTL)
				}
			}
			responseID := strings.TrimSpace(result.RequestID)
			if responseID != "" && stateStore != nil {
				ttl := o.ResponseStickyTTL
				p.BindWarning(groupID, o.ProviderID, responseID, stateStore.BindResponseProvider(ctx, groupID, responseID, o.ProviderID, ttl))
			}
			nextClientMessage, readErr := p.ReadClient()
			if readErr != nil {
				if p.IsDisconnect(readErr) {
					closeStatus, closeReason := p.SummarizeClose(readErr)
					p.Log(fmt.Sprintf(
						"ingress_ws_http_bridge_client_closed provider_id=%d close_status=%s close_reason=%s",
						o.ProviderID,
						closeStatus,
						p.TruncateLog(closeReason, 120),
					))
					return nil
				}
				return fmt.Errorf("read client websocket request: %w", readErr)
			}
			nextPayload, parseErr := p.Parse(nextClientMessage, true, turn+1)
			if parseErr != nil {
				return parseErr
			}
			currentBridgePayload = nextPayload
		}
	}
	if err := p.OpenPool(firstPayload); err != nil {
		return err
	}
	// Permit one provider-identity recovery across this session's acquisitions;
	// mark it consumed before recovery so a failed repair cannot reopen the same
	// dial loop.
	credentialRecovered := false
	acquireTurnLease := func(turn int, preferred string, force bool) (ConnLease, error) {
		for {
			lease, err := p.Acquire(turn, preferred, force, !credentialRecovered)
			var recovery *AcquireRecoveryError
			if errors.As(err, &recovery) && !credentialRecovered {
				credentialRecovered = true
				if recoveryErr := p.RecoverAcquire(ctx); recoveryErr != nil {
					return nil, fmt.Errorf("agent identity task recovery failed: %w", recoveryErr)
				}
				continue
			}
			return lease, err
		}
	}
	currentPayload := firstPayload.PayloadRaw
	currentOriginalModel := firstPayload.OriginalModel
	currentRoutingModel := firstPayload.RoutingModel
	currentImageBillingModel := firstPayload.ImageBillingModel
	currentImageSizeTier := firstPayload.ImageSizeTier
	currentImageInputSize := firstPayload.ImageInputSize
	currentPayloadBytes := firstPayload.PayloadBytes
	currentRequestedReasoningEffort := firstPayload.RequestedReasoningEffort
	isStrictAffinityTurn := func(payload []byte) bool {
		if !state.StoreDisabled {
			return false
		}
		return strings.TrimSpace(payloadString(payload, "previous_response_id")) != ""
	}
	var sessionLease ConnLease
	sessionConnID := ""
	pinnedSessionConnID := ""
	unpinSessionConn := func(connID string) {
		connID = strings.TrimSpace(connID)
		if connID == "" || pinnedSessionConnID != connID {
			return
		}
		// Release only the locally tracked pin for this exact connection, clearing
		// its tracking before a replacement lease or ordinary release can reuse the
		// session reservation.
		p.UnpinConn(o.ProviderID, connID)
		pinnedSessionConnID = ""
	}
	pinSessionConn := func(connID string) {
		if !state.StoreDisabled {
			return
		}
		connID = strings.TrimSpace(connID)
		if connID == "" || pinnedSessionConnID == connID {
			return
		}
		// Return any older connection reservation before pinning a new one, so the
		// store-disabled session does not retain two tracked pool pins.
		if pinnedSessionConnID != "" {
			p.UnpinConn(o.ProviderID, pinnedSessionConnID)
			pinnedSessionConnID = ""
		}
		if p.PinConn(o.ProviderID, connID) {
			pinnedSessionConnID = connID
		}
	}
	// lastTurnClean records a nil Relay error, including a returned failure
	// terminal. Relay can mark the connection broken independently; cleanup also
	// marks any unfinished relay work broken before release.
	lastTurnClean := false
	releaseSessionLease := func() {
		if sessionLease == nil {
			return
		}
		// An acquired lease with unfinished or failed relay work cannot return as a
		// reusable connection; keep any earlier broken mark and dispose it on
		// release.
		if !lastTurnClean {
			sessionLease.MarkBroken()
		}
		unpinSessionConn(sessionConnID)
		sessionLease.Release()
		if debugEnabled {
			p.Debug(fmt.Sprintf(
				"ingress_ws_upstream_released provider_id=%d conn_id=%s",
				o.ProviderID,
				p.TruncateLog(sessionConnID, 64),
			))
		}
	}
	defer releaseSessionLease()

	turn := 1
	turnRetry := 0
	turnPrevRecoveryTried := false
	lastTurnFinishedAt := time.Time{}
	lastTurnResponseID := ""
	lastTurnPayload := []byte(nil)
	var lastTurnStrictState PreviousTurn
	lastTurnReplayInput := []json.RawMessage(nil)
	lastTurnReplayInputExists := false
	currentTurnReplayInput := []json.RawMessage(nil)
	currentTurnReplayInputExists := false
	skipBeforeTurn := false
	hasCurrentOrReplayFunctionCallOutput := func(payload []byte) bool {
		if codec.HasOutput(payload) {
			return true
		}
		return currentTurnReplayInputExists && codec.ItemsHaveOutput(currentTurnReplayInput)
	}
	resetSessionLease := func(markBroken bool) {
		if sessionLease == nil {
			return
		}
		if markBroken {
			sessionLease.MarkBroken()
		}
		releaseSessionLease()
		sessionLease = nil
		sessionConnID = ""
		state.PreferredConnID = ""
	}
	// Missing-response recovery consumes one opportunity before payload mutation,
	// retains tool-dependent continuation, and skips repeated admission only after
	// both dropping the anchor and installing replay succeed.
	recoverIngressPrevResponseNotFound := func(relayErr error, turn int, connID string) bool {
		if !IsPreviousResponseNotFound(relayErr) {
			return false
		}
		if turnPrevRecoveryTried || !o.PreviousRecovery {
			return false
		}
		// Keep continuation whenever current or replay input contains a tool output;
		// this recovery path does not use the idle-ping path's complete-call-context
		// exception when deciding whether that output can lose its anchor.
		if hasCurrentOrReplayFunctionCallOutput(currentPayload) {
			return false
		}
		if isStrictAffinityTurn(currentPayload) {
			// For strict affinity, a missing continuation point permits one replay
			// after removing previous_response_id.
			p.Log(fmt.Sprintf(
				"ingress_ws_prev_response_recovery_layer2 provider_id=%d turn=%d conn_id=%s store_disabled_conn_mode=%s action=drop_previous_response_id_retry",
				o.ProviderID,
				turn,
				p.TruncateLog(connID, 64),
				p.NormalizeLog(storeDisabledConnMode),
			))
		}
		turnPrevRecoveryTried = true
		updatedPayload, removed, dropErr := codec.DropPrevious(currentPayload)
		// A failed or ineffective anchor removal leaves the current request unchanged
		// and ends this recovery attempt; its consumed budget cannot repeat the same
		// mutation.
		if dropErr != nil || !removed {
			reason := "not_removed"
			if dropErr != nil {
				reason = "drop_error"
			}
			p.Log(fmt.Sprintf(
				"ingress_ws_prev_response_recovery_skip provider_id=%d turn=%d conn_id=%s reason=%s",
				o.ProviderID,
				turn,
				p.TruncateLog(connID, 64),
				p.NormalizeLog(reason),
			))
			return false
		}
		updatedWithInput, setInputErr := codec.SetInput(
			updatedPayload,
			currentTurnReplayInput,
			currentTurnReplayInputExists,
		)
		// Retain the current anchored payload if full-input reconstruction fails,
		// because dropping continuation without replay would lose the earlier context
		// required by this turn.
		if setInputErr != nil {
			p.Log(fmt.Sprintf(
				"ingress_ws_prev_response_recovery_skip provider_id=%d turn=%d conn_id=%s reason=set_full_input_error cause=%s",
				o.ProviderID,
				turn,
				p.TruncateLog(connID, 64),
				p.TruncateLog(setInputErr.Error(), 160),
			))
			return false
		}
		p.Log(fmt.Sprintf(
			"ingress_ws_prev_response_recovery provider_id=%d turn=%d conn_id=%s action=drop_previous_response_id retry=1",
			o.ProviderID,
			turn,
			p.TruncateLog(connID, 64),
		))
		currentPayload = updatedWithInput
		currentPayloadBytes = len(updatedWithInput)
		resetSessionLease(true)
		skipBeforeTurn = true
		return true
	}
	// Retry only one pre-output transport failure when continuation does not force
	// the held connection; preserve turn admission by skipping its callbacks after
	// resetting the broken lease.
	retryIngressTurn := func(relayErr error, turn int, connID string) bool {
		if !IsIngressTurnRetryable(relayErr) || turnRetry >= 1 {
			return false
		}
		if isStrictAffinityTurn(currentPayload) {
			p.Log(fmt.Sprintf(
				"ingress_ws_turn_retry_skip provider_id=%d turn=%d conn_id=%s reason=strict_affinity",
				o.ProviderID,
				turn,
				p.TruncateLog(connID, 64),
			))
			return false
		}
		turnRetry++
		p.Log(fmt.Sprintf(
			"ingress_ws_turn_retry provider_id=%d turn=%d retry=%d reason=%s conn_id=%s",
			o.ProviderID,
			turn,
			turnRetry,
			p.TruncateLog(IngressTurnRetryReason(relayErr), 160),
			p.TruncateLog(connID, 64),
		))
		resetSessionLease(true)
		skipBeforeTurn = true
		return true
	}
	for {
		refresh()
		turnStartedAt := time.Now()
		if hooks != nil && hooks.TurnStarted != nil {
			hooks.TurnStarted(turn, turnStartedAt)
		}
		if turn > 1 && !skipBeforeTurn && hooks != nil && hooks.BeforeRequest != nil {
			currentPreviousResponseID := payloadString(currentPayload, "previous_response_id")
			updatedPayload, err := hooks.BeforeRequest(turn, currentPayload, currentOriginalModel, currentPreviousResponseID)
			if err != nil {
				return err
			}
			if len(updatedPayload) > 0 {
				currentPayload = updatedPayload
				currentPayloadBytes = len(updatedPayload)
			}
		}
		if !skipBeforeTurn && hooks != nil && hooks.BeforeTurn != nil {
			if err := hooks.BeforeTurn(turn); err != nil {
				return err
			}
		}
		skipBeforeTurn = false
		// Strip known-invalid ciphertext from the request and replay history.
		// Both sequences must stay aligned for prefix reuse.
		if invalidDigests := p.InvalidDigests(groupID, state.SessionHash); len(invalidDigests) > 0 {
			strippedPayload, strippedCount := p.StripInvalid(
				currentPayload, invalidDigests, "ingress_ws_invalid_encrypted_lineage_strip", o.ProviderID, turn,
			)
			if strippedCount > 0 {
				currentPayload = strippedPayload
				currentPayloadBytes = len(strippedPayload)
			}
			if lastTurnReplayInputExists {
				lastTurnReplayInput, _ = codec.StripItems(lastTurnReplayInput, invalidDigests)
			}
		}
		currentPreviousResponseID := payloadString(currentPayload, "previous_response_id")
		expectedPrev := strings.TrimSpace(lastTurnResponseID)
		toolSignals := wire.ToolContinuationSignals{
			HasFunctionCallOutput: codec.HasOutput(currentPayload),
		}
		if toolSignals.HasFunctionCallOutput {
			var currentReqBody map[string]any
			// Use detailed tool signals when decoding succeeds; otherwise retain the
			// earlier tool-output presence observation for the codec's continuation
			// inference decision.
			if err := json.Unmarshal(currentPayload, &currentReqBody); err == nil {
				toolSignals = wire.AnalyzeToolContinuationSignals(currentReqBody)
			}
		}
		hasFunctionCallOutput := toolSignals.HasFunctionCallOutput
		// For a later store-disabled tool output lacking actual call context, the
		// codec may infer the last completed response ID so upstream can associate
		// its call_id.
		if codec.ShouldInfer(
			state.StoreDisabled,
			turn,
			toolSignals,
			currentPreviousResponseID,
			expectedPrev,
		) {
			updatedPayload, setPrevErr := codec.SetPrevious(currentPayload, expectedPrev)
			// Inference failure is logged while retaining the supplied request bytes,
			// allowing the normal protocol path to handle its available continuation and
			// tool context.
			if setPrevErr != nil {
				p.Log(fmt.Sprintf(
					"ingress_ws_function_call_output_prev_infer_skip provider_id=%d turn=%d conn_id=%s reason=set_previous_response_id_error cause=%s expected_previous_response_id=%s",
					o.ProviderID,
					turn,
					p.TruncateLog(sessionConnID, 64),
					p.TruncateLog(setPrevErr.Error(), 160),
					p.TruncateLog(expectedPrev, 64),
				))
			} else {
				currentPayload = updatedPayload
				currentPayloadBytes = len(updatedPayload)
				currentPreviousResponseID = expectedPrev
				p.Log(fmt.Sprintf(
					"ingress_ws_function_call_output_prev_infer provider_id=%d turn=%d conn_id=%s action=set_previous_response_id previous_response_id=%s",
					o.ProviderID,
					turn,
					p.TruncateLog(sessionConnID, 64),
					p.TruncateLog(expectedPrev, 64),
				))
			}
		}
		nextReplayInput, nextReplayInputExists, replayInputErr := codec.Build(
			lastTurnReplayInput,
			lastTurnReplayInputExists,
			currentPayload,
			currentPreviousResponseID != "",
		)
		// Unavailable replay does not erase the direct request: clear optional
		// recovery history and forward current bytes, limiting later recovery to the
		// context that remains known.
		if replayInputErr != nil {
			p.Log(fmt.Sprintf(
				"ingress_ws_replay_input_skip provider_id=%d turn=%d conn_id=%s reason=build_error cause=%s",
				o.ProviderID,
				turn,
				p.TruncateLog(sessionConnID, 64),
				p.TruncateLog(replayInputErr.Error(), 160),
			))
			currentTurnReplayInput = nil
			currentTurnReplayInputExists = false
		} else {
			currentTurnReplayInput = nextReplayInput
			currentTurnReplayInputExists = nextReplayInputExists
		}
		replayHasFunctionCallOutput := currentTurnReplayInputExists &&
			codec.ItemsHaveOutput(currentTurnReplayInput)
		hasFunctionCallOutput = hasFunctionCallOutput || replayHasFunctionCallOutput
		// Strict store-disabled continuation compares the last accepted non-input
		// context. Any current or replay tool output forces retention so removing the
		// anchor cannot orphan its call.
		if state.StoreDisabled && turn > 1 && currentPreviousResponseID != "" {
			shouldKeepPreviousResponseID := false
			strictReason := ""
			var strictErr error
			if lastTurnStrictState != nil {
				shouldKeepPreviousResponseID, strictReason, strictErr = lastTurnStrictState.Keep(
					currentPayload,
					lastTurnResponseID,
					hasFunctionCallOutput,
				)
			} else {
				shouldKeepPreviousResponseID, strictReason, strictErr = codec.KeepPrevious(
					lastTurnPayload,
					currentPayload,
					lastTurnResponseID,
					hasFunctionCallOutput,
				)
			}
			// A comparison failure does not establish safe reconstruction, so keep the
			// supplied continuation and log the unavailable strict-evaluation reason.
			if strictErr != nil {
				p.Log(fmt.Sprintf(
					"ingress_ws_prev_response_strict_eval provider_id=%d turn=%d conn_id=%s action=keep_previous_response_id reason=%s cause=%s previous_response_id=%s expected_previous_response_id=%s has_function_call_output=%v",
					o.ProviderID,
					turn,
					p.TruncateLog(sessionConnID, 64),
					p.NormalizeLog(strictReason),
					p.TruncateLog(strictErr.Error(), 160),
					p.TruncateLog(currentPreviousResponseID, 64),
					p.TruncateLog(expectedPrev, 64),
					hasFunctionCallOutput,
				))
			} else if !shouldKeepPreviousResponseID {
				// Publish a full unanchored request only after both anchor removal and
				// input installation succeed; either mutation failure keeps the original
				// continuation bytes.
				updatedPayload, removed, dropErr := codec.DropPrevious(currentPayload)
				if dropErr != nil || !removed {
					dropReason := "not_removed"
					if dropErr != nil {
						dropReason = "drop_error"
					}
					p.Log(fmt.Sprintf(
						"ingress_ws_prev_response_strict_eval provider_id=%d turn=%d conn_id=%s action=keep_previous_response_id reason=%s drop_reason=%s previous_response_id=%s expected_previous_response_id=%s has_function_call_output=%v",
						o.ProviderID,
						turn,
						p.TruncateLog(sessionConnID, 64),
						p.NormalizeLog(strictReason),
						p.NormalizeLog(dropReason),
						p.TruncateLog(currentPreviousResponseID, 64),
						p.TruncateLog(expectedPrev, 64),
						hasFunctionCallOutput,
					))
				} else {
					updatedWithInput, setInputErr := codec.SetInput(
						updatedPayload,
						currentTurnReplayInput,
						currentTurnReplayInputExists,
					)
					if setInputErr != nil {
						p.Log(fmt.Sprintf(
							"ingress_ws_prev_response_strict_eval provider_id=%d turn=%d conn_id=%s action=keep_previous_response_id reason=%s drop_reason=set_full_input_error previous_response_id=%s expected_previous_response_id=%s cause=%s has_function_call_output=%v",
							o.ProviderID,
							turn,
							p.TruncateLog(sessionConnID, 64),
							p.NormalizeLog(strictReason),
							p.TruncateLog(currentPreviousResponseID, 64),
							p.TruncateLog(expectedPrev, 64),
							p.TruncateLog(setInputErr.Error(), 160),
							hasFunctionCallOutput,
						))
					} else {
						currentPayload = updatedWithInput
						currentPayloadBytes = len(updatedWithInput)
						p.Log(fmt.Sprintf(
							"ingress_ws_prev_response_strict_eval provider_id=%d turn=%d conn_id=%s action=drop_previous_response_id_full_create reason=%s previous_response_id=%s expected_previous_response_id=%s has_function_call_output=%v",
							o.ProviderID,
							turn,
							p.TruncateLog(sessionConnID, 64),
							p.NormalizeLog(strictReason),
							p.TruncateLog(currentPreviousResponseID, 64),
							p.TruncateLog(expectedPrev, 64),
							hasFunctionCallOutput,
						))
						currentPreviousResponseID = ""
					}
				}
			}
		}
		forcePreferredConn := isStrictAffinityTurn(currentPayload)
		if sessionLease == nil {
			acquiredLease, acquireErr := acquireTurnLease(turn, state.PreferredConnID, forcePreferredConn)
			if acquireErr != nil {
				return fmt.Errorf("acquire upstream websocket: %w", acquireErr)
			}
			sessionLease = acquiredLease
			sessionConnID = strings.TrimSpace(sessionLease.ConnID())
			if state.StoreDisabled {
				pinSessionConn(sessionConnID)
			} else {
				unpinSessionConn(sessionConnID)
			}
		}
		// Health-check a reused lease only between turns when idle ping is supported
		// and no transport retry is active; the idle threshold suppresses checks soon
		// after completion.
		shouldPreflightPing := turn > 1 && sessionLease != nil && sessionLease.SupportsIdlePingWithoutReader() && turnRetry == 0
		if shouldPreflightPing && o.PreflightPingIdle > 0 && !lastTurnFinishedAt.IsZero() {
			if time.Since(lastTurnFinishedAt) < o.PreflightPingIdle {
				shouldPreflightPing = false
			}
		}
		if shouldPreflightPing {
			if pingErr := sessionLease.PingWithTimeout(o.HealthCheckTimeout); pingErr != nil {
				p.Log(fmt.Sprintf(
					"ingress_ws_upstream_preflight_ping_fail provider_id=%d turn=%d conn_id=%s cause=%s",
					o.ProviderID,
					turn,
					p.TruncateLog(sessionConnID, 64),
					p.TruncateLog(pingErr.Error(), 160),
				))
				if forcePreferredConn {
					// Ping recovery has its own context-coverage gate, independent of the
					// missing-response recovery setting. A forced continuation cannot move
					// safely unless replay covers every required tool call.
					// Tool outputs need previous_response_id to recover their calls from the chain,
					// unless replay input supplies every matching function call.
					// Only that complete context makes dropping the continuation safe.
					hasFCOutput := hasFunctionCallOutput
					hasReplayToolContext := hasFCOutput &&
						currentTurnReplayInputExists &&
						codec.ItemsCoverOutput(currentTurnReplayInput)
					if !turnPrevRecoveryTried && currentPreviousResponseID != "" && (!hasFCOutput || hasReplayToolContext) {
						updatedPayload, removed, dropErr := codec.DropPrevious(currentPayload)
						if dropErr != nil || !removed {
							reason := "not_removed"
							if dropErr != nil {
								reason = "drop_error"
							}
							p.Log(fmt.Sprintf(
								"ingress_ws_preflight_ping_recovery_skip provider_id=%d turn=%d conn_id=%s reason=%s previous_response_id=%s",
								o.ProviderID,
								turn,
								p.TruncateLog(sessionConnID, 64),
								p.NormalizeLog(reason),
								p.TruncateLog(currentPreviousResponseID, 64),
							))
						} else {
							updatedWithInput, setInputErr := codec.SetInput(
								updatedPayload,
								currentTurnReplayInput,
								currentTurnReplayInputExists,
							)
							if setInputErr != nil {
								p.Log(fmt.Sprintf(
									"ingress_ws_preflight_ping_recovery_skip provider_id=%d turn=%d conn_id=%s reason=set_full_input_error previous_response_id=%s cause=%s",
									o.ProviderID,
									turn,
									p.TruncateLog(sessionConnID, 64),
									p.TruncateLog(currentPreviousResponseID, 64),
									p.TruncateLog(setInputErr.Error(), 160),
								))
							} else {
								p.Log(fmt.Sprintf(
									"ingress_ws_preflight_ping_recovery provider_id=%d turn=%d conn_id=%s action=drop_previous_response_id_retry previous_response_id=%s has_function_call_output=%v has_replay_tool_context=%v",
									o.ProviderID,
									turn,
									p.TruncateLog(sessionConnID, 64),
									p.TruncateLog(currentPreviousResponseID, 64),
									hasFCOutput,
									hasReplayToolContext,
								))
								turnPrevRecoveryTried = true
								currentPayload = updatedWithInput
								currentPayloadBytes = len(updatedWithInput)
								resetSessionLease(true)
								skipBeforeTurn = true
								continue
							}
						}
					}
					if hasFCOutput && currentPreviousResponseID != "" {
						reason := "function_call_output_missing_replay_context"
						if hasReplayToolContext {
							reason = "function_call_output_replay_not_applied"
						}
						p.Log(fmt.Sprintf(
							"ingress_ws_preflight_ping_recovery_skip provider_id=%d turn=%d conn_id=%s reason=%s action=fail_close previous_response_id=%s has_replay_tool_context=%v",
							o.ProviderID,
							turn,
							p.TruncateLog(sessionConnID, 64),
							reason,
							p.TruncateLog(currentPreviousResponseID, 64),
							hasReplayToolContext,
						))
					}
					resetSessionLease(true)
					return p.CloseError(
						1008,
						"upstream continuation connection is unavailable; please restart the conversation",
						pingErr,
					)
				}
				// An ordinary failed idle ping permits a new compatible lease, so discard
				// the broken transport and acquire again before forwarding this
				// still-admitted turn.
				resetSessionLease(true)

				acquiredLease, acquireErr := acquireTurnLease(turn, state.PreferredConnID, forcePreferredConn)
				if acquireErr != nil {
					return fmt.Errorf("acquire upstream websocket after preflight ping fail: %w", acquireErr)
				}
				sessionLease = acquiredLease
				sessionConnID = strings.TrimSpace(sessionLease.ConnID())
				if state.StoreDisabled {
					pinSessionConn(sessionConnID)
				}
			}
		}
		connID := sessionConnID
		if currentPreviousResponseID != "" {
			chainedFromLast := expectedPrev != "" && currentPreviousResponseID == expectedPrev
			currentPreviousResponseIDKind := codec.ClassifyPrevious(currentPreviousResponseID)
			p.Log(fmt.Sprintf(
				"ingress_ws_turn_chain provider_id=%d turn=%d conn_id=%s previous_response_id=%s previous_response_id_kind=%s last_turn_response_id=%s chained_from_last=%v preferred_conn_id=%s header_session_id=%s header_conversation_id=%s has_turn_state=%v turn_state_len=%d has_prompt_cache_key=%v store_disabled=%v",
				o.ProviderID,
				turn,
				p.TruncateLog(connID, 64),
				p.TruncateLog(currentPreviousResponseID, 64),
				p.NormalizeLog(currentPreviousResponseIDKind),
				p.TruncateLog(expectedPrev, 64),
				chainedFromLast,
				p.TruncateLog(state.PreferredConnID, 64),
				p.Header("session_id"),
				p.Header("conversation_id"),
				state.TurnState != "",
				len(state.TurnState),
				payloadString(currentPayload, "prompt_cache_key") != "",
				state.StoreDisabled,
			))
		}

		result, relayErr := p.Relay(turn, sessionLease, ClientPayload{PayloadRaw: currentPayload, PayloadBytes: currentPayloadBytes, OriginalModel: currentOriginalModel, RoutingModel: currentRoutingModel, ImageBillingModel: currentImageBillingModel, ImageSizeTier: currentImageSizeTier, ImageInputSize: currentImageInputSize, RequestedReasoningEffort: currentRequestedReasoningEffort})
		if relayErr != nil {
			// Try bounded local recovery before reporting final completion, preserving
			// the same admission resources and logical turn while its transport is
			// replaced.
			lastTurnClean = false
			if recoverIngressPrevResponseNotFound(relayErr, turn, connID) {
				continue
			}
			if retryIngressTurn(relayErr, turn, connID) {
				continue
			}
			finalErr := relayErr
			if unwrapped := errors.Unwrap(relayErr); unwrapped != nil {
				finalErr = unwrapped
			}
			if hooks != nil && hooks.AfterTurn != nil {
				hooks.AfterTurn(TurnCapture{
					Turn:               turn,
					StartedAt:          turnStartedAt,
					RequestBody:        append([]byte(nil), currentPayload...),
					OriginalModel:      currentOriginalModel,
					PreviousResponseID: currentPreviousResponseID,
					Err:                finalErr,
					PayloadSource:      "ctx_pool",
				})
			}
			sessionLease.MarkBroken()
			return finalErr
		}
		turnRetry = 0
		turnPrevRecoveryTried = false
		lastTurnFinishedAt = time.Now()
		lastTurnClean = true
		if hooks != nil && hooks.AfterTurn != nil {
			hooks.AfterTurn(TurnCapture{
				Turn:               turn,
				StartedAt:          turnStartedAt,
				RequestBody:        append([]byte(nil), currentPayload...),
				OriginalModel:      currentOriginalModel,
				PreviousResponseID: currentPreviousResponseID,
				Result:             result,
				PayloadSource:      "ctx_pool",
			})
		}
		if result == nil {
			return errors.New("websocket turn result is nil")
		}
		responseID := strings.TrimSpace(result.RequestID)
		lastTurnResponseID = responseID
		// Keep shared references to immutable request and replay bodies.
		// Combine allocates a new slice array when adding collected output.
		lastTurnReplayInput = currentTurnReplayInput
		lastTurnReplayInputExists = currentTurnReplayInputExists
		if result.WSReplayInputExists {
			lastTurnReplayInput = codec.Combine(lastTurnReplayInput, result.WSReplayInput)
			lastTurnReplayInputExists = true
		}
		nextStrictState, strictStateErr := codec.BuildStrict(currentPayload)
		if strictStateErr != nil {
			lastTurnStrictState = nil
			// Keep immutable full request bytes when compact comparison state cannot be
			// built, so the next turn can use the codec's raw-payload strict comparison
			// path.
			lastTurnPayload = currentPayload
			p.Log(fmt.Sprintf(
				"ingress_ws_prev_response_strict_state_skip provider_id=%d turn=%d conn_id=%s reason=build_error cause=%s",
				o.ProviderID,
				turn,
				p.TruncateLog(connID, 64),
				p.TruncateLog(strictStateErr.Error(), 160),
			))
		} else {
			// Successful construction selects its returned comparison state and releases
			// full request bytes. A nil returned state therefore leaves no raw-payload
			// comparison source for the next turn.
			lastTurnStrictState = nextStrictState
			lastTurnPayload = nil
		}

		// Write response-to-provider, response-to-connection, and owner observations
		// after completion. These writes are separate, and a provider-binding error
		// is reported while local affinity publication continues.
		if responseID != "" && stateStore != nil {
			ttl := o.ResponseStickyTTL
			p.BindWarning(groupID, o.ProviderID, responseID, stateStore.BindResponseProvider(ctx, groupID, responseID, o.ProviderID, ttl))
			stateStore.BindResponseConn(responseID, connID, ttl)
			p.BindOwner(ctx, responseID)
		}
		if stateStore != nil && state.StoreDisabled && state.SessionHash != "" {
			stateStore.BindSessionConn(groupID, state.SessionHash, connID, o.SessionStickyTTL)
		}
		if connID != "" {
			state.PreferredConnID = connID
		}

		nextClientMessage, readErr := p.ReadClient()
		if readErr != nil {
			if p.IsDisconnect(readErr) {
				closeStatus, closeReason := p.SummarizeClose(readErr)
				p.Log(fmt.Sprintf(
					"ingress_ws_client_closed provider_id=%d conn_id=%s close_status=%s close_reason=%s",
					o.ProviderID,
					p.TruncateLog(connID, 64),
					closeStatus,
					p.TruncateLog(closeReason, 120),
				))
				return nil
			}
			return fmt.Errorf("read client websocket request: %w", readErr)
		}

		nextPayload, parseErr := p.Parse(nextClientMessage, true, turn+1)
		if parseErr != nil {
			return parseErr
		}
		p.UpdateHeaders(nextPayload, state.TurnState)

		if nextPayload.PreviousResponseID != "" {
			expectedPrev := strings.TrimSpace(lastTurnResponseID)
			chainedFromLast := expectedPrev != "" && nextPayload.PreviousResponseID == expectedPrev
			nextPreviousResponseIDKind := codec.ClassifyPrevious(nextPayload.PreviousResponseID)
			p.Log(fmt.Sprintf(
				"ingress_ws_next_turn_chain provider_id=%d turn=%d next_turn=%d conn_id=%s previous_response_id=%s previous_response_id_kind=%s last_turn_response_id=%s chained_from_last=%v has_prompt_cache_key=%v store_disabled=%v",
				o.ProviderID,
				turn,
				turn+1,
				p.TruncateLog(connID, 64),
				p.TruncateLog(nextPayload.PreviousResponseID, 64),
				p.NormalizeLog(nextPreviousResponseIDKind),
				p.TruncateLog(expectedPrev, 64),
				chainedFromLast,
				nextPayload.PromptCacheKey != "",
				state.StoreDisabled,
			))
		}
		if stateStore != nil && nextPayload.PreviousResponseID != "" {
			if stickyConnID, ok := stateStore.GetResponseConn(nextPayload.PreviousResponseID); ok {
				// Keep the currently held lease when cached response affinity points
				// elsewhere; this loop does not transfer an active session to that cached
				// connection.
				if sessionConnID != "" && stickyConnID != "" && stickyConnID != sessionConnID {
					p.Log(fmt.Sprintf(
						"ingress_ws_keep_session_conn provider_id=%d turn=%d conn_id=%s sticky_conn_id=%s previous_response_id=%s",
						o.ProviderID,
						turn,
						p.TruncateLog(sessionConnID, 64),
						p.TruncateLog(stickyConnID, 64),
						p.TruncateLog(nextPayload.PreviousResponseID, 64),
					))
				} else {
					state.PreferredConnID = stickyConnID
				}
			}
		}
		currentPayload = nextPayload.PayloadRaw
		currentOriginalModel = nextPayload.OriginalModel
		currentRoutingModel = nextPayload.RoutingModel
		currentImageBillingModel = nextPayload.ImageBillingModel
		currentImageSizeTier = nextPayload.ImageSizeTier
		currentImageInputSize = nextPayload.ImageInputSize
		currentPayloadBytes = nextPayload.PayloadBytes
		currentRequestedReasoningEffort = nextPayload.RequestedReasoningEffort
		state.StoreDisabled = p.StoreDisabled(currentPayload)
		if !state.StoreDisabled {
			unpinSessionConn(sessionConnID)
		}
		turn++
	}
}
