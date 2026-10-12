package provider

import (
	"strings"
)

type CodexProviderIndex struct {
	providersByKey   map[string][]Record
	keysByProviderID map[int64]map[string]struct{}
}

type CodexSeenIdentity struct {
	index  int
	userID string
}

// BuildCodexStoredIdentityKeys indexes stored provider and user identities
// so a later full OAuth import can update an access-token-only provider.
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

func (i *CodexProviderIndex) Add(provider Record) {
	if i == nil {
		return
	}
	// The index owns a copy of the caller's mutable provider data.
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

func (i *CodexProviderIndex) removeFromKey(key string, providerID int64) {
	providers := i.providersByKey[key]
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

// upsertCodexProvider retains all candidates sharing a key and replaces existing IDs in place.
// Matching considers candidates in their stored order.
func upsertCodexProvider(providers []Record, provider Record) []Record {
	for idx := range providers {
		if providers[idx].ID == provider.ID {
			providers[idx] = provider
			return providers
		}
	}
	return append(providers, provider)
}

// Find returns the first candidate passing the user-identity check and the key that matched.
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

// codexIdentityConflicts detects different users sharing a ChatGPT team provider key.
// A conflict requires nonempty, unequal user IDs on both sides.
// Provider keys remain indexed; a missing user ID on either side permits matching,
// including full OAuth imports that upgrade existing access-token-only providers.
func codexIdentityConflicts(key, userID, storedUserID string) bool {
	if !strings.HasPrefix(key, "provider:") {
		return false
	}
	userID = strings.TrimSpace(userID)
	storedUserID = strings.TrimSpace(storedUserID)
	return userID != "" && storedUserID != "" && userID != storedUserID
}

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

func MarkCodexIdentitySeen(seen map[string]CodexSeenIdentity, keys []string, index int, userID string) {
	for _, key := range keys {
		seen[key] = CodexSeenIdentity{index: index, userID: userID}
	}
}
