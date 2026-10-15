// YCast (Go edition): a self-hosted replacement for the vTuner internet radio
// service used by many network receivers (Denon, Marantz, Yamaha, Onkyo...).
//
// This is a Go port of YCast by milaq (https://github.com/milaq/YCast),
// licensed under the GNU General Public License v3 or later.
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
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/hgomez/ycast-go/internal/server"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "1.0.0"

func main() {
	var cfg server.Config
	var port int
	var showVersion bool
	flag.StringVar(&cfg.StationsFile, "c", "", "station configuration file (stations.yml)")
	flag.StringVar(&cfg.Listen, "l", "0.0.0.0", "listen address")
	flag.IntVar(&port, "p", 80, "listen port")
	flag.BoolVar(&cfg.Debug, "d", false, "enable debug logging")
	var logJSON bool
	flag.BoolVar(&logJSON, "log-json", false, "log in JSON instead of plain text (for log collectors)")
	flag.BoolVar(&cfg.ProxyHTTPS, "proxy-https", true,
		"relay HTTPS streams through this server (receivers cannot do TLS); when false they are downgraded to HTTP")
	flag.IntVar(&cfg.MaxStations, "max-stations", 500,
		"maximum number of stations listed per country/language/genre (most voted first)")
	flag.StringVar(&cfg.CacheDir, "cache", defaultCacheDir(), `icon cache directory ("none" disables caching)`)
	flag.StringVar(&cfg.RBAPI, "rb-api", "", "Radio Browser API base URL (default: discovered through DNS)")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Println("ycast-go", version)
		return
	}
	if cfg.CacheDir == "none" {
		cfg.CacheDir = ""
	}
	if cfg.MaxStations < 1 {
		cfg.MaxStations = 1
	}
	cfg.Version = version

	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	var log *slog.Logger
	if logJSON {
		log = slog.New(slog.NewJSONHandler(os.Stderr, opts))
	} else {
		log = slog.New(slog.NewTextHandler(os.Stderr, opts))
	}

	// os.Exit lives here, outside of run(): any deferred call inside run()
	// (signal context release) must run before the process exits.
	if err := run(cfg, port, log); err != nil {
		os.Exit(1)
	}
}

// run serves until the first fatal error (returned) or a signal (graceful
// shutdown, nil return).
func run(cfg server.Config, port int, log *slog.Logger) error {
	log.Info("YCast server starting", "version", cfg.Version)

	srv := server.New(cfg, log)
	if cfg.StationsFile != "" {
		if n := len(srv.MyCategories()); n == 0 {
			log.Warn("no usable station configuration, 'My Stations' stays disabled until the file is valid", "file", cfg.StationsFile)
		}
	}

	httpSrv := &http.Server{
		Addr:              net.JoinHostPort(cfg.Listen, strconv.Itoa(port)),
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: relayed streams run for hours.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()

	select {
	case err := <-errCh:
		if err != nil && isPermissionError(err) {
			log.Error("no permission to create socket; ports below 1024 need elevated rights (root, CAP_NET_BIND_SERVICE or -p)")
		} else {
			log.Error("server failed", "err", err)
		}
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			// Deliberate: active streams did not end in time, the sockets are
			// torn down whatever the error.
			_ = httpSrv.Close()
		}
	}
	return nil
}

func isPermissionError(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return errors.Is(opErr.Err, os.ErrPermission) || errors.Is(opErr.Err, syscall.EACCES)
	}
	return false
}

func defaultCacheDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "ycast-go", "icons")
	}
	return filepath.Join(os.TempDir(), "ycast-go", "icons")
}
