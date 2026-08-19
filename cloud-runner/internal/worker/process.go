package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func runCommand(ctx context.Context, cwd string, env []string, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = cwd
	if env != nil {
		command.Env = env
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if stdoutMessage := strings.TrimSpace(stdout.String()); stdoutMessage != "" {
			if message != "" {
				message += "\n"
			}
			message += stdoutMessage
		}
		if len(message) > 4000 {
			message = message[len(message)-4000:]
		}
		return stdout.Bytes(), fmt.Errorf("%s failed: %w: %s", name, err, message)
	}
	return stdout.Bytes(), nil
}

func gitEnvironment(token string) []string {
	env := sanitizedEnvironment("")
	if token == "" {
		return env
	}
	authorization := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	return append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_0="+authorization)
}

func sanitizedEnvironment(codexHome string) []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "LANG": true, "LC_ALL": true,
		"TERM": true, "TMPDIR": true, "TMP": true, "TEMP": true, "SHELL": true, "CI": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
		"PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH": true, "PLAYWRIGHT_BROWSERS_PATH": true,
		"HARNESS_PLAYWRIGHT_WORKERS": true, "PLAYWRIGHT_WORKERS": true,
	}
	var result []string
	for _, pair := range os.Environ() {
		key, _, _ := strings.Cut(pair, "=")
		if allowed[key] {
			result = append(result, pair)
		}
	}
	if codexHome != "" {
		result = append(result, "CODEX_HOME="+codexHome)
	}
	return result
}
