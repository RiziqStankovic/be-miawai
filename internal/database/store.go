package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"be-miawai/internal/models"
)

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) FindOrCreateOAuthUser(ctx context.Context, profile models.OAuthProfile) (models.User, error) {
	if profile.Provider == "" || profile.ProviderUserID == "" {
		return models.User{}, errors.New("provider and provider user id are required")
	}
	if profile.Email == "" {
		profile.Email = profile.Provider + ":" + profile.ProviderUserID + "@miaw.local"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.User{}, err
	}
	defer tx.Rollback()

	var userID string
	err = tx.QueryRowContext(
		ctx,
		`SELECT user_id FROM oauth_accounts WHERE provider = $1 AND provider_user_id = $2`,
		profile.Provider,
		profile.ProviderUserID,
	).Scan(&userID)

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return models.User{}, err
	}

	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE email = $1`, profile.Email).Scan(&userID)
		if errors.Is(err, sql.ErrNoRows) {
			userID = newID("usr")
			_, err = tx.ExecContext(
				ctx,
				`INSERT INTO users (id, email, name, avatar_url) VALUES ($1, $2, $3, $4)`,
				userID,
				profile.Email,
				profile.Name,
				profile.AvatarURL,
			)
		}
		if err != nil {
			return models.User{}, err
		}

		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO oauth_accounts (id, user_id, provider, provider_user_id, email)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (provider, provider_user_id)
			 DO UPDATE SET email = EXCLUDED.email, updated_at = NOW()`,
			newID("oa"),
			userID,
			profile.Provider,
			profile.ProviderUserID,
			profile.Email,
		)
		if err != nil {
			return models.User{}, err
		}
	}

	_, err = tx.ExecContext(
		ctx,
		`UPDATE users
		 SET name = COALESCE(NULLIF($2, ''), name),
		     avatar_url = COALESCE(NULLIF($3, ''), avatar_url),
		     updated_at = NOW()
		 WHERE id = $1`,
		userID,
		profile.Name,
		profile.AvatarURL,
	)
	if err != nil {
		return models.User{}, err
	}

	user, err := scanUser(tx.QueryRowContext(ctx, userSelectSQL()+` WHERE id = $1`, userID))
	if err != nil {
		return models.User{}, err
	}
	if err := tx.Commit(); err != nil {
		return models.User{}, err
	}
	return user, nil
}

func (s *Store) GetUserByID(ctx context.Context, id string) (models.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, userSelectSQL()+` WHERE id = $1`, id))
}

func (s *Store) InsertChatMessage(ctx context.Context, userID string, role string, content string) (models.ChatMessage, error) {
	message := models.ChatMessage{
		ID:        newID("msg"),
		Role:      role,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO chat_messages (id, user_id, role, content, created_at) VALUES ($1, $2, $3, $4, $5)`,
		message.ID,
		userID,
		role,
		content,
		message.CreatedAt,
	)
	return message, err
}

func (s *Store) ListChatMessagesByUser(ctx context.Context, userID string, limit int) ([]models.ChatMessage, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, role, content, created_at
		 FROM chat_messages
		 WHERE user_id = $1
		 ORDER BY created_at ASC
		 LIMIT $2`,
		userID,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := make([]models.ChatMessage, 0, limit)
	for rows.Next() {
		var message models.ChatMessage
		if err := rows.Scan(&message.ID, &message.Role, &message.Content, &message.CreatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *Store) UpsertSubscription(ctx context.Context, userID string, platform string, productID string, status string, currentPeriodEnd *time.Time, raw json.RawMessage) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}

	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO subscriptions (id, user_id, platform, product_id, status, current_period_end, raw_payload)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (user_id, platform, product_id)
		 DO UPDATE SET status = EXCLUDED.status,
		               current_period_end = EXCLUDED.current_period_end,
		               raw_payload = EXCLUDED.raw_payload,
		               updated_at = NOW()`,
		newID("sub"),
		userID,
		platform,
		productID,
		status,
		currentPeriodEnd,
		raw,
	)
	if err != nil {
		return err
	}
	return s.refreshUserEntitlement(ctx, userID)
}

func (s *Store) refreshUserEntitlement(ctx context.Context, userID string) error {
	var entitledUntil sql.NullTime
	var activeCount int
	err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*), MAX(current_period_end)
		 FROM subscriptions
		 WHERE user_id = $1
		   AND status IN ('trialing', 'active')
		   AND (current_period_end IS NULL OR current_period_end > NOW())`,
		userID,
	).Scan(&activeCount, &entitledUntil)
	if err != nil {
		return err
	}

	plan := "free"
	status := "none"
	var until any
	if activeCount > 0 {
		plan = "pro"
		status = "active"
	}
	if entitledUntil.Valid {
		until = entitledUntil.Time
	}

	_, err = s.db.ExecContext(
		ctx,
		`UPDATE users
		 SET plan = $2, subscription_status = $3, entitled_until = $4, updated_at = NOW()
		 WHERE id = $1`,
		userID,
		plan,
		status,
		until,
	)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (models.User, error) {
	var user models.User
	var entitledUntil sql.NullTime
	err := row.Scan(
		&user.ID,
		&user.Email,
		&user.Name,
		&user.AvatarURL,
		&user.Plan,
		&user.SubscriptionStatus,
		&entitledUntil,
		&user.CreatedAt,
	)
	if err != nil {
		return models.User{}, err
	}
	if entitledUntil.Valid {
		user.EntitledUntil = &entitledUntil.Time
	}
	return user, nil
}

func userSelectSQL() string {
	return `SELECT id, email, name, avatar_url, plan, subscription_status, entitled_until, created_at FROM users`
}

func newID(prefix string) string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}
