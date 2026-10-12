package app

import (
	"context"
	"database/sql"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/site"
	"github.com/TokenFlux/TokenRouter/internal/team"
	teampostgres "github.com/TokenFlux/TokenRouter/internal/team/postgres"
	usagepostgres "github.com/TokenFlux/TokenRouter/internal/usage/postgres"
)

type teamIdentityUsers struct{ Users identity.UserRepository }

// provideTeamRepository shares the SQL pool across membership, key lifecycle, and member usage operations.
func provideTeamRepository(db *sql.DB, calendar timezone.Calendar, reports *usagepostgres.Store) team.TeamRepository {
	return teampostgres.NewTeamRepositoryWithReports(db, keypostgres.NewTeamKeys(db), billingpostgres.NewMemberUsageStore(db, &calendar), calendar, reports)
}

func (p teamIdentityUsers) GetByID(ctx context.Context, id int64) (*team.UserSnapshot, error) {
	u, e := p.Users.GetByID(ctx, id)
	if u == nil {
		return nil, e
	}
	return &team.UserSnapshot{ID: u.ID, Email: u.Email, Username: u.Username}, e
}

func (p teamIdentityUsers) GetByEmail(ctx context.Context, email string) (*team.UserSnapshot, error) {
	u, e := p.Users.GetByEmail(ctx, email)
	if u == nil {
		return nil, e
	}
	return &team.UserSnapshot{ID: u.ID, Email: u.Email, Username: u.Username}, e
}

func provideTeam(repo team.TeamRepository, users *identitypostgres.UserStore, email *notification.Mailer, cache apikey.APIKeyCache, limiter team.TeamInvitationLimiter, settings *site.DisplaySettings, cfg *config.Config) *team.TeamService {
	var options *team.Options
	if cfg != nil {
		options = &team.Options{Enabled: cfg.Team.Enabled, SelfServiceEnabled: cfg.Team.SelfServiceEnabled, DefaultMemberLimit: cfg.Team.DefaultMemberLimit, FrontendURL: cfg.Server.FrontendURL, Now: time.Now}
	}
	var settingReader team.Settings
	if settings != nil {
		settingReader = settings
	}
	var invalidator team.KeyInvalidator
	if cache != nil {
		invalidator = apikey.PublishedAuthCacheInvalidator{Cache: cache}
	}
	return team.NewTeamService(repo, teamIdentityUsers{Users: users}, team.NewEmailNotifications(email, teamIdentityUsers{Users: users}), invalidator, limiter, settingReader, options)
}
