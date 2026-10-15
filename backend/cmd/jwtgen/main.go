package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/app/bootstrap"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// main.go issues a maintenance JWT for a selected database user using the
// persisted application signing secret. The command
// bootstraps the database, selects a user by email or the first active
// administrator, and prints the user's identity with the generated JWT after
// successful signing.

// main executes token generation and reports failures through the logger.
// A failed run exits with status one after run has attempted database cleanup,
// allowing shell callers to detect lookup, bootstrap, or signing failures.
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// run prints an access token for the selected user after bootstrap. A supplied
// email selects that user; otherwise it selects the first active administrator.
// It defers closing the Ent-owned SQL pool after successful bootstrap and logs
// close failures.
func run() error {
	email := flag.String("email", "", "Admin email to issue a JWT for (defaults to first active admin)")
	flag.Parse()

	cfg, err := config.LoadForBootstrap()
	if err != nil {
		return fmt.Errorf("failed to load config: %v", err)
	}

	client, sqlDB, err := bootstrap.InitEnt(context.Background(), cfg)
	if err != nil {
		return fmt.Errorf("failed to init db: %v", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			// Closing occurs after token work has selected its result. Report cleanup
			// failure through logging because this defer cannot replace run's result.
			log.Printf("failed to close db: %v", err)
		}
	}()

	access := bootstrap.NewJWTIdentity(client, sqlDB, cfg)

	// User queries consume this deadline; GenerateToken uses ctx to read session
	// binding data, so the timeout is a lookup budget.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var user *identity.User
	if *email != "" {
		user, err = access.Users.GetByEmail(ctx, *email)
	} else {
		user, err = access.Users.GetFirstAdmin(ctx)
	}
	if err != nil {
		return fmt.Errorf("failed to resolve admin user: %v", err)
	}

	token, err := access.Tokens.GenerateToken(ctx, user)
	if err != nil {
		return fmt.Errorf("failed to generate token: %v", err)
	}

	fmt.Printf("ADMIN_EMAIL=%s\nADMIN_USER_ID=%d\nJWT=%s\n", user.Email, user.ID, token)
	return nil
}
