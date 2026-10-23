package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/spf13/pflag"

	"github.com/freezxp/syslogc/backend/internal/app"
	"github.com/freezxp/syslogc/backend/internal/metadata/postgres"
	"github.com/freezxp/syslogc/backend/internal/servicetrends"
)

// resetTrends clears what the service-trend rollup has remembered.
//
// Two things are remembered, and either can be wrong in a way the rollup
// cannot work out for itself.
//
// The learned query sizes are what this deployment's storage turned out to be
// able to answer. They are discovered by asking for too much and being
// refused, which is slow, so they are kept — but a bad run can teach the
// wrong lesson, and then every later run starts from it. Clearing them makes
// the rollup measure again.
//
// The recorded state is how far each window has been counted. Clearing it
// makes the rollup fill in from its backfill, trimmed to the retention period
// — so nothing is lost that still exists, and counts that are recorded twice
// replace themselves rather than adding up.
func resetTrends(args []string, stdout, stderr io.Writer) int {
	fs := pflag.NewFlagSet("reset-trends", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	sizesOnly := fs.Bool("sizes", false,
		"clear only the learned query sizes, keeping how far each window has been recorded")
	stateOnly := fs.Bool("state", false,
		"clear only how far each window has been recorded, keeping the learned query sizes")
	configFile := fs.String("config", defaultConfigFile, "configuration file holding the database connection")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *sizesOnly && *stateOnly {
		fmt.Fprintln(stderr, "error: --sizes and --state together is the default; give neither to clear both")
		return 2
	}
	clearSizes, clearState := !*stateOnly, !*sizesOnly

	configArgs := []string{}
	if *configFile != "" {
		configArgs = []string{"--config", *configFile}
	}
	cfg, err := loadConfig("reset-trends", configArgs, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	dsn, err := app.PostgresDSN(cfg.Metadata.Postgres)
	if err != nil || dsn == "" {
		fmt.Fprintf(stderr, "error: no metadata database is configured, and the rollup keeps its state in it: %v\n", err)
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

	// Reported before clearing, so what was thrown away is on the record.
	if sizes, err := servicetrends.LoadSizes(ctx, store); err == nil {
		if len(sizes.Buckets) == 0 && len(sizes.Chunks) == 0 {
			fmt.Fprintln(stdout, "no query sizes had been learned")
		} else {
			fmt.Fprintf(stdout, "learned query sizes: buckets %v, services %v\n", sizes.Buckets, sizes.Chunks)
		}
	}
	if state, err := servicetrends.LoadState(ctx, store); err == nil && len(state) > 0 {
		for window, at := range state {
			fmt.Fprintf(stdout, "%s recorded up to %s\n", window, at.UTC().Format(time.RFC3339))
		}
	}

	if clearSizes {
		if err := servicetrends.SaveSizes(ctx, store, servicetrends.Sizes{}); err != nil {
			fmt.Fprintf(stderr, "error: clearing the learned query sizes: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "cleared the learned query sizes; the rollup will measure again")
	}
	if clearState {
		if err := servicetrends.Reset(ctx, store); err != nil {
			fmt.Fprintf(stderr, "error: clearing the recorded state: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "cleared the recorded state; the rollup will fill in from its backfill")
	}
	fmt.Fprintln(stdout, "restart Syslogc for this to take effect: docker compose restart syslogc")
	return 0
}
