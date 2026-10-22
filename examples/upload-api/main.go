package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/parmayadav/gobulkin/pkg/loader"
)

const (
	defaultAddr       = "127.0.0.1:8080"
	minAPIKeyLength   = 16
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 60 * time.Second
	writeTimeout      = loader.DefaultTimeout + 10*time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 10 * time.Second
)

type config struct {
	addr   string
	apiKey string
	dsn    string
}

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	pool, err := openDatabase(context.Background(), cfg.dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	customers, err := newCustomerLoader()
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr: cfg.addr,
		Handler: newRouter(routerConfig{
			loader:               customers,
			db:                   pool,
			apiKey:               cfg.apiKey,
			logger:               logger,
			maxConcurrentImports: defaultMaxImports,
		}),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.addr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func loadConfig() (config, error) {
	apiKey := os.Getenv("UPLOAD_API_KEY")
	if len(apiKey) < minAPIKeyLength {
		return config{}, fmt.Errorf("UPLOAD_API_KEY must be set and at least %d characters", minAPIKeyLength)
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return config{}, errors.New("DATABASE_URL must be set")
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	return config{addr: addr, apiKey: apiKey, dsn: dsn}, nil
}
