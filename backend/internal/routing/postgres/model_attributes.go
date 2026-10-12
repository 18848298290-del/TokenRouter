package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lib/pq"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

const attributeSelect = `SELECT c.id,c.name,c.description,c.status,c.rules,c.created_at,c.updated_at,
 COALESCE((SELECT array_agg(g.group_id ORDER BY g.group_id) FROM model_attribute_config_groups g WHERE g.config_id=c.id),'{}'::bigint[])
 FROM model_attribute_configs c `

// ModelAttributeStore saves profiles and replaces their group associations in a transaction.
type ModelAttributeStore struct{ db *sql.DB }

func NewModelAttributeStore(db *sql.DB) *ModelAttributeStore { return &ModelAttributeStore{db: db} }

func scanAttributeConfig(row interface{ Scan(...any) error }) (*routing.ModelAttributeConfig, error) {
	var result routing.ModelAttributeConfig
	var body []byte
	var groups pq.Int64Array
	if err := row.Scan(&result.ID, &result.Name, &result.Description, &result.Status, &body, &result.CreatedAt, &result.UpdatedAt, &groups); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &result.Rules); err != nil {
		return nil, err
	}
	result.GroupIDs = []int64(groups)
	routing.EnsureModelRuleIDs(&result)
	return &result, nil
}

func (s *ModelAttributeStore) List(ctx context.Context) ([]routing.ModelAttributeConfig, error) {
	rows, err := s.db.QueryContext(ctx, attributeSelect+"ORDER BY c.created_at DESC,c.id DESC")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []routing.ModelAttributeConfig{}
	for rows.Next() {
		value, err := scanAttributeConfig(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *value)
	}
	return result, rows.Err()
}

func (s *ModelAttributeStore) Get(ctx context.Context, id int64) (*routing.ModelAttributeConfig, error) {
	value, err := scanAttributeConfig(s.db.QueryRowContext(ctx, attributeSelect+"WHERE c.id=$1", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, routing.ErrAttributeConfigNotFound
	}
	return value, err
}

func (s *ModelAttributeStore) ForGroup(ctx context.Context, id int64) (*routing.ModelAttributeConfig, error) {
	value, err := scanAttributeConfig(s.db.QueryRowContext(ctx, attributeSelect+"WHERE c.status='active' AND EXISTS (SELECT 1 FROM model_attribute_config_groups g WHERE g.config_id=c.id AND g.group_id=$1)", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return value, err
}

// ForGroups reads active profiles for the requested groups, decoding each shared profile once.
func (s *ModelAttributeStore) ForGroups(ctx context.Context, ids []int64) (map[int64]*routing.ModelAttributeConfig, error) {
	result := make(map[int64]*routing.ModelAttributeConfig, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	requested := make(map[int64]bool, len(ids))
	for _, id := range ids {
		requested[id] = true
	}
	rows, err := s.db.QueryContext(ctx, attributeSelect+"WHERE c.status='active' AND EXISTS (SELECT 1 FROM model_attribute_config_groups g WHERE g.config_id=c.id AND g.group_id=ANY($1))", pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		config, err := scanAttributeConfig(rows)
		if err != nil {
			return nil, err
		}
		for _, id := range config.GroupIDs {
			if requested[id] {
				result[id] = config
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func attributeStoreError(err error) error {
	if pg, ok := errors.AsType[*pq.Error](err); ok {
		if pg.Code == "23505" {
			return routing.ErrAttributeConfigConflict
		}
		if pg.Code == "23503" {
			return apperror.BadRequest("INVALID_ATTRIBUTE_GROUP", "selected group does not exist")
		}
	}
	return err
}

func (s *ModelAttributeStore) Save(ctx context.Context, config *routing.ModelAttributeConfig) (err error) {
	defer func() { err = attributeStoreError(err) }()
	body, err := json.Marshal(config.Rules)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if config.ID == 0 {
		err = tx.QueryRowContext(ctx, `INSERT INTO model_attribute_configs(name,description,status,rules) VALUES ($1,$2,$3,$4) RETURNING id,created_at,updated_at`, config.Name, config.Description, config.Status, string(body)).Scan(&config.ID, &config.CreatedAt, &config.UpdatedAt)
	} else {
		err = tx.QueryRowContext(ctx, `UPDATE model_attribute_configs SET name=$2,description=$3,status=$4,rules=$5,updated_at=NOW() WHERE id=$1 AND ($6::timestamptz IS NULL OR updated_at=$6) RETURNING created_at,updated_at`, config.ID, config.Name, config.Description, config.Status, string(body), optionalAttributeTimestamp(config.ExpectedUpdatedAt)).Scan(&config.CreatedAt, &config.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return locale.ErrConflict
		}
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM model_attribute_config_groups WHERE config_id=$1`, config.ID); err != nil {
		return err
	}
	for _, id := range config.GroupIDs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO model_attribute_config_groups(config_id,group_id) VALUES ($1,$2)`, config.ID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *ModelAttributeStore) Delete(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM model_attribute_configs WHERE id=$1`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return routing.ErrAttributeConfigNotFound
	}
	return err
}

// optionalAttributeTimestamp returns nil for an unset optimistic-lock version.
func optionalAttributeTimestamp(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
