//go:build integration

package apikey_test

import (
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Key list ordering is selected by the pagination request before rows are
// returned to the user. PostgreSQL suite cases check ascending names and
// descending IDs using two inserted keys whose expected orders differ from one
// another.

// TestListByUserID_SortByNameAsc inserts names in reverse alphabetical order in
// the suite transaction. An ascending name request returns both keys with a-key
// before z-key.
func (s *APIKeyRepoSuite) TestListByUserID_SortByNameAsc() {
	user := s.mustCreateUser("sort-name@example.com")
	s.mustCreateApiKey(user.ID, "sk-z", "z-key", nil)
	s.mustCreateApiKey(user.ID, "sk-a", "a-key", nil)

	keys, _, err := s.repo.ListByUserID(s.ctx, user.ID, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "name",
		SortOrder: "asc",
	}, apikey.APIKeyListFilters{})
	s.Require().NoError(err)
	s.Require().Len(keys, 2)
	s.Require().Equal("a-key", keys[0].Name)
	s.Require().Equal("z-key", keys[1].Name)
}

// TestListByUserID_SortByID creates two keys in increasing storage-ID order in
// the suite transaction. A descending ID request returns the second key before
// the first.
func (s *APIKeyRepoSuite) TestListByUserID_SortByID() {
	user := s.mustCreateUser("sort-id@example.com")
	first := s.mustCreateApiKey(user.ID, "sk-id-a", "a-key", nil)
	second := s.mustCreateApiKey(user.ID, "sk-id-b", "b-key", nil)

	keys, _, err := s.repo.ListByUserID(s.ctx, user.ID, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "id",
		SortOrder: "desc",
	}, apikey.APIKeyListFilters{})
	s.Require().NoError(err)
	s.Require().Len(keys, 2)
	s.Require().Equal(second.ID, keys[0].ID)
	s.Require().Equal(first.ID, keys[1].ID)
}
