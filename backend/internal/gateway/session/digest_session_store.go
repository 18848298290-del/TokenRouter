package session

import (
	"strconv"
	"strings"
	"time"

	gocache "github.com/patrickmn/go-cache"
)

// digestSessionTTL is the default lifetime of a digest session.
const digestSessionTTL = 5 * time.Minute

// sessionEntry binds a digest chain to a session and provider.
type sessionEntry struct {
	uuid       string
	providerID int64
}

// DigestSessionStore stores digest sessions in memory.
// Keys have the form "{groupID}:{prefixHash}|{digestChain}".
type DigestSessionStore struct {
	cache *gocache.Cache
}

// NewDigestSessionStore creates an in-memory digest store with external expiry cleanup.
func NewDigestSessionStore() *DigestSessionStore {
	return &DigestSessionStore{
		cache: gocache.New(digestSessionTTL, 0),
	}
}

// Save stores the session and removes oldDigestChain when it differs. Pass the matchedChain returned by Find.
func (s *DigestSessionStore) Save(groupID int64, prefixHash, digestChain, uuid string, providerID int64, oldDigestChain string) {
	if s == nil || digestChain == "" {
		return
	}
	ns := buildNS(groupID, prefixHash)
	s.cache.Set(ns+digestChain, &sessionEntry{uuid: uuid, providerID: providerID}, gocache.DefaultExpiration)
	if oldDigestChain != "" && oldDigestChain != digestChain {
		s.cache.Delete(ns + oldDigestChain)
	}
}

// Find returns the longest stored prefix of digestChain and its session binding.
func (s *DigestSessionStore) Find(groupID int64, prefixHash, digestChain string) (uuid string, providerID int64, matchedChain string, found bool) {
	if s == nil || digestChain == "" {
		return "", 0, "", false
	}
	ns := buildNS(groupID, prefixHash)
	chain := digestChain
	for {
		if val, ok := s.cache.Get(ns + chain); ok {
			if e, ok := val.(*sessionEntry); ok {
				return e.uuid, e.providerID, chain, true
			}
		}
		i := strings.LastIndex(chain, "-")
		if i < 0 {
			return "", 0, "", false
		}
		chain = chain[:i]
	}
}

// buildNS scopes digest chains by group and request prefix.
func buildNS(groupID int64, prefixHash string) string {
	return strconv.FormatInt(groupID, 10) + ":" + prefixHash + "|"
}

// ExpireRuntimeCaches removes expired entries when called by the application timing wheel.
func (s *DigestSessionStore) ExpireRuntimeCaches() {
	if s != nil {
		if s.cache != nil {
			s.cache.DeleteExpired()
		}
	}
}
