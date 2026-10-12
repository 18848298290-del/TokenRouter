package identity

import (
	"context"
	"strings"
)

// DingTalkProfileSnapshot contains provider fields used for profile synchronization.
type DingTalkProfileSnapshot struct {
	UserID, Name, Nickname, Email string
	DeptIDs                       []int64
}

// DingTalkSyncOptions contains the settings read for the current login.
type DingTalkSyncOptions struct {
	CorpRestrictionPolicy                                         string
	SyncCorpEmail, SyncDisplayName, SyncDept                      bool
	SyncCorpEmailAttrKey, SyncDisplayNameAttrKey, SyncDeptAttrKey string
}
type DingTalkDepartmentReader interface {
	ResolveDepartmentPath(context.Context, int64) (string, error)
}
type DingTalkDepartmentFunc func(context.Context, int64) (string, error)

// ProfileSyncObserver sends profile-sync diagnostics to the logger wired by app.
type (
	ProfileSyncObserver func(level, message string, args ...any)
	DingTalkProfileSync struct {
		Users      *UserService
		Attributes *UserAttributeService
		Observe    ProfileSyncObserver
	}
)

func (f DingTalkDepartmentFunc) ResolveDepartmentPath(ctx context.Context, id int64) (string, error) {
	return f(ctx, id)
}

func (s *DingTalkProfileSync) log(level, message string, args ...any) {
	if s.Observe != nil {
		s.Observe(level, message, args...)
	}
}

// Sync copies enabled DingTalk profile fields to user attributes in internal_only mode.
// syncUsername also permits updating users.username.
// Errors are logged so synchronization can run independently of login completion.
func (s *DingTalkProfileSync) Sync(ctx context.Context, cfg DingTalkSyncOptions, client DingTalkDepartmentReader, userID int64, staff *DingTalkProfileSnapshot, syncUsername bool) {
	s.log("info", "dingtalk sync: entry",
		"user_id", userID,
		"policy", cfg.CorpRestrictionPolicy,
		"sync_corp_email", cfg.SyncCorpEmail,
		"sync_display_name", cfg.SyncDisplayName,
		"sync_dept", cfg.SyncDept,
		"sync_username", syncUsername,
		"attr_key_email", cfg.SyncCorpEmailAttrKey,
		"attr_key_name", cfg.SyncDisplayNameAttrKey,
		"attr_key_dept", cfg.SyncDeptAttrKey,
		"staff_nil", staff == nil,
	)
	if cfg.CorpRestrictionPolicy != "internal_only" || staff == nil {
		s.log("info", "dingtalk sync: skip, not internal_only or staff nil")
		return
	}
	s.log("info", "dingtalk sync: staff snapshot",
		"name", staff.Name, "email", staff.Email, "dept_ids", staff.DeptIDs,
	)
	if !cfg.SyncCorpEmail && !cfg.SyncDisplayName && !cfg.SyncDept {
		s.log("info", "dingtalk sync: skip, all flags disabled")
		return
	}
	if s.Attributes == nil {
		s.log("warn", "dingtalk sync: userAttributeService not available, skipping")
		return
	}

	// Callers enable username synchronization at signup to retain later user edits.
	// The display-name attribute is synchronized separately on every eligible login.
	if syncUsername && cfg.SyncDisplayName {
		username := strings.TrimSpace(staff.Nickname)
		source := "nickname"
		if username == "" {
			username = strings.TrimSpace(staff.Name)
			source = "name(fallback)"
		}
		if username != "" && s.Users != nil {
			if _, err := s.Users.UpdateProfile(ctx, userID, UpdateProfileRequest{Username: &username}); err != nil {
				s.log("warn", "dingtalk sync: failed to update username", "user_id", userID, "err", err)
			} else {
				s.log("info", "dingtalk sync: username updated (register)", "user_id", userID, "username", username, "source", source)
			}
		}
	}

	// Use configured attribute keys; the settings reader supplies their defaults.
	type syncField struct {
		key   string
		value string
	}
	var fields []syncField

	if cfg.SyncDisplayName && strings.TrimSpace(staff.Name) != "" {
		fields = append(fields, syncField{cfg.SyncDisplayNameAttrKey, strings.TrimSpace(staff.Name)})
	}
	if cfg.SyncCorpEmail && strings.TrimSpace(staff.Email) != "" {
		fields = append(fields, syncField{cfg.SyncCorpEmailAttrKey, strings.TrimSpace(staff.Email)})
	}
	if cfg.SyncDept && len(staff.DeptIDs) > 0 {
		// Prefer the first non-root department; otherwise use the first supplied department.
		primaryDeptID := int64(0)
		for _, id := range staff.DeptIDs {
			if id > 1 {
				primaryDeptID = id
				break
			}
		}
		if primaryDeptID == 0 {
			primaryDeptID = staff.DeptIDs[0]
		}
		s.log("info", "dingtalk sync: pick primary dept", "user_id", userID, "all_dept_ids", staff.DeptIDs, "primary", primaryDeptID)
		path, err := client.ResolveDepartmentPath(ctx, primaryDeptID)
		if err != nil {
			s.log("warn", "dingtalk sync: failed to resolve dept path", "user_id", userID, "dept_id", primaryDeptID, "err", err)
		} else {
			// An empty root-department path clears the previous attribute value.
			fields = append(fields, syncField{cfg.SyncDeptAttrKey, path})
		}
	}

	if len(fields) == 0 {
		return
	}

	for _, f := range fields {
		if err := s.setUserAttributeByKey(ctx, userID, f.key, f.value); err != nil {
			s.log("warn", "dingtalk sync: failed to set attribute", "user_id", userID, "key", f.key, "err", err)
		}
	}
}

// setUserAttributeByKey resolves the attribute definition and upserts its value.
// Definition lookup failures are logged and skipped.
func (s *DingTalkProfileSync) setUserAttributeByKey(ctx context.Context, userID int64, key, value string) error {
	def, err := s.Attributes.GetDefinitionByKey(ctx, key)
	if err != nil {
		s.log("warn", "dingtalk sync: attribute definition not found, skipping", "key", key, "err", err.Error())
		return nil
	}
	if err := s.Attributes.UpdateUserAttributes(ctx, userID, []UpdateUserAttributeInput{
		{AttributeID: def.ID, Value: value},
	}); err != nil {
		return err
	}
	s.log("info", "dingtalk sync: attribute upserted", "user_id", userID, "key", key, "attr_id", def.ID)
	return nil
}

// DingTalkProfileFromClaims rebuilds the profile fields needed for synchronization.
func DingTalkProfileFromClaims(claims map[string]any) *DingTalkProfileSnapshot {
	if claims == nil {
		return &DingTalkProfileSnapshot{}
	}
	staff := &DingTalkProfileSnapshot{}
	if v, ok := claims["username"].(string); ok {
		staff.Name = v
	}
	if v, ok := claims["nickname"].(string); ok {
		staff.Nickname = v
	}
	if v, ok := claims["email"].(string); ok {
		staff.Email = v
	}
	if v, ok := claims["corp_user_id"].(string); ok {
		staff.UserID = v
	}
	// JSON decoding changes numeric department IDs from int64 to float64.
	switch v := claims["primary_dept_id"].(type) {
	case int64:
		if v > 0 {
			staff.DeptIDs = []int64{v}
		}
	case float64:
		if id := int64(v); id > 0 {
			staff.DeptIDs = []int64{id}
		}
	}
	return staff
}
