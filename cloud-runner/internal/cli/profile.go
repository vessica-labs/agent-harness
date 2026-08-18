package cli

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type profileFile struct {
	Current  string             `json:"current"`
	Profiles map[string]profile `json:"profiles"`
}

type profile struct {
	URL string `json:"url"`
}

func loadProfile(name string) (string, string, error) {
	if url, token := os.Getenv("AGENT_HARNESS_URL"), os.Getenv("AGENT_HARNESS_TOKEN"); url != "" && token != "" {
		return strings.TrimRight(url, "/"), token, nil
	}
	config, err := readProfiles()
	if err != nil {
		return "", "", err
	}
	if name == "" {
		name = config.Current
	}
	if name == "" {
		name = "default"
	}
	selected, ok := config.Profiles[name]
	if !ok || selected.URL == "" {
		return "", "", errors.New("cloud profile is not configured; run agent-harness cloud profile set")
	}
	token, err := loadSecret(name)
	if err != nil {
		return "", "", err
	}
	return strings.TrimRight(selected.URL, "/"), token, nil
}

func saveProfile(name, url, token string) error {
	if name == "" {
		name = "default"
	}
	config, err := readProfiles()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if config.Profiles == nil {
		config.Profiles = map[string]profile{}
	}
	config.Current = name
	config.Profiles[name] = profile{URL: strings.TrimRight(url, "/")}
	body, _ := json.MarshalIndent(config, "", "  ")
	path, err := configPath("profiles.json")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		return err
	}
	return saveSecret(name, token)
}

func readProfiles() (profileFile, error) {
	path, err := configPath("profiles.json")
	if err != nil {
		return profileFile{}, err
	}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return profileFile{Profiles: map[string]profile{}}, nil
	}
	if err != nil {
		return profileFile{}, err
	}
	var value profileFile
	err = json.Unmarshal(body, &value)
	return value, err
}

func configPath(name string) (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "agent-harness", name), nil
}

func secretService(name string) string { return "agent-harness:" + name }

func saveSecret(name, token string) error {
	if runtime.GOOS == "darwin" {
		command := exec.Command("security", "add-generic-password", "-U", "-a", "default", "-s", secretService(name), "-w", token)
		if err := command.Run(); err == nil {
			return nil
		}
	}
	path, err := configPath("credentials.json")
	if err != nil {
		return err
	}
	values := map[string]string{}
	if body, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(body, &values)
	}
	values[name] = token
	body, _ := json.MarshalIndent(values, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o600)
}

func loadSecret(name string) (string, error) {
	if runtime.GOOS == "darwin" {
		output, err := exec.Command("security", "find-generic-password", "-a", "default", "-s", secretService(name), "-w").Output()
		if err == nil && strings.TrimSpace(string(output)) != "" {
			return strings.TrimSpace(string(output)), nil
		}
	}
	path, err := configPath("credentials.json")
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("cloud profile token is missing; set the profile again")
	}
	values := map[string]string{}
	if json.Unmarshal(body, &values) != nil || values[name] == "" {
		return "", errors.New("cloud profile token is missing; set the profile again")
	}
	return values[name], nil
}
