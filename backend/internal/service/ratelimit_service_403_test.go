//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type runtimeBlockRecorder struct {
	accounts   []*Account
	until      []time.Time
	reasons    []string
	clearedIDs []int64
}

func (r *runtimeBlockRecorder) BlockAccountScheduling(account *Account, until time.Time, reason string) {
	r.accounts = append(r.accounts, account)
	r.until = append(r.until, until)
	r.reasons = append(r.reasons, reason)
}

func (r *runtimeBlockRecorder) ClearAccountSchedulingBlock(accountID int64) {
	r.clearedIDs = append(r.clearedIDs, accountID)
}

func TestRateLimitService_HandleUpstreamError_OpenAI403FirstHitTempUnschedulable(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &openAI403CounterCacheStub{counts: []int64{1}}
	blocker := &runtimeBlockRecorder{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOpenAI403CounterCache(counter)
	service.SetAccountRuntimeBlocker(blocker)
	account := &Account{
		ID:       301,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"message":"temporary edge rejection"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.Contains(t, repo.lastTempReason, "temporary edge rejection")
	require.Contains(t, repo.lastTempReason, "consecutive_403=1")
	require.Len(t, blocker.accounts, 1)
	require.Equal(t, account.ID, blocker.accounts[0].ID)
	require.Equal(t, "openai_403_temp", blocker.reasons[0])
	require.True(t, blocker.until[0].After(time.Now()))
}

func TestRateLimitService_HandleUpstreamError_OpenAI403RepeatedHitsRemainTemporary(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &openAI403CounterCacheStub{counts: []int64{1, 2, 3, 4}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOpenAI403CounterCache(counter)
	account := &Account{
		ID:       302,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	for hit := 1; hit <= 4; hit++ {
		shouldDisable := service.HandleUpstreamError(
			context.Background(),
			account,
			http.StatusForbidden,
			http.Header{},
			[]byte(`{"error":{"message":"workspace forbidden by policy"}}`),
		)

		require.True(t, shouldDisable)
		require.Zero(t, repo.setErrorCalls, "hit %d must not permanently disable", hit)
		require.Equal(t, hit, repo.tempCalls)
		require.Contains(t, repo.lastTempReason, "workspace forbidden by policy")
	}
}

func TestRateLimitService_OpenAI403DependencyFailuresNeverBecomePermanent(t *testing.T) {
	for _, scenario := range []string{"no_counter", "counter_error", "persist_error"} {
		t.Run(scenario, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			counter := &openAI403CounterCacheStub{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			if scenario != "no_counter" {
				svc.SetOpenAI403CounterCache(counter)
			}
			if scenario == "counter_error" {
				counter.err = errors.New("redis unavailable")
			}
			if scenario == "persist_error" {
				repo.tempErr = errors.New("database unavailable")
			}
			blocker := &runtimeBlockRecorder{}
			svc.SetAccountRuntimeBlocker(blocker)
			account := &Account{ID: 302, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			require.True(t, svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden,
				http.Header{}, []byte(`{"error":{"message":"Insufficient account balance"}}`)))
			require.Zero(t, repo.setErrorCalls)
			require.Equal(t, 1, repo.tempCalls)
			require.Len(t, blocker.until, 1)
			require.WithinDuration(t, time.Now().Add(10*time.Minute), blocker.until[0], time.Second)
		})
	}
}
