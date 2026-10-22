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

// Team service tests keep the acting membership separate from the fixed owner
// record, so reports and management decisions can be observed under either
// role. Fixture result pointers/slices are retained, including Key items that
// the service masks and clears in place.
//
// Repository overrides record selected inputs while unimplemented methods
// come from an embedded interface. The invitation limiter selects represented
// admission outcomes without Redis storage, and the mail scenario uses the
// notification renderer with a test SMTP server. Assertions cover delegated
// scope, validation-before-write, URL selection and selected delivery output;
// they leave persistence transaction behavior to repository tests.

// fakeTeamInvitationLimiter supplies selected admission outcomes to
// team-service invitation tests.
type fakeTeamInvitationLimiter struct {
	allowed bool // configured invitation admission result
	// retryAfter supplies the delay returned with the configured flag.
	// TeamService rounds denied delays upward to at least one retry second.
	retryAfter time.Duration
	// err is returned with the configured admission observations. TeamService
	// treats any limiter error as unavailable before considering the allowed
	// flag.
	err error
}

// fakeTeamRepository records selected team operations and retains configured
// result data for sequential service tests.
type fakeTeamRepository struct {
	// TeamRepository supplies methods outside this fixture's selected overrides.
	// A nil embedded interface cannot handle an unexpected service operation
	// introduced into a test flow.
	TeamRepository

	// teamContext is returned as a shared pointer for any identity. Mutation
	// helpers require its Team and change that retained object directly.
	teamContext *TeamContext
	// teamContextError accompanies the configured context, allowing report tests
	// to check that lookup errors take precedence over membership validation.
	teamContextError error
	contextUserID    int64 // most recent context requester
	// usageEvents records context lookup, injected clock sampling and report
	// reads in call order. Tests use it to detect work performed before admission.
	usageEvents []string
	// members is the retained slice returned by ListMembers. Owner responses
	// share its storage; ordinary-member filtering copies selected outer records
	// into a new slice.
	members []TeamMembership
	// usageSummary is returned by retained pointer after recording the usage
	// query. Nil is a permitted fixture result, leaving result interpretation to
	// the test caller.
	usageSummary *TeamUsageSummary
	// usageQuery captures the submitted value after service normalization/scope
	// selection. ActorUserID and APIKeyID pointers remain shared with the passed
	// query.
	usageQuery  TeamUsageQuery
	usageTeamID int64 // most recent report tenant
	// usageSeries and usageItems retain the configured report slices, including
	// nil versus empty values and backing storage. Report methods return these
	// values alongside usageError so callers can check their result handling.
	usageSeries []TeamMemberUsageSeries // configured member trend results
	usageItems  []TeamUsageLogItem      // configured usage record results
	usageTotal  int64                   // configured matching record total
	usageError  error                   // configured report read failure
	// teamKeys is returned with its existing backing storage. TeamService masks
	// and clears each item, so those changes also affect retained fixture data.
	teamKeys []TeamAPIKeyItem
	// teamKeyActor receives a fresh copied ID for nonnil filters. A later
	// nil-filter call leaves this prior pointer unchanged while updating
	// teamKeyActorIsNil.
	teamKeyActor      *int64
	teamKeyActorIsNil bool       // observed absent creator filter
	status            string     // most recent submitted team status
	name              string     // most recent submitted team name
	defaultLimits     [3]float64 // daily weekly monthly USD defaults
	defaultLimitsSet  bool       // observed default triple write
	// adminUpdate captures the submitted administrator value and its retained
	// pointers. Tests keep pointed inputs stable while reading this recorded
	// request.
	adminUpdate       TeamAdminUpdate
	adminUpdateCount  int // observed administrator update count
	invitationCreates int // observed invitation creation count
	// invitationPreview is returned by shared pointer after delegated preview
	// inputs are recorded. Tests supply it directly rather than storing
	// invitation rows.
	invitationPreview *TeamInvitationPreview
	previewTokenHash  string    // most recent hashed review token
	previewEmail      string    // most recent normalized current mailbox
	previewAt         time.Time // most recent preview evaluation time
}

// fakeTeamUserRepository supplies a retained account mailbox for team
// invitation preview tests.
type fakeTeamUserRepository struct {
	// UserReader supplies account operations outside the overridden GetByID
	// method. A nil embedded interface cannot service another lookup reached by
	// changed test flow.
	UserReader

	// user is returned by shared pointer for any account ID. Preview callers
	// supply a nonnil account before the service reads its mailbox.
	user *UserSnapshot
}

// TestTeamServiceListMembersMemberOnlySeesOwnerAndSelf lists three
// represented memberships as an ordinary member. It checks that the first two
// returned records carry the owner ID followed by the acting member ID.
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

// TestTeamServiceUsageScopeCannotBeExpandedByMember submits another actor
// filter as an ordinary member. The repository receives the current member ID
// instead, protecting service scope narrowing before usage reporting.
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

// TestTeamServiceUsageScopeOwnerKeepsMemberFilter submits a target member
// filter as the owner. The repository receives that positive target
// unchanged, allowing owner reports to select one represented member.
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

// TestTeamServiceUsageReportsNormalizeAndScope exercises all member report
// entries with owner and member filters. Repository observations protect date
// defaults, pagination, pointer retention and one clock sample after admission.
func TestTeamServiceUsageReportsNormalizeAndScope(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	now := time.Date(2026, 11, 2, 10, 11, 12, 13, location)
	defaultFrom := time.Date(2026, 10, 3, 10, 11, 12, 13, location)
	early := time.Date(2026, 9, 1, 3, 4, 5, 6, location)
	late := time.Date(2026, 10, 1, 3, 4, 5, 6, location)
	actorID := int64(99)
	keyID := int64(31)
	selfID := int64(2)
	tests := []struct {
		name  string         // descriptive report query case
		role  string         // configured acting membership role
		query TeamUsageQuery // caller supplied query value
		want  TeamUsageQuery // expected repository query value
	}{
		{
			name: "owner defaults", role: TeamRoleOwner,
			want: TeamUsageQuery{From: defaultFrom, To: now, Limit: 20},
		},
		{
			name: "owner actor and key filters", role: TeamRoleOwner,
			query: TeamUsageQuery{From: early, To: late, ActorUserID: &actorID, APIKeyID: &keyID, Limit: 100, Offset: 47},
			want:  TeamUsageQuery{From: early, To: late, ActorUserID: &actorID, APIKeyID: &keyID, Limit: 100, Offset: 47},
		},
		{
			name: "member defaults", role: TeamRoleMember,
			want: TeamUsageQuery{From: defaultFrom, To: now, ActorUserID: &selfID, Limit: 20},
		},
		{
			name: "member replaces another actor", role: TeamRoleMember,
			query: TeamUsageQuery{ActorUserID: &actorID, APIKeyID: &keyID, Limit: 101, Offset: -7},
			want:  TeamUsageQuery{From: defaultFrom, To: now, ActorUserID: &selfID, APIKeyID: &keyID, Limit: 20},
		},
		{
			name: "member own actor and smallest page", role: TeamRoleMember,
			query: TeamUsageQuery{From: early, To: late, ActorUserID: &selfID, APIKeyID: &keyID, Limit: 1, Offset: 200},
			want:  TeamUsageQuery{From: early, To: late, ActorUserID: &selfID, APIKeyID: &keyID, Limit: 1, Offset: 200},
		},
		{
			name: "start follows selected end", role: TeamRoleOwner,
			query: TeamUsageQuery{To: late, Limit: -1},
			want:  TeamUsageQuery{From: early, To: late, Limit: 20},
		},
		{
			name: "reversed dates", role: TeamRoleMember,
			query: TeamUsageQuery{From: late, To: early, Limit: 25, Offset: -1},
			want:  TeamUsageQuery{From: early, To: late, ActorUserID: &selfID, Limit: 25},
		},
		{
			name: "missing end is filled before swapping", role: TeamRoleOwner,
			query: TeamUsageQuery{From: now.Add(time.Hour), Limit: 10},
			want:  TeamUsageQuery{From: now, To: now.Add(time.Hour), Limit: 10},
		},
	}
	for _, report := range []string{"summary", "series", "logs"} {
		for _, tt := range tests {
			t.Run(report+"/"+tt.name, func(t *testing.T) {
				repo := &fakeTeamRepository{teamContext: teamServiceTestContext(2, tt.role)}
				svc := NewTeamService(repo, nil, nil, nil, nil, nil, &Options{
					Enabled: true,
					Now: func() time.Time {
						repo.usageEvents = append(repo.usageEvents, "clock")
						return now
					},
				})
				query := tt.query

				result, err := runTeamUsageReport(t, svc, report, 2, query)

				require.NoError(t, err)
				require.Equal(t, []string{"context", "clock", report}, repo.usageEvents)
				require.Equal(t, int64(2), repo.contextUserID)
				require.Equal(t, int64(11), repo.usageTeamID)
				require.Equal(t, tt.want, repo.usageQuery)
				require.Equal(t, tt.query, query)
				require.Equal(t, int64(99), actorID)
				require.Equal(t, int64(2), selfID)
				require.True(t, query.APIKeyID == repo.usageQuery.APIKeyID)
				if tt.role == TeamRoleOwner {
					require.True(t, query.ActorUserID == repo.usageQuery.ActorUserID)
				} else {
					require.NotSame(t, query.ActorUserID, repo.usageQuery.ActorUserID)
				}
				if report == "logs" {
					page, ok := result.(*TeamUsagePage)
					require.True(t, ok)
					require.Equal(t, tt.want.Limit, page.Limit)
					require.Equal(t, tt.want.Offset, page.Offset)
				}
			})
		}
	}
}

// TestTeamServiceUsageReportsRejectBeforeQuery exercises feature, context and
// team-state failures across all member reports. Each rejection returns its
// existing error before the service samples time or reads report data.
func TestTeamServiceUsageReportsRejectBeforeQuery(t *testing.T) {
	lookupError := errors.New("context lookup failed")
	suspendedOwner := teamServiceTestContext(2, TeamRoleOwner)
	suspendedOwner.Team.Status = TeamStatusSuspended
	suspendedMember := teamServiceTestContext(2, TeamRoleMember)
	suspendedMember.Team.Status = TeamStatusSuspended
	tests := []struct {
		name        string       // descriptive admission failure case
		disabled    bool         // configured feature gate rejection
		teamContext *TeamContext // configured membership lookup result
		lookupError error        // configured membership lookup failure
		wantError   error        // expected report admission failure
	}{
		{name: "disabled precedes lookup", disabled: true, lookupError: lookupError, wantError: ErrTeamFeatureDisabled},
		{name: "lookup error precedes context validation", lookupError: lookupError, wantError: lookupError},
		{name: "missing context", wantError: ErrTeamMembershipRequired},
		{name: "missing team", teamContext: &TeamContext{Membership: &TeamMembership{}}, wantError: ErrTeamMembershipRequired},
		{name: "missing membership precedes suspension", teamContext: &TeamContext{Team: &Team{Status: TeamStatusSuspended}}, wantError: ErrTeamMembershipRequired},
		{name: "suspended owner", teamContext: suspendedOwner, wantError: ErrTeamSuspended},
		{name: "suspended member", teamContext: suspendedMember, wantError: ErrTeamSuspended},
		{name: "lookup error precedes suspension", teamContext: suspendedMember, lookupError: lookupError, wantError: lookupError},
	}
	for _, report := range []string{"summary", "series", "logs"} {
		for _, tt := range tests {
			t.Run(report+"/"+tt.name, func(t *testing.T) {
				repo := &fakeTeamRepository{teamContext: tt.teamContext, teamContextError: tt.lookupError}
				svc := NewTeamService(repo, nil, nil, nil, nil, nil, &Options{
					Enabled: !tt.disabled,
					Now: func() time.Time {
						repo.usageEvents = append(repo.usageEvents, "clock")
						return time.Time{}
					},
				})

				result, err := runTeamUsageReport(t, svc, report, 2, TeamUsageQuery{})

				require.ErrorIs(t, err, tt.wantError)
				require.Nil(t, result)
				if tt.disabled {
					require.Empty(t, repo.usageEvents)
				} else {
					require.Equal(t, []string{"context"}, repo.usageEvents)
				}
			})
		}
	}
}

// TestTeamServiceUsageReportsRetainRepositoryResults supplies nil, empty and
// populated report values with successful and failed reads. Summary and series
// forward repository results, while logs build a page only after success.
func TestTeamServiceUsageReportsRetainRepositoryResults(t *testing.T) {
	readError := errors.New("report read failed")
	for _, report := range []string{"summary", "series", "logs"} {
		for _, shape := range []string{"nil", "empty", "populated"} {
			for _, failed := range []bool{false, true} {
				name := report + "/" + shape + "/success"
				if failed {
					name = report + "/" + shape + "/failure"
				}
				t.Run(name, func(t *testing.T) {
					repo := &fakeTeamRepository{teamContext: teamServiceTestContext(2, TeamRoleMember), usageTotal: 73}
					if shape != "nil" {
						repo.usageSummary = &TeamUsageSummary{}
						repo.usageSeries = []TeamMemberUsageSeries{}
						repo.usageItems = []TeamUsageLogItem{}
					}
					if shape == "populated" {
						repo.usageSummary.ActualCost = 3
						repo.usageSeries = append(repo.usageSeries, TeamMemberUsageSeries{ActorUserID: 2})
						repo.usageItems = append(repo.usageItems, TeamUsageLogItem{ID: 41})
					}
					if failed {
						repo.usageError = readError
					}
					svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

					result, err := runTeamUsageReport(t, svc, report, 2, TeamUsageQuery{Limit: 7, Offset: 8})

					require.ErrorIs(t, err, repo.usageError)
					switch report {
					case "summary":
						summary, ok := result.(*TeamUsageSummary)
						require.True(t, ok)
						require.True(t, summary == repo.usageSummary)
					case "series":
						series, ok := result.([]TeamMemberUsageSeries)
						require.True(t, ok)
						require.Equal(t, repo.usageSeries, series)
						if len(series) > 0 {
							require.Same(t, &repo.usageSeries[0], &series[0])
						}
					case "logs":
						if failed {
							require.Nil(t, result)
							return
						}
						page, ok := result.(*TeamUsagePage)
						require.True(t, ok)
						require.Equal(t, &TeamUsagePage{Items: repo.usageItems, Total: 73, Limit: 7, Offset: 8}, page)
						if len(page.Items) > 0 {
							require.Same(t, &repo.usageItems[0], &page.Items[0])
						}
					}
				})
			}
		}
	}
}

// runTeamUsageReport invokes one selected public member-report entry for
// shared characterization cases. It retains each entry's concrete result type
// inside any so tests can inspect nil values and backing storage.
func runTeamUsageReport(t *testing.T, svc *TeamService, report string, userID int64, query TeamUsageQuery) (any, error) {
	t.Helper()
	switch report {
	case "summary":
		return svc.GetUsageSummary(t.Context(), userID, query)
	case "series":
		return svc.ListMemberUsageSeries(t.Context(), userID, query)
	case "logs":
		return svc.ListUsageLogs(t.Context(), userID, query)
	default:
		t.Fatalf("unknown report: %s", report)
		return nil, nil
	}
}

// TestTeamServiceAdminUsageKeepsMemberFilter submits a member filter through
// administrator usage reporting. After the configured team context is
// accepted, the repository receives the same selected member ID.
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

// TestTeamServiceAdminUpdatesName requests an administrator rename with
// surrounding spaces. The recorded repository value contains the trimmed
// Unicode team name and the operation returns success.
func TestTeamServiceAdminUpdatesName(t *testing.T) {
	repo := &fakeTeamRepository{teamContext: teamServiceTestContext(1, TeamRoleOwner)}
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

	err := svc.AdminUpdateName(context.Background(), 11, "  新团队名称  ")
	require.NoError(t, err)
	require.Equal(t, "新团队名称", repo.name)
}

// TestTeamServiceAdminUpdateValidatesBeforeWriting combines a usable name
// with a negative member ceiling. Validation returns an error and the
// repository records no administrator update or name mutation.
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

// TestTeamServiceAdminUpdateWritesAllFieldsOnce submits name, status and
// member ceiling together. One recorded repository update contains trimmed
// name and the returned shared context reflects selected status and ceiling.
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

// TestTeamServiceListTeamKeysMasksSecretsAndAppliesRoleFilter lists one
// plaintext fixture credential as owner and ordinary member. Returned data
// clears that credential, exposes its mask and passes nil or self creator
// filtering according to role.
func TestTeamServiceListTeamKeysMasksSecretsAndAppliesRoleFilter(t *testing.T) {
	tests := []struct {
		name   string // descriptive role filtering case
		userID int64  // acting account identity value
		role   string // represented acting membership role
		// wantActorIsNil and wantActorUserID describe the selected creator filter.
		// A true nil expectation checks owner-wide access and skips the ID
		// assertion; false checks the represented member ID.
		wantActorIsNil  bool  // expected absent creator filter
		wantActorUserID int64 // expected selected creator identity
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

// TestTeamServiceSuspendedOwnerCanResume sets a represented owner's team to
// suspended before requesting active status. The recorded status and returned
// team context become active through the owner-inclusive suspended path.
func TestTeamServiceSuspendedOwnerCanResume(t *testing.T) {
	repo := &fakeTeamRepository{teamContext: teamServiceTestContext(1, TeamRoleOwner)}
	repo.teamContext.Team.Status = TeamStatusSuspended
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

	teamCtx, err := svc.SetStatus(context.Background(), 1, TeamStatusActive)
	require.NoError(t, err)
	require.Equal(t, TeamStatusActive, repo.status)
	require.Equal(t, TeamStatusActive, teamCtx.Team.Status)
}

// TestTeamServiceSuspendedMemberCannotChangeStatus requests resume as an
// ordinary member of a suspended team. Owner-required rejection protects
// status mutation even though the membership lookup permits suspended
// context.
func TestTeamServiceSuspendedMemberCannotChangeStatus(t *testing.T) {
	repo := &fakeTeamRepository{teamContext: teamServiceTestContext(2, TeamRoleMember)}
	repo.teamContext.Team.Status = TeamStatusSuspended
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

	_, err := svc.SetStatus(context.Background(), 2, TeamStatusActive)
	require.ErrorIs(t, err, ErrTeamOwnerRequired)
}

// TestTeamServiceOwnerUpdatesDefaultMemberLimits submits nonnegative
// daily/weekly/monthly defaults as the owner. The fixture records the
// complete USD triple and the returned context reflects its daily default.
func TestTeamServiceOwnerUpdatesDefaultMemberLimits(t *testing.T) {
	repo := &fakeTeamRepository{teamContext: teamServiceTestContext(1, TeamRoleOwner)}
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

	teamCtx, err := svc.UpdateDefaultMemberLimits(context.Background(), 1, 1.5, 8, 30)
	require.NoError(t, err)
	require.True(t, repo.defaultLimitsSet)
	require.Equal(t, [3]float64{1.5, 8, 30}, repo.defaultLimits)
	require.Equal(t, 1.5, teamCtx.Team.DefaultDailyLimitUSD)
}

// TestTeamServiceRejectsNegativeDefaultMemberLimits supplies a negative daily
// default beside usable weekly/monthly amounts. The service returns an error
// and the fixture records no default-limit write.
func TestTeamServiceRejectsNegativeDefaultMemberLimits(t *testing.T) {
	repo := &fakeTeamRepository{teamContext: teamServiceTestContext(1, TeamRoleOwner)}
	svc := NewTeamService(repo, nil, nil, nil, nil, nil, nil)

	_, err := svc.UpdateDefaultMemberLimits(context.Background(), 1, -1, 8, 30)
	require.Error(t, err)
	require.False(t, repo.defaultLimitsSet)
}

// TestTeamServiceInvitationEmailUsesCustomNotificationTemplate sends an
// invitation through the configured notification template and test SMTP
// server. One message uses the custom English template, saved frontend URL
// and formatted expiry instead of configured URL/built-in text.
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

// TestTeamServiceFrontendLinkFallsBackToConfig uses a display-settings reader
// with no stored frontend URL and an injected startup callback. The resulting
// review link uses that reader's startup value with its trailing slash
// removed.
func TestTeamServiceFrontendLinkFallsBackToConfig(t *testing.T) {
	ctx := context.Background()
	cfg := &Options{FrontendURL: "https://config.example/"}
	settingService := site.NewDisplaySettings(mailtest.NewMemorySettings(), func() string { return cfg.FrontendURL })
	svc := NewTeamService(nil, nil, nil, nil, nil, settingService, cfg)

	link, err := svc.FrontendLink(ctx, "/team", "invitation", "test-token")

	require.NoError(t, err)
	require.Equal(t, "https://config.example/team?invitation=test-token", link)
}

// TestTeamServiceFrontendLinkRejectsMissingBaseURL supplies a
// display-settings reader whose saved/startup URL choices are empty.
// FrontendLink returns unavailable and empty link, protecting review-link
// rejection before configured invitation delivery.
func TestTeamServiceFrontendLinkRejectsMissingBaseURL(t *testing.T) {
	ctx := context.Background()
	cfg := &Options{}
	settingService := site.NewDisplaySettings(mailtest.NewMemorySettings(), func() string { return cfg.FrontendURL })
	svc := NewTeamService(nil, nil, nil, nil, nil, settingService, cfg)

	link, err := svc.FrontendLink(ctx, "/team", "invitation", "test-token")

	require.ErrorIs(t, err, ErrTeamFrontendURLUnavailable)
	require.Empty(t, link)
}

// TestTeamServiceInviteRejectsMissingBaseURLBeforeCreatingInvitation admits
// invitation rate checking but leaves frontend URL empty with a configured
// mail sender. The service returns unavailable and nil invitation before the
// repository records creation.
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

// TestTeamServicePreviewInvitationUsesTokenHashAndCurrentUserEmail previews a
// spaced token for an account with mixed-case padded email. The repository
// receives the trimmed-token hash, normalized current mailbox and a nonzero
// time, returning its shared preview.
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

// TestTeamServiceInvitationLimitReturnsRetryAfter denies admission with a
// 1.5-second configured delay. The returned rate-limit error includes two
// retry seconds, protecting upward rounding for the caller-facing hint.
func TestTeamServiceInvitationLimitReturnsRetryAfter(t *testing.T) {
	svc := NewTeamService(nil, nil, nil, nil, &fakeTeamInvitationLimiter{retryAfter: 1500 * time.Millisecond}, nil, nil)

	err := svc.CheckInvitationRate(context.Background(), 11, "member@example.com")
	require.ErrorIs(t, err, ErrTeamInvitationRateLimited)
	var appErr *apperror.ApplicationError
	require.True(t, errors.As(err, &appErr))
	require.Equal(t, "2", appErr.Metadata["retry_after"])
}

// TestTeamServiceInvitationLimitFailsClosedWhenRedisUnavailable makes the
// configured invitation limiter fail. The service returns
// invitation-unavailable, protecting rejection of admission when its shared
// limiter cannot produce a usable result.
func TestTeamServiceInvitationLimitFailsClosedWhenRedisUnavailable(t *testing.T) {
	svc := NewTeamService(nil, nil, nil, nil, &fakeTeamInvitationLimiter{err: errors.New("redis unavailable")}, nil, nil)

	err := svc.CheckInvitationRate(context.Background(), 11, "member@example.com")
	require.ErrorIs(t, err, ErrTeamInvitationUnavailable)
}

// CheckAndRecord returns the configured admission flag, delay and error for
// every input. It records no permit or mailbox history, so tests use it to
// select service branches rather than reproduce Redis behavior.
func (l *fakeTeamInvitationLimiter) CheckAndRecord(context.Context, int64, string) (bool, time.Duration, error) {
	return l.allowed, l.retryAfter, l.err
}

// GetContextByUserID returns the configured shared team context for every
// requested account after recording the identity and lookup event. Its
// configured error lets tests check validation ordering for incomplete contexts.
func (r *fakeTeamRepository) GetContextByUserID(_ context.Context, userID int64) (*TeamContext, error) {
	r.contextUserID = userID
	r.usageEvents = append(r.usageEvents, "context")
	return r.teamContext, r.teamContextError
}

// GetContextByTeamID returns the configured shared team context for every
// requested team. The fixture models no missing-row error, so administrator
// tests establish availability by supplying the context directly.
func (r *fakeTeamRepository) GetContextByTeamID(context.Context, int64) (*TeamContext, error) {
	return r.teamContext, nil
}

// ListMembers returns the configured membership slice with its backing
// storage retained. Requested team IDs are ignored; ordinary-member service
// filtering can allocate a subset while owner results share this slice.
func (r *fakeTeamRepository) ListMembers(context.Context, int64) ([]TeamMembership, error) {
	return r.members, nil
}

// GetUsageSummary records the supplied tenant and query before returning the
// shared summary and configured error. Captured pointer filters remain shared,
// so tests keep selected identities stable while inspecting scope.
func (r *fakeTeamRepository) GetUsageSummary(_ context.Context, teamID int64, query TeamUsageQuery) (*TeamUsageSummary, error) {
	r.usageEvents = append(r.usageEvents, "summary")
	r.usageTeamID = teamID
	r.usageQuery = query
	return r.usageSummary, r.usageError
}

// ListMemberUsageSeries records the supplied tenant and query before returning
// the configured slice and error. Its retained backing storage lets report
// tests check direct forwarding of member trends.
func (r *fakeTeamRepository) ListMemberUsageSeries(_ context.Context, teamID int64, query TeamUsageQuery) ([]TeamMemberUsageSeries, error) {
	r.usageEvents = append(r.usageEvents, "series")
	r.usageTeamID = teamID
	r.usageQuery = query
	return r.usageSeries, r.usageError
}

// ListUsageLogs records the supplied tenant and query before returning the
// configured records, total and error. Nil and empty slices remain distinct,
// and populated slices retain their configured backing storage.
func (r *fakeTeamRepository) ListUsageLogs(_ context.Context, teamID int64, query TeamUsageQuery) ([]TeamUsageLogItem, int64, error) {
	r.usageEvents = append(r.usageEvents, "logs")
	r.usageTeamID = teamID
	r.usageQuery = query
	return r.usageItems, r.usageTotal, r.usageError
}

// ListTeamKeys records nil creator filtering or copies the supplied creator
// ID into fixture-owned storage. It returns the shared Key slice, allowing
// service masking and plaintext clearing to mutate retained items.
func (r *fakeTeamRepository) ListTeamKeys(_ context.Context, _ int64, actorUserID *int64) ([]TeamAPIKeyItem, error) {
	r.teamKeyActorIsNil = actorUserID == nil
	if actorUserID != nil {
		value := *actorUserID
		r.teamKeyActor = &value
	}
	return r.teamKeys, nil
}

// SetStatus records status and changes the shared team context. A nonnil
// context/team is required; no database constraints or authentication-cache
// publication are implemented by this fixture.
func (r *fakeTeamRepository) SetStatus(_ context.Context, _ int64, status string) error {
	r.status = status
	r.teamContext.Team.Status = status
	return nil
}

// UpdateName records name and changes the shared team context. A nonnil
// context/team is required, and service tests inspect the received normalized
// text rather than database validation.
func (r *fakeTeamRepository) UpdateName(_ context.Context, _ int64, name string) error {
	r.name = name
	r.teamContext.Team.Name = name
	return nil
}

// SetDefaultMemberLimits records the complete USD triple and sets its
// observation flag before updating shared team defaults. A nonnil
// context/team is required; existing membership limits remain untouched.
func (r *fakeTeamRepository) SetDefaultMemberLimits(_ context.Context, _ int64, daily, weekly, monthly float64) error {
	r.defaultLimits = [3]float64{daily, weekly, monthly}
	r.defaultLimitsSet = true
	r.teamContext.Team.DefaultDailyLimitUSD = daily
	r.teamContext.Team.DefaultWeeklyLimitUSD = weekly
	r.teamContext.Team.DefaultMonthlyLimitUSD = monthly
	return nil
}

// CreateInvitation counts creation and returns a fresh invitation containing
// submitted identity/email/expiry fields. It ignores token hash, stores no
// invitation and leaves status/record ID zero for service-order assertions.
func (r *fakeTeamRepository) CreateInvitation(_ context.Context, teamID, inviterUserID int64, email, _ string, expiresAt time.Time) (*TeamInvitation, error) {
	r.invitationCreates++
	return &TeamInvitation{TeamID: teamID, InviterUserID: inviterUserID, Email: email, ExpiresAt: expiresAt}, nil
}

// PreviewInvitation records token hash, normalized mailbox and evaluation
// time before returning the configured preview pointer. No token-state
// validation occurs here; service tests inspect its delegated inputs.
func (r *fakeTeamRepository) PreviewInvitation(_ context.Context, tokenHash, normalizedEmail string, now time.Time) (*TeamInvitationPreview, error) {
	r.previewTokenHash = tokenHash
	r.previewEmail = normalizedEmail
	r.previewAt = now
	return r.invitationPreview, nil
}

// GetByID returns the configured recipient account pointer for every
// requested identity. It can return nil with nil error; callers using it for
// preview supply a nonnil account in setup.
func (r *fakeTeamUserRepository) GetByID(context.Context, int64) (*UserSnapshot, error) {
	return r.user, nil
}

// UpdateAdmin records the supplied update value and increments its call count
// before applying each nonnil field to shared team context. Pointer fields in
// the recorded update remain shared; service validation is tested before this
// fixture call.
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

// teamServiceTestContext creates fresh active team, actor membership and
// owner records for service tests. The supplied role/actor ID is retained
// independently from the fixed owner identity, so ownership permissions
// follow the represented actor role.
func teamServiceTestContext(userID int64, role string) *TeamContext {
	return &TeamContext{
		Team:       &Team{ID: 11, Name: "测试团队", Status: TeamStatusActive},
		Membership: &TeamMembership{ID: 21, TeamID: 11, UserID: userID, Role: role},
		Owner:      &TeamMembership{ID: 20, TeamID: 11, UserID: 1, Role: TeamRoleOwner},
	}
}
