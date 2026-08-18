package ui

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

//go:embed index.html
var indexHTML []byte

type Server struct {
	http   *http.Server
	logger *slog.Logger
}

func New(address, cloudURL, token string, logger *slog.Logger) (*Server, error) {
	target, err := url.Parse(strings.TrimRight(cloudURL, "/"))
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, errors.New("a valid cloud control-plane URL is required")
	}
	if token == "" {
		return nil, errors.New("a cloud management token is required")
	}
	if address == "" {
		address = "127.0.0.1:7373"
	}
	if !strings.HasPrefix(address, "127.0.0.1:") && !strings.HasPrefix(address, "localhost:") && !strings.HasPrefix(address, "[::1]:") {
		return nil, errors.New("local UI must bind to a loopback address")
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		incomingPath := request.URL.Path
		originalDirector(request)
		request.URL.Path = strings.TrimPrefix(incomingPath, "/api")
		if incomingPath == "/events" {
			request.URL.Path = "/v1/events"
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Del("Cookie")
	}
	proxy.FlushInterval = -1
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
		w.Write(indexHTML)
	})
	mux.Handle("GET /api/", proxy)
	mux.Handle("GET /events", proxy)
	return &Server{logger: logger, http: &http.Server{Addr: address, Handler: mux,
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 75 * time.Second}}, nil
}

func (s *Server) ListenAndServe() error {
	s.logger.Info("local runner UI listening", "url", "http://"+s.http.Addr)
	err := s.http.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve local UI: %w", err)
}

func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func (s *Server) Handler() http.Handler { return s.http.Handler }
