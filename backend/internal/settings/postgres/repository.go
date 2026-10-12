package postgres

import (
	"context"
	"sort"
	"time"

	"github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/setting"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

type settingRepository struct {
	client *ent.Client
}

func NewSettingRepository(client *ent.Client) settings.Repository {
	return &settingRepository{client: client}
}

func (r *settingRepository) Get(ctx context.Context, key string) (*settings.Setting, error) {
	m, err := r.client.Setting.Query().Where(setting.KeyEQ(key)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, settings.ErrSettingNotFound
		}
		return nil, err
	}
	return &settings.Setting{
		ID:        m.ID,
		Key:       m.Key,
		Value:     m.Value,
		UpdatedAt: m.UpdatedAt,
	}, nil
}

func (r *settingRepository) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *settingRepository) Set(ctx context.Context, key, value string) error {
	now := time.Now()
	return r.client.Setting.
		Create().
		SetKey(key).
		SetValue(value).
		SetUpdatedAt(now).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

func (r *settingRepository) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}
	settings, err := r.client.Setting.Query().Where(setting.KeyIn(keys...)).All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) SetMultiple(ctx context.Context, settings map[string]string) error {
	if len(settings) == 0 {
		return nil
	}

	now := time.Now()
	builders := make([]*ent.SettingCreate, 0, len(settings))
	for key, value := range settings {
		builders = append(builders, r.client.Setting.Create().SetKey(key).SetValue(value).SetUpdatedAt(now))
	}
	return r.client.Setting.
		CreateBulk(builders...).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

func (r *settingRepository) GetAll(ctx context.Context) (map[string]string, error) {
	settings, err := r.client.Setting.Query().All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) Delete(ctx context.Context, key string) error {
	_, err := r.client.Setting.Delete().Where(setting.KeyEQ(key)).Exec(ctx)
	return err
}

// CompareAndSetMultiple locks and compares expected values, then commits all settings in one transaction.
func (r *settingRepository) CompareAndSetMultiple(ctx context.Context, values map[string]string, expected map[string]*string) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	keys := make([]string, 0, len(expected))
	for key := range expected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := tx.Client().ExecContext(ctx, "INSERT INTO settings (key,value,updated_at) VALUES ($1,'',now()) ON CONFLICT (key) DO NOTHING", key); err != nil {
			return err
		}
		current, err := tx.Setting.Query().Where(setting.KeyEQ(key)).ForUpdate().Only(ctx)
		if err != nil {
			return err
		}
		prior := expected[key]
		if prior == nil && current.Value != "" || prior != nil && current.Value != *prior {
			return locale.ErrConflict
		}
	}
	now := time.Now()
	for key, value := range values {
		if err := tx.Setting.Create().SetKey(key).SetValue(value).SetUpdatedAt(now).OnConflictColumns(setting.FieldKey).UpdateNewValues().Exec(ctx); err != nil {
			return err
		}
	}
	return tx.Commit()
}
