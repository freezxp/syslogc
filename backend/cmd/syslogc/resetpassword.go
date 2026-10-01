package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/spf13/pflag"

	"github.com/freezxp/syslogc/backend/internal/app"
	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/metadata"
	"github.com/freezxp/syslogc/backend/internal/metadata/postgres"
)

// resetPassword sets a new password for an account, from the host rather than
// through the web interface.
//
// It is the way back in. The initial password is printed once, to the log, and
// a log rotates — so without this an administrator who loses it is locked out
// of their own server for good, with every log still arriving and no way to
// read them. Running it requires access to the machine and its database,
// which is the same bar as reading the data directly.
func resetPassword(args []string, stdout, stderr io.Writer) int {
	fs := pflag.NewFlagSet("reset-password", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	username := fs.String("username", "admin", "account to set a password for")
	password := fs.String("password", "", "the new password; one is generated when this is empty")
	keepSessions := fs.Bool("keep-sessions", false,
		"leave existing sessions signed in (by default they are ended, since a reset usually means the password was lost or leaked)")
	// The same file the server reads, so running this inside the container
	// needs no arguments at all — which is the state someone is in when they
	// have lost the password.
	configFile := fs.String("config", defaultConfigFile, "configuration file holding the database connection")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	configArgs := []string{}
	if *configFile != "" {
		configArgs = []string{"--config", *configFile}
	}
	cfg, err := loadConfig("reset-password", configArgs, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	dsn, err := app.PostgresDSN(cfg.Metadata.Postgres)
	if err != nil || dsn == "" {
		fmt.Fprintf(stderr, "error: no metadata database is configured, and accounts live in it: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := postgres.Open(ctx, dsn, cfg.Metadata.Postgres.MaxConns,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		fmt.Fprintf(stderr, "error: cannot reach the database: %v\n", err)
		return 1
	}
	defer store.Close()

	user, err := store.UserByUsername(ctx, *username)
	if errors.Is(err, metadata.ErrNotFound) || user == nil {
		fmt.Fprintf(stderr, "error: there is no account named %q\n", *username)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	newPassword := *password
	generated := newPassword == ""
	if generated {
		if newPassword, err = auth.GeneratedPassword(); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}
	if !generated {
		if err := auth.ValidatePasswordPolicy(newPassword, *username); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		// The password rules are the same ones the web interface applies, so
		// the reason reads the same either way.
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	// A generated password must be changed at the next sign-in; one chosen
	// here was chosen deliberately and is left alone.
	if err := store.UpdatePassword(ctx, user.ID, hash, generated); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if !*keepSessions {
		if err := store.DeleteUserSessions(ctx, user.ID, nil); err != nil {
			fmt.Fprintf(stderr, "warning: the password was changed but existing sessions could not be ended: %v\n", err)
		}
	}

	fmt.Fprintf(stdout, "Password set for %q.\n", *username)
	if generated {
		fmt.Fprintf(stdout, "  Password: %s\n  It must be changed at the next sign-in.\n", newPassword)
	}
	if !*keepSessions {
		fmt.Fprintln(stdout, "  Existing sessions were ended.")
	}
	return 0
}
