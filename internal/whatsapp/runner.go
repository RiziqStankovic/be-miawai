package whatsapp

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"be-miawai/internal/handlers"
	"be-miawai/internal/models"

	"github.com/mdp/qrterminal/v3"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

type Backend interface {
	GetOrCreateCentralWhatsAppAccount(ctx context.Context) (models.WhatsAppAccount, error)
	HandleWhatsAppInbound(ctx context.Context, input handlers.WhatsAppInboundInput) (handlers.WhatsAppInboundResult, error)
	UpdateWhatsAppStatus(ctx context.Context, input handlers.WhatsAppStatusInput) (models.WhatsAppAccount, error)
}

type Config struct {
	Enabled      bool
	ListenGroups bool
	SessionDB    string
}

type Runner struct {
	cfg     Config
	backend Backend
	account models.WhatsAppAccount
	db      *sql.DB
	client  *whatsmeow.Client

	healthMu      sync.Mutex
	healthCancel  context.CancelFunc
	lastEventAt   time.Time
	lastMessageAt time.Time
	lastStatus    string
}

func NewRunner(cfg Config, backend Backend) *Runner {
	return &Runner{cfg: cfg, backend: backend}
}

func (r *Runner) Start(ctx context.Context) error {
	if !r.cfg.Enabled {
		log.Printf("whatsapp embedded disabled")
		return nil
	}
	if r.backend == nil {
		return errors.New("whatsapp backend is required")
	}
	account, err := r.backend.GetOrCreateCentralWhatsAppAccount(ctx)
	if err != nil {
		return err
	}
	r.account = account

	sessionDB := strings.TrimSpace(r.cfg.SessionDB)
	if sessionDB == "" {
		sessionDB = "data/whatsapp.db"
	}
	if err := os.MkdirAll(filepath.Dir(sessionDB), 0o755); err != nil {
		return err
	}
	db, err := openWhatsAppDB(sessionDB)
	if err != nil {
		return err
	}
	r.db = db

	container := sqlstore.NewWithDB(db, "sqlite3", waLog.Stdout("WhatsAppDB", "WARN", true))
	if err := container.Upgrade(ctx); err != nil {
		return err
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return err
	}

	client := whatsmeow.NewClient(device, waLog.Stdout("WhatsApp", "INFO", true))
	r.client = client
	client.AddEventHandler(r.handleEvent)

	if client.Store.ID == nil {
		qrChan, err := client.GetQRChannel(ctx)
		if err != nil {
			return err
		}
		go r.handleQR(ctx, qrChan)
	}

	if err := client.Connect(); err != nil {
		return err
	}
	r.reportStatus(context.Background(), "connected")
	r.markHealth("connected", false)
	healthCtx, cancel := context.WithCancel(ctx)
	r.healthCancel = cancel
	go r.healthLoop(healthCtx)
	log.Printf("miaw wa embedded running account=%s listen_groups=%t", r.account.ID, r.cfg.ListenGroups)
	return nil
}

func (r *Runner) Stop(ctx context.Context) {
	if r.healthCancel != nil {
		r.healthCancel()
	}
	if r.client != nil {
		r.client.Disconnect()
		r.reportStatus(ctx, "disconnected")
		r.markHealth("disconnected", false)
	}
	if r.db != nil {
		if err := r.db.Close(); err != nil {
			log.Printf("close whatsapp db: %v", err)
		}
	}
}

func openWhatsAppDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func sqliteDSN(path string) string {
	return "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
}

func (r *Runner) handleQR(ctx context.Context, qrChan <-chan whatsmeow.QRChannelItem) {
	for evt := range qrChan {
		if evt.Event == "code" {
			qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			r.reportQRCode(ctx, evt.Code)
			continue
		}
		if evt.Event == "success" {
			r.reportStatus(ctx, "connected")
		} else if evt.Event == "timeout" {
			r.reportStatus(ctx, "pending_qr")
		}
		log.Printf("whatsapp qr event: %s", evt.Event)
	}
}

func (r *Runner) handleEvent(evt any) {
	msg, ok := evt.(*events.Message)
	if !ok {
		r.handleHealthEvent(evt)
		log.Printf("whatsapp event account=%s type=%T", r.account.ID, evt)
		return
	}
	r.markHealth("", true)
	log.Printf(
		"whatsapp message event account=%s id=%s chat=%s sender=%s from_me=%t chat_server=%s push_name=%q",
		r.account.ID,
		msg.Info.ID,
		msg.Info.Chat,
		msg.Info.Sender,
		msg.Info.IsFromMe,
		msg.Info.Chat.Server,
		msg.Info.PushName,
	)
	if msg.Info.IsFromMe {
		log.Printf("whatsapp message ignored account=%s id=%s reason=from_me chat=%s", r.account.ID, msg.Info.ID, msg.Info.Chat)
		return
	}
	if msg.Info.Chat.Server == types.GroupServer && !r.cfg.ListenGroups {
		log.Printf("whatsapp message ignored account=%s id=%s reason=group_disabled chat=%s", r.account.ID, msg.Info.ID, msg.Info.Chat)
		return
	}
	text := strings.TrimSpace(extractText(msg))
	hasImage := msg.Message != nil && msg.Message.GetImageMessage() != nil
	if text == "" && !hasImage {
		log.Printf("whatsapp message ignored account=%s id=%s reason=empty_text chat=%s", r.account.ID, msg.Info.ID, msg.Info.Chat)
		return
	}
	go r.reply(context.Background(), msg, text)
}

func (r *Runner) handleHealthEvent(evt any) {
	r.markHealth("", false)
	switch event := evt.(type) {
	case *events.Connected:
		log.Printf("whatsapp health account=%s status=connected", r.account.ID)
		r.reportStatus(context.Background(), "connected")
		r.markHealth("connected", false)
	case *events.Disconnected:
		log.Printf("whatsapp health account=%s status=disconnected", r.account.ID)
		r.reportStatus(context.Background(), "disconnected")
		r.markHealth("disconnected", false)
	case *events.KeepAliveTimeout:
		log.Printf("whatsapp health account=%s status=keepalive_timeout error_count=%d last_success=%s", r.account.ID, event.ErrorCount, event.LastSuccess.Format(time.RFC3339))
	case *events.KeepAliveRestored:
		log.Printf("whatsapp health account=%s status=keepalive_restored", r.account.ID)
		r.reportStatus(context.Background(), "connected")
		r.markHealth("connected", false)
	case *events.LoggedOut:
		log.Printf("whatsapp health account=%s status=logged_out on_connect=%t reason=%s", r.account.ID, event.OnConnect, event.Reason.String())
		r.reportStatus(context.Background(), "disconnected")
		r.markHealth("disconnected", false)
	case *events.StreamReplaced:
		log.Printf("whatsapp health account=%s status=stream_replaced reason=%q", r.account.ID, event.PermanentDisconnectDescription())
		r.reportStatus(context.Background(), "disconnected")
		r.markHealth("disconnected", false)
	case *events.ClientOutdated:
		log.Printf("whatsapp health account=%s status=client_outdated reason=%q", r.account.ID, event.PermanentDisconnectDescription())
		r.reportStatus(context.Background(), "disconnected")
		r.markHealth("disconnected", false)
	case *events.TemporaryBan:
		log.Printf("whatsapp health account=%s status=temporary_ban detail=%q", r.account.ID, event.String())
		r.reportStatus(context.Background(), "disconnected")
		r.markHealth("disconnected", false)
	case *events.ConnectFailure:
		log.Printf("whatsapp health account=%s status=connect_failure reason=%s message=%q", r.account.ID, event.Reason.String(), event.Message)
		r.reportStatus(context.Background(), "disconnected")
		r.markHealth("disconnected", false)
	case *events.CATRefreshError:
		log.Printf("whatsapp health account=%s status=cat_refresh_error error=%v", r.account.ID, event.Error)
		r.reportStatus(context.Background(), "disconnected")
		r.markHealth("disconnected", false)
	case *events.StreamError:
		log.Printf("whatsapp health account=%s status=stream_error code=%s", r.account.ID, event.Code)
	}
}

func (r *Runner) healthLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.logHealthSnapshot()
		}
	}
}

func (r *Runner) logHealthSnapshot() {
	if r.client == nil {
		return
	}
	connected := r.client.IsConnected()
	loggedIn := r.client.IsLoggedIn()
	status := "disconnected"
	if connected && loggedIn {
		status = "connected"
	}
	if r.client.Store != nil && r.client.Store.ID == nil {
		status = "pending_qr"
	}
	if status != "connected" {
		r.reportStatus(context.Background(), status)
	}
	lastEventAt, lastMessageAt, lastStatus := r.healthSnapshot()
	log.Printf(
		"whatsapp health account=%s status=%s socket_connected=%t logged_in=%t phone=%s last_event=%s last_message=%s last_reported_status=%s",
		r.account.ID,
		status,
		connected,
		loggedIn,
		r.account.PhoneJID,
		formatHealthTime(lastEventAt),
		formatHealthTime(lastMessageAt),
		lastStatus,
	)
}

func (r *Runner) markHealth(status string, message bool) {
	r.healthMu.Lock()
	defer r.healthMu.Unlock()
	now := time.Now().UTC()
	r.lastEventAt = now
	if message {
		r.lastMessageAt = now
	}
	if status != "" {
		r.lastStatus = status
	}
}

func (r *Runner) healthSnapshot() (time.Time, time.Time, string) {
	r.healthMu.Lock()
	defer r.healthMu.Unlock()
	return r.lastEventAt, r.lastMessageAt, r.lastStatus
}

func formatHealthTime(value time.Time) string {
	if value.IsZero() {
		return "never"
	}
	return value.Format(time.RFC3339)
}

func (r *Runner) reply(ctx context.Context, msg *events.Message, text string) {
	chat := msg.Info.Chat
	sender := msg.Info.Sender
	r.markMessageRead(ctx, msg)
	r.setChatTyping(ctx, chat, true)
	defer r.setChatTyping(context.Background(), chat, false)

	contact := chat.String()
	backendContact := contact
	backendSender := sender.String()
	groupJID := ""
	if chat.Server == types.GroupServer {
		groupJID = chat.String()
		contact = groupJID
		backendContact = contact
	} else if pn := r.phoneNumberJID(ctx, chat); !pn.IsEmpty() {
		backendContact = pn.String()
		backendSender = pn.String()
	}

	images := r.extractImages(ctx, msg)
	if text == "" && len(images) > 0 {
		text = "Tolong bantu analisis gambar ini."
	}
	log.Printf("incoming account=%s chat=%s sender=%s contact=%s images=%d text=%q", r.account.ID, chat, sender, backendContact, len(images), trimLog(text, 120))
	response, err := r.backend.HandleWhatsAppInbound(ctx, handlers.WhatsAppInboundInput{
		AccountID:   r.account.ID,
		ContactJID:  backendContact,
		SenderJID:   backendSender,
		MessageID:   msg.Info.ID,
		Text:        text,
		Images:      images,
		Timestamp:   msg.Info.Timestamp.UTC().Format(time.RFC3339),
		DisplayName: firstNonEmpty(msg.Info.PushName, sender.User),
		GroupJID:    groupJID,
	})
	if err != nil {
		log.Printf("whatsapp inbound error chat=%s: %v", chat, err)
		return
	}
	if !response.ShouldReply || strings.TrimSpace(response.ReplyText) == "" {
		log.Printf("no reply account=%s chat=%s conversation=%s", r.account.ID, chat, response.ConversationID)
		return
	}

	log.Printf("outgoing account=%s chat=%s conversation=%s text=%q", r.account.ID, chat, response.ConversationID, trimLog(response.ReplyText, 240))
	r.setChatTyping(ctx, chat, false)
	if _, err := r.client.SendMessage(ctx, chat, &waE2E.Message{Conversation: proto.String(response.ReplyText)}); err != nil {
		log.Printf("send whatsapp error chat=%s: %v", chat, err)
	}
}

func (r *Runner) markMessageRead(ctx context.Context, msg *events.Message) {
	if r.client == nil || msg == nil || msg.Info.ID == "" {
		return
	}
	sender := types.EmptyJID
	if msg.Info.Chat.Server == types.GroupServer {
		sender = msg.Info.Sender
	}
	if err := r.client.MarkRead(ctx, []types.MessageID{msg.Info.ID}, time.Now(), msg.Info.Chat, sender); err != nil {
		log.Printf("whatsapp mark read failed account=%s chat=%s id=%s error=%v", r.account.ID, msg.Info.Chat, msg.Info.ID, err)
		return
	}
	log.Printf("whatsapp mark read account=%s chat=%s id=%s", r.account.ID, msg.Info.Chat, msg.Info.ID)
}

func (r *Runner) setChatTyping(ctx context.Context, chat types.JID, typing bool) {
	if r.client == nil || chat.IsEmpty() {
		return
	}
	state := types.ChatPresencePaused
	if typing {
		state = types.ChatPresenceComposing
	}
	if err := r.client.SendChatPresence(ctx, chat, state, types.ChatPresenceMediaText); err != nil {
		log.Printf("whatsapp typing presence failed account=%s chat=%s typing=%t error=%v", r.account.ID, chat, typing, err)
	}
}

func (r *Runner) phoneNumberJID(ctx context.Context, jid types.JID) types.JID {
	if jid.IsEmpty() || jid.Server != types.HiddenUserServer || r.client == nil || r.client.Store == nil || r.client.Store.LIDs == nil {
		log.Printf("resolve lid phone skipped lid=%s server=%s", jid, jid.Server)
		return types.EmptyJID
	}
	pn, err := r.client.Store.LIDs.GetPNForLID(ctx, jid)
	if err != nil || pn.IsEmpty() {
		log.Printf("resolve lid phone failed lid=%s error=%v", jid, err)
		return types.EmptyJID
	}
	return pn
}

func (r *Runner) reportStatus(ctx context.Context, status string) {
	input := handlers.WhatsAppStatusInput{
		AccountID: r.account.ID,
		Status:    status,
	}
	if r.client != nil && r.client.Store != nil && r.client.Store.ID != nil {
		input.PhoneJID = r.client.Store.ID.String()
	}
	if status == "pending_qr" && r.account.QRCode != "" {
		input.QRCode = r.account.QRCode
	}
	r.reportAccountStatus(ctx, input)
}

func (r *Runner) reportQRCode(ctx context.Context, code string) {
	r.reportAccountStatus(ctx, handlers.WhatsAppStatusInput{
		AccountID: r.account.ID,
		Status:    "pending_qr",
		QRCode:    code,
	})
}

func (r *Runner) reportAccountStatus(ctx context.Context, input handlers.WhatsAppStatusInput) {
	account, err := r.backend.UpdateWhatsAppStatus(ctx, input)
	if err != nil {
		log.Printf("report whatsapp status=%s failed: %v", input.Status, err)
		return
	}
	r.account = account
}

func extractText(msg *events.Message) string {
	if msg.Message == nil {
		return ""
	}
	if text := msg.Message.GetConversation(); text != "" {
		return text
	}
	if ext := msg.Message.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	if image := msg.Message.GetImageMessage(); image != nil {
		return image.GetCaption()
	}
	return ""
}

func (r *Runner) extractImages(ctx context.Context, msg *events.Message) []models.ChatImageInput {
	if r.client == nil || msg == nil || msg.Message == nil {
		return nil
	}
	imageMessage := msg.Message.GetImageMessage()
	if imageMessage == nil {
		return nil
	}
	mimeType := normalizeImageMimeType(imageMessage.GetMimetype())
	if mimeType == "" {
		log.Printf("whatsapp image ignored account=%s id=%s reason=unsupported_mime mime=%q", r.account.ID, msg.Info.ID, imageMessage.GetMimetype())
		return nil
	}
	if imageMessage.GetFileLength() > 8*1024*1024 {
		log.Printf("whatsapp image ignored account=%s id=%s reason=too_large size=%d", r.account.ID, msg.Info.ID, imageMessage.GetFileLength())
		return nil
	}
	data, err := r.client.Download(ctx, imageMessage)
	if err != nil {
		log.Printf("whatsapp image download failed account=%s id=%s error=%v", r.account.ID, msg.Info.ID, err)
		return nil
	}
	if len(data) == 0 || len(data) > 8*1024*1024 {
		log.Printf("whatsapp image ignored account=%s id=%s reason=invalid_size size=%d", r.account.ID, msg.Info.ID, len(data))
		return nil
	}
	log.Printf("whatsapp image downloaded account=%s id=%s mime=%s bytes=%d", r.account.ID, msg.Info.ID, mimeType, len(data))
	return []models.ChatImageInput{{
		Name:       "whatsapp-" + msg.Info.ID,
		MimeType:   mimeType,
		DataBase64: base64.StdEncoding.EncodeToString(data),
	}}
}

func normalizeImageMimeType(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/png":
		return "image/png"
	case "image/webp":
		return "image/webp"
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func trimLog(value string, limit int) string {
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
