package handlers

import (
	"slices"
	"strings"

	"be-miawai/internal/auth"
	"be-miawai/internal/models"
)

const (
	accessRuntimeRead         = "runtime:read"
	accessRuntimeWrite        = "runtime:write"
	accessConversationsRead   = "conversations:read"
	accessConversationsWrite  = "conversations:write"
	accessConversationsDelete = "conversations:delete"
	accessChatRead            = "chat:read"
	accessChatWrite           = "chat:write"
	accessBillingRead         = "billing:read"
	accessBillingWrite        = "billing:write"
	accessWhatsAppRead        = "whatsapp:read"
	accessWhatsAppWrite       = "whatsapp:write"
	accessAdmin               = "admin:access"
)

var (
	memberAccess = []string{
		accessRuntimeRead,
		accessRuntimeWrite,
		accessConversationsRead,
		accessConversationsWrite,
		accessConversationsDelete,
		accessChatRead,
		accessChatWrite,
		accessBillingRead,
		accessWhatsAppRead,
		accessWhatsAppWrite,
	}
	adminAccess  = append(append([]string(nil), memberAccess...), accessBillingWrite, accessAdmin)
	viewerAccess = []string{
		accessConversationsRead,
		accessChatRead,
	}
	devAccountsByEmail = map[string]devAccount{
		"admin@miaw.local": {
			Email:    "admin@miaw.local",
			Password: "admin123",
			Name:     "Miaw Admin",
			Role:     "admin",
			Access:   adminAccess,
		},
		"editor@miaw.local": {
			Email:    "editor@miaw.local",
			Password: "editor123",
			Name:     "Miaw Editor",
			Role:     "editor",
			Access:   memberAccess,
		},
		"viewer@miaw.local": {
			Email:    "viewer@miaw.local",
			Password: "viewer123",
			Name:     "Miaw Viewer",
			Role:     "viewer",
			Access:   viewerAccess,
		},
	}
)

type devAccount struct {
	Email    string
	Password string
	Name     string
	Role     string
	Access   []string
}

func lookupDevAccount(email string) (devAccount, bool) {
	account, ok := devAccountsByEmail[normalizeEmail(email)]
	if !ok {
		return devAccount{}, false
	}

	account.Access = append([]string(nil), account.Access...)
	return account, true
}

func applySessionAccess(user models.User, identity auth.SessionIdentity) models.User {
	if identity.Role != "" {
		user.Role = identity.Role
	}
	decorated := decorateUserAccess(user)
	if len(identity.Access) == 0 {
		return decorated
	}
	decorated.Access = mergeAccess(decorated.Access, identity.Access)
	user.Role = decorated.Role
	user.Access = decorated.Access
	return user
}

func decorateUserAccess(user models.User) models.User {
	if account, ok := lookupDevAccount(user.Email); ok {
		user.Role = account.Role
		user.Access = append([]string(nil), account.Access...)
		return user
	}

	user.Role = "member"
	user.Access = append([]string(nil), memberAccess...)
	return user
}

func hasAccess(user models.User, permission string) bool {
	if permission == "" {
		return true
	}
	return slices.Contains(user.Access, permission)
}

func mergeAccess(primary []string, extra []string) []string {
	out := append([]string(nil), primary...)
	for _, permission := range extra {
		if permission == "" || slices.Contains(out, permission) {
			continue
		}
		out = append(out, permission)
	}
	return out
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
