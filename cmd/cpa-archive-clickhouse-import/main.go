package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"cpa-session-archive/internal/chimport"
)

func main() {
	var o chimport.Options
	flag.StringVar(&o.SQLite, "sqlite", "", "archive SQLite file (opened mode=ro)")
	flag.StringVar(&o.State, "state", "archive-clickhouse-import.json", "durable checkpoint path")
	flag.StringVar(&o.Config, "config", "/data/compose/3/config.yaml", "CPA redaction config")
	flag.StringVar(&o.AuthDir, "auth-dir", "/var/lib/docker/volumes/cli-proxy-api_cpa-auth/_data", "CPA redaction auth directory")
	flag.IntVar(&o.Batch, "batch-size", 16, "records per batch (1..128)")
	flag.IntVar(&o.Newest, "newest", 0, "sample newest N proxy records; 0 imports full snapshot")
	flag.IntVar(&o.ExternalSessions, "external-sessions", 0, "first N native sessions; 0 all, -1 none")
	flag.Float64Var(&o.Rate, "rate-limit", 10, "maximum records per second; 0 unlimited")
	flag.Parse()
	o.URL = os.Getenv("CLICKHOUSE_URL")
	o.User = os.Getenv("CLICKHOUSE_USER")
	o.Log = os.Stderr
	if path := os.Getenv("CLICKHOUSE_PASSWORD_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ClickHouse password file unreadable")
			os.Exit(1)
		}
		o.Password = strings.TrimRight(string(b), "\r\n")
	} else {
		fmt.Fprintln(os.Stderr, "CLICKHOUSE_PASSWORD_FILE is required")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := chimport.Run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
