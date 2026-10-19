//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type openAI403CooldownAccountRepo struct {
	rateLimitAccountRepoStub
	account *Account
}

func (r *openAI403CooldownAccountRepo) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	if err := r.rateLimitAccountRepoStub.SetTempUnschedulable(ctx, id, until, reason); err != nil {
		return err
	}
	r.account.TempUnschedulableUntil = &until
	r.account.TempUnschedulableReason = reason
	return nil
}

func (r *openAI403CooldownAccountRepo) SetError(ctx context.Context, id int64, message string) error {
	r.account.Status = StatusError
	r.account.Schedulable = false
	return r.rateLimitAccountRepoStub.SetError(ctx, id, message)
}

func TestOpenAI403GatewayCooldownExpiresWithoutPermanentDisable(t *testing.T) {
	account := &Account{ID: 403, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	repo := &openAI403CooldownAccountRepo{account: account}
	rateLimits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rateLimits.SetOpenAI403CounterCache(&openAI403CounterCacheStub{counts: []int64{1, 2, 3, 4}})
	gateway := &OpenAIGatewayService{rateLimitService: rateLimits}
	rateLimits.SetAccountRuntimeBlocker(gateway)

	for hit := 1; hit <= 4; hit++ {
		require.True(t, gateway.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusForbidden,
			http.Header{}, []byte(`{"error":{"message":"Insufficient account balance"}}`), "deepseek-v4.1-flash"))
		require.Zero(t, repo.setErrorCalls)
		require.Equal(t, StatusActive, account.Status)
		require.True(t, account.Schedulable)
		require.False(t, account.IsSchedulable())
		require.True(t, gateway.isOpenAIAccountRequestRuntimeBlocked(account, "deepseek-v4.1-flash"))
		block := gateway.peekOpenAIAccountRuntimeBlock(account)
		require.True(t, block.blocked)
		require.Equal(t, *account.TempUnschedulableUntil, block.until,
			"the gateway bridge must preserve the finite cooldown deadline")
	}

	// Advance both stored deadlines past expiry without waiting ten minutes.
	expired := time.Now().Add(-time.Second)
	account.TempUnschedulableUntil = &expired
	gateway.openaiAccountRuntimeBlockUntil.Store(account.ID, expired)
	require.True(t, account.IsSchedulable())
	require.False(t, gateway.isOpenAIAccountRequestRuntimeBlocked(account, "deepseek-v4.1-flash"))
	require.False(t, gateway.isOpenAIAccountRuntimeBlocked(account))

	account.Schedulable = false
	require.False(t, account.IsSchedulable(), "an administrator's pause survives cooldown expiry")
}
