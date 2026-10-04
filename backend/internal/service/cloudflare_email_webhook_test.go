package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCloudflareEmailWebhookSignsAndSends(t *testing.T) {
	const secret = "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		timestamp := request.Header.Get("X-CheapBuddy-Timestamp")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(timestamp + "."))
		_, _ = mac.Write(body)
		require.Equal(t, "sha256="+hex.EncodeToString(mac.Sum(nil)), request.Header.Get("X-CheapBuddy-Signature"))
		require.Contains(t, string(body), `"event_id":"delivery-1"`)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	webhook := &CloudflareEmailWebhook{URL: server.URL, Secret: secret, Now: func() time.Time { return time.Unix(1700000000, 0) }}
	err := webhook.Send(context.Background(), CloudflareEmailRequest{EventID: "delivery-1", Recipient: "user@example.com", Subject: "Low balance", Text: "Top up", HTML: "<p>Top up</p>"})
	require.NoError(t, err)
}

func TestCloudflareEmailWebhookRejectsIncompleteRequest(t *testing.T) {
	webhook := &CloudflareEmailWebhook{URL: "https://example.com", Secret: "secret"}
	err := webhook.Send(context.Background(), CloudflareEmailRequest{EventID: "", Recipient: "user@example.com", Subject: "subject", HTML: "<p>body</p>"})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "incomplete"))
}

func TestCloudflareEmailWebhookReportsWorkerFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte("invalid signature"))
	}))
	defer server.Close()

	webhook := &CloudflareEmailWebhook{URL: server.URL, Secret: "secret", Now: func() time.Time { return time.Unix(1700000000, 0) }}
	err := webhook.Send(context.Background(), CloudflareEmailRequest{EventID: "delivery-1", Recipient: "user@example.com", Subject: "subject", HTML: "<p>body</p>"})
	require.Error(t, err)
	require.Contains(t, err.Error(), strconv.Itoa(http.StatusUnauthorized))
}
