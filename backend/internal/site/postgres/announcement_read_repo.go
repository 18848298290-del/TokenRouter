package postgres

import (
	"context"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/announcementread"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// Read markers identify an announcement and user pair and retain its first
// read time across repeated acknowledgements. Insert conflicts keep that timestamp,
// while display queries return maps keyed by announcement or user for joining
// markers to separately selected records.

// announcementReadRepository persists first-read pairs and supplies keyed read-time queries through Ent.
type announcementReadRepository struct {
	// client is shared with app-owned storage; read queries use this bound
	// client, while MarkRead prefers the Ent transaction carried by its context.
	client *dbent.Client
}

// NewAnnouncementReadRepository binds the Ent client used for announcement first-read markers.
// Construction performs no queries. The app keeps client lifecycle ownership,
// and MarkRead selects a transaction client from context when one is present.
func NewAnnouncementReadRepository(client *dbent.Client) site.AnnouncementReadRepository {
	return &announcementReadRepository{client: client}
}

// MarkRead inserts the first acknowledgement for an announcement and user pair.
// Repeated acknowledgements keep the existing timestamp through conflict handling.
// A no-rows result is treated as success, while other storage failures reach
// the caller; context transactions participate in this write when present.
func (r *announcementReadRepository) MarkRead(ctx context.Context, announcementID, userID int64, readAt time.Time) error {
	client := clientFromContext(ctx, r.client)
	// The pair's unique conflict rule makes retries retain the first read time.
	// DoNothing avoids moving that marker when another acknowledgement already created the row.
	err := client.AnnouncementRead.Create().
		SetAnnouncementID(announcementID).
		SetUserID(userID).
		SetReadAt(readAt).
		OnConflictColumns(announcementread.FieldAnnouncementID, announcementread.FieldUserID).
		DoNothing().
		Exec(ctx)
	if isSQLNoRowsError(err) {
		// DO NOTHING can report no inserted row after retaining the first marker.
		// Accept the shared classifier's no-rows result so a repeated acknowledgement still returns success.
		return nil
	}
	return err
}

// GetReadMapByUser returns first-read timestamps keyed by selected announcement IDs.
// Empty input returns an empty map, and missing markers remain absent. Queries
// use the bound client so callers can join these results by map membership.
func (r *announcementReadRepository) GetReadMapByUser(ctx context.Context, userID int64, announcementIDs []int64) (map[int64]time.Time, error) {
	if len(announcementIDs) == 0 {
		return map[int64]time.Time{}, nil
	}

	rows, err := r.client.AnnouncementRead.Query().
		Where(
			announcementread.UserIDEQ(userID),
			announcementread.AnnouncementIDIn(announcementIDs...),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make(map[int64]time.Time, len(rows))
	for i := range rows {
		out[rows[i].AnnouncementID] = rows[i].ReadAt
	}
	return out, nil
}

// GetReadMapByUsers returns first-read timestamps keyed by the selected user IDs.
// Empty user input returns an empty map, and missing markers remain absent.
// The bound-client query supports joining one announcement's read state to a user page.
func (r *announcementReadRepository) GetReadMapByUsers(ctx context.Context, announcementID int64, userIDs []int64) (map[int64]time.Time, error) {
	if len(userIDs) == 0 {
		return map[int64]time.Time{}, nil
	}

	rows, err := r.client.AnnouncementRead.Query().
		Where(
			announcementread.AnnouncementIDEQ(announcementID),
			announcementread.UserIDIn(userIDs...),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make(map[int64]time.Time, len(rows))
	for i := range rows {
		out[rows[i].UserID] = rows[i].ReadAt
	}
	return out, nil
}

// CountByAnnouncementID returns the number of user read markers for one announcement.
func (r *announcementReadRepository) CountByAnnouncementID(ctx context.Context, announcementID int64) (int64, error) {
	count, err := r.client.AnnouncementRead.Query().
		Where(announcementread.AnnouncementIDEQ(announcementID)).
		Count(ctx)
	if err != nil {
		return 0, err
	}
	return int64(count), nil
}

// isSQLNoRowsError reports whether the PostgreSQL classifier recognizes a no-rows result.
// That classifier accepts wrapped sql.ErrNoRows and messages containing its
// known no-result text; this adapter uses the result to acknowledge conflict-safe read writes.
func isSQLNoRowsError(err error) bool { return postgresinfra.IsNoRows(err) }
