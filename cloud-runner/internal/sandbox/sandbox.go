package sandbox

import "context"

type CreateSpec struct {
	Checkpoint         string
	IdleTimeoutMinutes int
	Variables          map[string]string
}

type Instance struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Session string `json:"session,omitempty"`
}

type Provider interface {
	Create(context.Context, CreateSpec) (Instance, error)
	StartWorker(context.Context, string) (string, error)
	Heartbeat(context.Context, string) error
	Status(context.Context, string) (Instance, error)
	Destroy(context.Context, string) error
}
