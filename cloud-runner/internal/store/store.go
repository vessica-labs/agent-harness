package store

import (
	"context"
	"errors"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrNoRunnableRun = errors.New("no runnable run")
	ErrNoAuthSlot    = errors.New("no authentication slot available")
	ErrConflict      = errors.New("conflict")
)

type Store interface {
	Ping(context.Context) error
	Close()
	Migrate(context.Context) error

	PutRepository(context.Context, model.Repository) (model.Repository, error)
	ListRepositories(context.Context) ([]model.Repository, error)
	GetRepository(context.Context, string) (model.Repository, error)
	DisableRepository(context.Context, string) error
	FindLinearRepository(context.Context, string, string, string) (model.Repository, error)

	AcceptLinearDelivery(context.Context, model.Repository, model.LinearDelivery) (model.DeliveryResult, error)
	RecordIgnoredLinearDelivery(context.Context, model.LinearDelivery, string, string) (bool, error)
	ClaimNextRun(context.Context, string, int, time.Duration) (model.Run, error)
	GetRun(context.Context, string) (model.Run, error)
	ListRuns(context.Context, model.RunFilter) ([]model.Run, error)
	SetRunState(context.Context, string, string, string, string) error
	RequeueRun(context.Context, string, string) error
	SetSandbox(context.Context, string, string, string) error
	SetAuthSlot(context.Context, string, string) error
	SetDelivery(context.Context, string, string, string) error
	Heartbeat(context.Context, string, string, time.Duration) error
	UpdateRunInput(context.Context, string, string) error
	ResumeRun(context.Context, string) error
	CancelRun(context.Context, string) error
	PutStage(context.Context, model.StageState) error
	ListStages(context.Context, string) ([]model.StageState, error)
	PutTicket(context.Context, model.TicketState) error
	ListTickets(context.Context, string) ([]model.TicketState, error)

	AppendEvent(context.Context, model.Event) (model.Event, error)
	ListEvents(context.Context, model.EventFilter) ([]model.Event, error)

	PutArtifact(context.Context, model.Artifact) error
	GetArtifact(context.Context, string, string) (model.Artifact, error)
	ListArtifacts(context.Context, string) ([]model.Artifact, error)
	GetExternalSync(context.Context, string, string, string) (model.ExternalSync, error)
	PutExternalSync(context.Context, model.ExternalSync) error

	PutCredential(context.Context, model.Credential) error
	GetCredential(context.Context, string) (model.Credential, error)
	PutAuthSlot(context.Context, model.AuthSlot) error
	ListAuthSlots(context.Context) ([]model.AuthSlot, error)
	LeaseAuthSlots(context.Context, string, int, time.Duration) ([]model.AuthSlot, error)
	ReleaseAuthSlot(context.Context, string, string, []byte, string) error
	QuarantineAuthSlot(context.Context, string, string, string) error
}
