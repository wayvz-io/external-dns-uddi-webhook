// Package server runs the ExternalDNS webhook API and a health and metrics
// listener.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"sigs.k8s.io/external-dns/provider"
	"sigs.k8s.io/external-dns/provider/webhook/api"
)

// Config configures the listeners.
type Config struct {
	// ServerAddr is the webhook API listen address, for example "localhost:8888".
	ServerAddr string
	// HealthzAddr is the health and metrics listen address, for example "0.0.0.0:8080".
	HealthzAddr string
	// ReadTimeout and WriteTimeout are passed to the webhook API server.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// ShutdownTimeout bounds graceful shutdown (default 10s).
	ShutdownTimeout time.Duration
	Logger          *slog.Logger
}

// Server owns the health listener and the readiness flag.
type Server struct {
	cfg   Config
	prov  provider.Provider
	log   *slog.Logger
	ready atomic.Bool

	health   *http.Server
	healthLn net.Listener
	errCh    chan error
}

// New creates a Server. Call Start to begin serving.
func New(cfg Config, p provider.Provider) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 10 * time.Second
	}
	s := &Server{cfg: cfg, prov: p, log: cfg.Logger.With("component", "server"), errCh: make(chan error, 1)}
	s.health = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
	}
	return s
}

// Handler returns the health and metrics handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/readyz", s.healthz)
	mux.Handle("/metrics", promhttp.Handler())
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !s.ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("webhook api not started\n"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// Ready reports whether the webhook API has started listening.
func (s *Server) Ready() bool { return s.ready.Load() }

// HealthzAddr returns the bound health listener address (nil before Start).
func (s *Server) HealthzAddr() net.Addr {
	if s.healthLn == nil {
		return nil
	}
	return s.healthLn.Addr()
}

// Start binds the health listener and starts the webhook API. It returns after
// binding the health listener. The server becomes ready when the API starts.
func (s *Server) Start(ctx context.Context) error {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", s.cfg.HealthzAddr)
	if err != nil {
		return fmt.Errorf("listen healthz %s: %w", s.cfg.HealthzAddr, err)
	}
	s.healthLn = ln
	s.log.Info("health listener bound", "addr", ln.Addr().String())

	go func() {
		if err := s.health.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.errCh <- fmt.Errorf("health server: %w", err)
		}
	}()

	started := make(chan struct{}, 1)
	go func() {
		select {
		case <-started:
			s.ready.Store(true)
			s.log.Info("webhook api listening", "addr", s.cfg.ServerAddr)
		case <-ctx.Done():
		}
	}()
	// StartHTTPApi blocks and calls log.Fatal if it cannot bind.
	go api.StartHTTPApi(s.prov, started, s.cfg.ReadTimeout, s.cfg.WriteTimeout, s.cfg.ServerAddr)
	return nil
}

// Err returns a channel that receives a fatal listener error, if any.
func (s *Server) Err() <-chan error { return s.errCh }

// Shutdown stops the health listener gracefully and marks the server not ready.
func (s *Server) Shutdown(ctx context.Context) error {
	s.ready.Store(false)
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ShutdownTimeout)
	defer cancel()
	if err := s.health.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown health server: %w", err)
	}
	return nil
}

// Run serves until ctx is canceled or the process receives SIGINT or SIGTERM.
func Run(ctx context.Context, cfg Config, p provider.Provider) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	s := New(cfg, p)
	if err := s.Start(ctx); err != nil {
		return err
	}
	var runErr error
	select {
	case <-ctx.Done():
		s.log.Info("shutdown requested")
	case runErr = <-s.Err():
		s.log.Error("listener failed", "error", runErr)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		return errors.Join(runErr, err)
	}
	return runErr
}
