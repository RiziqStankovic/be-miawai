package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

const resendAPIURL = "https://api.resend.com/emails"

type ResendConfig struct {
	APIKey    string
	FromEmail string
	FromName  string
	Enabled   bool
}

type ResendClient struct {
	cfg        ResendConfig
	httpClient *http.Client
}

func NewResendClient(cfg ResendConfig) *ResendClient {
	return &ResendClient{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *ResendClient) Enabled() bool {
	return c != nil && c.cfg.Enabled && strings.TrimSpace(c.cfg.APIKey) != "" && strings.TrimSpace(c.cfg.FromEmail) != ""
}

func (c *ResendClient) SendOTP(ctx context.Context, to string, code string) error {
	if !c.Enabled() {
		return errors.New("resend email provider is not enabled")
	}
	to = strings.TrimSpace(to)
	code = strings.TrimSpace(code)
	if to == "" || code == "" {
		return errors.New("recipient and otp code are required")
	}

	payload := map[string]any{
		"from":    c.fromHeader(),
		"to":      []string{to},
		"subject": "Kode OTP Registrasi SALOME",
		"html":    otpHTML(code),
		"text":    fmt.Sprintf("Kode OTP registrasi SALOME kamu adalah %s. Kode ini berlaku 10 menit. Jangan bagikan kode ini kepada siapa pun.", code),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resendAPIURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.APIKey))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("resend returned status %d", resp.StatusCode)
	}
	return nil
}

func (c *ResendClient) fromHeader() string {
	fromEmail := strings.TrimSpace(c.cfg.FromEmail)
	fromName := strings.TrimSpace(c.cfg.FromName)
	if fromName == "" {
		return fromEmail
	}
	return fmt.Sprintf("%s <%s>", fromName, fromEmail)
}

func otpHTML(code string) string {
	escapedCode := html.EscapeString(code)
	return fmt.Sprintf(`
<div style="font-family:Arial,sans-serif;line-height:1.5;color:#111827">
  <h2>Kode OTP Registrasi SALOME</h2>
  <p>Masukkan kode berikut untuk menyelesaikan registrasi akun kamu:</p>
  <p style="font-size:28px;font-weight:700;letter-spacing:6px;margin:24px 0">%s</p>
  <p>Kode ini berlaku selama 10 menit. Jangan bagikan kode ini kepada siapa pun.</p>
</div>`, escapedCode)
}
