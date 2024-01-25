package migration

import (
	"context"
	"encoding/json"

	"github.com/zitadel/zitadel/internal/api/authz"
	"github.com/zitadel/zitadel/internal/api/service"
	"github.com/zitadel/zitadel/internal/errors"
	"github.com/zitadel/zitadel/internal/eventstore"
)

func init() {
	eventstore.RegisterFilterEventMapper(SystemAggregate, StartedType, SetupMapper)
	eventstore.RegisterFilterEventMapper(SystemAggregate, DoneType, SetupMapper)
	eventstore.RegisterFilterEventMapper(SystemAggregate, failedType, SetupMapper)
	eventstore.RegisterFilterEventMapper(SystemAggregate, repeatableDoneType, SetupMapper)
}

// SetupStep is the command pushed on the eventstore
type SetupStep struct {
	eventstore.BaseEvent `json:"-"`
	migration            Migration
	Name                 string      `json:"name"`
	Error                error       `json:"error,omitempty"`
	LastRun              interface{} `json:"lastRun,omitempty"`
}

func (s *SetupStep) UnmarshalJSON(data []byte) error {
	fields := struct {
		Name    string                 `json:"name,"`
		Error   *errors.CaosError      `json:"error"`
		LastRun map[string]interface{} `json:"lastRun,omitempty"`
	}{}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	s.Name = fields.Name
	s.Error = fields.Error
	s.LastRun = fields.LastRun
	return nil
}

func setupStartedCmd(ctx context.Context, migration Migration) eventstore.Command {
	ctx = authz.SetCtxData(service.WithService(ctx, "system"), authz.CtxData{UserID: "system", OrgID: "SYSTEM", ResourceOwner: "SYSTEM"})
	return &SetupStep{
		BaseEvent: *eventstore.NewBaseEventForPush(
			ctx,
			eventstore.NewAggregate(ctx, SystemAggregateID, SystemAggregate, "v1"),
			StartedType),
		migration: migration,
		Name:      migration.String(),
	}
}

func setupDoneCmd(ctx context.Context, migration Migration, err error) eventstore.Command {
	ctx = authz.SetCtxData(service.WithService(ctx, "system"), authz.CtxData{UserID: "system", OrgID: "SYSTEM", ResourceOwner: "SYSTEM"})
	typ := DoneType
	var lastRun interface{}
	if repeatable, ok := migration.(RepeatableMigration); ok {
		typ = repeatableDoneType
		lastRun = repeatable
	}
	if err != nil {
		typ = failedType
	}

	s := &SetupStep{
		migration: migration,
		Name:      migration.String(),
		Error:     err,
		LastRun:   lastRun,
	}

	s.BaseEvent = *eventstore.NewBaseEventForPush(
		ctx,
		eventstore.NewAggregate(ctx, SystemAggregateID, SystemAggregate, "v1"),
		typ)

	return s
}

func (s *SetupStep) Payload() interface{} {
	return s
}

func (s *SetupStep) UniqueConstraints() []*eventstore.UniqueConstraint {
	switch s.Type() {
	case StartedType:
		return []*eventstore.UniqueConstraint{
			eventstore.NewAddGlobalUniqueConstraint("migration_started", s.migration.String(), "Errors.Step.Started.AlreadyExists"),
		}
	case failedType,
		repeatableDoneType:
		return []*eventstore.UniqueConstraint{
			eventstore.NewRemoveGlobalUniqueConstraint("migration_started", s.migration.String()),
		}
	default:
		return []*eventstore.UniqueConstraint{
			eventstore.NewAddGlobalUniqueConstraint("migration_done", s.migration.String(), "Errors.Step.Done.AlreadyExists"),
		}
	}
}

func SetupMapper(event eventstore.Event) (eventstore.Event, error) {
	step := &SetupStep{
		BaseEvent: *eventstore.BaseEventFromRepo(event),
	}
	err := event.Unmarshal(step)
	if err != nil {
		return nil, errors.ThrowInternal(err, "IAM-hYp7M", "unable to unmarshal step")
	}

	return step, nil
}
