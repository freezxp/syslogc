// Command syslogc is the Syslogc server.
//
//	syslogc serve       [--config FILE] [--<config.key>=VALUE ...]
//	syslogc config      validate|print [--config FILE]
//	syslogc healthcheck [--url URL]
//	syslogc version
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
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

	a, err := app.New(cfg, app.BuildInfo{Version: version, Commit: buildCommit()}, log)
	if err != nil {
		log.Error("startup failed", "error", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
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
