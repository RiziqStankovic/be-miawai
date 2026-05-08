package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const SessionCookieName = "miaw_session"

type Manager struct {
	secret []byte
	secure bool
}

type SessionIdentity struct {
	UserID string
	Email  string
	Role   string
	Access []string
}

type sessionClaims struct {
	Subject string   `json:"sub"`
	Email   string   `json:"email,omitempty"`
	Role    string   `json:"role,omitempty"`
	Access  []string `json:"access,omitempty"`
	Issued  int64    `json:"iat"`
	Exp     int64    `json:"exp"`
}

func NewManager(secret string, secure bool) *Manager {
	return &Manager{secret: []byte(secret), secure: secure}
}

func (m *Manager) SetSession(w http.ResponseWriter, identity SessionIdentity) error {
	token, err := m.SignSession(identity, 30*24*time.Hour)
	if err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int((30 * 24 * time.Hour).Seconds()),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (m *Manager) ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (m *Manager) SignSession(identity SessionIdentity, ttl time.Duration) (string, error) {
	if identity.UserID == "" {
		return "", errors.New("user id is required")
	}

	claims := sessionClaims{
		Subject: identity.UserID,
		Email:   identity.Email,
		Role:    identity.Role,
		Access:  append([]string(nil), identity.Access...),
		Issued:  time.Now().Unix(),
		Exp:     time.Now().Add(ttl).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	header, err := json.Marshal(map[string]string{
		"alg": "HS256",
		"typ": "JWT",
	})
	if err != nil {
		return "", err
	}

	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := encodedHeader + "." + encodedPayload
	signature := m.sign(signingInput)
	return signingInput + "." + signature, nil
}

func (m *Manager) ParseSession(token string) (SessionIdentity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return SessionIdentity{}, errors.New("invalid session token")
	}

	signingInput := parts[0] + "." + parts[1]
	expected := m.sign(signingInput)
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return SessionIdentity{}, errors.New("invalid session signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return SessionIdentity{}, err
	}

	var claims sessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return SessionIdentity{}, err
	}
	if claims.Subject == "" || time.Now().Unix() > claims.Exp {
		return SessionIdentity{}, errors.New("session expired")
	}
	return SessionIdentity{
		UserID: claims.Subject,
		Email:  claims.Email,
		Role:   claims.Role,
		Access: append([]string(nil), claims.Access...),
	}, nil
}

func (m *Manager) sign(payload string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
