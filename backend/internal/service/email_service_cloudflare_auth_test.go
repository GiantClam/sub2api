package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Only verification-code operations are needed by these transport tests.
type cloudflareAuthEmailCache struct {
	EmailCache
	mu     sync.Mutex
	data   *VerificationCodeData
	ttl    time.Duration
	setErr error
}

func (c *cloudflareAuthEmailCache) GetVerificationCode(context.Context, string) (*VerificationCodeData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data == nil {
		return nil, nil
	}
	copy := *c.data
	return &copy, nil
}

func (c *cloudflareAuthEmailCache) SetVerificationCode(_ context.Context, _ string, data *VerificationCodeData, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.setErr != nil {
		return c.setErr
	}
	copy := *data
	c.data, c.ttl = &copy, ttl
	return nil
}

func (c *cloudflareAuthEmailCache) DeleteVerificationCode(context.Context, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = nil
	return nil
}

func (c *cloudflareAuthEmailCache) IncrVerificationCodeAttempts(context.Context, string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data == nil {
		return 0, errors.New("code missing")
	}
	c.data.Attempts++
	return c.data.Attempts, nil
}

func TestCloudflareAuthEmailSendsWithoutSMTP(t *testing.T) {
	for _, templated := range []bool{false, true} {
		name := "legacy"
		if templated {
			name = "localized_template"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", "true")
			deliveries := make(chan CloudflareEmailRequest, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var message CloudflareEmailRequest
				if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				deliveries <- message
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			cache := &cloudflareAuthEmailCache{}
			repo := newNotificationEmailMemorySettingRepo()
			require.NoError(t, repo.Set(context.Background(), SettingKeySiteName, "Site <unsafe>"))
			svc := NewEmailService(repo, cache)
			svc.SetCloudflareEmailWebhook(&CloudflareEmailWebhook{URL: server.URL, Secret: "unit-test-only"})
			if templated {
				NewNotificationEmailService(repo, svc)
			}
			require.NoError(t, svc.SendVerifyCode(context.Background(), "signup@example.com", "Site <unsafe>", "zh-CN"))
			sent := <-deliveries
			data, err := cache.GetVerificationCode(context.Background(), "signup@example.com")
			require.NoError(t, err)
			require.NotNil(t, data)
			require.Regexp(t, `^\d{6}$`, data.Code)
			require.Equal(t, verifyCodeTTL, cache.ttl)
			require.WithinDuration(t, data.CreatedAt.Add(verifyCodeTTL), data.ExpiresAt, time.Second)
			require.Equal(t, 0, data.Attempts)
			require.Equal(t, "signup@example.com", sent.Recipient)
			require.NotEmpty(t, sent.EventID)
			require.Regexp(t, `^auth\.verify_code:[a-f0-9]{32}$`, sent.EventID)
			require.Contains(t, sent.HTML, data.Code)
			require.Contains(t, sent.HTML, "Site &lt;unsafe&gt;")
			require.NotContains(t, sent.HTML, "{{verification_code}}")
			require.ErrorIs(t, svc.SendVerifyCode(context.Background(), "signup@example.com", "Site"), ErrVerifyCodeTooFrequent)
			require.NoError(t, svc.VerifyCode(context.Background(), "signup@example.com", data.Code))
			require.ErrorIs(t, svc.VerifyCode(context.Background(), "signup@example.com", data.Code), ErrInvalidVerifyCode)
		})
	}
}

func TestCloudflareAuthEmailRequiresExplicitOptIn(t *testing.T) {
	for _, flag := range []string{"", "false", "invalid"} {
		t.Run("flag="+flag, func(t *testing.T) {
			t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", flag)
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			svc := NewEmailService(newNotificationEmailMemorySettingRepo(), &cloudflareAuthEmailCache{})
			svc.SetCloudflareEmailWebhook(&CloudflareEmailWebhook{URL: server.URL, Secret: "unit-test-only"})
			require.ErrorIs(t, svc.SendVerifyCode(context.Background(), "signup@example.com", "Site"), ErrEmailNotConfigured)
			require.Zero(t, calls.Load())
			// Existing balance notifications remain enabled independently of the auth opt-in.
			require.NoError(t, svc.SendNotificationEmail(context.Background(), NotificationEmailEventBalanceLow, "balance-test", "signup@example.com", "Balance", "<p>Low balance</p>"))
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestCloudflareAuthEmailFailureDoesNotFallBackOrExposeCode(t *testing.T) {
	t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", "true")
	smtp := startNotificationEmailTestSMTPServer(t)
	repo := newNotificationEmailMemorySettingRepo()
	require.NoError(t, repo.SetMultiple(context.Background(), smtp.settings()))
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(body) // A failing proxy must not leak echoed codes into error logs.
	}))
	defer server.Close()
	cache := &cloudflareAuthEmailCache{}
	svc := NewEmailService(repo, cache)
	svc.SetCloudflareEmailWebhook(&CloudflareEmailWebhook{URL: server.URL, Secret: "unit-test-only"})
	NewNotificationEmailService(repo, svc)
	err := svc.SendVerifyCode(context.Background(), "signup@example.com", "Site")
	require.Error(t, err)
	require.Contains(t, err.Error(), "502")
	data, _ := cache.GetVerificationCode(context.Background(), "signup@example.com")
	require.NotNil(t, data)
	require.NotContains(t, err.Error(), data.Code)
	require.NotContains(t, err.Error(), "recipient")
	require.Zero(t, smtp.messageCount())
	require.EqualValues(t, 1, calls.Load())
	require.ErrorIs(t, svc.SendVerifyCode(context.Background(), "signup@example.com", "Site"), ErrVerifyCodeTooFrequent)
	require.EqualValues(t, 1, calls.Load())
}

func TestCloudflareAuthEmailPreservesAttemptLimit(t *testing.T) {
	t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	cache := &cloudflareAuthEmailCache{}
	svc := NewEmailService(newNotificationEmailMemorySettingRepo(), cache)
	svc.SetCloudflareEmailWebhook(&CloudflareEmailWebhook{URL: server.URL, Secret: "unit-test-only"})
	require.NoError(t, svc.SendVerifyCode(context.Background(), "signup@example.com", "Site"))
	data, _ := cache.GetVerificationCode(context.Background(), "signup@example.com")
	for attempt := 1; attempt < maxVerifyCodeAttempts; attempt++ {
		require.ErrorIs(t, svc.VerifyCode(context.Background(), "signup@example.com", "wrong"), ErrInvalidVerifyCode)
	}
	require.ErrorIs(t, svc.VerifyCode(context.Background(), "signup@example.com", "wrong"), ErrVerifyCodeMaxAttempts)
	require.ErrorIs(t, svc.VerifyCode(context.Background(), "signup@example.com", data.Code), ErrVerifyCodeMaxAttempts)
}

func TestCloudflareAuthEmailQueueUsesWebhookWithoutSMTP(t *testing.T) {
	t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", "true")
	sent := make(chan CloudflareEmailRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message CloudflareEmailRequest
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		sent <- message
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	svc := NewEmailService(newNotificationEmailMemorySettingRepo(), &cloudflareAuthEmailCache{})
	svc.SetCloudflareEmailWebhook(&CloudflareEmailWebhook{URL: server.URL, Secret: "unit-test-only"})
	queue := NewEmailQueueService(svc, 1)
	defer queue.Stop()
	require.NoError(t, queue.EnqueueVerifyCode("queued@example.com", "Site", "zh-CN"))
	select {
	case message := <-sent:
		require.Equal(t, "queued@example.com", message.Recipient)
	case <-time.After(3 * time.Second):
		t.Fatal("verification queue did not deliver through the webhook")
	}
}

func TestCloudflareAuthEmailCacheFailurePreventsDelivery(t *testing.T) {
	t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", "true")
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	svc := NewEmailService(newNotificationEmailMemorySettingRepo(), &cloudflareAuthEmailCache{setErr: errors.New("cache unavailable")})
	svc.SetCloudflareEmailWebhook(&CloudflareEmailWebhook{URL: server.URL, Secret: "unit-test-only"})
	require.ErrorContains(t, svc.SendVerifyCode(context.Background(), "signup@example.com", "Site"), "save verify code")
	require.Zero(t, calls.Load())
}

func TestCloudflareAuthEmailDoesNotRouteOtherEvents(t *testing.T) {
	t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", "true")
	svc := NewEmailService(newNotificationEmailMemorySettingRepo(), nil)
	svc.SetCloudflareEmailWebhook(&CloudflareEmailWebhook{URL: "http://127.0.0.1:1", Secret: "unit-test-only"})
	err := svc.SendNotificationEmail(context.Background(), NotificationEmailEventAuthPasswordReset, "", "signup@example.com", "Reset", "<p>Reset</p>")
	require.ErrorIs(t, err, ErrEmailNotConfigured)
	require.False(t, strings.Contains(err.Error(), "cloudflare"))
}

func TestCloudflareEmailWebhookRoutesPublishedAnnouncementsWithoutSMTP(t *testing.T) {
	deliveries := make(chan CloudflareEmailRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message CloudflareEmailRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&message))
		deliveries <- message
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	svc := NewEmailService(nil, nil)
	svc.SetCloudflareEmailWebhook(&CloudflareEmailWebhook{URL: server.URL, Secret: "unit-test-only"})
	require.NoError(t, svc.SendNotificationEmail(
		context.Background(),
		NotificationEmailEventAnnouncementPublished,
		"announcement:42:user@example.com",
		"user@example.com",
		"公告 / Announcement",
		"<p>中英文公告</p>",
	))

	sent := <-deliveries
	require.Equal(t, "announcement:42:user@example.com", sent.EventID)
	require.Equal(t, "user@example.com", sent.Recipient)
	require.Equal(t, "公告 / Announcement", sent.Subject)
	require.Equal(t, "<p>中英文公告</p>", sent.HTML)
}

func TestCloudflareAuthEmailPreflightRejectsIncompleteWebhook(t *testing.T) {
	t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", "true")
	for _, webhook := range []*CloudflareEmailWebhook{
		nil,
		{URL: "http://127.0.0.1:1"},
		{Secret: "unit-test-only"},
		{URL: " ", Secret: "unit-test-only"},
	} {
		cache := &cloudflareAuthEmailCache{}
		// No settings repository: Cloudflare preflight must not consult SMTP.
		svc := NewEmailService(nil, cache)
		svc.SetCloudflareEmailWebhook(webhook)
		require.ErrorIs(t, svc.CheckVerificationEmailConfigured(context.Background()), ErrEmailNotConfigured)
		require.ErrorIs(t, svc.SendVerifyCode(context.Background(), "signup@example.com", "Site"), ErrEmailNotConfigured)
		data, _ := cache.GetVerificationCode(context.Background(), "signup@example.com")
		require.Nil(t, data, "an unconfigured transport must not issue a code")
	}
}

func TestCloudflareAuthEmailAdminTestUsesSignedWebhookAndUniqueIDs(t *testing.T) {
	t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", "true")
	deliveries := make(chan CloudflareEmailRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message CloudflareEmailRequest
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("X-CheapBuddy-Signature") == "" || r.Header.Get("X-CheapBuddy-Timestamp") == "" {
			t.Error("missing signed webhook headers")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		deliveries <- message
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	svc := NewEmailService(nil, nil)
	svc.SetCloudflareEmailWebhook(&CloudflareEmailWebhook{URL: server.URL, Secret: "unit-test-only"})
	require.True(t, svc.IsCloudflareAuthEmailEnabled())
	require.NoError(t, svc.CheckVerificationEmailConfigured(context.Background()))
	for range 2 {
		require.NoError(t, svc.SendTestEmail(context.Background(), "admin@example.com", "Delivery test", "<p>Rendered test</p>"))
	}
	sent := []CloudflareEmailRequest{<-deliveries, <-deliveries}
	require.Equal(t, "admin@example.com", sent[0].Recipient)
	require.Equal(t, "Delivery test", sent[0].Subject)
	require.Equal(t, "<p>Rendered test</p>", sent[0].HTML)
	require.NotEqual(t, sent[0].EventID, sent[1].EventID)
}

func TestCloudflareAuthEmailAdminTestPropagatesFailure(t *testing.T) {
	t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	svc := NewEmailService(nil, nil)
	svc.SetCloudflareEmailWebhook(&CloudflareEmailWebhook{URL: server.URL, Secret: "unit-test-only"})
	require.ErrorContains(t, svc.SendTestEmail(context.Background(), "admin@example.com", "Delivery test", "<p>Rendered test</p>"), "HTTP 502")
}

func TestCloudflareAuthEmailDisabledPreservesSMTP(t *testing.T) {
	t.Setenv("CLOUDFLARE_AUTH_EMAIL_ENABLED", "false")
	smtp := startNotificationEmailTestSMTPServer(t)
	repo := newNotificationEmailMemorySettingRepo()
	require.NoError(t, repo.SetMultiple(context.Background(), smtp.settings()))
	svc := NewEmailService(repo, &cloudflareAuthEmailCache{})
	// Even with a configured webhook, auth emails stay on SMTP unless opted in.
	svc.SetCloudflareEmailWebhook(&CloudflareEmailWebhook{URL: "http://127.0.0.1:1", Secret: "unit-test-only"})
	require.NoError(t, svc.CheckVerificationEmailConfigured(context.Background()))
	require.NoError(t, svc.SendVerifyCode(context.Background(), "signup@example.com", "Site"))
	require.NoError(t, svc.SendTestEmail(context.Background(), "admin@example.com", "Delivery test", "<p>Rendered test</p>"))
	require.EqualValues(t, 2, smtp.messageCount())
}
