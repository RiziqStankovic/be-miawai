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

const GuestCookieName = "miaw_guest"

type GuestManager struct {
	secret []byte
	secure bool
}

type guestClaims struct {
	Used int   `json:"used"`
	Exp  int64 `json:"exp"`
}

func NewGuestManager(secret string, secure bool) *GuestManager {
	return &GuestManager{secret: []byte(secret), secure: secure}
}

func (m *GuestManager) SetUsage(w http.ResponseWriter, used int, ttl time.Duration) error {
	token, err := m.SignUsage(used, ttl)
	if err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     GuestCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (m *GuestManager) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     GuestCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (m *GuestManager) SignUsage(used int, ttl time.Duration) (string, error) {
	claims := guestClaims{
		Used: used,
		Exp:  time.Now().Add(ttl).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signature := m.sign(encodedPayload)
	return encodedPayload + "." + signature, nil
}

func (m *GuestManager) ParseUsage(token string) (int, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return 0, errors.New("invalid guest token")
	}

	expected := m.sign(parts[0])
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return 0, errors.New("invalid guest signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return 0, err
	}

	var claims guestClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return 0, err
	}
	if claims.Used < 0 || time.Now().Unix() > claims.Exp {
		return 0, errors.New("guest session expired")
	}
	return claims.Used, nil
}

func (m *GuestManager) sign(payload string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
