package provider

import (
	"context"
	"slices"
)

// ModelSyncService tracks request-triggered model queries through an injected fetch function.
type ModelSyncService struct {
	fetch    func(context.Context, *Record) ([]string, error)
	activity operationActivity
}

func NewModelSyncService(fetch func(context.Context, *Record) ([]string, error)) *ModelSyncService {
	if fetch == nil {
		return nil
	}
	return &ModelSyncService{fetch: fetch}
}

func (s *ModelSyncService) Fetch(ctx context.Context, v *Record) ([]string, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrRefreshStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	models, err := s.fetch(ctx, CloneRecord(v))
	return slices.Clone(models), err
}

func (s *ModelSyncService) StopContext(ctx context.Context) error {
	return s.activity.stop(ctx, "provider model list requests")
}
