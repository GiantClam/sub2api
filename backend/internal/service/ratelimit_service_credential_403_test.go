package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type credential403AccountRepo struct {
	AccountRepository
	errorMessages []string
	tempReasons   []string
}

func (r *credential403AccountRepo) SetError(_ context.Context, _ int64, message string) error {
	r.errorMessages = append(r.errorMessages, message)
	return nil
}

func (r *credential403AccountRepo) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, reason string) error {
	r.tempReasons = append(r.tempReasons, reason)
	return nil
}

func TestRateLimitServiceStructured403CredentialStateRemainsPermanent(t *testing.T) {
	for _, payload := range []string{
		`{"error":{"code":"invalid_api_key","message":"credential rejected"}}`,
		`{"error":{"type":"authentication_error","message":"credential rejected"}}`,
		`{"response":{"error":{"code":"token_revoked","message":"credential rejected"}}}`,
		`{"error":{"code":"workspace_suspended","message":"workspace unavailable"}}`,
		`{"detail":{"code":"deactivated_workspace","message":"workspace unavailable"}}`,
	} {
		t.Run(payload, func(t *testing.T) {
			repo := &credential403AccountRepo{}
			counter := &openAIAuthPolicy403Counter{counts: []int64{4}}
			svc := &RateLimitService{accountRepo: repo, openAI403CounterCache: counter}
			account := &Account{ID: 303, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

			require.True(t, svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, nil, []byte(payload)))
			require.Len(t, repo.errorMessages, 1)
			require.Contains(t, repo.errorMessages[0], "Access forbidden (403):")
			require.Empty(t, repo.tempReasons)
			require.Len(t, counter.counts, 1, "permanent credential errors do not consume cooldown counters")
		})
	}
}

func TestRateLimitService403WithoutStructuredCredentialStateRemainsTemporary(t *testing.T) {
	for _, payload := range []string{
		`{"error":{"message":"Insufficient account balance"}}`,
		`{"error":{"code":"insufficient_quota","message":"Insufficient account balance"}}`,
		`{"error":{"message":"workspace has been suspended"}}`,
		`{"error":{"message":"invalid_api_key was echoed by the request"}}`,
		`{"error":{"code":"forbidden","message":"request rejected"},"echo":{"code":"invalid_api_key"}}`,
	} {
		t.Run(payload, func(t *testing.T) {
			repo := &credential403AccountRepo{}
			svc := &RateLimitService{accountRepo: repo}
			account := &Account{ID: 304, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

			require.True(t, svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, nil, []byte(payload)))
			require.Empty(t, repo.errorMessages)
			require.Len(t, repo.tempReasons, 1)
			require.Contains(t, repo.tempReasons[0], OpenAI403CooldownReasonPrefix)
		})
	}
}
