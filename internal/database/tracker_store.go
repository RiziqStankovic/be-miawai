package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"

	"be-miawai/internal/models"
)

var allowedTrackerModules = map[string]bool{
	"finance": true,
	"assets":  true,
	"pangan":  true,
	"health":  true,
	"persona": true,
}

func (s *Store) ListTrackerEntries(ctx context.Context, userID string, module string) ([]models.TrackerEntry, error) {
	module = normalizeTrackerModule(module)
	args := []any{userID}
	filter := ""
	if module != "" {
		args = append(args, module)
		filter = " AND module = $2"
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, module, title, amount, status, category, detail, source, updated_from, metadata, created_at, updated_at
		 FROM tracker_entries
		 WHERE user_id = $1 AND deleted_at IS NULL`+filter+`
		 ORDER BY updated_at DESC`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTrackerEntries(rows)
}

func (s *Store) CreateTrackerEntry(ctx context.Context, userID string, input models.TrackerEntryInput) (models.TrackerEntry, error) {
	return createTrackerEntryWithExec(ctx, s.db, userID, input)
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func createTrackerEntryWithExec(ctx context.Context, exec queryRower, userID string, input models.TrackerEntryInput) (models.TrackerEntry, error) {
	input = normalizeTrackerInput(input)
	rawMetadata, err := json.Marshal(input.Metadata)
	if err != nil {
		return models.TrackerEntry{}, err
	}

	contentHash := trackerContentHash(userID, input)
	row := exec.QueryRowContext(
		ctx,
		`INSERT INTO tracker_entries (
		   id, user_id, module, title, amount, status, category, detail, source, updated_from, metadata, content_hash
		 )
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 ON CONFLICT (user_id, content_hash) WHERE deleted_at IS NULL
		 DO UPDATE SET status = EXCLUDED.status,
		               source = EXCLUDED.source,
		               updated_from = EXCLUDED.updated_from,
		               metadata = tracker_entries.metadata || EXCLUDED.metadata,
		               updated_at = NOW()
		 RETURNING id, module, title, amount, status, category, detail, source, updated_from, metadata, created_at, updated_at`,
		newID("trk"),
		userID,
		input.Module,
		input.Title,
		input.Amount,
		input.Status,
		input.Category,
		input.Detail,
		input.Source,
		input.UpdatedFrom,
		rawMetadata,
		contentHash,
	)
	return scanTrackerEntry(row)
}

func trackerContentHash(userID string, input models.TrackerEntryInput) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(
		userID+"|"+input.Module+"|"+input.Title+"|"+input.Amount+"|"+input.Category+"|"+input.Detail,
	), " "))
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])[:32]
}

func (s *Store) UpdateTrackerEntry(ctx context.Context, userID string, entryID string, input models.TrackerEntryInput) (models.TrackerEntry, error) {
	input = normalizeTrackerInput(input)
	rawMetadata, err := json.Marshal(input.Metadata)
	if err != nil {
		return models.TrackerEntry{}, err
	}

	row := s.db.QueryRowContext(
		ctx,
		`UPDATE tracker_entries
		 SET module = $3,
		     title = $4,
		     amount = $5,
		     status = $6,
		     category = $7,
		     detail = $8,
		     source = $9,
		     updated_from = $10,
		     metadata = $11,
		     updated_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
		 RETURNING id, module, title, amount, status, category, detail, source, updated_from, metadata, created_at, updated_at`,
		entryID,
		userID,
		input.Module,
		input.Title,
		input.Amount,
		input.Status,
		input.Category,
		input.Detail,
		input.Source,
		input.UpdatedFrom,
		rawMetadata,
	)
	return scanTrackerEntry(row)
}

func (s *Store) DeleteTrackerEntry(ctx context.Context, userID string, entryID string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE tracker_entries
		 SET deleted_at = NOW(), updated_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		entryID,
		userID,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func scanTrackerEntries(rows *sql.Rows) ([]models.TrackerEntry, error) {
	entries := make([]models.TrackerEntry, 0)
	for rows.Next() {
		entry, err := scanTrackerEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func scanTrackerEntry(row rowScanner) (models.TrackerEntry, error) {
	var entry models.TrackerEntry
	var rawMetadata []byte
	if err := row.Scan(
		&entry.ID,
		&entry.Module,
		&entry.Title,
		&entry.Amount,
		&entry.Status,
		&entry.Category,
		&entry.Detail,
		&entry.Source,
		&entry.UpdatedFrom,
		&rawMetadata,
		&entry.CreatedAt,
		&entry.UpdatedAt,
	); err != nil {
		return models.TrackerEntry{}, err
	}
	if len(rawMetadata) > 0 {
		_ = json.Unmarshal(rawMetadata, &entry.Metadata)
	}
	if entry.Metadata == nil {
		entry.Metadata = map[string]any{}
	}
	return entry, nil
}

func normalizeTrackerInput(input models.TrackerEntryInput) models.TrackerEntryInput {
	input.Module = normalizeTrackerModule(input.Module)
	if input.Module == "" {
		input.Module = "finance"
	}
	input.Title = strings.TrimSpace(input.Title)
	input.Amount = strings.TrimSpace(input.Amount)
	input.Status = strings.TrimSpace(input.Status)
	input.Category = strings.TrimSpace(input.Category)
	input.Detail = strings.TrimSpace(input.Detail)
	input.Source = normalizeTrackerSource(input.Source)
	input.UpdatedFrom = strings.TrimSpace(input.UpdatedFrom)
	if input.Status == "" {
		input.Status = "tracked"
	}
	if input.Category == "" {
		input.Category = "general"
	}
	if input.UpdatedFrom == "" {
		input.UpdatedFrom = input.Source
	}
	if input.Metadata == nil {
		input.Metadata = map[string]any{}
	}
	return input
}

func normalizeTrackerModule(module string) string {
	module = strings.ToLower(strings.TrimSpace(module))
	if allowedTrackerModules[module] {
		return module
	}
	return ""
}

func normalizeTrackerSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "miaw ai chat", "chat", "ai", "ai_chat":
		return "Miaw AI Chat"
	case "image upload", "image", "upload", "receipt":
		return "Image Upload"
	default:
		return "Manual"
	}
}
