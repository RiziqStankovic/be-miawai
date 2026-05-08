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

type sessionClaims struct {
	UserID string `json:"uid"`
	Exp    int64  `json:"exp"`
}

func NewManager(secret string, secure bool) *Manager {
	return &Manager{secret: []byte(secret), secure: secure}
}

func (m *Manager) SetSession(w http.ResponseWriter, userID string) error {
	token, err := m.SignSession(userID, 30*24*time.Hour)
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

func (m *Manager) SignSession(userID string, ttl time.Duration) (string, error) {
	claims := sessionClaims{UserID: userID, Exp: time.Now().Add(ttl).Unix()}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signature := m.sign(encodedPayload)
	return encodedPayload + "." + signature, nil
}

func (m *Manager) ParseSession(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", errors.New("invalid session token")
	}

	expected := m.sign(parts[0])
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return "", errors.New("invalid session signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", err
	}

	var claims sessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	if claims.UserID == "" || time.Now().Unix() > claims.Exp {
		return "", errors.New("session expired")
	}
	return claims.UserID, nil
}

func (m *Manager) sign(payload string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
