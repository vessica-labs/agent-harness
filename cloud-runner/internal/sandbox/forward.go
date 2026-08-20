package sandbox

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Forwarder exposes a sandbox application port on a control-plane loopback
// address. Providers that cannot forward ports simply do not implement it.
type Forwarder interface {
	Forward(ctx context.Context, id string, remotePort int) (localURL string, stop func(), err error)
}

const forwardOutputLimit = 64 << 10

// Forward runs Railway's native loopback forward. The Railway CLI handles
// ordinary relay reconnects internally; if that process eventually exits, the
// supervisor restarts it on the same local port with bounded backoff.
func (r RailwayCLI) Forward(ctx context.Context, id string, remotePort int) (string, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("reserve Railway forward port: %w", err)
	}
	localPort := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	forwardCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	output := &boundedTailBuffer{limit: forwardOutputLimit}
	args := []string{"sandbox", "forward", "--project", r.Project, "--environment", r.Environment,
		"--id", id, "--strict", fmt.Sprintf("%d:%d", localPort, remotePort)}
	go r.superviseForward(forwardCtx, done, output, id, args)

	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	localURL := fmt.Sprintf("http://127.0.0.1:%d", localPort)
	if err := waitForHTTP(ctx, localURL, 30*time.Second); err != nil {
		stop()
		return "", nil, fmt.Errorf("railway sandbox forward: %w: %s", err, output.String())
	}
	return localURL, stop, nil
}

func (r RailwayCLI) superviseForward(ctx context.Context, done chan<- struct{}, output *boundedTailBuffer, id string, args []string) {
	defer close(done)
	binary := r.Binary
	if binary == "" {
		binary = "railway"
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = append(os.Environ(), "RAILWAY_API_TOKEN="+r.APIToken,
			"RAILWAY_CALLER=agent-harness-control-plane", "RAILWAY_AGENT_SESSION=agent-harness-control-plane")
		command.Stdout, command.Stderr = output, output
		if err := command.Start(); err == nil {
			_ = command.Wait()
		}
		if ctx.Err() != nil {
			return
		}
		fmt.Fprintf(os.Stderr, "Railway sandbox forward exited for sandbox %s; restarting in %s\n", id, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func waitForHTTP(ctx context.Context, target string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 3 * time.Second}
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("endpoint %s did not answer within %s: %w", strings.TrimSpace(target), timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

type boundedTailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (b *boundedTailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.limit:]...)
	}
	return len(p), nil
}

func (b *boundedTailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
