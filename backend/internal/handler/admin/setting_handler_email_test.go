//go:build unit

package admin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func bindSMTPRequest[T any](t *testing.T, body string) T {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req

	var payload T
	require.NoError(t, c.ShouldBindJSON(&payload))
	return payload
}

func TestTestSMTPRequestUseTLSOmissionFallsBackToSavedSetting(t *testing.T) {
	req := bindSMTPRequest[TestSMTPRequest](t, `{}`)

	require.Nil(t, req.SMTPUseTLS)
	require.True(t, resolveSMTPUseTLS(req.SMTPUseTLS, &service.SMTPConfig{UseTLS: true}))
}

func TestTestSMTPRequestExplicitFalseOverridesSavedUseTLS(t *testing.T) {
	req := bindSMTPRequest[TestSMTPRequest](t, `{"smtp_use_tls":false}`)

	require.NotNil(t, req.SMTPUseTLS)
	require.False(t, resolveSMTPUseTLS(req.SMTPUseTLS, &service.SMTPConfig{UseTLS: true}))
}

func TestTestSMTPRequestExplicitTrueOverridesMissingSavedUseTLS(t *testing.T) {
	req := bindSMTPRequest[TestSMTPRequest](t, `{"smtp_use_tls":true}`)

	require.NotNil(t, req.SMTPUseTLS)
	require.True(t, resolveSMTPUseTLS(req.SMTPUseTLS, nil))
}

func TestSendTestEmailRequestPreservesUseTLSOmissionSemantics(t *testing.T) {
	omitted := bindSMTPRequest[SendTestEmailRequest](t, `{"email":"admin@example.com"}`)
	explicitFalse := bindSMTPRequest[SendTestEmailRequest](t, `{"email":"admin@example.com","smtp_use_tls":false}`)
	saved := &service.SMTPConfig{UseTLS: true}

	require.True(t, resolveSMTPUseTLS(omitted.SMTPUseTLS, saved))
	require.False(t, resolveSMTPUseTLS(explicitFalse.SMTPUseTLS, saved))
}

type testEmailSettingsRepo struct {
	*settingHandlerRepoStub
	smtpReads int
}

func (r *testEmailSettingsRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	r.smtpReads++
	return r.settingHandlerRepoStub.GetMultiple(ctx, keys)
}

func runTestEmailHandler(t *testing.T, repo *testEmailSettingsRepo, webhook *service.CloudflareEmailWebhook, body string) *httptest.ResponseRecorder {
	t.Helper()
	settings := service.NewSettingService(repo, &config.Config{})
	email := service.NewEmailService(repo, nil)
	email.SetCloudflareEmailWebhook(webhook)
	handler := NewSettingHandler(settings, email, nil, nil, nil, nil, nil)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/send-test-email", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.SendTestEmail(c)
	return recorder
}

func TestSendTestEmailCloudflareWithoutSMTP(t *testing.T) {
	t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", "true")
	const secret = "handler-unit-test-only"
	delivered := make(chan service.CloudflareEmailRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(r.Header.Get("X-CheapBuddy-Timestamp") + "."))
		_, _ = mac.Write(body)
		if r.Header.Get("X-CheapBuddy-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Error("invalid webhook HMAC")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var message service.CloudflareEmailRequest
		if err := json.Unmarshal(body, &message); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		delivered <- message
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	repo := &testEmailSettingsRepo{settingHandlerRepoStub: &settingHandlerRepoStub{values: map[string]string{service.SettingKeySiteName: "Site <unsafe>"}}}
	recorder := runTestEmailHandler(t, repo, &service.CloudflareEmailWebhook{URL: server.URL, Secret: secret}, `{"email":"admin@example.com"}`)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "Test email sent successfully")
	message := <-delivered
	require.Equal(t, "admin@example.com", message.Recipient)
	require.Equal(t, "[Site <unsafe>] Test Email", message.Subject)
	require.Contains(t, message.HTML, "Site &lt;unsafe&gt;")
	require.NotContains(t, message.HTML, "<unsafe>")
	require.NotEmpty(t, message.EventID)
	require.Zero(t, repo.smtpReads, "Cloudflare delivery must not require stored SMTP settings")
}

func TestSendTestEmailCloudflareFailureIsNotReportedAsSent(t *testing.T) {
	t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("sensitive echoed email payload"))
	}))
	defer server.Close()
	repo := &testEmailSettingsRepo{settingHandlerRepoStub: &settingHandlerRepoStub{values: map[string]string{}}}
	recorder := runTestEmailHandler(t, repo, &service.CloudflareEmailWebhook{URL: server.URL, Secret: "unit-test-only"}, `{"email":"admin@example.com"}`)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "HTTP 502")
	require.NotContains(t, recorder.Body.String(), "sent successfully")
	require.NotContains(t, recorder.Body.String(), "sensitive echoed")
	require.Zero(t, repo.smtpReads)
}

func TestSendTestEmailCloudflareMissingWebhookFailsClosed(t *testing.T) {
	t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", "true")
	repo := &testEmailSettingsRepo{settingHandlerRepoStub: &settingHandlerRepoStub{values: map[string]string{}}}
	recorder := runTestEmailHandler(t, repo, nil, `{"email":"admin@example.com"}`)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "EMAIL_NOT_CONFIGURED")
	require.NotContains(t, recorder.Body.String(), "sent successfully")
	require.Zero(t, repo.smtpReads)
}

func TestSendTestEmailCloudflarePreservesRecipientValidation(t *testing.T) {
	t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", "true")
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, body := range []string{`{}`, `{"email":"invalid"}`} {
		repo := &testEmailSettingsRepo{settingHandlerRepoStub: &settingHandlerRepoStub{values: map[string]string{}}}
		recorder := runTestEmailHandler(t, repo, &service.CloudflareEmailWebhook{URL: server.URL, Secret: "unit-test-only"}, body)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		require.Contains(t, recorder.Body.String(), "Invalid request")
	}
	require.Zero(t, calls.Load())
}

func TestSendTestEmailWithoutCloudflareOptInStillRequiresSMTP(t *testing.T) {
	for _, flag := range []string{"", "false"} {
		t.Run("flag="+flag, func(t *testing.T) {
			t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", flag)
			repo := &testEmailSettingsRepo{settingHandlerRepoStub: &settingHandlerRepoStub{values: map[string]string{}}}
			recorder := runTestEmailHandler(t, repo, &service.CloudflareEmailWebhook{URL: "http://127.0.0.1:1", Secret: "unit-test-only"}, `{"email":"admin@example.com"}`)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), "SMTP host is required")
			require.Equal(t, 1, repo.smtpReads)
		})
	}
}
