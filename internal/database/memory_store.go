package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"be-miawai/internal/models"
)

func (s *Store) UpsertMemory(ctx context.Context, userID string, input models.MemoryInput) (models.UserMemory, error) {
	input.Domain = normalizeMemoryField(input.Domain, "chat")
	input.Kind = normalizeMemoryField(input.Kind, "fact")
	input.Title = strings.TrimSpace(input.Title)
	input.Content = strings.TrimSpace(input.Content)
	if input.Title == "" {
		input.Title = memoryTitleFromContent(input.Content)
	}
	if input.Confidence <= 0 || input.Confidence > 1 {
		input.Confidence = 0.7
	}
	if input.Metadata == nil {
		input.Metadata = map[string]any{}
	}
	contentHash := memoryContentHash(input.Domain, input.Kind, input.Content)

	rawMetadata, err := json.Marshal(input.Metadata)
	if err != nil {
		return models.UserMemory{}, err
	}

	row := s.db.QueryRowContext(
		ctx,
		`INSERT INTO user_memories (
		   id, user_id, domain, kind, title, content, content_hash, source_conversation_id, confidence, metadata
		 )
		 VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9, $10)
		 ON CONFLICT (user_id, content_hash) WHERE deleted_at IS NULL
		 DO UPDATE SET domain = EXCLUDED.domain,
		               kind = EXCLUDED.kind,
		               title = EXCLUDED.title,
		               content = EXCLUDED.content,
		               content_hash = EXCLUDED.content_hash,
		               source_conversation_id = EXCLUDED.source_conversation_id,
		               confidence = EXCLUDED.confidence,
		               metadata = EXCLUDED.metadata,
		               updated_at = NOW(),
		               deleted_at = NULL
		 RETURNING id, domain, kind, title, content, source_conversation_id, confidence, metadata, created_at, updated_at`,
		newID("mem"),
		userID,
		input.Domain,
		input.Kind,
		input.Title,
		input.Content,
		contentHash,
		input.SourceConversationID,
		input.Confidence,
		rawMetadata,
	)
	return scanMemory(row)
}

func (s *Store) ListMemoriesByUser(ctx context.Context, userID string, limit int) ([]models.UserMemory, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, domain, kind, title, content, source_conversation_id, confidence, metadata, created_at, updated_at
		 FROM user_memories
		 WHERE user_id = $1 AND deleted_at IS NULL
		 ORDER BY updated_at DESC
		 LIMIT $2`,
		userID,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemories(rows)
}

func (s *Store) UpdateMemory(ctx context.Context, userID string, memoryID string, input models.MemoryInput) (models.UserMemory, error) {
	input.Domain = normalizeMemoryField(input.Domain, "chat")
	input.Kind = normalizeMemoryField(input.Kind, "fact")
	input.Title = strings.TrimSpace(input.Title)
	input.Content = strings.TrimSpace(input.Content)
	if input.Title == "" {
		input.Title = memoryTitleFromContent(input.Content)
	}
	if input.Confidence <= 0 || input.Confidence > 1 {
		input.Confidence = 0.7
	}
	if input.Metadata == nil {
		input.Metadata = map[string]any{}
	}
	contentHash := memoryContentHash(input.Domain, input.Kind, input.Content)

	rawMetadata, err := json.Marshal(input.Metadata)
	if err != nil {
		return models.UserMemory{}, err
	}

	row := s.db.QueryRowContext(
		ctx,
		`UPDATE user_memories
		 SET domain = $3,
		     kind = $4,
		     title = $5,
		     content = $6,
		     content_hash = $7,
		     source_conversation_id = NULLIF($8, ''),
		     confidence = $9,
		     metadata = $10,
		     updated_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
		 RETURNING id, domain, kind, title, content, source_conversation_id, confidence, metadata, created_at, updated_at`,
		memoryID,
		userID,
		input.Domain,
		input.Kind,
		input.Title,
		input.Content,
		contentHash,
		input.SourceConversationID,
		input.Confidence,
		rawMetadata,
	)
	return scanMemory(row)
}

func (s *Store) SearchMemories(ctx context.Context, userID string, query string, limit int) ([]models.UserMemory, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []models.UserMemory{}, nil
	}
	if limit <= 0 || limit > 20 {
		limit = 8
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, domain, kind, title, content, source_conversation_id, confidence, metadata, created_at, updated_at
		 FROM user_memories
		 WHERE user_id = $1
		   AND deleted_at IS NULL
		   AND to_tsvector('simple', coalesce(title, '') || ' ' || coalesce(content, ''))
		       @@ plainto_tsquery('simple', $2)
		 ORDER BY ts_rank(
		            to_tsvector('simple', coalesce(title, '') || ' ' || coalesce(content, '')),
		            plainto_tsquery('simple', $2)
		          ) DESC,
		          updated_at DESC
		 LIMIT $3`,
		userID,
		query,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	memories, err := scanMemories(rows)
	if err != nil {
		return nil, err
	}
	if len(memories) > 0 {
		return memories, nil
	}
	return s.recentMemories(ctx, userID, limit)
}

func (s *Store) DeleteMemory(ctx context.Context, userID string, memoryID string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE user_memories
		 SET deleted_at = NOW(), updated_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		memoryID,
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

func (s *Store) EnqueueMemoryExtractionJob(ctx context.Context, userID string, conversationID string, userMessage string, assistantReply string) (models.MemoryExtractionJob, error) {
	now := time.Now().UTC()
	job := models.MemoryExtractionJob{
		ID:             newID("memjob"),
		UserID:         userID,
		ConversationID: conversationID,
		UserMessage:    strings.TrimSpace(userMessage),
		AssistantReply: strings.TrimSpace(assistantReply),
		Status:         "pending",
		RunAfter:       now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO memory_extraction_jobs (
		   id, user_id, conversation_id, user_message, assistant_reply, status, attempts, run_after, created_at, updated_at
		 )
		 VALUES ($1, $2, $3, $4, $5, 'pending', 0, $6, $6, $6)`,
		job.ID,
		userID,
		conversationID,
		job.UserMessage,
		job.AssistantReply,
		now,
	)
	return job, err
}

func (s *Store) ClaimDueMemoryExtractionJobs(ctx context.Context, limit int) ([]models.MemoryExtractionJob, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	rows, err := s.db.QueryContext(
		ctx,
		`UPDATE memory_extraction_jobs
		 SET status = 'running',
		     locked_at = NOW(),
		     updated_at = NOW()
		 WHERE id IN (
		   SELECT id
		   FROM memory_extraction_jobs
		   WHERE status IN ('pending', 'retry')
		     AND run_after <= NOW()
		     AND attempts < max_attempts
		   ORDER BY created_at ASC
		   LIMIT $1
		   FOR UPDATE SKIP LOCKED
		 )
		 RETURNING id, user_id, conversation_id, user_message, assistant_reply, status, attempts, last_error, run_after, created_at, updated_at`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	jobs := make([]models.MemoryExtractionJob, 0, limit)
	for rows.Next() {
		job, err := scanMemoryExtractionJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *Store) CompleteMemoryExtractionJob(ctx context.Context, jobID string) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE memory_extraction_jobs
		 SET status = 'done',
		     last_error = '',
		     updated_at = NOW()
		 WHERE id = $1`,
		jobID,
	)
	return err
}

func (s *Store) FailMemoryExtractionJob(ctx context.Context, jobID string, errText string) error {
	errText = strings.TrimSpace(errText)
	if len(errText) > 1000 {
		errText = errText[:1000]
	}
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE memory_extraction_jobs
		 SET attempts = attempts + 1,
		     status = CASE WHEN attempts + 1 >= max_attempts THEN 'failed' ELSE 'retry' END,
		     last_error = $2,
		     run_after = NOW() + ((attempts + 1) * INTERVAL '30 seconds'),
		     locked_at = NULL,
		     updated_at = NOW()
		 WHERE id = $1`,
		jobID,
		errText,
	)
	return err
}

func (s *Store) recentMemories(ctx context.Context, userID string, limit int) ([]models.UserMemory, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, domain, kind, title, content, source_conversation_id, confidence, metadata, created_at, updated_at
		 FROM user_memories
		 WHERE user_id = $1 AND deleted_at IS NULL
		 ORDER BY updated_at DESC
		 LIMIT $2`,
		userID,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemories(rows)
}

func scanMemories(rows *sql.Rows) ([]models.UserMemory, error) {
	memories := make([]models.UserMemory, 0)
	for rows.Next() {
		memory, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		memories = append(memories, memory)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return memories, nil
}

func scanMemory(row rowScanner) (models.UserMemory, error) {
	var memory models.UserMemory
	var sourceConversationID sql.NullString
	var rawMetadata []byte
	if err := row.Scan(
		&memory.ID,
		&memory.Domain,
		&memory.Kind,
		&memory.Title,
		&memory.Content,
		&sourceConversationID,
		&memory.Confidence,
		&rawMetadata,
		&memory.CreatedAt,
		&memory.UpdatedAt,
	); err != nil {
		return models.UserMemory{}, err
	}
	if sourceConversationID.Valid {
		memory.SourceConversationID = sourceConversationID.String
	}
	if len(rawMetadata) > 0 {
		_ = json.Unmarshal(rawMetadata, &memory.Metadata)
	}
	if memory.Metadata == nil {
		memory.Metadata = map[string]any{}
	}
	return memory, nil
}

func scanMemoryExtractionJob(row rowScanner) (models.MemoryExtractionJob, error) {
	var job models.MemoryExtractionJob
	if err := row.Scan(
		&job.ID,
		&job.UserID,
		&job.ConversationID,
		&job.UserMessage,
		&job.AssistantReply,
		&job.Status,
		&job.Attempts,
		&job.LastError,
		&job.RunAfter,
		&job.CreatedAt,
		&job.UpdatedAt,
	); err != nil {
		return models.MemoryExtractionJob{}, err
	}
	return job, nil
}

func normalizeMemoryField(value string, fallback string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return fallback
	}
	return normalized
}

func memoryTitleFromContent(content string) string {
	trimmed := strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	if trimmed == "" {
		return "Memory"
	}
	if len(trimmed) <= 80 {
		return trimmed
	}
	return trimmed[:77] + "..."
}

func memoryContentHash(domain string, kind string, content string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(domain+"|"+kind+"|"+content), " "))
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])[:32]
}
