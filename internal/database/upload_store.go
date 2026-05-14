package database

import (
	"context"
)

func (s *Store) InsertChatUpload(ctx context.Context, userID string, conversationID string, messageID string, originalName string, mimeType string, localPath string, publicURL string, sizeBytes int) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO chat_uploads (id, user_id, conversation_id, message_id, original_name, mime_type, local_path, public_url, size_bytes)
		 VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, $6, $7, $8, $9)`,
		newID("upl"),
		userID,
		conversationID,
		messageID,
		originalName,
		mimeType,
		localPath,
		publicURL,
		sizeBytes,
	)
	return err
}
