package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"be-miawai/internal/database"
	"be-miawai/internal/models"
)

type WhatsAppInboundInput struct {
	AccountID   string
	ContactJID  string
	SenderJID   string
	MessageID   string
	Text        string
	Images      []models.ChatImageInput
	Timestamp   string
	DisplayName string
	GroupJID    string
}

type WhatsAppInboundResult struct {
	ConversationID string
	ReplyText      string
	ShouldReply    bool
}

type WhatsAppStatusInput struct {
	AccountID   string
	Status      string
	PhoneJID    string
	DisplayName string
	QRCode      string
}

func (s *Server) listWhatsAppAccounts(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessWhatsAppRead) {
		writeError(w, http.StatusForbidden, "whatsapp access is not allowed for this role")
		return
	}
	accounts, err := s.store.ListWhatsAppAccountsByUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load whatsapp accounts")
		return
	}
	if !hasAccess(user, accessAdmin) && strings.TrimSpace(s.cfg.WhatsAppOwnerUserID) != "" {
		central, err := s.store.GetOrCreateCentralWhatsAppAccount(r.Context(), s.cfg.WhatsAppOwnerUserID)
		if err != nil {
			log.Printf("load central whatsapp account failed: %v", err)
		} else if strings.EqualFold(central.Status, "connected") && strings.TrimSpace(central.PhoneJID) != "" {
			central.QRCode = ""
			central.SessionRef = ""
			accounts = append(accounts, central)
		}
	}
	writeJSON(w, http.StatusOK, map[string][]models.WhatsAppAccount{"accounts": accounts})
}

func (s *Server) createWhatsAppAccount(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessWhatsAppWrite) {
		writeError(w, http.StatusForbidden, "whatsapp connection is not allowed for this role")
		return
	}
	var body struct {
		Mode        string `json:"mode"`
		DisplayName string `json:"displayName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	var account models.WhatsAppAccount
	var err error
	if strings.EqualFold(strings.TrimSpace(body.Mode), "central_bot") && hasAccess(user, accessAdmin) {
		account, err = s.store.GetOrCreateCentralWhatsAppAccount(r.Context(), user.ID)
	} else {
		account, err = s.store.CreateWhatsAppAccount(r.Context(), user.ID, body.Mode, body.DisplayName)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create whatsapp account")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]models.WhatsAppAccount{"account": account})
}

func (s *Server) deleteWhatsAppAccount(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessWhatsAppWrite) {
		writeError(w, http.StatusForbidden, "whatsapp unlink is not allowed for this role")
		return
	}
	if err := s.store.RevokeWhatsAppAccount(r.Context(), user.ID, r.PathValue("id")); err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "whatsapp account not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to revoke whatsapp account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) updateWhatsAppAccount(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessWhatsAppWrite) {
		writeError(w, http.StatusForbidden, "whatsapp settings are not allowed for this role")
		return
	}
	var body struct {
		AccessPolicy string `json:"accessPolicy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	account, err := s.store.UpdateWhatsAppAccountPolicy(r.Context(), user.ID, r.PathValue("id"), body.AccessPolicy)
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "whatsapp account not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update whatsapp account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]models.WhatsAppAccount{"account": account})
}

func (s *Server) refreshWhatsAppPairing(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessWhatsAppWrite) {
		writeError(w, http.StatusForbidden, "whatsapp pairing refresh is not allowed for this role")
		return
	}
	accountID := strings.TrimSpace(r.PathValue("id"))
	account, err := s.store.GetWhatsAppAccountByID(r.Context(), accountID)
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "whatsapp account not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load whatsapp account")
		return
	}
	if account.OwnerUserID != user.ID && !hasAccess(user, accessAdmin) {
		writeError(w, http.StatusForbidden, "whatsapp account does not belong to this user")
		return
	}
	if s.whatsAppRefresh == nil {
		writeError(w, http.StatusServiceUnavailable, "whatsapp runner is not available")
		return
	}
	if err := s.whatsAppRefresh(r.Context(), account.ID); err != nil {
		log.Printf("refresh whatsapp pairing account=%s failed: %v", account.ID, err)
		writeError(w, http.StatusServiceUnavailable, "failed to refresh whatsapp pairing")
		return
	}
	refreshed, err := s.store.GetWhatsAppAccountByID(r.Context(), account.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to refresh whatsapp account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]models.WhatsAppAccount{"account": refreshed})
}

func (s *Server) createWhatsAppLinkCode(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessWhatsAppWrite) {
		writeError(w, http.StatusForbidden, "whatsapp linking is not allowed for this role")
		return
	}
	var body struct {
		PhoneNumber string `json:"phoneNumber"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	link, err := s.store.CreateWhatsAppLinkCode(r.Context(), user.ID, body.PhoneNumber)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]models.WhatsAppLinkCode{"linkCode": link})
}

func (s *Server) listLinkedWhatsAppContacts(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessWhatsAppRead) {
		writeError(w, http.StatusForbidden, "whatsapp access is not allowed for this role")
		return
	}
	contacts, err := s.store.ListLinkedWhatsAppContactsByUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load linked whatsapp contacts")
		return
	}
	writeJSON(w, http.StatusOK, map[string][]models.WhatsAppContact{"contacts": contacts})
}

func (s *Server) revokeLinkedWhatsAppContact(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessWhatsAppWrite) {
		writeError(w, http.StatusForbidden, "whatsapp unlink is not allowed for this role")
		return
	}
	if err := s.store.RevokeLinkedWhatsAppContact(r.Context(), user.ID, r.PathValue("id")); err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "linked whatsapp number not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to revoke whatsapp number")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) adminWhatsAppConversations(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessAdmin) || !hasAccess(user, accessWhatsAppRead) {
		writeError(w, http.StatusForbidden, "admin whatsapp access is not allowed for this role")
		return
	}
	conversations, err := s.store.ListWhatsAppConversations(r.Context(), 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load whatsapp conversations")
		return
	}
	writeJSON(w, http.StatusOK, map[string][]models.Conversation{"conversations": conversations})
}

func (s *Server) adminWhatsAppConversation(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessAdmin) || !hasAccess(user, accessWhatsAppRead) || !hasAccess(user, accessChatRead) {
		writeError(w, http.StatusForbidden, "admin whatsapp monitoring is not allowed for this role")
		return
	}
	conversation, err := s.store.GetWhatsAppConversationByID(r.Context(), r.PathValue("id"))
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "whatsapp conversation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load whatsapp conversation")
		return
	}
	messages, err := s.chatStorage.GetMessages(conversation.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load whatsapp messages")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"conversation": conversation,
		"messages":     messages,
	})
}

func (s *Server) adminWhatsAppEvents(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessAdmin) || !hasAccess(user, accessWhatsAppRead) {
		writeError(w, http.StatusForbidden, "admin whatsapp monitoring is not allowed for this role")
		return
	}
	events, err := s.store.ListWhatsAppEvents(r.Context(), 160)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load whatsapp events")
		return
	}
	writeJSON(w, http.StatusOK, map[string][]models.WhatsAppEvent{"events": events})
}

func (s *Server) adminWhatsAppAllowList(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessAdmin) || !hasAccess(user, accessWhatsAppRead) {
		writeError(w, http.StatusForbidden, "admin whatsapp access is not allowed for this role")
		return
	}
	account, err := s.store.GetOrCreateCentralWhatsAppAccount(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load central whatsapp account")
		return
	}
	contacts, err := s.store.ListAllowedWhatsAppContacts(r.Context(), account.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load allowed whatsapp contacts")
		return
	}
	writeJSON(w, http.StatusOK, map[string][]models.WhatsAppContact{"contacts": contacts})
}

func (s *Server) adminWhatsAppAllowContact(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessAdmin) || !hasAccess(user, accessWhatsAppWrite) {
		writeError(w, http.StatusForbidden, "admin whatsapp write access is not allowed for this role")
		return
	}
	var body struct {
		PhoneNumber string `json:"phoneNumber"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	account, err := s.store.GetOrCreateCentralWhatsAppAccount(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load central whatsapp account")
		return
	}
	contact, err := s.store.AllowWhatsAppContact(r.Context(), account, body.PhoneNumber)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]models.WhatsAppContact{"contact": contact})
}

func (s *Server) adminWhatsAppRemoveAllowedContact(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessAdmin) || !hasAccess(user, accessWhatsAppWrite) {
		writeError(w, http.StatusForbidden, "admin whatsapp write access is not allowed for this role")
		return
	}
	account, err := s.store.GetOrCreateCentralWhatsAppAccount(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load central whatsapp account")
		return
	}
	if err := s.store.RemoveAllowedWhatsAppContact(r.Context(), account.ID, r.PathValue("id")); err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "allowed whatsapp contact not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to remove allowed whatsapp contact")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) whatsAppAccountStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireWhatsAppService(w, r) {
		return
	}
	var body struct {
		Status      string `json:"status"`
		PhoneJID    string `json:"phoneJid"`
		DisplayName string `json:"displayName"`
		QRCode      string `json:"qrCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	account, err := s.UpdateWhatsAppStatus(r.Context(), WhatsAppStatusInput{
		AccountID:   r.PathValue("id"),
		Status:      body.Status,
		PhoneJID:    body.PhoneJID,
		DisplayName: body.DisplayName,
		QRCode:      body.QRCode,
	})
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "whatsapp account not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update whatsapp account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]models.WhatsAppAccount{"account": account})
}

func (s *Server) UpdateWhatsAppStatus(ctx context.Context, input WhatsAppStatusInput) (models.WhatsAppAccount, error) {
	return s.store.UpsertWhatsAppAccountStatus(ctx, input.AccountID, input.Status, input.PhoneJID, input.DisplayName, input.QRCode)
}

func (s *Server) whatsAppInbound(w http.ResponseWriter, r *http.Request) {
	if !s.requireWhatsAppService(w, r) {
		return
	}
	var body struct {
		AccountID   string `json:"accountId"`
		ContactJID  string `json:"contactJid"`
		SenderJID   string `json:"senderJid"`
		MessageID   string `json:"messageId"`
		Text        string `json:"text"`
		Timestamp   string `json:"timestamp"`
		DisplayName string `json:"displayName"`
		GroupJID    string `json:"groupJid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	result, err := s.HandleWhatsAppInbound(r.Context(), WhatsAppInboundInput{
		AccountID:   body.AccountID,
		ContactJID:  body.ContactJID,
		SenderJID:   body.SenderJID,
		MessageID:   body.MessageID,
		Text:        body.Text,
		Timestamp:   body.Timestamp,
		DisplayName: body.DisplayName,
		GroupJID:    body.GroupJID,
	})
	if err != nil {
		if errors.Is(err, errWhatsAppBadRequest) {
			writeError(w, http.StatusBadRequest, "accountId, contactJid, and text are required")
			return
		}
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "whatsapp account not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"conversationId": result.ConversationID,
		"shouldReply":    result.ShouldReply,
		"replyText":      result.ReplyText,
	})
}

var errWhatsAppBadRequest = errors.New("invalid whatsapp inbound request")

const maxWhatsAppInboundAge = 5 * time.Minute

func (s *Server) HandleWhatsAppInbound(ctx context.Context, input WhatsAppInboundInput) (WhatsAppInboundResult, error) {
	text := strings.TrimSpace(input.Text)
	if strings.TrimSpace(input.AccountID) == "" || strings.TrimSpace(input.ContactJID) == "" || (text == "" && len(input.Images) == 0) {
		return WhatsAppInboundResult{}, errWhatsAppBadRequest
	}
	if !isRecentWhatsAppInbound(input.Timestamp, time.Now()) {
		log.Printf("whatsapp inbound ignored account=%s contact=%s message=%s reason=stale timestamp=%q", input.AccountID, input.ContactJID, input.MessageID, input.Timestamp)
		return WhatsAppInboundResult{}, nil
	}
	_, _ = s.store.CreateWhatsAppEvent(ctx, models.WhatsAppEvent{
		WhatsAppAccountID: input.AccountID,
		ContactJID:        input.ContactJID,
		SenderJID:         input.SenderJID,
		Direction:         "incoming",
		MessageID:         input.MessageID,
		Text:              text,
	})

	account, contact, resolvedUserID, err := s.store.ResolveWhatsAppContact(ctx, input.AccountID, input.ContactJID, input.DisplayName)
	if err != nil {
		if database.IsNotFound(err) {
			return WhatsAppInboundResult{}, err
		}
		return WhatsAppInboundResult{}, errors.New("failed to resolve whatsapp contact")
	}
	if !strings.EqualFold(account.Status, "connected") {
		log.Printf("whatsapp inbound ignored account=%s contact=%s message=%s reason=account_not_connected status=%s", account.ID, contact.ContactJID, input.MessageID, account.Status)
		return WhatsAppInboundResult{}, nil
	}
	if account.Mode == "central_bot" && contact.AllowStatus != "allowed" {
		if contact.AllowStatus == "blocked" && !isWhatsAppLinkCodeText(text) {
			return WhatsAppInboundResult{}, nil
		}
		_, matched, attempts, err := s.store.VerifyWhatsAppLinkCode(ctx, account.ID, contact.ID, contact.ContactJID, text)
		if err != nil {
			return WhatsAppInboundResult{}, errors.New("failed to verify whatsapp link code")
		}
		if matched {
			result := WhatsAppInboundResult{
				ShouldReply: true,
				ReplyText:   "Nomor WhatsApp berhasil terhubung ke akun Miaw. Sekarang kamu bisa chat dengan Miaw dari WhatsApp ini.",
			}
			s.logWhatsAppOutgoing(ctx, input, result)
			return result, nil
		}
		reply := "Nomor WhatsApp ini belum terhubung ke akun Miaw. Buka Miaw web, Settings > WhatsApp, generate kode, lalu kirim kode itu ke sini."
		if attempts > 0 && attempts < 3 {
			reply = "Kode verifikasi WhatsApp belum cocok. Sisa percobaan: " + fmtInt(3-attempts) + "."
		} else if attempts >= 3 {
			reply = "Kode verifikasi WhatsApp terkunci setelah 3 percobaan. Generate kode baru dari Miaw web."
		}
		result := WhatsAppInboundResult{
			ShouldReply: true,
			ReplyText:   reply,
		}
		s.logWhatsAppOutgoing(ctx, input, result)
		return result, nil
	}
	if account.Mode == "central_bot" && isWhatsAppLinkCodeText(text) {
		differentContact, err := s.store.WhatsAppLinkCodeBelongsToDifferentContact(ctx, contact.ContactJID, text)
		if err != nil {
			return WhatsAppInboundResult{}, errors.New("failed to check whatsapp link code owner")
		}
		if differentContact {
			return WhatsAppInboundResult{}, nil
		}
	}
	if account.Mode == "user_linked" && !s.whatsAppContactAllowed(account, contact) {
		return WhatsAppInboundResult{}, nil
	}
	user, err := s.store.GetUserByID(ctx, resolvedUserID)
	if err != nil {
		return WhatsAppInboundResult{}, errors.New("failed to load resolved user")
	}

	reply, conversation, err := s.processWhatsAppMessage(ctx, user, account, contact, text, input.Images)
	if err != nil {
		log.Printf("whatsapp inbound failed account=%s contact=%s user=%s error=%q", account.ID, contact.ContactJID, user.ID, err.Error())
		result := WhatsAppInboundResult{
			ConversationID: conversation.ID,
			ShouldReply:    true,
			ReplyText:      "Maaf, Miaw sedang tidak bisa menjawab. Coba lagi sebentar ya.",
		}
		s.logWhatsAppOutgoing(ctx, input, result)
		return result, nil
	}

	result := WhatsAppInboundResult{
		ConversationID: conversation.ID,
		ShouldReply:    true,
		ReplyText:      reply,
	}
	s.logWhatsAppOutgoing(ctx, input, result)
	return result, nil
}

func (s *Server) logWhatsAppOutgoing(ctx context.Context, input WhatsAppInboundInput, result WhatsAppInboundResult) {
	if !result.ShouldReply || strings.TrimSpace(result.ReplyText) == "" {
		return
	}
	_, _ = s.store.CreateWhatsAppEvent(ctx, models.WhatsAppEvent{
		WhatsAppAccountID: input.AccountID,
		ContactJID:        input.ContactJID,
		SenderJID:         "",
		Direction:         "outgoing",
		MessageID:         input.MessageID,
		ConversationID:    result.ConversationID,
		Text:              result.ReplyText,
	})
}

func (s *Server) GetOrCreateCentralWhatsAppAccount(ctx context.Context) (models.WhatsAppAccount, error) {
	return s.store.GetOrCreateCentralWhatsAppAccount(ctx, s.cfg.WhatsAppOwnerUserID)
}

func (s *Server) whatsAppContactAllowed(account models.WhatsAppAccount, contact models.WhatsAppContact) bool {
	switch strings.ToLower(strings.TrimSpace(account.AccessPolicy)) {
	case "linked_only":
		return contact.AllowStatus == "allowed"
	case "deny_unknown":
		return contact.AllowStatus == "allowed"
	default:
		return true
	}
}

func fmtInt(value int) string {
	return strconv.Itoa(value)
}

func isWhatsAppLinkCodeText(text string) bool {
	text = strings.TrimSpace(text)
	if len(text) != 6 {
		return false
	}
	for _, char := range text {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func isRecentWhatsAppInbound(rawTimestamp string, now time.Time) bool {
	timestamp, err := time.Parse(time.RFC3339, strings.TrimSpace(rawTimestamp))
	if err != nil || timestamp.IsZero() {
		return false
	}
	age := now.Sub(timestamp)
	return age >= -time.Minute && age <= maxWhatsAppInboundAge
}

func (s *Server) processWhatsAppMessage(ctx context.Context, user models.User, account models.WhatsAppAccount, contact models.WhatsAppContact, text string, images []models.ChatImageInput) (string, models.Conversation, error) {
	settings, err := s.loadRuntimeSettings(ctx, user.ID)
	if err != nil {
		return "", models.Conversation{}, err
	}
	settings = applyRuntimeDefaults(settings)
	if strings.TrimSpace(settings.APIKey) == "" && s.cfg.ManagedAIApiKey != "" {
		settings.APIKey = s.cfg.ManagedAIApiKey
		if user.Plan != "pro" {
			settings.Models.Active = "gpt-4o-mini"
		}
	}
	if strings.TrimSpace(settings.BaseURL) == "" || strings.TrimSpace(settings.Models.Active) == "" || strings.TrimSpace(settings.APIKey) == "" {
		return "", models.Conversation{}, errors.New("runtime settings are incomplete or missing API key")
	}
	webResearchAllowed := false
	if !isStatusCommand(text) {
		var err error
		webResearchAllowed, err = s.checkUserQuota(ctx, user, len(images), false)
		if err != nil {
			return "", models.Conversation{}, err
		}
	}

	threadID := "wa:" + account.ID + ":" + contact.ContactJID
	displayName := firstNonEmpty(contact.DisplayName, contact.ContactJID)
	conversation, err := s.store.GetOrCreateChannelConversation(
		ctx,
		user.ID,
		"whatsapp",
		threadID,
		displayName,
		"WhatsApp: "+displayName,
		settings.Provider,
		settings.Models.Active,
	)
	if err != nil {
		return "", models.Conversation{}, err
	}
	_ = s.store.SetWhatsAppContactConversation(ctx, contact.ID, conversation.ID)

	messages, err := s.loadConversationMessages(ctx, user.ID, conversation)
	if err != nil {
		return "", conversation, err
	}
	savedImages, err := s.saveChatImages(ctx, user.ID, conversation.ID, "", images)
	if err != nil {
		return "", conversation, err
	}
	userMessage := newConversationMessage(conversation.ID, "user", text)
	userMessage.ImageURLs = publicURLs(savedImages)
	messages = append(messages, userMessage)
	for _, image := range savedImages {
		_ = s.store.InsertChatUpload(ctx, user.ID, conversation.ID, userMessage.ID, image.Name, image.MimeType, image.LocalPath, image.PublicURL, image.SizeBytes)
	}
	if err := s.chatStorage.SaveMessages(conversation.ID, messages); err != nil {
		return "", conversation, err
	}
	if err := s.store.UpdateConversationStats(ctx, user.ID, conversation.ID, text, len(messages)); err != nil {
		return "", conversation, err
	}

	result, err := s.processChatReply(
		ctx,
		chatReplyInput{
			User:           user,
			Conversation:   conversation,
			Settings:       settings,
			Messages:       messages,
			UserMessage:    userMessage,
			SavedImages:    savedImages,
			Web:            false,
			WebAllowed:     webResearchAllowed,
			Stream:         false,
			ResearchLogTag: "whatsapp",
		},
	)
	if err != nil {
		return "", conversation, err
	}
	return result.AssistantMessage.Content, result.Conversation, nil
}

func (s *Server) checkUserQuota(ctx context.Context, user models.User, imageCount int, webRequested bool) (bool, error) {
	hasCredit, err := s.hasRemainingWeeklyUsageCredit(ctx, user)
	if err != nil {
		return false, err
	}
	if !hasCredit {
		return false, errors.New("weekly usage credit exhausted")
	}

	usage, err := s.store.GetDailyUsage(ctx, user.ID)
	if err != nil {
		return false, err
	}
	if user.Plan != "pro" {
		if s.cfg.FreeUserDailyPromptLimit > 0 && usage.PromptCount >= s.cfg.FreeUserDailyPromptLimit {
			return false, errors.New("free user daily chat limit reached")
		}
		if s.cfg.FreeUserDailyImageLimit > 0 && usage.ImageCount+imageCount > s.cfg.FreeUserDailyImageLimit {
			return false, errors.New("free user daily image limit reached")
		}
		if webRequested {
			return false, errors.New("web research is available on pro")
		}
		return false, nil
	}
	if s.cfg.ProUserDailyPromptLimit > 0 && usage.PromptCount >= s.cfg.ProUserDailyPromptLimit {
		return false, errors.New("pro user daily chat limit reached")
	}
	if s.cfg.ProUserDailyImageLimit > 0 && usage.ImageCount+imageCount > s.cfg.ProUserDailyImageLimit {
		return false, errors.New("pro user daily image limit reached")
	}
	if s.cfg.ProUserDailyWebResearchLimit > 0 && usage.ResearchCount >= s.cfg.ProUserDailyWebResearchLimit {
		return false, errors.New("pro user daily web research limit reached")
	}
	return true, nil
}

func (s *Server) requireWhatsAppService(w http.ResponseWriter, r *http.Request) bool {
	expected := strings.TrimSpace(s.cfg.WhatsAppInternalToken)
	if expected == "" {
		writeError(w, http.StatusServiceUnavailable, "whatsapp internal token is not configured")
		return false
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	if token == "" || token != expected {
		writeError(w, http.StatusUnauthorized, "invalid whatsapp service token")
		return false
	}
	return true
}
