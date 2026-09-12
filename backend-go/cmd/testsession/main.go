// testsession — mints a studio session for an existing user (E2E/testing
// helper). Prints {token, csrf, user} as JSON on stdout.
//
// The token is exactly what the browser holds in the studio_session
// cookie; Redis keeps only its SHA-256. Sessions created here are real
// sessions and can be revoked via logout.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/creation-agent-studio/backend-go/internal/app"
	"github.com/creation-agent-studio/backend-go/internal/identity"
	"github.com/creation-agent-studio/backend-go/internal/platform/crypto"
)

func main() {
	username := flag.String("user", "", "username to mint a session for")
	flag.Parse()
	if *username == "" {
		fmt.Fprintln(os.Stderr, "-user is required")
		os.Exit(1)
	}
	cfg, err := app.LoadConfig()
	if err != nil {
		fatal(err)
	}
	a, err := app.Build(context.Background(), cfg)
	if err != nil {
		fatal(err)
	}
	defer a.Close()

	user, ident, err := a.IdentityRepo.UserWithIdentity(context.Background(), byUsername(a, *username))
	if err != nil {
		fatal(fmt.Errorf("user %q: %w", *username, err))
	}
	token, csrf, err := a.Sessions.Create(context.Background(), identity.Session{
		UserID:     user.ID,
		Username:   user.Username,
		AuthSource: user.AuthSource,
		IsStaff:    user.IsStaff,
	})
	if err != nil {
		fatal(err)
	}
	payload := identity.SessionPayload(user, ident)
	out, _ := json.Marshal(map[string]any{
		"token":       token,
		"csrf":        csrf,
		"cookie_name": a.Sessions.CookieName(),
		"csrf_name":   a.Sessions.CSRFName(),
		"user":        payload,
		"token_hash":  crypto.HashToken(token),
	})
	fmt.Println(string(out))
}

func byUsername(a *app.App, username string) int64 {
	user, err := a.IdentityRepo.UserByUsername(context.Background(), username)
	if err != nil {
		fatal(err)
	}
	return user.ID
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "testsession:", err)
	os.Exit(1)
}
