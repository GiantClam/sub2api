package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	cloudflareEmailWebhookURLEnv    = "CLOUDFLARE_EMAIL_WEBHOOK_URL"
	cloudflareEmailWebhookSecretEnv = "CLOUDFLARE_EMAIL_WEBHOOK_SECRET"
	cloudflareAuthEmailEnabledEnv   = "CLOUDFLARE_AUTH_EMAIL_ENABLED"
	cloudflareEmailWebhookTimeout   = 15 * time.Second
)

// CloudflareEmailWebhook sends rendered notification emails to a Cloudflare
// Worker, which owns the EmailMessage/send_email binding.
type CloudflareEmailWebhook struct {
	URL    string
	Secret string
	Client *http.Client
	Now    func() time.Time
}

type CloudflareEmailRequest struct {
	EventID   string `json:"event_id"`
	Recipient string `json:"recipient"`
	Subject   string `json:"subject"`
	Text      string `json:"text,omitempty"`
	HTML      string `json:"html"`
}

// NewCloudflareEmailWebhookFromEnv enables the integration only when both
// production secrets are present. An empty result preserves SMTP behavior.
func NewCloudflareEmailWebhookFromEnv() *CloudflareEmailWebhook {
	url := strings.TrimSpace(os.Getenv(cloudflareEmailWebhookURLEnv))
	secret := strings.TrimSpace(os.Getenv(cloudflareEmailWebhookSecretEnv))
	if url == "" || secret == "" {
		return nil
	}
	return &CloudflareEmailWebhook{URL: url, Secret: secret, Client: &http.Client{Timeout: cloudflareEmailWebhookTimeout}, Now: time.Now}
}

func (w *CloudflareEmailWebhook) Send(ctx context.Context, input CloudflareEmailRequest) error {
	if w == nil || strings.TrimSpace(w.URL) == "" || strings.TrimSpace(w.Secret) == "" {
		return errors.New("cloudflare email webhook is not configured")
	}
	if strings.TrimSpace(input.EventID) == "" || strings.TrimSpace(input.Recipient) == "" || strings.TrimSpace(input.Subject) == "" || strings.TrimSpace(input.HTML) == "" {
		return errors.New("cloudflare email webhook request is incomplete")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode cloudflare email webhook request: %w", err)
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	timestamp := fmt.Sprintf("%d", now().Unix())
	signature := hmac.New(sha256.New, []byte(w.Secret))
	_, _ = signature.Write([]byte(timestamp))
	_, _ = signature.Write([]byte("."))
	_, _ = signature.Write(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create cloudflare email webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CheapBuddy-Timestamp", timestamp)
	req.Header.Set("X-CheapBuddy-Signature", "sha256="+hex.EncodeToString(signature.Sum(nil)))
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: cloudflareEmailWebhookTimeout}
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("call cloudflare email webhook: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		// Worker/proxy errors can echo the signed payload, including verification
		// codes. Queue workers log this error, so report only the numeric status.
		return fmt.Errorf("cloudflare email webhook returned HTTP %d", response.StatusCode)
	}
	return nil
}
