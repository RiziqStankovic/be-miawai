package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type oauthState struct {
	Provider string `json:"provider"`
	Exp      int64  `json:"exp"`
}

func SignOAuthState(secret string, provider string) (string, error) {
	payload, err := json.Marshal(oauthState{
		Provider: provider,
		Exp:      time.Now().Add(10 * time.Minute).Unix(),
	})
	if err != nil {
		return "", err
	}

	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signature := sign([]byte(secret), encodedPayload)
	return encodedPayload + "." + signature, nil
}

func VerifyOAuthState(secret string, token string, expectedProvider string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return errors.New("invalid oauth state")
	}

	expected := sign([]byte(secret), parts[0])
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return errors.New("invalid oauth state signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return err
	}

	var state oauthState
	if err := json.Unmarshal(payload, &state); err != nil {
		return err
	}
	if state.Provider != expectedProvider || time.Now().Unix() > state.Exp {
		return errors.New("oauth state expired")
	}
	return nil
}

func sign(secret []byte, payload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
