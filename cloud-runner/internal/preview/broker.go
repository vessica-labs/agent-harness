// Package preview exposes completed-run sandbox applications through the
// control plane behind run-scoped capability links.
package preview

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

const CookieName = "harness_preview"

// ReservedPrefix is a broker-owned subpath under each preview that is never
// proxied to the sandbox application. The overlay panel is served from it.
const ReservedPrefix = "__harness__"

type target struct {
	url    *url.URL
	cancel context.CancelFunc
}

type capability struct {
	runID     string
	expiresAt time.Time
	deadline  time.Time
}

// Broker maps run IDs to loopback sandbox forwards and authorizes requests
// with sliding-expiry capabilities. A cookie preserves root-relative assets
// and websocket requests after the initial tokenized URL.
type Broker struct {
	mu              sync.RWMutex
	targets         map[string]target
	capabilities    map[string]capability
	ttl             time.Duration
	overlayProvider func(string) string
	panelHandler    http.Handler
	onActivity      func(runID string, expiresAt time.Time)
}

func NewBroker(ttl time.Duration) *Broker {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &Broker{targets: map[string]target{}, capabilities: map[string]capability{}, ttl: ttl}
}

func (b *Broker) SetOverlayProvider(provider func(string) string) {
	b.mu.Lock()
	b.overlayProvider = provider
	b.mu.Unlock()
}

func (b *Broker) SetPanelHandler(handler http.Handler) {
	b.mu.Lock()
	b.panelHandler = handler
	b.mu.Unlock()
}

// SetActivityCallback registers a callback invoked whenever a valid request
// extends a capability's sliding expiry.
func (b *Broker) SetActivityCallback(callback func(runID string, expiresAt time.Time)) {
	b.mu.Lock()
	b.onActivity = callback
	b.mu.Unlock()
}

func (b *Broker) Register(runID, targetURL string, cancel context.CancelFunc) error {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return err
	}
	b.mu.Lock()
	if old, ok := b.targets[runID]; ok && old.cancel != nil {
		old.cancel()
	}
	b.targets[runID] = target{url: parsed, cancel: cancel}
	b.mu.Unlock()
	return nil
}

func (b *Broker) Remove(runID string) {
	b.mu.Lock()
	value, ok := b.targets[runID]
	delete(b.targets, runID)
	for token, cap := range b.capabilities {
		if cap.runID == runID {
			delete(b.capabilities, token)
		}
	}
	b.mu.Unlock()
	if ok && value.cancel != nil {
		value.cancel()
	}
}

func (b *Broker) Registered(runID string) bool {
	b.mu.RLock()
	_, ok := b.targets[runID]
	b.mu.RUnlock()
	return ok
}

// Issue mints a run-scoped capability whose sliding expiry starts at the
// broker TTL and can never extend past the supplied hard deadline.
func (b *Broker) Issue(runID string, deadline time.Time) (string, time.Time, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.targets[runID]; !ok {
		return "", time.Time{}, errors.New("preview is not available")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := clampExpiry(time.Now().Add(b.ttl), deadline)
	b.capabilities[token] = capability{runID: runID, expiresAt: expires, deadline: deadline}
	return token, expires, nil
}

// RestoreCapability re-registers a persisted capability after a restart.
func (b *Broker) RestoreCapability(token, runID string, expiresAt, deadline time.Time) error {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(runID) == "" {
		return errors.New("preview capability and run id are required")
	}
	b.mu.Lock()
	b.capabilities[token] = capability{runID: runID, expiresAt: expiresAt, deadline: deadline}
	b.mu.Unlock()
	return nil
}

// touch validates a capability and extends its sliding expiry.
func (b *Broker) touch(token string) (string, bool) {
	now := time.Now()
	b.mu.Lock()
	value, ok := b.capabilities[token]
	if !ok || now.After(value.expiresAt) {
		if ok {
			delete(b.capabilities, token)
		}
		b.mu.Unlock()
		return "", false
	}
	extended := clampExpiry(now.Add(b.ttl), value.deadline)
	if extended.After(value.expiresAt) {
		value.expiresAt = extended
		b.capabilities[token] = value
	}
	callback := b.onActivity
	b.mu.Unlock()
	if callback != nil {
		callback(value.runID, value.expiresAt)
	}
	return value.runID, true
}

func clampExpiry(candidate, deadline time.Time) time.Time {
	if !deadline.IsZero() && candidate.After(deadline) {
		return deadline
	}
	return candidate
}

func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, runID := "", ""
	if strings.HasPrefix(r.URL.Path, "/previews/") {
		rest := strings.TrimPrefix(r.URL.Path, "/previews/")
		parts := strings.SplitN(rest, "/", 2)
		runID = parts[0]
		if query := r.URL.Query().Get("cap"); query != "" {
			token = query
			resolved, valid := b.touch(token)
			if !valid || resolved != runID {
				http.Error(w, "preview authorization is invalid or expired", http.StatusUnauthorized)
				return
			}
			query := r.URL.Query()
			query.Del("cap")
			r.URL.RawQuery = query.Encode()
			secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
			sameSite := http.SameSiteLaxMode
			if secure {
				sameSite = http.SameSiteNoneMode
			}
			http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: sameSite, MaxAge: 3600})
			clean := *r.URL
			clean.RawQuery = query.Encode()
			http.Redirect(w, r, clean.String(), http.StatusSeeOther)
			return
		} else if cookie, err := r.Cookie(CookieName); err == nil {
			token = cookie.Value
			resolved, valid := b.touch(token)
			if !valid || resolved != runID {
				http.Error(w, "preview authorization is invalid or expired", http.StatusUnauthorized)
				return
			}
		} else {
			http.Error(w, "preview authorization is required", http.StatusUnauthorized)
			return
		}
		if len(parts) == 1 || parts[1] == "" {
			r.URL.Path = "/"
		} else {
			r.URL.Path = "/" + parts[1]
		}
	} else if cookie, err := r.Cookie(CookieName); err == nil {
		token = cookie.Value
		runID, _ = b.touch(token)
	}
	if runID == "" {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/"+ReservedPrefix+"/") {
		b.mu.RLock()
		panel := b.panelHandler
		b.mu.RUnlock()
		if panel == nil {
			http.NotFound(w, r)
			return
		}
		r.SetPathValue("run_id", runID)
		panel.ServeHTTP(w, r)
		return
	}
	b.mu.RLock()
	value, ok := b.targets[runID]
	overlayProvider := b.overlayProvider
	b.mu.RUnlock()
	if !ok {
		http.Error(w, "preview is no longer available", http.StatusGone)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(value.url)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		// Dev servers such as Vite reject the public broker host. Present the
		// loopback forward as the upstream host while retaining forwarded headers.
		req.Host = value.url.Host
		req.Header.Del("Accept-Encoding")
	}
	if overlayProvider != nil {
		proxy.ModifyResponse = func(resp *http.Response) error {
			if resp.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") || resp.Header.Get("Content-Encoding") != "" {
				return nil
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			overlay := []byte(overlayProvider(runID))
			lower := bytes.ToLower(body)
			if index := bytes.LastIndex(lower, []byte("</body>")); index >= 0 {
				body = append(append(append([]byte{}, body[:index]...), overlay...), body[index:]...)
			} else {
				body = append(body, overlay...)
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", fmt.Sprint(len(body)))
			resp.Header.Del("ETag")
			return nil
		}
	}
	proxy.ServeHTTP(w, r)
}
