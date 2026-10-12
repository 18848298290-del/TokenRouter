package team

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/notification/smtp"
	mailtest "github.com/TokenFlux/TokenRouter/internal/notification/testkit"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

type fakeTeamInvitationLimiter struct {
	allowed    bool
	retryAfter time.Duration
	err        error
}

// fakeTeamRepository records team management inputs and returns configured results.
type fakeTeamRepository struct {
	TeamRepository

	teamContext       *TeamContext
	members           []TeamMembership
	usageSummary      *TeamUsageSummary
	usageQuery        TeamUsageQuery
	teamKeys          []TeamAPIKeyItem
	teamKeyActor      *int64
	teamKeyActorIsNil bool
	status            string
	name              string
	defaultLimits     [3]float64
	defaultLimitsSet  bool
	adminUpdate       TeamAdminUpdate
	adminUpdateCount  int
	invitationCreates int
	invitationPreview *TeamInvitationPreview
	previewTokenHash  string
	previewEmail      string
	previewAt         time.Time
}

// fakeTeamUserRepository supplies the signed-in user's email for invitation previews.
type fakeTeamUserRepository struct {
	UserReader

	user *UserSnapshot
}

func TestTeamServiceListMembersMemberOnlySeesOwnerAndSelf(t *testing.T) {
	repo := &fakeTeamRepository{
		teamContext: teamServiceTestContext(2, TeamRoleMember),
		members: []TeamMembership{
			{UserID: 1, Role: TeamRoleOwner},
			{UserID: 2, Role: TeamRoleMember},
			{UserID: 3, Role: TeamRoleMember},
		},
	}
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

	members, err := svc.ListMembers(context.Background(), 2)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, []int64{members[0].UserID, members[1].UserID})
}

func TestTeamServiceUsageScopeCannotBeExpandedByMember(t *testing.T) {
	otherUserID := int64(99)
	repo := &fakeTeamRepository{
		teamContext:  teamServiceTestContext(2, TeamRoleMember),
		usageSummary: &TeamUsageSummary{},
	}
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

	_, err := svc.GetUsageSummary(context.Background(), 2, TeamUsageQuery{ActorUserID: &otherUserID})
	require.NoError(t, err)
	require.NotNil(t, repo.usageQuery.ActorUserID)
	require.Equal(t, int64(2), *repo.usageQuery.ActorUserID)
}

func TestTeamServiceUsageScopeOwnerKeepsMemberFilter(t *testing.T) {
	targetUserID := int64(3)
	repo := &fakeTeamRepository{
		teamContext:  teamServiceTestContext(1, TeamRoleOwner),
		usageSummary: &TeamUsageSummary{},
	}
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

	_, err := svc.GetUsageSummary(context.Background(), 1, TeamUsageQuery{ActorUserID: &targetUserID})
	require.NoError(t, err)
	require.NotNil(t, repo.usageQuery.ActorUserID)
	require.Equal(t, targetUserID, *repo.usageQuery.ActorUserID)
}

func TestTeamServiceAdminUsageKeepsMemberFilter(t *testing.T) {
	targetUserID := int64(3)
	repo := &fakeTeamRepository{
		teamContext:  teamServiceTestContext(1, TeamRoleOwner),
		usageSummary: &TeamUsageSummary{},
	}
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

	_, err := svc.AdminGetUsageSummary(context.Background(), 11, TeamUsageQuery{ActorUserID: &targetUserID})
	require.NoError(t, err)
	require.NotNil(t, repo.usageQuery.ActorUserID)
	require.Equal(t, targetUserID, *repo.usageQuery.ActorUserID)
}

func TestTeamServiceAdminUpdatesName(t *testing.T) {
	repo := &fakeTeamRepository{teamContext: teamServiceTestContext(1, TeamRoleOwner)}
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

	err := svc.AdminUpdateName(context.Background(), 11, "  新团队名称  ")
	require.NoError(t, err)
	require.Equal(t, "新团队名称", repo.name)
}

func TestTeamServiceAdminUpdateValidatesBeforeWriting(t *testing.T) {
	repo := &fakeTeamRepository{teamContext: teamServiceTestContext(1, TeamRoleOwner)}
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)
	validName := "  新团队名称  "
	invalidLimit := -1

	_, err := svc.AdminUpdate(context.Background(), 11, TeamAdminUpdate{Name: &validName, MemberLimit: &invalidLimit})
	require.Error(t, err)
	require.Zero(t, repo.adminUpdateCount)
	require.Empty(t, repo.name)
}

func TestTeamServiceAdminUpdateWritesAllFieldsOnce(t *testing.T) {
	repo := &fakeTeamRepository{teamContext: teamServiceTestContext(1, TeamRoleOwner)}
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)
	name := "  新团队名称  "
	status := TeamStatusSuspended
	memberLimit := 12

	teamCtx, err := svc.AdminUpdate(context.Background(), 11, TeamAdminUpdate{Name: &name, Status: &status, MemberLimit: &memberLimit})
	require.NoError(t, err)
	require.Equal(t, 1, repo.adminUpdateCount)
	require.NotNil(t, repo.adminUpdate.Name)
	require.Equal(t, "新团队名称", *repo.adminUpdate.Name)
	require.Equal(t, TeamStatusSuspended, teamCtx.Team.Status)
	require.Equal(t, 12, teamCtx.Team.MemberLimit)
}

func TestTeamServiceListTeamKeysMasksSecretsAndAppliesRoleFilter(t *testing.T) {
	tests := []struct {
		name            string
		userID          int64
		role            string
		wantActorIsNil  bool
		wantActorUserID int64
	}{
		{name: "owner", userID: 1, role: TeamRoleOwner, wantActorIsNil: true},
		{name: "member", userID: 2, role: TeamRoleMember, wantActorUserID: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeTeamRepository{
				teamContext: teamServiceTestContext(tt.userID, tt.role),
				teamKeys:    []TeamAPIKeyItem{{ID: 31, Key: "sk-team-secret-value", Name: "team"}},
			}
			svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

			keys, err := svc.ListTeamKeys(context.Background(), tt.userID)
			require.NoError(t, err)
			require.Len(t, keys, 1)
			require.Empty(t, keys[0].Key)
			require.Equal(t, "sk-tea****alue", keys[0].MaskedKey)
			require.Equal(t, tt.wantActorIsNil, repo.teamKeyActorIsNil)
			if !tt.wantActorIsNil {
				require.NotNil(t, repo.teamKeyActor)
				require.Equal(t, tt.wantActorUserID, *repo.teamKeyActor)
			}
		})
	}
}

func TestTeamServiceSuspendedOwnerCanResume(t *testing.T) {
	repo := &fakeTeamRepository{teamContext: teamServiceTestContext(1, TeamRoleOwner)}
	repo.teamContext.Team.Status = TeamStatusSuspended
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

	teamCtx, err := svc.SetStatus(context.Background(), 1, TeamStatusActive)
	require.NoError(t, err)
	require.Equal(t, TeamStatusActive, repo.status)
	require.Equal(t, TeamStatusActive, teamCtx.Team.Status)
}

func TestTeamServiceSuspendedMemberCannotChangeStatus(t *testing.T) {
	repo := &fakeTeamRepository{teamContext: teamServiceTestContext(2, TeamRoleMember)}
	repo.teamContext.Team.Status = TeamStatusSuspended
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

	_, err := svc.SetStatus(context.Background(), 2, TeamStatusActive)
	require.ErrorIs(t, err, ErrTeamOwnerRequired)
}

func TestTeamServiceOwnerUpdatesDefaultMemberLimits(t *testing.T) {
	repo := &fakeTeamRepository{teamContext: teamServiceTestContext(1, TeamRoleOwner)}
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

	teamCtx, err := svc.UpdateDefaultMemberLimits(context.Background(), 1, 1.5, 8, 30)
	require.NoError(t, err)
	require.True(t, repo.defaultLimitsSet)
	require.Equal(t, [3]float64{1.5, 8, 30}, repo.defaultLimits)
	require.Equal(t, 1.5, teamCtx.Team.DefaultDailyLimitUSD)
}

func TestTeamServiceRejectsNegativeDefaultMemberLimits(t *testing.T) {
	repo := &fakeTeamRepository{teamContext: teamServiceTestContext(1, TeamRoleOwner)}
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

	_, err := svc.UpdateDefaultMemberLimits(context.Background(), 1, -1, 8, 30)
	require.Error(t, err)
	require.False(t, repo.defaultLimitsSet)
}

func TestTeamServiceInvitationEmailUsesCustomNotificationTemplate(t *testing.T) {
	ctx := context.Background()
	settingRepo := mailtest.NewMemorySettings()
	smtpServer := mailtest.StartSMTPServer(t)
	require.NoError(t, settingRepo.SetMultiple(ctx, smtpServer.Settings()))
	require.NoError(t, settingRepo.Set(ctx, site.SettingKeyFrontendURL, "https://database.example"))

	emailService := notification.NewMailer(settingRepo, smtp.New())
	notificationService := notification.NewNotificationEmailService(settingRepo, emailService)
	emailService.SetNotificationEmailService(notificationService)
	_, err := notificationService.UpdateTemplate(
		ctx,
		notification.NotificationEmailEventTeamInvitation,
		"en",
		"Custom invitation for {{team_name}}",
		`<h1>Custom team invitation</h1><p>{{recipient_name}}</p><a href="{{invitation_url}}">Join {{team_name}}</a><p>{{expires_at}}</p>`,
	)
	require.NoError(t, err)

	cfg := &Options{FrontendURL: "https://config.example"}
	settingService := site.NewDisplaySettings(settingRepo, func() string { return cfg.FrontendURL })
	svc := NewTeamService(nil, nil, NewEmailNotifications(emailService, nil), nil, nil, settingService, cfg)
	expiresAt := time.Date(2026, 8, 3, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	link, err := svc.FrontendLink(ctx, "/team", "invitation", "test-token")
	require.NoError(t, err)
	require.NoError(t, svc.SendInvitationEmail(ctx, "member@example.com", "Platform Team", link, expiresAt))

	require.Equal(t, int64(1), smtpServer.MessageCount())
	message := smtpServer.LastMessage()
	messageBody := smtpServer.LastMessageBody(t)
	require.Contains(t, message, "Subject: Custom invitation for Platform Team")
	require.Contains(t, messageBody, "Custom team invitation")
	require.Contains(t, messageBody, "https://database.example/team?invitation=test-token")
	require.NotContains(t, messageBody, "https://config.example")
	require.Contains(t, messageBody, expiresAt.Format(time.RFC3339))
	require.False(t, strings.Contains(messageBody, "你被邀请加入团队"))
}

func TestTeamServiceFrontendLinkFallsBackToConfig(t *testing.T) {
	ctx := context.Background()
	cfg := &Options{FrontendURL: "https://config.example/"}
	settingService := site.NewDisplaySettings(mailtest.NewMemorySettings(), func() string { return cfg.FrontendURL })
	svc := NewTeamService(nil, nil, nil, nil, nil, settingService, cfg)

	link, err := svc.FrontendLink(ctx, "/team", "invitation", "test-token")

	require.NoError(t, err)
	require.Equal(t, "https://config.example/team?invitation=test-token", link)
}

func TestTeamServiceFrontendLinkRejectsMissingBaseURL(t *testing.T) {
	ctx := context.Background()
	cfg := &Options{}
	settingService := site.NewDisplaySettings(mailtest.NewMemorySettings(), func() string { return cfg.FrontendURL })
	svc := NewTeamService(nil, nil, nil, nil, nil, settingService, cfg)

	link, err := svc.FrontendLink(ctx, "/team", "invitation", "test-token")

	require.ErrorIs(t, err, ErrTeamFrontendURLUnavailable)
	require.Empty(t, link)
}

func TestTeamServiceInviteRejectsMissingBaseURLBeforeCreatingInvitation(t *testing.T) {
	ctx := context.Background()
	repo := &fakeTeamRepository{teamContext: teamServiceTestContext(1, TeamRoleOwner)}
	settingRepo := mailtest.NewMemorySettings()
	cfg := &Options{Enabled: true}
	settingService := site.NewDisplaySettings(settingRepo, func() string { return cfg.FrontendURL })
	emailService := notification.NewMailer(settingRepo, smtp.New())
	svc := NewTeamService(repo, nil, NewEmailNotifications(emailService, nil), nil, &fakeTeamInvitationLimiter{allowed: true}, settingService, cfg)

	invitation, err := svc.Invite(ctx, 1, "member@example.com")

	require.ErrorIs(t, err, ErrTeamFrontendURLUnavailable)
	require.Nil(t, invitation)
	require.Zero(t, repo.invitationCreates)
}

func TestTeamServicePreviewInvitationUsesTokenHashAndCurrentUserEmail(t *testing.T) {
	preview := &TeamInvitationPreview{
		TeamName:     "平台团队",
		InviterName:  "owner",
		InviterEmail: "owner@example.com",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	repo := &fakeTeamRepository{invitationPreview: preview}
	userRepo := &fakeTeamUserRepository{user: &UserSnapshot{ID: 7, Email: " Member@Example.COM "}}
	svc := NewTeamService(repo, userRepo, nil, nil, nil, nil, nil)

	result, err := svc.PreviewInvitation(context.Background(), 7, " raw-token ")

	require.NoError(t, err)
	require.Same(t, preview, result)
	require.Equal(t, HashTeamToken("raw-token"), repo.previewTokenHash)
	require.Equal(t, "member@example.com", repo.previewEmail)
	require.False(t, repo.previewAt.IsZero())
}

func TestTeamServiceInvitationLimitReturnsRetryAfter(t *testing.T) {
	svc := NewTeamService(nil, nil, nil, nil, &fakeTeamInvitationLimiter{retryAfter: 1500 * time.Millisecond}, nil, nil)

	err := svc.CheckInvitationRate(context.Background(), 11, "member@example.com")
	require.ErrorIs(t, err, ErrTeamInvitationRateLimited)
	var appErr *apperror.ApplicationError
	require.True(t, errors.As(err, &appErr))
	require.Equal(t, "2", appErr.Metadata["retry_after"])
}

func TestTeamServiceInvitationLimitFailsClosedWhenRedisUnavailable(t *testing.T) {
	svc := NewTeamService(nil, nil, nil, nil, &fakeTeamInvitationLimiter{err: errors.New("redis unavailable")}, nil, nil)

	err := svc.CheckInvitationRate(context.Background(), 11, "member@example.com")
	require.ErrorIs(t, err, ErrTeamInvitationUnavailable)
}

func (l *fakeTeamInvitationLimiter) CheckAndRecord(context.Context, int64, string) (bool, time.Duration, error) {
	return l.allowed, l.retryAfter, l.err
}

func (r *fakeTeamRepository) GetContextByUserID(context.Context, int64) (*TeamContext, error) {
	return r.teamContext, nil
}

func (r *fakeTeamRepository) GetContextByTeamID(context.Context, int64) (*TeamContext, error) {
	return r.teamContext, nil
}

func (r *fakeTeamRepository) ListMembers(context.Context, int64) ([]TeamMembership, error) {
	return r.members, nil
}

func (r *fakeTeamRepository) GetUsageSummary(_ context.Context, _ int64, query TeamUsageQuery) (*TeamUsageSummary, error) {
	r.usageQuery = query
	return r.usageSummary, nil
}

func (r *fakeTeamRepository) ListTeamKeys(_ context.Context, _ int64, actorUserID *int64) ([]TeamAPIKeyItem, error) {
	r.teamKeyActorIsNil = actorUserID == nil
	if actorUserID != nil {
		value := *actorUserID
		r.teamKeyActor = &value
	}
	return r.teamKeys, nil
}

func (r *fakeTeamRepository) SetStatus(_ context.Context, _ int64, status string) error {
	r.status = status
	r.teamContext.Team.Status = status
	return nil
}

func (r *fakeTeamRepository) UpdateName(_ context.Context, _ int64, name string) error {
	r.name = name
	r.teamContext.Team.Name = name
	return nil
}

func (r *fakeTeamRepository) SetDefaultMemberLimits(_ context.Context, _ int64, daily, weekly, monthly float64) error {
	r.defaultLimits = [3]float64{daily, weekly, monthly}
	r.defaultLimitsSet = true
	r.teamContext.Team.DefaultDailyLimitUSD = daily
	r.teamContext.Team.DefaultWeeklyLimitUSD = weekly
	r.teamContext.Team.DefaultMonthlyLimitUSD = monthly
	return nil
}

func (r *fakeTeamRepository) CreateInvitation(_ context.Context, teamID, inviterUserID int64, email, _ string, expiresAt time.Time) (*TeamInvitation, error) {
	r.invitationCreates++
	return &TeamInvitation{TeamID: teamID, InviterUserID: inviterUserID, Email: email, ExpiresAt: expiresAt}, nil
}

func (r *fakeTeamRepository) PreviewInvitation(_ context.Context, tokenHash, normalizedEmail string, now time.Time) (*TeamInvitationPreview, error) {
	r.previewTokenHash = tokenHash
	r.previewEmail = normalizedEmail
	r.previewAt = now
	return r.invitationPreview, nil
}

func (r *fakeTeamUserRepository) GetByID(context.Context, int64) (*UserSnapshot, error) {
	return r.user, nil
}

func (r *fakeTeamRepository) UpdateAdmin(_ context.Context, _ int64, update TeamAdminUpdate) error {
	r.adminUpdate = update
	r.adminUpdateCount++
	if update.Name != nil {
		r.teamContext.Team.Name = *update.Name
	}
	if update.Status != nil {
		r.teamContext.Team.Status = *update.Status
	}
	if update.MemberLimit != nil {
		r.teamContext.Team.MemberLimit = *update.MemberLimit
	}
	return nil
}

func teamServiceTestContext(userID int64, role string) *TeamContext {
	return &TeamContext{
		Team:       &Team{ID: 11, Name: "测试团队", Status: TeamStatusActive},
		Membership: &TeamMembership{ID: 21, TeamID: 11, UserID: userID, Role: role},
		Owner:      &TeamMembership{ID: 20, TeamID: 11, UserID: 1, Role: TeamRoleOwner},
	}
}
