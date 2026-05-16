package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"be-miawai/internal/models"
)

func (s *Store) CreateWhatsAppAccount(ctx context.Context, userID string, mode string, displayName string) (models.WhatsAppAccount, error) {
	account := models.WhatsAppAccount{
		ID:           newID("waacc"),
		OwnerUserID:  userID,
		Mode:         normalizeWhatsAppMode(mode),
		DisplayName:  strings.TrimSpace(displayName),
		Status:       "pending_qr",
		SessionRef:   newID("wasess"),
		AccessPolicy: "allow_all",
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if account.DisplayName == "" {
		account.DisplayName = "WhatsApp"
	}

	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO whatsapp_accounts (id, owner_user_id, mode, display_name, status, session_ref, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		account.ID,
		account.OwnerUserID,
		account.Mode,
		account.DisplayName,
		account.Status,
		account.SessionRef,
		account.CreatedAt,
		account.UpdatedAt,
	)
	if err != nil {
		return models.WhatsAppAccount{}, err
	}
	return account, nil
}

func (s *Store) GetOrCreateCentralWhatsAppAccount(ctx context.Context, ownerUserID string) (models.WhatsAppAccount, error) {
	ownerUserID = strings.TrimSpace(ownerUserID)
	if ownerUserID == "" {
		return models.WhatsAppAccount{}, errors.New("whatsapp owner user id is required")
	}

	account, err := scanWhatsAppAccount(s.db.QueryRowContext(
		ctx,
		`SELECT id, owner_user_id, mode, display_name, phone_jid, status, session_ref, access_policy, qr_code, created_at, updated_at
		 FROM whatsapp_accounts
		 WHERE owner_user_id = $1 AND mode = 'central_bot' AND status <> 'revoked'
		 ORDER BY updated_at DESC
		 LIMIT 1`,
		ownerUserID,
	))
	if err == nil {
		return account, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return models.WhatsAppAccount{}, err
	}

	account, err = s.CreateWhatsAppAccount(ctx, ownerUserID, "central_bot", "Miaw AI WhatsApp")
	if err != nil {
		return models.WhatsAppAccount{}, err
	}
	return s.UpdateWhatsAppAccountPolicy(ctx, ownerUserID, account.ID, "linked_only")
}

func (s *Store) ListWhatsAppAccountsByUser(ctx context.Context, userID string) ([]models.WhatsAppAccount, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, owner_user_id, mode, display_name, phone_jid, status, session_ref, access_policy, qr_code, created_at, updated_at
		 FROM whatsapp_accounts
		 WHERE owner_user_id = $1 AND status <> 'revoked'
		 ORDER BY updated_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	accounts := make([]models.WhatsAppAccount, 0)
	for rows.Next() {
		account, err := scanWhatsAppAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func (s *Store) RevokeWhatsAppAccount(ctx context.Context, userID string, accountID string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE whatsapp_accounts
		 SET status = 'revoked', updated_at = NOW()
		 WHERE id = $1 AND owner_user_id = $2`,
		accountID,
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

func (s *Store) UpsertWhatsAppAccountStatus(ctx context.Context, accountID string, status string, phoneJID string, displayName string, qrCode string) (models.WhatsAppAccount, error) {
	status = normalizeWhatsAppStatus(status)
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE whatsapp_accounts
		 SET status = $2,
		     phone_jid = COALESCE(NULLIF($3, ''), phone_jid),
		     display_name = COALESCE(NULLIF($4, ''), display_name),
		     qr_code = $5,
		     updated_at = NOW()
		 WHERE id = $1 AND status <> 'revoked'`,
		accountID,
		status,
		strings.TrimSpace(phoneJID),
		strings.TrimSpace(displayName),
		strings.TrimSpace(qrCode),
	)
	if err != nil {
		return models.WhatsAppAccount{}, err
	}
	return s.GetWhatsAppAccountByID(ctx, accountID)
}

func (s *Store) GetWhatsAppAccountByID(ctx context.Context, accountID string) (models.WhatsAppAccount, error) {
	return scanWhatsAppAccount(s.db.QueryRowContext(
		ctx,
		`SELECT id, owner_user_id, mode, display_name, phone_jid, status, session_ref, access_policy, qr_code, created_at, updated_at
		 FROM whatsapp_accounts
		 WHERE id = $1 AND status <> 'revoked'`,
		accountID,
	))
}

func (s *Store) ResolveWhatsAppContact(ctx context.Context, accountID string, contactJID string, displayName string) (models.WhatsAppAccount, models.WhatsAppContact, string, error) {
	contactJID = strings.TrimSpace(contactJID)
	if contactJID == "" {
		return models.WhatsAppAccount{}, models.WhatsAppContact{}, "", errors.New("contact jid is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.WhatsAppAccount{}, models.WhatsAppContact{}, "", err
	}
	defer tx.Rollback()

	account, err := scanWhatsAppAccount(tx.QueryRowContext(
		ctx,
		`SELECT id, owner_user_id, mode, display_name, phone_jid, status, session_ref, access_policy, qr_code, created_at, updated_at
		 FROM whatsapp_accounts
		 WHERE id = $1 AND status <> 'revoked'
		 FOR UPDATE`,
		accountID,
	))
	if err != nil {
		return models.WhatsAppAccount{}, models.WhatsAppContact{}, "", err
	}

	contactID := newID("wact")
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO whatsapp_contacts (id, whatsapp_account_id, owner_user_id, contact_jid, display_name)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (whatsapp_account_id, contact_jid)
		 DO UPDATE SET display_name = COALESCE(NULLIF(EXCLUDED.display_name, ''), whatsapp_contacts.display_name),
		               updated_at = NOW()`,
		contactID,
		account.ID,
		account.OwnerUserID,
		contactJID,
		strings.TrimSpace(displayName),
	)
	if err != nil {
		return models.WhatsAppAccount{}, models.WhatsAppContact{}, "", err
	}

	contact, err := scanWhatsAppContact(tx.QueryRowContext(
		ctx,
		`SELECT id, whatsapp_account_id, owner_user_id, contact_jid, display_name, COALESCE(linked_user_id, ''), COALESCE(default_conversation_id, ''), allow_status, verification_attempts, created_at, updated_at
		 FROM whatsapp_contacts
		 WHERE whatsapp_account_id = $1 AND contact_jid = $2`,
		account.ID,
		contactJID,
	))
	if err != nil {
		return models.WhatsAppAccount{}, models.WhatsAppContact{}, "", err
	}

	resolvedUserID := account.OwnerUserID
	if account.Mode == "central_bot" {
		if contact.LinkedUserID != "" {
			resolvedUserID = contact.LinkedUserID
		}
	}

	if err := tx.Commit(); err != nil {
		return models.WhatsAppAccount{}, models.WhatsAppContact{}, "", err
	}
	return account, contact, resolvedUserID, nil
}

func (s *Store) GetOrCreateChannelConversation(ctx context.Context, userID string, channel string, threadID string, displayName string, title string, provider string, model string) (models.Conversation, error) {
	channel = strings.TrimSpace(channel)
	threadID = strings.TrimSpace(threadID)
	if channel == "" || threadID == "" {
		return models.Conversation{}, errors.New("channel and thread id are required")
	}

	now := time.Now().UTC()
	conversation := models.Conversation{
		ID:                 newID("con"),
		Title:              fallbackTitle(title),
		IsCloudSynced:      true,
		Channel:            channel,
		ChannelThreadID:    threadID,
		ChannelDisplayName: strings.TrimSpace(displayName),
		Provider:           strings.TrimSpace(provider),
		Model:              strings.TrimSpace(model),
		UpdatedAt:          now,
	}

	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO conversations (
		   id, user_id, title, provider, model, last_message_preview, message_count, created_at, updated_at, is_cloud_synced,
		   channel, channel_thread_id, channel_display_name
		 )
		 VALUES ($1, $2, $3, $4, $5, '', 0, $6, $6, true, $7, $8, $9)
		 ON CONFLICT (user_id, channel, channel_thread_id) WHERE channel_thread_id <> ''
		 DO UPDATE SET channel_display_name = COALESCE(NULLIF(EXCLUDED.channel_display_name, ''), conversations.channel_display_name),
		               provider = COALESCE(NULLIF(EXCLUDED.provider, ''), conversations.provider),
		               model = COALESCE(NULLIF(EXCLUDED.model, ''), conversations.model),
		               updated_at = conversations.updated_at`,
		conversation.ID,
		userID,
		conversation.Title,
		conversation.Provider,
		conversation.Model,
		now,
		conversation.Channel,
		conversation.ChannelThreadID,
		conversation.ChannelDisplayName,
	)
	if err != nil {
		return models.Conversation{}, err
	}

	return scanConversation(s.db.QueryRowContext(
		ctx,
		`SELECT id, title, pinned, is_cloud_synced, channel, channel_thread_id, channel_display_name, provider, model, last_message_preview, message_count, updated_at
		 FROM conversations
		 WHERE user_id = $1 AND channel = $2 AND channel_thread_id = $3`,
		userID,
		channel,
		threadID,
	))
}

func (s *Store) SetWhatsAppContactConversation(ctx context.Context, contactID string, conversationID string) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE whatsapp_contacts
		 SET default_conversation_id = $2, updated_at = NOW()
		 WHERE id = $1`,
		contactID,
		conversationID,
	)
	return err
}

func (s *Store) ListAllowedWhatsAppContacts(ctx context.Context, accountID string) ([]models.WhatsAppContact, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, whatsapp_account_id, owner_user_id, contact_jid, display_name, COALESCE(linked_user_id, ''), COALESCE(default_conversation_id, ''), allow_status, verification_attempts, created_at, updated_at
		 FROM whatsapp_contacts
		 WHERE whatsapp_account_id = $1 AND allow_status = 'allowed'
		 ORDER BY updated_at DESC`,
		accountID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	contacts := make([]models.WhatsAppContact, 0)
	for rows.Next() {
		contact, err := scanWhatsAppContact(rows)
		if err != nil {
			return nil, err
		}
		contacts = append(contacts, contact)
	}
	return contacts, rows.Err()
}

func (s *Store) ListLinkedWhatsAppContactsByUser(ctx context.Context, userID string) ([]models.WhatsAppContact, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, whatsapp_account_id, owner_user_id, contact_jid, display_name, COALESCE(linked_user_id, ''), COALESCE(default_conversation_id, ''), allow_status, verification_attempts, created_at, updated_at
		 FROM whatsapp_contacts
		 WHERE linked_user_id = $1 AND allow_status = 'allowed'
		 ORDER BY updated_at DESC
		 LIMIT 1`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	contacts := make([]models.WhatsAppContact, 0, 1)
	for rows.Next() {
		contact, err := scanWhatsAppContact(rows)
		if err != nil {
			return nil, err
		}
		contacts = append(contacts, contact)
	}
	return contacts, rows.Err()
}

func (s *Store) AllowWhatsAppContact(ctx context.Context, account models.WhatsAppAccount, phoneNumber string) (models.WhatsAppContact, error) {
	phoneNumber = normalizePhoneNumber(phoneNumber)
	if phoneNumber == "" {
		return models.WhatsAppContact{}, errors.New("phone number is required")
	}
	contactJID := phoneNumber + "@s.whatsapp.net"
	contactID := newID("wact")
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO whatsapp_contacts (id, whatsapp_account_id, owner_user_id, contact_jid, display_name, allow_status)
		 VALUES ($1, $2, $3, $4, $5, 'allowed')
		 ON CONFLICT (whatsapp_account_id, contact_jid)
		 DO UPDATE SET allow_status = 'allowed',
		               display_name = COALESCE(NULLIF(EXCLUDED.display_name, ''), whatsapp_contacts.display_name),
		               verification_attempts = 0,
		               updated_at = NOW()`,
		contactID,
		account.ID,
		account.OwnerUserID,
		contactJID,
		phoneNumber,
	)
	if err != nil {
		return models.WhatsAppContact{}, err
	}
	return scanWhatsAppContact(s.db.QueryRowContext(
		ctx,
		`SELECT id, whatsapp_account_id, owner_user_id, contact_jid, display_name, COALESCE(linked_user_id, ''), COALESCE(default_conversation_id, ''), allow_status, verification_attempts, created_at, updated_at
		 FROM whatsapp_contacts
		 WHERE whatsapp_account_id = $1 AND contact_jid = $2`,
		account.ID,
		contactJID,
	))
}

func (s *Store) RemoveAllowedWhatsAppContact(ctx context.Context, accountID string, contactID string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE whatsapp_contacts
		 SET allow_status = 'blocked',
		     linked_user_id = NULL,
		     default_conversation_id = NULL,
		     verification_attempts = 0,
		     updated_at = NOW()
		 WHERE id = $1 AND whatsapp_account_id = $2`,
		contactID,
		accountID,
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

func (s *Store) RevokeLinkedWhatsAppContact(ctx context.Context, userID string, contactID string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE whatsapp_contacts
		 SET allow_status = 'blocked',
		     linked_user_id = NULL,
		     default_conversation_id = NULL,
		     verification_attempts = 0,
		     updated_at = NOW()
		 WHERE id = $1 AND linked_user_id = $2`,
		contactID,
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

func (s *Store) CreateWhatsAppEvent(ctx context.Context, event models.WhatsAppEvent) (models.WhatsAppEvent, error) {
	event.ID = strings.TrimSpace(event.ID)
	if event.ID == "" {
		event.ID = newID("waevt")
	}
	event.WhatsAppAccountID = strings.TrimSpace(event.WhatsAppAccountID)
	event.ContactJID = strings.TrimSpace(event.ContactJID)
	event.SenderJID = strings.TrimSpace(event.SenderJID)
	event.Direction = normalizeWhatsAppEventDirection(event.Direction)
	event.MessageID = strings.TrimSpace(event.MessageID)
	event.ConversationID = strings.TrimSpace(event.ConversationID)
	event.Text = strings.TrimSpace(event.Text)
	event.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO whatsapp_events (id, whatsapp_account_id, contact_jid, sender_jid, direction, message_id, conversation_id, text, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		event.ID,
		event.WhatsAppAccountID,
		event.ContactJID,
		event.SenderJID,
		event.Direction,
		event.MessageID,
		event.ConversationID,
		event.Text,
		event.CreatedAt,
	)
	if err != nil {
		return models.WhatsAppEvent{}, err
	}
	return event, nil
}

func (s *Store) ListWhatsAppEvents(ctx context.Context, limit int) ([]models.WhatsAppEvent, error) {
	if limit <= 0 || limit > 300 {
		limit = 120
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, whatsapp_account_id, contact_jid, sender_jid, direction, message_id, conversation_id, text, created_at
		 FROM whatsapp_events
		 ORDER BY created_at DESC
		 LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]models.WhatsAppEvent, 0)
	for rows.Next() {
		event, err := scanWhatsAppEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func scanWhatsAppEvent(row rowScanner) (models.WhatsAppEvent, error) {
	var event models.WhatsAppEvent
	err := row.Scan(
		&event.ID,
		&event.WhatsAppAccountID,
		&event.ContactJID,
		&event.SenderJID,
		&event.Direction,
		&event.MessageID,
		&event.ConversationID,
		&event.Text,
		&event.CreatedAt,
	)
	if err != nil {
		return models.WhatsAppEvent{}, err
	}
	return event, nil
}

func scanWhatsAppAccount(row rowScanner) (models.WhatsAppAccount, error) {
	var account models.WhatsAppAccount
	err := row.Scan(
		&account.ID,
		&account.OwnerUserID,
		&account.Mode,
		&account.DisplayName,
		&account.PhoneJID,
		&account.Status,
		&account.SessionRef,
		&account.AccessPolicy,
		&account.QRCode,
		&account.CreatedAt,
		&account.UpdatedAt,
	)
	if err != nil {
		return models.WhatsAppAccount{}, err
	}
	return account, nil
}

func scanWhatsAppContact(row rowScanner) (models.WhatsAppContact, error) {
	var contact models.WhatsAppContact
	err := row.Scan(
		&contact.ID,
		&contact.WhatsAppAccountID,
		&contact.OwnerUserID,
		&contact.ContactJID,
		&contact.DisplayName,
		&contact.LinkedUserID,
		&contact.DefaultConversationID,
		&contact.AllowStatus,
		&contact.VerificationAttempts,
		&contact.CreatedAt,
		&contact.UpdatedAt,
	)
	if err != nil {
		return models.WhatsAppContact{}, err
	}
	return contact, nil
}

func (s *Store) UpdateWhatsAppAccountPolicy(ctx context.Context, userID string, accountID string, accessPolicy string) (models.WhatsAppAccount, error) {
	accessPolicy = normalizeWhatsAppAccessPolicy(accessPolicy)
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE whatsapp_accounts
		 SET access_policy = $3, updated_at = NOW()
		 WHERE id = $1 AND owner_user_id = $2 AND status <> 'revoked'`,
		accountID,
		userID,
		accessPolicy,
	)
	if err != nil {
		return models.WhatsAppAccount{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return models.WhatsAppAccount{}, err
	}
	if affected == 0 {
		return models.WhatsAppAccount{}, sql.ErrNoRows
	}
	return s.GetWhatsAppAccountByID(ctx, accountID)
}

func (s *Store) CreateWhatsAppLinkCode(ctx context.Context, userID string, phoneNumber string) (models.WhatsAppLinkCode, error) {
	phoneNumber = normalizePhoneNumber(phoneNumber)
	if phoneNumber == "" {
		return models.WhatsAppLinkCode{}, errors.New("phone number is required")
	}
	var linkedCount int
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*)
		 FROM whatsapp_contacts
		 WHERE linked_user_id = $1 AND allow_status = 'allowed'`,
		userID,
	).Scan(&linkedCount); err != nil {
		return models.WhatsAppLinkCode{}, err
	}
	if linkedCount > 0 {
		return models.WhatsAppLinkCode{}, errors.New("only one WhatsApp number can be linked; revoke the current number first")
	}
	code, err := randomNumericCode(6)
	if err != nil {
		return models.WhatsAppLinkCode{}, err
	}
	now := time.Now().UTC()
	link := models.WhatsAppLinkCode{
		ID:          newID("walink"),
		UserID:      userID,
		PhoneNumber: phoneNumber,
		Code:        code,
		Status:      "pending",
		ExpiresAt:   now.Add(10 * time.Minute),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO whatsapp_link_codes (id, user_id, phone_number, code, status, expires_at, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, 'pending', $5, $6, $6)`,
		link.ID,
		link.UserID,
		link.PhoneNumber,
		link.Code,
		link.ExpiresAt,
		now,
	)
	if err != nil {
		return models.WhatsAppLinkCode{}, err
	}
	return link, nil
}

func (s *Store) VerifyWhatsAppLinkCode(ctx context.Context, accountID string, contactID string, contactJID string, text string) (string, bool, int, error) {
	phoneNumber := normalizePhoneNumber(contactJIDUser(contactJID))
	code := strings.TrimSpace(text)
	if phoneNumber == "" || code == "" {
		return "", false, 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, 0, err
	}
	defer tx.Rollback()

	var link models.WhatsAppLinkCode
	err = tx.QueryRowContext(
		ctx,
		`SELECT id, user_id, phone_number, code, status, attempts, expires_at, matched_contact_jid, created_at, updated_at
		 FROM whatsapp_link_codes
		 WHERE phone_number = $1 AND status = 'pending'
		 ORDER BY created_at DESC
		 LIMIT 1
		 FOR UPDATE`,
		phoneNumber,
	).Scan(
		&link.ID,
		&link.UserID,
		&link.PhoneNumber,
		&link.Code,
		&link.Status,
		&link.Attempts,
		&link.ExpiresAt,
		&link.MatchedContactJID,
		&link.CreatedAt,
		&link.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, 0, nil
	}
	if err != nil {
		return "", false, 0, err
	}
	if time.Now().UTC().After(link.ExpiresAt) {
		_, _ = tx.ExecContext(ctx, `UPDATE whatsapp_link_codes SET status = 'expired', updated_at = NOW() WHERE id = $1`, link.ID)
		if err := tx.Commit(); err != nil {
			return "", false, 0, err
		}
		return "", false, link.Attempts, nil
	}

	if code != link.Code {
		attempts := link.Attempts + 1
		status := "pending"
		if attempts >= 3 {
			status = "locked"
		}
		_, err = tx.ExecContext(
			ctx,
			`UPDATE whatsapp_link_codes SET attempts = $2, status = $3, updated_at = NOW() WHERE id = $1`,
			link.ID,
			attempts,
			status,
		)
		if err != nil {
			return "", false, 0, err
		}
		_, _ = tx.ExecContext(
			ctx,
			`UPDATE whatsapp_contacts SET verification_attempts = $2, updated_at = NOW() WHERE id = $1`,
			contactID,
			attempts,
		)
		if err := tx.Commit(); err != nil {
			return "", false, 0, err
		}
		return "", false, attempts, nil
	}

	_, err = tx.ExecContext(
		ctx,
		`UPDATE whatsapp_link_codes
		 SET status = 'verified', matched_contact_jid = $2, updated_at = NOW()
		 WHERE id = $1`,
		link.ID,
		contactJID,
	)
	if err != nil {
		return "", false, 0, err
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE whatsapp_contacts
		 SET allow_status = 'blocked',
		     linked_user_id = NULL,
		     default_conversation_id = NULL,
		     verification_attempts = 0,
		     updated_at = NOW()
		 WHERE linked_user_id = $1 AND id <> $2`,
		link.UserID,
		contactID,
	)
	if err != nil {
		return "", false, 0, err
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE whatsapp_contacts
		 SET linked_user_id = $2, allow_status = 'allowed', verification_attempts = 0, updated_at = NOW()
		 WHERE id = $1`,
		contactID,
		link.UserID,
	)
	if err != nil {
		return "", false, 0, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, 0, err
	}
	return link.UserID, true, 0, nil
}

func (s *Store) WhatsAppLinkCodeBelongsToDifferentContact(ctx context.Context, contactJID string, text string) (bool, error) {
	phoneNumber := normalizePhoneNumber(contactJIDUser(contactJID))
	code := strings.TrimSpace(text)
	if phoneNumber == "" || code == "" {
		return false, nil
	}

	var matchedPhone string
	err := s.db.QueryRowContext(
		ctx,
		`SELECT phone_number
		 FROM whatsapp_link_codes
		 WHERE code = $1 AND status = 'pending' AND expires_at > NOW()
		 ORDER BY created_at DESC
		 LIMIT 1`,
		code,
	).Scan(&matchedPhone)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return normalizePhoneNumber(matchedPhone) != phoneNumber, nil
}

func normalizeWhatsAppMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "central_bot":
		return "central_bot"
	default:
		return "user_linked"
	}
}

func normalizeWhatsAppStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "connected", "disconnected", "revoked":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return "pending_qr"
	}
}

func normalizeWhatsAppAccessPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "linked_only", "deny_unknown":
		return strings.ToLower(strings.TrimSpace(policy))
	default:
		return "allow_all"
	}
}

func normalizeWhatsAppEventDirection(direction string) string {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "incoming", "outgoing", "system":
		return strings.ToLower(strings.TrimSpace(direction))
	default:
		return "system"
	}
}

var nonDigitPattern = regexp.MustCompile(`\D+`)

func normalizePhoneNumber(value string) string {
	return nonDigitPattern.ReplaceAllString(value, "")
}

func contactJIDUser(jid string) string {
	if index := strings.Index(jid, "@"); index >= 0 {
		return jid[:index]
	}
	return jid
}

func randomNumericCode(length int) (string, error) {
	if length <= 0 {
		length = 6
	}
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	for i, b := range bytes {
		bytes[i] = '0' + (b % 10)
	}
	if bytes[0] == '0' {
		bytes[0] = '1'
	}
	return fmt.Sprintf("%s", bytes), nil
}
