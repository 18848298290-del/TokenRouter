package bootstrap

import (
	"database/sql"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
)

// JWTIdentity provides user lookup and access-token signing for maintenance commands.
type JWTIdentity struct {
	Users  *identitypostgres.UserStore
	Tokens *identity.SessionService
}

// NewJWTIdentity uses a bootstrapped database. The calling command closes the connection before returning.
func NewJWTIdentity(client *dbent.Client, db *sql.DB, cfg *config.Config) JWTIdentity {
	users := identitypostgres.NewUserStore(client, db)
	options := identity.SessionOptions{Now: time.Now, Secret: cfg.JWT.Secret, ExpireHour: cfg.JWT.ExpireHour, AccessTokenExpireMinutes: cfg.JWT.AccessTokenExpireMinutes, RefreshTokenExpireDays: cfg.JWT.RefreshTokenExpireDays}
	return JWTIdentity{Users: users, Tokens: identity.NewSessionService(options, users, nil, nil, nil)}
}
