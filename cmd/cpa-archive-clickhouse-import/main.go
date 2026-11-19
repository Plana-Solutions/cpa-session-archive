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
	flag.Float64Var(&o.Rate, "rate-limit", 10, "maximum proxy records per second; native events uncapped; 0 unlimited")
	flag.IntVar(&o.BatchBytes, "batch-bytes", 16<<20, "maximum redacted JSON bytes per batch; oversized records run alone")
	flag.Uint64Var(&o.MinFreeBytes, "min-free-bytes", 8<<30, "stop below this ClickHouse disk free space")
	flag.IntVar(&o.DiskCheckBatches, "disk-check-batches", 20, "check disk space and print active parts every N batches")
	flag.Int64Var(&o.FirstID, "first-id", 0, "first inclusive archive ID; exclusive with --newest")
	flag.Int64Var(&o.UpperID, "upper-id", 0, "last inclusive archive ID; 0 captures max ID")
	flag.IntVar(&o.MaxQuarantined, "max-quarantined", 10, "abort when quarantine reaches N; any quarantine exits nonzero")
	flag.BoolVar(&o.RetryQuarantined, "retry-quarantined", false, "retry only quarantined IDs using the same checkpoint")
	verify := flag.Bool("verify-sample", false, "read-only byte-exact and live-extractor gate on bounded throwaway sample")
	flag.BoolVar(&o.DedupFinal, "dedup-final", false, "confirm sink plana.7 body-dedup is enabled permanently before import")
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	run := chimport.Run
	if *verify {
		run = func(ctx context.Context, o chimport.Options) error { return chimport.VerifySample(ctx, o, os.Stdout) }
	}
	if err := run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
