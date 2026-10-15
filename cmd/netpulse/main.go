// Command netpulse runs the NetPulse internet weather station: it probes DNS,
// HTTP and TCP targets on a schedule, stores results in SQLite and serves a
// REST API, a WebSocket feed and a live dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/avkad3/netpulse/internal/api"
	"github.com/avkad3/netpulse/internal/config"
	"github.com/avkad3/netpulse/internal/hub"
	"github.com/avkad3/netpulse/internal/probe"
	"github.com/avkad3/netpulse/internal/scheduler"
	"github.com/avkad3/netpulse/internal/store"
	"github.com/avkad3/netpulse/web"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "netpulse:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		cfgPath = flag.String("config", os.Getenv("NETPULSE_CONFIG"), "path to JSON config (default: built-in targets)")
		listen  = flag.String("listen", os.Getenv("NETPULSE_LISTEN"), "listen address, overrides config")
		dbPath  = flag.String("db", os.Getenv("NETPULSE_DB"), "SQLite database path, overrides config")
		debug   = flag.Bool("debug", false, "verbose logging")
		showVer = flag.Bool("version", false, "print version and exit")
		health  = flag.Bool("healthcheck", false, "query a running instance's /api/health and exit (for containers)")
	)
	flag.Parse()
	if *showVer {
		fmt.Println("netpulse", version)
		return nil
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *dbPath != "" {
		cfg.DBPath = *dbPath
	}
	if *health {
		return healthcheck(cfg.Listen)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// The scheduler needs the API server's result handler and the server
	// needs the scheduler; srv is assigned before the scheduler starts.
	var srv *api.Server
	sched, err := scheduler.New(cfg.Targets, cfg.Workers, func(r probe.Result) { srv.HandleResult(r) })
	if err != nil {
		return err
	}
	srv = api.New(st, sched, hub.New(log), web.Static(), log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("netpulse listening", "addr", cfg.Listen, "targets", len(cfg.Targets),
			"workers", cfg.Workers, "db", cfg.DBPath, "version", version)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	schedDone := make(chan struct{})
	go func() {
		sched.Run(ctx)
		close(schedDone)
	}()
	go prune(ctx, st, cfg.Retention.Duration, log)

	select {
	case err := <-errc:
		stop()
		<-schedDone
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown", "err", err)
	}
	<-schedDone
	return nil
}

// healthcheck exits non-zero unless the local instance reports healthy.
func healthcheck(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	res, err := client.Get("http://" + net.JoinHostPort(host, port) + "/api/health")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: HTTP %d", res.StatusCode)
	}
	return nil
}

// prune deletes results older than the retention period once an hour.
func prune(ctx context.Context, st *store.Store, retention time.Duration, log *slog.Logger) {
	if retention <= 0 {
		return
	}
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		n, err := st.Prune(ctx, time.Now().Add(-retention))
		if err != nil && ctx.Err() == nil {
			log.Error("prune", "err", err)
		} else if n > 0 {
			log.Info("pruned old results", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
