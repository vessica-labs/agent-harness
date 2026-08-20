package server

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Address               string
	PublicURL             string
	ManagementToken       string
	LinearWebhookSecret   string
	MaxRequestBytes       int64
	MaxJournalBytes       int64
	WebhookTolerance      time.Duration
	InternalLeaseDuration time.Duration
	PreviewPublicURL      string
	PreviewEdgeToken      string
	PreviewTTL            time.Duration
	PreviewMaxAge         time.Duration
}

func ConfigFromEnv() (Config, error) {
	port := env("PORT", "8080")
	config := Config{
		Address:               env("HARNESS_LISTEN_ADDRESS", ":"+port),
		PublicURL:             strings.TrimRight(os.Getenv("HARNESS_PUBLIC_URL"), "/"),
		ManagementToken:       os.Getenv("HARNESS_MANAGEMENT_TOKEN"),
		LinearWebhookSecret:   os.Getenv("LINEAR_WEBHOOK_SECRET"),
		MaxRequestBytes:       int64(envInt("HARNESS_MAX_REQUEST_BYTES", 4<<20)),
		MaxJournalBytes:       int64(envInt("HARNESS_MAX_JOURNAL_BYTES", 100<<20)),
		WebhookTolerance:      time.Minute,
		InternalLeaseDuration: 15 * time.Minute,
		PreviewPublicURL:      strings.TrimRight(os.Getenv("HARNESS_PREVIEW_PUBLIC_URL"), "/"),
		PreviewEdgeToken:      os.Getenv("HARNESS_PREVIEW_EDGE_TOKEN"),
		PreviewTTL:            envDuration("HARNESS_PREVIEW_TTL", time.Hour),
		PreviewMaxAge:         envDuration("HARNESS_PREVIEW_MAX_AGE", 4*time.Hour),
	}
	if config.ManagementToken == "" {
		return config, fmt.Errorf("HARNESS_MANAGEMENT_TOKEN is required")
	}
	return config, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
