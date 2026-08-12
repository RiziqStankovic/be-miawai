package database

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"be-miawai/internal/models"
)

type Store struct {
	db *sql.DB
}

var ErrEmailAlreadyExists = errors.New("email already exists")
var ErrEmailOTPInvalid = errors.New("email otp is invalid or expired")

type PasswordAccount struct {
	User         models.User
	PasswordHash string
}

type PendingEmailOTP struct {
	ID           string
	Email        string
	Name         string
	PasswordHash string
	CodeHash     string
	Attempts     int
	ExpiresAt    time.Time
}

var ErrDesktopAuthCodeInvalid = errors.New("desktop auth code is invalid or expired")

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

func (s *Store) CreateEmailOTP(ctx context.Context, email string, name string, passwordHash string, codeHash string, ttl time.Duration) (PendingEmailOTP, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	if email == "" {
		return PendingEmailOTP{}, errors.New("email is required")
	}
	if name == "" {
		name = strings.Split(email, "@")[0]
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}

	var existingID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&existingID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return PendingEmailOTP{}, err
	}
	if err == nil {
		return PendingEmailOTP{}, ErrEmailAlreadyExists
	}

	otp := PendingEmailOTP{
		ID:           newID("otp"),
		Email:        email,
		Name:         name,
		PasswordHash: passwordHash,
		CodeHash:     codeHash,
		ExpiresAt:    time.Now().UTC().Add(ttl),
	}
	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO email_otps (id, email, name, password_hash, code_hash, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		otp.ID,
		otp.Email,
		otp.Name,
		otp.PasswordHash,
		otp.CodeHash,
		otp.ExpiresAt,
	)
	if err != nil {
		return PendingEmailOTP{}, err
	}
	return otp, nil
}

func (s *Store) ConsumeEmailOTP(ctx context.Context, email string, codeHash string, maxAttempts int) (models.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	codeHash = strings.TrimSpace(codeHash)
	if email == "" || codeHash == "" {
		return models.User{}, ErrEmailOTPInvalid
	}
	if maxAttempts <= 0 {
		maxAttempts = 5
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.User{}, err
	}
	defer tx.Rollback()

	var otp PendingEmailOTP
	err = tx.QueryRowContext(
		ctx,
		`SELECT id, email, name, password_hash, code_hash, attempts, expires_at
		 FROM email_otps
		 WHERE email = $1 AND consumed_at IS NULL AND expires_at > NOW()
		 ORDER BY created_at DESC
		 LIMIT 1
		 FOR UPDATE`,
		email,
	).Scan(&otp.ID, &otp.Email, &otp.Name, &otp.PasswordHash, &otp.CodeHash, &otp.Attempts, &otp.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.User{}, ErrEmailOTPInvalid
	}
	if err != nil {
		return models.User{}, err
	}
	if otp.Attempts >= maxAttempts {
		return models.User{}, ErrEmailOTPInvalid
	}
	if !hmac.Equal([]byte(otp.CodeHash), []byte(codeHash)) {
		_, _ = tx.ExecContext(ctx, `UPDATE email_otps SET attempts = attempts + 1, updated_at = NOW() WHERE id = $1`, otp.ID)
		if commitErr := tx.Commit(); commitErr != nil {
			return models.User{}, commitErr
		}
		return models.User{}, ErrEmailOTPInvalid
	}

	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&existingID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return models.User{}, err
	}
	if err == nil {
		return models.User{}, ErrEmailAlreadyExists
	}

	userID := newID("usr")
	_, err = tx.ExecContext(ctx, `INSERT INTO users (id, email, name) VALUES ($1, $2, $3)`, userID, otp.Email, otp.Name)
	if err != nil {
		return models.User{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO password_accounts (user_id, email, password_hash) VALUES ($1, $2, $3)`, userID, otp.Email, otp.PasswordHash)
	if err != nil {
		return models.User{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE email_otps SET consumed_at = NOW(), updated_at = NOW() WHERE id = $1`, otp.ID)
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

func (s *Store) CreatePasswordUser(ctx context.Context, email string, name string, passwordHash string) (models.User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.User{}, err
	}
	defer tx.Rollback()

	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&existingID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return models.User{}, err
	}
	if err == nil {
		return models.User{}, ErrEmailAlreadyExists
	}

	userID := newID("usr")
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO users (id, email, name) VALUES ($1, $2, $3)`,
		userID,
		email,
		name,
	)
	if err != nil {
		return models.User{}, err
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO password_accounts (user_id, email, password_hash) VALUES ($1, $2, $3)`,
		userID,
		email,
		passwordHash,
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

func (s *Store) EnsurePasswordUser(ctx context.Context, email string, name string, passwordHash string) (models.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	if email == "" {
		return models.User{}, errors.New("email is required")
	}
	if name == "" {
		name = strings.Split(email, "@")[0]
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.User{}, err
	}
	defer tx.Rollback()

	var userID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		userID = newID("usr")
		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO users (id, email, name) VALUES ($1, $2, $3)`,
			userID,
			email,
			name,
		)
	}
	if err != nil {
		return models.User{}, err
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO password_accounts (user_id, email, password_hash)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (user_id) DO UPDATE
		 SET email = EXCLUDED.email,
		     password_hash = EXCLUDED.password_hash,
		     updated_at = NOW()`,
		userID,
		email,
		passwordHash,
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

func (s *Store) StartTrialSubscription(ctx context.Context, userID string, duration time.Duration) error {
	if duration <= 0 {
		duration = 72 * time.Hour
	}
	var existingID string
	err := s.db.QueryRowContext(
		ctx,
		`SELECT id FROM subscriptions WHERE user_id = $1 AND product_id = 'miaw_pro_trial_3d' LIMIT 1`,
		userID,
	).Scan(&existingID)
	if err == nil {
		return errors.New("trial already used")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	entitledUntil := time.Now().UTC().Add(duration)
	return s.UpsertSubscription(
		ctx,
		userID,
		"miaw",
		"miaw_pro_trial_3d",
		"trialing",
		&entitledUntil,
		json.RawMessage(`{"source":"register","trialDays":3}`),
	)
}

func (s *Store) FindPasswordAccountByEmail(ctx context.Context, email string) (PasswordAccount, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT u.id, u.email, u.name, u.avatar_url, u.plan, u.subscription_status, u.entitled_until, u.created_at, pa.password_hash
		 FROM password_accounts pa
		 JOIN users u ON u.id = pa.user_id
		 WHERE pa.email = $1`,
		email,
	)

	var account PasswordAccount
	var entitledUntil sql.NullTime
	err := row.Scan(
		&account.User.ID,
		&account.User.Email,
		&account.User.Name,
		&account.User.AvatarURL,
		&account.User.Plan,
		&account.User.SubscriptionStatus,
		&entitledUntil,
		&account.User.CreatedAt,
		&account.PasswordHash,
	)
	if err != nil {
		return PasswordAccount{}, err
	}
	if entitledUntil.Valid {
		account.User.EntitledUntil = &entitledUntil.Time
	}
	return account, nil
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
	if err := s.refreshUserEntitlement(ctx, id); err != nil {
		return models.User{}, err
	}
	return scanUser(s.db.QueryRowContext(ctx, userSelectSQL()+` WHERE id = $1`, id))
}

func (s *Store) CreateDesktopAuthCode(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	code := newID("dac")
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO desktop_auth_codes (code, user_id, expires_at)
		 VALUES ($1, $2, $3)`,
		code,
		userID,
		time.Now().UTC().Add(ttl),
	)
	return code, err
}

func (s *Store) ConsumeDesktopAuthCode(ctx context.Context, code string) (models.User, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return models.User{}, ErrDesktopAuthCodeInvalid
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.User{}, err
	}
	defer tx.Rollback()

	var userID string
	err = tx.QueryRowContext(
		ctx,
		`UPDATE desktop_auth_codes
		 SET used_at = NOW()
		 WHERE code = $1
		   AND used_at IS NULL
		   AND expires_at > NOW()
		 RETURNING user_id`,
		code,
	).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return models.User{}, ErrDesktopAuthCodeInvalid
	}
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
	status = NormalizeSubscriptionStatus(status)

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
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT status, current_period_end
		 FROM subscriptions
		 WHERE user_id = $1
		 ORDER BY updated_at DESC`,
		userID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	plan := "free"
	status := "none"
	var until any
	var fallbackStatus string
	now := time.Now().UTC()

	for rows.Next() {
		var subscriptionStatus string
		var currentPeriodEnd sql.NullTime
		if err := rows.Scan(&subscriptionStatus, &currentPeriodEnd); err != nil {
			return err
		}

		normalized := NormalizeSubscriptionStatus(subscriptionStatus)
		if normalized == "trialing" || normalized == "active" {
			if !currentPeriodEnd.Valid || currentPeriodEnd.Time.After(now) {
				plan = "pro"
				status = normalized
				if currentPeriodEnd.Valid {
					until = currentPeriodEnd.Time
				}
				break
			}
			if fallbackStatus == "" {
				fallbackStatus = "expired"
			}
			continue
		}

		if fallbackStatus == "" || subscriptionStatusRank(normalized) < subscriptionStatusRank(fallbackStatus) {
			fallbackStatus = normalized
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if status == "none" && fallbackStatus != "" {
		status = fallbackStatus
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

func NormalizeSubscriptionStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "trial", "trialing":
		return "trialing"
	case "active", "settlement", "paid":
		return "active"
	case "hold", "on_hold", "payment_hold", "account_hold":
		return "hold"
	case "suspend", "suspended":
		return "suspended"
	case "past_due", "pastdue", "grace_period":
		return "past_due"
	case "cancel", "cancelled", "canceled":
		return "canceled"
	case "expire", "expired":
		return "expired"
	default:
		if strings.TrimSpace(status) == "" {
			return "none"
		}
		return strings.ToLower(strings.TrimSpace(status))
	}
}

func subscriptionStatusRank(status string) int {
	switch status {
	case "hold":
		return 1
	case "suspended":
		return 2
	case "past_due":
		return 3
	case "canceled":
		return 4
	case "expired":
		return 5
	default:
		return 9
	}
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

func NewID(prefix string) string {
	return newID(prefix)
}
