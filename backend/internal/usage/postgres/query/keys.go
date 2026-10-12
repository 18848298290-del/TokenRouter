package query

import (
	"context"
	"fmt"
	"strings"

	"entgo.io/ent/dialect"
	"github.com/lib/pq"

	infra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// KeyLatestUsageLogIPsQuery builds a dialect-specific latest-IP query.
// PostgreSQL uses one ordered index probe per key with an array parameter;
// other dialects use individual placeholders and rank matching records.
func KeyLatestUsageLogIPsQuery(apiKeyIDs []int64, dialectName string) (string, []any) {
	if dialectName == dialect.Postgres {
		return `
		SELECT requested.api_key_id, latest.ip_address
		FROM unnest($1::bigint[]) AS requested(api_key_id)
		CROSS JOIN LATERAL (
			SELECT ul.ip_address
			FROM usage_logs AS ul
			WHERE ul.api_key_id = requested.api_key_id
				AND ul.ip_address IS NOT NULL
				AND ul.ip_address <> ''
			ORDER BY ul.created_at DESC, ul.id DESC
			LIMIT 1
		) AS latest`, []any{pq.Array(apiKeyIDs)}
	}

	placeholders := make([]string, len(apiKeyIDs))
	args := make([]any, len(apiKeyIDs))
	for i, id := range apiKeyIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	return fmt.Sprintf(`
		SELECT api_key_id, ip_address
		FROM (
			SELECT api_key_id, ip_address,
				ROW_NUMBER() OVER (PARTITION BY api_key_id ORDER BY created_at DESC, id DESC) AS rn
			FROM usage_logs
			WHERE api_key_id IN (%s)
				AND ip_address IS NOT NULL
				AND ip_address <> ''
		) ranked
		WHERE rn = 1`, strings.Join(placeholders, ", ")), args
}

// KeyLatestUsageLogIPs returns each API key's latest nonempty IP from usage_logs.
func KeyLatestUsageLogIPs(ctx context.Context, db infra.Executor, dialectName string, apiKeyIDs []int64) (result map[int64]string, err error) {
	if len(apiKeyIDs) == 0 || db == nil {
		return map[int64]string{}, nil
	}

	query, args := KeyLatestUsageLogIPsQuery(apiKeyIDs, dialectName)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	out := make(map[int64]string, len(apiKeyIDs))
	for rows.Next() {
		var apiKeyID int64
		var ipAddress string
		if err := rows.Scan(&apiKeyID, &ipAddress); err != nil {
			return nil, err
		}
		out[apiKeyID] = ipAddress
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
