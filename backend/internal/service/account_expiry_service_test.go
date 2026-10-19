//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type expiryRecoveryRepoStub struct {
	mockAccountRepoForGemini
	pauseErr     error
	recoverErr   error
	pauseCalls   int
	recoverCalls int
	pauseAt      time.Time
	recoverAt    time.Time
}

func (r *expiryRecoveryRepoStub) AutoPauseExpiredAccounts(_ context.Context, now time.Time) (int64, error) {
	r.pauseCalls++
	r.pauseAt = now
	return 0, r.pauseErr
}

func (r *expiryRecoveryRepoStub) RecoverExpiredOpenAI403Cooldowns(_ context.Context, now time.Time) (int64, error) {
	r.recoverCalls++
	r.recoverAt = now
	return 1, r.recoverErr
}

func TestAccountExpiryServiceRunOnceRecoversExpiredCooldowns(t *testing.T) {
	for _, pauseErr := range []error{nil, errors.New("expiry query failed")} {
		repo := &expiryRecoveryRepoStub{pauseErr: pauseErr}
		NewAccountExpiryService(repo, time.Minute).runOnce()
		require.Equal(t, 1, repo.pauseCalls)
		require.Equal(t, 1, repo.recoverCalls, "an independent expiry failure must not skip cooldown recovery")
		require.Equal(t, repo.pauseAt, repo.recoverAt)
	}
}

func TestAccountExpiryServiceOptionalRecoveryKeepsLegacyRepositoriesCompatible(t *testing.T) {
	svc := NewAccountExpiryService(&mockAccountRepoForGemini{}, time.Minute)
	require.NotPanics(t, svc.runOnce)
}

func TestAccountExpiryServiceRecoveryFailureIsRetried(t *testing.T) {
	repo := &expiryRecoveryRepoStub{recoverErr: errors.New("outbox unavailable")}
	svc := NewAccountExpiryService(repo, time.Minute)
	svc.runOnce()
	repo.recoverErr = nil
	svc.runOnce()
	require.Equal(t, 2, repo.recoverCalls)
	require.Equal(t, 2, repo.pauseCalls)
}

type expiryStartupRepoStub struct {
	mockAccountRepoForGemini
	recovered chan struct{}
}

func (r *expiryStartupRepoStub) RecoverExpiredOpenAI403Cooldowns(context.Context, time.Time) (int64, error) {
	r.recovered <- struct{}{}
	return 0, nil
}

func TestAccountExpiryServiceStartRecoversWithoutWaitingForFirstTick(t *testing.T) {
	repo := &expiryStartupRepoStub{recovered: make(chan struct{}, 1)}
	svc := NewAccountExpiryService(repo, time.Hour)
	svc.Start()
	defer svc.Stop()
	select {
	case <-repo.recovered:
	case <-time.After(time.Second):
		t.Fatal("startup must recover expired cooldowns immediately")
	}
}
