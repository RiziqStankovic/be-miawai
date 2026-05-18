package database

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"be-miawai/internal/models"
)

var ErrAPIKeyInvalid = errors.New("api key is invalid")

type APIKeyCredential struct {
	Key      models.UserAPIKey
	PlainKey string
}

func (s *Store) CreateUserAPIKey(ctx context.Context, userID string, name string) (APIKeyCredential, error) {
	plainKey, prefix, hash, err := generateUserAPIKey()
	if err != nil {
		return APIKeyCredential{}, err
	}
	key := models.UserAPIKey{
		ID:        newID("uapk"),
		UserID:    userID,
		Name:      strings.TrimSpace(name),
		KeyPrefix: prefix,
		Status:    "active",
	}
	if key.Name == "" {
		key.Name = "API key"
	}
	var lastUsedAt sql.NullTime
	var revokedAt sql.NullTime
	err = s.db.QueryRowContext(
		ctx,
		`INSERT INTO user_api_keys (id, user_id, name, key_prefix, key_hash, status)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, user_id, name, key_prefix, status, last_used_at, created_at, revoked_at`,
		key.ID,
		key.UserID,
		key.Name,
		key.KeyPrefix,
		hash,
		key.Status,
	).Scan(
		&key.ID,
		&key.UserID,
		&key.Name,
		&key.KeyPrefix,
		&key.Status,
		&lastUsedAt,
		&key.CreatedAt,
		&revokedAt,
	)
	if err != nil {
		return APIKeyCredential{}, err
	}
	if lastUsedAt.Valid {
		key.LastUsedAt = &lastUsedAt.Time
	}
	if revokedAt.Valid {
		key.RevokedAt = &revokedAt.Time
	}
	return APIKeyCredential{Key: key, PlainKey: plainKey}, nil
}

func (s *Store) ListUserAPIKeys(ctx context.Context, userID string) ([]models.UserAPIKey, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, user_id, name, key_prefix, status, last_used_at, created_at, revoked_at
		 FROM user_api_keys
		 WHERE user_id = $1
		 ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keys := []models.UserAPIKey{}
	for rows.Next() {
		key, err := scanUserAPIKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) RevokeUserAPIKey(ctx context.Context, userID string, keyID string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE user_api_keys
		 SET status = 'revoked', revoked_at = COALESCE(revoked_at, NOW())
		 WHERE id = $1 AND user_id = $2 AND status <> 'revoked'`,
		keyID,
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

func (s *Store) AuthenticateUserAPIKey(ctx context.Context, plainKey string) (models.UserAPIKey, models.User, error) {
	plainKey = strings.TrimSpace(plainKey)
	if plainKey == "" {
		return models.UserAPIKey{}, models.User{}, ErrAPIKeyInvalid
	}
	hash := hashUserAPIKey(plainKey)
	row := s.db.QueryRowContext(
		ctx,
		`SELECT k.id, k.user_id, k.name, k.key_prefix, k.status, k.last_used_at, k.created_at, k.revoked_at,
		        u.id, u.email, u.name, u.avatar_url, u.plan, u.subscription_status, u.entitled_until, u.created_at
		 FROM user_api_keys k
		 JOIN users u ON u.id = k.user_id
		 WHERE k.key_hash = $1 AND k.status = 'active' AND k.revoked_at IS NULL`,
		hash,
	)
	var key models.UserAPIKey
	var user models.User
	var entitledUntil sql.NullTime
	var keyLastUsedAt sql.NullTime
	var keyRevokedAt sql.NullTime
	err := row.Scan(
		&key.ID,
		&key.UserID,
		&key.Name,
		&key.KeyPrefix,
		&key.Status,
		&keyLastUsedAt,
		&key.CreatedAt,
		&keyRevokedAt,
		&user.ID,
		&user.Email,
		&user.Name,
		&user.AvatarURL,
		&user.Plan,
		&user.SubscriptionStatus,
		&entitledUntil,
		&user.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return models.UserAPIKey{}, models.User{}, ErrAPIKeyInvalid
	}
	if err != nil {
		return models.UserAPIKey{}, models.User{}, err
	}
	if entitledUntil.Valid {
		user.EntitledUntil = &entitledUntil.Time
	}
	if keyLastUsedAt.Valid {
		key.LastUsedAt = &keyLastUsedAt.Time
	}
	if keyRevokedAt.Valid {
		key.RevokedAt = &keyRevokedAt.Time
	}
	return key, user, nil
}

func (s *Store) MarkUserAPIKeyUsed(ctx context.Context, keyID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE user_api_keys SET last_used_at = NOW() WHERE id = $1`, keyID)
	return err
}

func scanUserAPIKey(row rowScanner) (models.UserAPIKey, error) {
	var key models.UserAPIKey
	var lastUsedAt sql.NullTime
	var revokedAt sql.NullTime
	err := row.Scan(
		&key.ID,
		&key.UserID,
		&key.Name,
		&key.KeyPrefix,
		&key.Status,
		&lastUsedAt,
		&key.CreatedAt,
		&revokedAt,
	)
	if err != nil {
		return models.UserAPIKey{}, err
	}
	if lastUsedAt.Valid {
		key.LastUsedAt = &lastUsedAt.Time
	}
	if revokedAt.Valid {
		key.RevokedAt = &revokedAt.Time
	}
	return key, nil
}

func generateUserAPIKey() (string, string, string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", "", "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(bytes[:])
	plainKey := "miaw_" + secret
	prefix := plainKey
	if len(prefix) > 14 {
		prefix = prefix[:14]
	}
	return plainKey, prefix, hashUserAPIKey(plainKey), nil
}

func hashUserAPIKey(plainKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(plainKey)))
	return hex.EncodeToString(sum[:])
}
