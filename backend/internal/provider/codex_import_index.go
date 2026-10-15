package provider

import (
	"strings"
)

// codex_import_index.go indexes stored Codex providers and matches ordered
// import identities while tracking duplicates within a batch.
// Codex imports match stored rows through ordered identity keys while
// retaining multiple candidates for shared accounts. User keys precede token
// fingerprints and the account fallback for full OAuth, whereas access-only
// imports select only their token fingerprint and Agent Identity imports
// select account identity.
//
// Account-key matches reject two distinct nonempty user IDs but allow missing
// identity on either side for legacy upgrade. User-key matches have no account
// comparison, so matching must be understood per credential mode. Index
// replacement removes old keys and updates retained-key candidates in place,
// preserving their order. Copies isolate supported Record values on insertion
// and lookup; opaque Extra or credential objects outside CloneValues support
// can remain shared. Callers serialize mutations and reads of the index maps.

// CodexProviderIndex owns ordered stored-row candidates and per-row reverse
// identity keys.
type CodexProviderIndex struct {
	// providersByKey and keysByProviderID are updated together by serialized Add
	// calls. The first retains ordered candidates; the reverse map identifies
	// associations to remove or replace when one row changes credentials.
	providersByKey map[string][]Record // ordered candidates by identity
	// reverse identity keys by row
	keysByProviderID map[int64]map[string]struct{}
}

// CodexSeenIdentity retains a batch entry index and its observed user
// identity.
type CodexSeenIdentity struct {
	index  int    // prior source entry index
	userID string // observed upstream user identity
}

// BuildCodexStoredIdentityKeys returns trimmed user, access fingerprint, and
// account keys in matching order. Email is lowercased and included only when
// both account and user are absent; blank token adds no fingerprint.
//
// A user key wins before an account key, which remains the final fallback.
// This order allows later full OAuth entries to find access-only stored
// records, while account-key conflict checking protects known different users
// sharing that account. User-key matching itself does not compare account IDs,
// and callers choose a different key policy for access-only or Agent Identity
// imports.
func BuildCodexStoredIdentityKeys(providerID, userID, email, accessToken string) []string {
	keys := make([]string, 0, 3)
	providerID = strings.TrimSpace(providerID)
	userID = strings.TrimSpace(userID)
	accessToken = strings.TrimSpace(accessToken)
	if userID != "" {
		keys = append(keys, "user:"+userID)
	}
	if providerID == "" && userID == "" {
		if email = strings.ToLower(strings.TrimSpace(email)); email != "" {
			keys = append(keys, "email:"+email)
		}
	}
	if accessToken != "" {
		keys = append(keys, "access:"+CodexTokenFingerprint(accessToken))
	}
	if providerID != "" {
		keys = append(keys, "provider:"+providerID)
	}
	return keys
}

// BuildCodexProviderIndex creates an empty identity index and adds rows in
// supplied order. Add copies supported record data, and callers serialize
// later operations because the index itself has no synchronization.
func BuildCodexProviderIndex(providers []Record) *CodexProviderIndex {
	index := &CodexProviderIndex{
		providersByKey:   map[string][]Record{},
		keysByProviderID: map[int64]map[string]struct{}{},
	}
	for _, provider := range providers {
		index.Add(provider)
	}
	return index
}

// Add replaces one stored row across its current identity keys and copies
// supported mutable Record values. Nil receiver skips work; callers serialize
// access and keep the input stable while CloneRecord reads it.
//
// Reverse keys identify prior associations. Retained keys replace the same row
// in place, removed keys discard that candidate, and new keys append it after
// existing candidates. Runtime identity adds an agent key alongside ordinary
// stored keys. Empty resulting identity removes the reverse entry, so later
// lookup cannot retain a stale key association from that row.
func (i *CodexProviderIndex) Add(provider Record) {
	if i == nil {
		return
	}
	// Copy Record data before indexing so supported nested maps and slices
	// remain stable after caller edits; opaque values retain CloneRecord’s
	// borrowing limits.
	provider = *CloneRecord(&provider)
	if i.providersByKey == nil {
		i.providersByKey = map[string][]Record{}
	}
	if i.keysByProviderID == nil {
		i.keysByProviderID = map[int64]map[string]struct{}{}
	}
	keys := BuildCodexStoredIdentityKeys(
		CodexCredentialString(provider.Credentials, "chatgpt_account_id"),
		CodexCredentialString(provider.Credentials, "chatgpt_user_id"),
		CodexCredentialString(provider.Credentials, "email"),
		CodexCredentialString(provider.Credentials, "access_token"),
	)
	orderedKeys := make([]string, 0, len(keys)+1)
	providerKeys := make(map[string]struct{}, len(keys)+1)
	for _, key := range keys {
		if _, exists := providerKeys[key]; exists {
			continue
		}
		providerKeys[key] = struct{}{}
		orderedKeys = append(orderedKeys, key)
	}
	if runtimeID := CodexCredentialString(provider.Credentials, "agent_runtime_id"); runtimeID != "" {
		key := "agent:" + runtimeID
		if _, exists := providerKeys[key]; !exists {
			providerKeys[key] = struct{}{}
			orderedKeys = append(orderedKeys, key)
		}
	}

	// Replace retained-key candidates before appending new associations, and
	// remove obsolete keys using the reverse map. This keeps shared-key order
	// stable while preventing stale credential identities from matching later
	// entries.
	previousKeys := i.keysByProviderID[provider.ID]
	for key := range previousKeys {
		if _, retained := providerKeys[key]; retained {
			i.providersByKey[key] = upsertCodexProvider(i.providersByKey[key], provider)
			continue
		}
		i.removeFromKey(key, provider.ID)
	}
	for _, key := range orderedKeys {
		if _, retained := previousKeys[key]; retained {
			continue
		}
		i.providersByKey[key] = append(i.providersByKey[key], provider)
	}

	if len(providerKeys) > 0 {
		i.keysByProviderID[provider.ID] = providerKeys
		return
	}
	delete(i.keysByProviderID, provider.ID)
}

// removeFromKey removes the selected row from a key’s candidate slice in
// place. It deletes an empty key and requires serialized index access because
// backing slices are reused.
func (i *CodexProviderIndex) removeFromKey(key string, providerID int64) {
	providers := i.providersByKey[key]
	// Reuse candidate storage while removing this row, then drop an empty key.
	// Other candidates retain order so later matching sees the same first viable
	// shared-account observation after stale associations are invalidated.
	kept := providers[:0]
	for _, provider := range providers {
		if provider.ID != providerID {
			kept = append(kept, provider)
		}
	}
	if len(kept) == 0 {
		delete(i.providersByKey, key)
		return
	}
	i.providersByKey[key] = kept
}

// upsertCodexProvider replaces the same row ID in place or appends a new
// candidate. Other candidates retain order; the supplied records and slice
// remain borrowed within serialized index updates.
func upsertCodexProvider(providers []Record, provider Record) []Record {
	for idx := range providers {
		if providers[idx].ID == provider.ID {
			providers[idx] = provider
			return providers
		}
	}
	return append(providers, provider)
}

// Find returns the first acceptable candidate in key order and a copied
// record. Nil or unmatched index returns nil and an empty key; callers
// synchronize map reads against Add.
//
// Only provider-prefixed keys reject different nonempty user IDs. Missing user
// identity allows legacy matching, and user/access/agent keys have no such
// account-fallback guard. Candidate order within each key remains insertion
// order with same-row replacement in place. CloneRecord copies supported
// mutable fields for caller use while retaining any opaque values outside its
// cloning support.
func (i *CodexProviderIndex) Find(keys []string, userID string) (*Record, string) {
	if i == nil {
		return nil, ""
	}
	for _, key := range keys {
		for _, provider := range i.providersByKey[key] {
			if codexIdentityConflicts(key, userID, CodexCredentialString(provider.Credentials, "chatgpt_user_id")) {
				continue
			}
			return CloneRecord(&provider), key
		}
	}
	return nil, ""
}

// codexIdentityConflicts detects two distinct trimmed nonempty user IDs under
// a provider-prefixed key. Other key families and either missing user are
// compatible, allowing legacy account-key upgrade while rejecting known
// different account members.
func codexIdentityConflicts(key, userID, storedUserID string) bool {
	if !strings.HasPrefix(key, "provider:") {
		return false
	}
	userID = strings.TrimSpace(userID)
	storedUserID = strings.TrimSpace(storedUserID)
	return userID != "" && storedUserID != "" && userID != storedUserID
}

// FirstSeenCodexIdentity returns the first compatible prior entry under the
// supplied key order. Absence returns zero and false; callers use the boolean
// because supplied source indexes can themselves be zero.
//
// Provider-key comparison rejects different nonempty users, while missing user
// identity allows that account fallback. Other key families select their
// stored observation without that guard. Key order chooses the result rather
// than the numerically earliest index, and MarkCodexIdentitySeen can replace a
// key with a later entry. Callers serialize access to this borrowed map.
func FirstSeenCodexIdentity(seen map[string]CodexSeenIdentity, keys []string, userID string) (int, bool) {
	for _, key := range keys {
		entry, ok := seen[key]
		if !ok {
			continue
		}
		if codexIdentityConflicts(key, userID, entry.userID) {
			continue
		}
		return entry.index, true
	}
	return 0, false
}

// MarkCodexIdentitySeen writes the supplied batch index and user under every
// key. A nil map panics when keys exist; Import records these identities
// before persistence, so later duplicates can skip a failed earlier item.
func MarkCodexIdentitySeen(seen map[string]CodexSeenIdentity, keys []string, index int, userID string) {
	for _, key := range keys {
		seen[key] = CodexSeenIdentity{index: index, userID: userID}
	}
}
