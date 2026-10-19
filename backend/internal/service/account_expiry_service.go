package service

import (
	"context"
	"log"
	"sync"
	"time"
)

// AccountExpiryService maintains account expiry and bounded system cooldowns.
type AccountExpiryService struct {
	accountRepo AccountRepository
	interval    time.Duration
	stopCh      chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
}

// Optional so repositories that only support account expiry remain compatible.
type expiredOpenAI403CooldownRecoverer interface {
	RecoverExpiredOpenAI403Cooldowns(ctx context.Context, now time.Time) (int64, error)
}

func NewAccountExpiryService(accountRepo AccountRepository, interval time.Duration) *AccountExpiryService {
	return &AccountExpiryService{
		accountRepo: accountRepo,
		interval:    interval,
		stopCh:      make(chan struct{}),
	}
}

func (s *AccountExpiryService) Start() {
	if s == nil || s.accountRepo == nil || s.interval <= 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		s.runOnce()
		for {
			select {
			case <-ticker.C:
				s.runOnce()
			case <-s.stopCh:
				return
			}
		}
	}()
}

func (s *AccountExpiryService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()
}

func (s *AccountExpiryService) runOnce() {
	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	updated, err := s.accountRepo.AutoPauseExpiredAccounts(ctx, now)
	cancel()
	if err != nil {
		log.Printf("[AccountExpiry] Auto pause expired accounts failed: %v", err)
	} else if updated > 0 {
		log.Printf("[AccountExpiry] Auto paused %d expired accounts", updated)
	}
	if recoverer, ok := s.accountRepo.(expiredOpenAI403CooldownRecoverer); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		updated, err = recoverer.RecoverExpiredOpenAI403Cooldowns(ctx, now)
		if err != nil {
			log.Printf("[AccountExpiry] Recover expired OpenAI 403 cooldowns failed: %v", err)
		} else if updated > 0 {
			log.Printf("[AccountExpiry] Recovered %d expired OpenAI 403 cooldowns", updated)
		}
	}
}
