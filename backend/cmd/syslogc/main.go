// Command syslogc is the Syslogc server.
//
//	syslogc serve       [--config FILE] [--<config.key>=VALUE ...]
//	syslogc config      validate|print [--config FILE]
//	syslogc healthcheck [--url URL]
//	syslogc version
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/freezxp/syslogc/backend/internal/app"
	"github.com/freezxp/syslogc/backend/internal/config"
)

// Set by -ldflags "-X main.version=… -X main.commit=…".
var (
	version = "dev"
	commit  = ""
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "serve":
		return serve(args[1:], stderr)
	case "config":
		return configCmd(args[1:], stdout, stderr)
	case "healthcheck":
		return healthcheck(args[1:], stderr)
	case "init-secrets":
		return initSecrets(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "syslogc %s (commit %s)\n", version, buildCommit())
		return 0
	case "help", "--help", "-h":
		usage(stdout)
		return 0
	}
	fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: syslogc <command> [flags]

Commands:
  serve                 Run the server
  config validate       Validate configuration and exit
  config print          Print the effective configuration as YAML
  healthcheck           Probe a running server's /health endpoint (for container health checks)
  init-secrets          Create missing secrets (database password, DSN, signing key) in a directory
  version               Print version information

Configuration precedence: flags > SYSLOGC_* environment variables > YAML file > defaults.
Any scalar configuration key can be set as a flag, e.g. --storage.victorialogs.insert_url=http://vl:9428
`)
}

func loadConfig(name string, args []string, stderr io.Writer) (*config.Config, error) {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.StringP("config", "c", "", "path to YAML configuration file")
	config.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	return config.Load(config.LoadOptions{File: *file, Flags: fs})
}

func serve(args []string, stderr io.Writer) int {
	cfg, err := loadConfig("serve", args, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "configuration error:\n%v\n", err)
		return 2
	}
	log := app.NewLogger(os.Stderr, cfg.Log, cfg.Node.ID)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	a, err := app.New(ctx, cfg, app.BuildInfo{Version: version, Commit: buildCommit()}, log)
	if err != nil {
		log.Error("startup failed", "error", err)
		return 1
	}
	go func() {
		<-ctx.Done()
		stop() // a second signal terminates immediately
	}()
	if err := a.Run(ctx, nil); err != nil {
		log.Error("syslogc stopped with error", "error", err)
		return 1
	}
	return 0
}

func configCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "validate" && args[0] != "print") {
		fmt.Fprintln(stderr, "usage: syslogc config validate|print [--config FILE]")
		return 2
	}
	cfg, err := loadConfig("config", args[1:], stderr)
	if err != nil {
		fmt.Fprintf(stderr, "configuration error:\n%v\n", err)
		return 1
	}
	if args[0] == "validate" {
		fmt.Fprintln(stdout, "configuration is valid")
		return 0
	}
	out, err := cfg.YAML()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_, _ = stdout.Write(out)
	return 0
}

func healthcheck(args []string, stderr io.Writer) int {
	fs := pflag.NewFlagSet("healthcheck", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", "http://127.0.0.1:8080/health", "health endpoint URL")
	timeout := fs.Duration("timeout", 3*time.Second, "request timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	client := &http.Client{Timeout: *timeout}
	resp, err := client.Get(*url)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "unhealthy: HTTP %d\n", resp.StatusCode)
		return 1
	}
	return 0
}

func buildCommit() string {
	if commit != "" {
		return commit
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				return s.Value[:12]
			}
		}
	}
	return "unknown"
}

// initSecrets writes random secrets for the Docker Compose stack if they do
// not exist yet. It never overwrites existing files.
func initSecrets(args []string, stdout, stderr io.Writer) int {
	fs := pflag.NewFlagSet("init-secrets", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "/secrets", "directory to write secrets to")
	dbHost := fs.String("db-host", "postgres:5432", "PostgreSQL host:port for the generated DSN")
	dbName := fs.String("db-name", "syslogc", "database and user name")
	dsnName := fs.String("dsn-name", "postgres_dsn", "file name for the generated DSN")
	owner := fs.String("owner", "", "uid:gid to own the private secrets (for a non-root server container)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	uid, gid := -1, -1
	if *owner != "" {
		u, g, ok := strings.Cut(*owner, ":")
		var err1, err2 error
		uid, err1 = strconv.Atoi(u)
		gid, err2 = strconv.Atoi(g)
		if !ok || err1 != nil || err2 != nil {
			fmt.Fprintln(stderr, "--owner must be uid:gid")
			return 2
		}
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil { //nolint:gosec // postgres container must traverse it
		fmt.Fprintln(stderr, err)
		return 1
	}
	random := func(n int) string {
		b := make([]byte, n)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		return hex.EncodeToString(b)
	}
	passwordPath := filepath.Join(*dir, "postgres_password")
	password := ""
	if data, err := os.ReadFile(passwordPath); err == nil { //nolint:gosec // operator-provided directory
		password = strings.TrimSpace(string(data))
	} else {
		password = random(24)
		// Readable by the postgres container user (volume is private to the stack).
		if err := os.WriteFile(passwordPath, []byte(password+"\n"), 0o644); err != nil { //nolint:gosec // shared with postgres container
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "created", passwordPath)
	}
	files := map[string]string{
		*dsnName:     fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable\n", *dbName, password, *dbHost, *dbName),
		"secret_key": random(32) + "\n",
	}
	for name, content := range files {
		p := filepath.Join(*dir, name)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if uid >= 0 {
			if err := os.Chown(p, uid, gid); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		}
		fmt.Fprintln(stdout, "created", p)
	}
	return 0
}
