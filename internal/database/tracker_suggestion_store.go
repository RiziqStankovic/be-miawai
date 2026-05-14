package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"be-miawai/internal/models"
)

func (s *Store) CreateTrackerSuggestion(ctx context.Context, userID string, input models.TrackerEntryInput) (models.TrackerSuggestion, error) {
	input = normalizeTrackerInput(input)
	rawMetadata, err := json.Marshal(input.Metadata)
	if err != nil {
		return models.TrackerSuggestion{}, err
	}
	contentHash := trackerContentHash(userID, input)
	row := s.db.QueryRowContext(
		ctx,
		`INSERT INTO tracker_suggestions (
		   id, user_id, module, title, amount, status, category, detail, source, updated_from, metadata, content_hash
		 )
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 ON CONFLICT (user_id, content_hash) WHERE review_status = 'pending'
		 DO UPDATE SET metadata = tracker_suggestions.metadata || EXCLUDED.metadata,
		               updated_at = NOW()
		 RETURNING id, module, title, amount, status, category, detail, source, updated_from, metadata, review_status, created_tracker_entry_id, created_at, updated_at`,
		newID("sug"),
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
	return scanTrackerSuggestion(row)
}

func (s *Store) ListTrackerSuggestions(ctx context.Context, userID string) ([]models.TrackerSuggestion, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, module, title, amount, status, category, detail, source, updated_from, metadata, review_status, created_tracker_entry_id, created_at, updated_at
		 FROM tracker_suggestions
		 WHERE user_id = $1 AND review_status = 'pending'
		 ORDER BY updated_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	suggestions := []models.TrackerSuggestion{}
	for rows.Next() {
		suggestion, err := scanTrackerSuggestion(rows)
		if err != nil {
			return nil, err
		}
		suggestions = append(suggestions, suggestion)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return suggestions, nil
}

func (s *Store) ApproveTrackerSuggestion(ctx context.Context, userID string, suggestionID string) (models.TrackerEntry, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.TrackerEntry{}, err
	}
	defer tx.Rollback()

	suggestion, err := scanTrackerSuggestion(tx.QueryRowContext(
		ctx,
		`SELECT id, module, title, amount, status, category, detail, source, updated_from, metadata, review_status, created_tracker_entry_id, created_at, updated_at
		 FROM tracker_suggestions
		 WHERE id = $1 AND user_id = $2 AND review_status = 'pending'`,
		suggestionID,
		userID,
	))
	if err != nil {
		return models.TrackerEntry{}, err
	}

	entry, err := createTrackerEntryWithExec(ctx, tx, userID, models.TrackerEntryInput{
		Module:      suggestion.Module,
		Title:       suggestion.Title,
		Amount:      suggestion.Amount,
		Status:      suggestion.Status,
		Category:    suggestion.Category,
		Detail:      suggestion.Detail,
		Source:      suggestion.Source,
		UpdatedFrom: suggestion.UpdatedFrom,
		Metadata:    suggestion.Metadata,
	})
	if err != nil {
		return models.TrackerEntry{}, err
	}

	_, err = tx.ExecContext(
		ctx,
		`UPDATE tracker_suggestions
		 SET review_status = 'approved', created_tracker_entry_id = $3, updated_at = NOW()
		 WHERE id = $1 AND user_id = $2`,
		suggestionID,
		userID,
		entry.ID,
	)
	if err != nil {
		return models.TrackerEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return models.TrackerEntry{}, err
	}
	return entry, nil
}

func (s *Store) DismissTrackerSuggestion(ctx context.Context, userID string, suggestionID string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE tracker_suggestions
		 SET review_status = 'dismissed', updated_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND review_status = 'pending'`,
		suggestionID,
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

func scanTrackerSuggestion(row rowScanner) (models.TrackerSuggestion, error) {
	var suggestion models.TrackerSuggestion
	var rawMetadata []byte
	var trackerID sql.NullString
	if err := row.Scan(
		&suggestion.ID,
		&suggestion.Module,
		&suggestion.Title,
		&suggestion.Amount,
		&suggestion.Status,
		&suggestion.Category,
		&suggestion.Detail,
		&suggestion.Source,
		&suggestion.UpdatedFrom,
		&rawMetadata,
		&suggestion.ReviewStatus,
		&trackerID,
		&suggestion.CreatedAt,
		&suggestion.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.TrackerSuggestion{}, err
		}
		return models.TrackerSuggestion{}, err
	}
	if trackerID.Valid {
		suggestion.CreatedTrackerEntryID = trackerID.String
	}
	if len(rawMetadata) > 0 {
		_ = json.Unmarshal(rawMetadata, &suggestion.Metadata)
	}
	if suggestion.Metadata == nil {
		suggestion.Metadata = map[string]any{}
	}
	return suggestion, nil
}
