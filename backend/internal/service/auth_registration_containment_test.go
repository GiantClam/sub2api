//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthService_RegisterCaptchaSkipStillRequiresVerifiedEmail(t *testing.T) {
	for _, tc := range []struct {
		name     string
		code     string
		cache    *emailCacheStub
		disabled bool
		wantErr  error
	}{
		{"missing code", "", &emailCacheStub{}, false, ErrEmailVerifyRequired},
		{"arbitrary code", "111111", &emailCacheStub{data: &VerificationCodeData{Code: "654321"}}, false, ErrInvalidVerifyCode},
		{"no cached code", "654321", &emailCacheStub{}, false, ErrInvalidVerifyCode},
		{"cache failure", "654321", &emailCacheStub{err: errors.New("cache unavailable")}, false, ErrInvalidVerifyCode},
		{"attempts exhausted", "654321", &emailCacheStub{data: &VerificationCodeData{Code: "654321", Attempts: maxVerifyCodeAttempts}}, false, ErrVerifyCodeMaxAttempts},
		{"registration disabled", "654321", &emailCacheStub{data: &VerificationCodeData{Code: "654321"}}, true, ErrRegDisabled},
		{"verified email", "654321", &emailCacheStub{data: &VerificationCodeData{Code: "654321"}}, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &userRepoStub{nextID: 7}
			registrationEnabled := "true"
			if tc.disabled {
				registrationEnabled = "false"
			}
			svc := newAuthService(repo, map[string]string{
				SettingKeyRegistrationEnabled:                 registrationEnabled,
				SettingKeyEmailVerifyEnabled:                  "true",
				SettingKeyTurnstileEnabled:                    "true",
				SettingKeyAuthSourceDefaultEmailGrantOnSignup: "false",
			}, tc.cache, nil)
			verifier := &turnstileVerifierSpy{}
			svc.turnstileService = NewTurnstileService(svc.settingService, verifier)
			ctx := context.Background()
			if tc.code != "" {
				// Match the handler's two-stage flow: a nonempty email code skips
				// the second captcha challenge, but never proves email ownership.
				require.NoError(t, svc.VerifyCaptchaForRegister(ctx, CaptchaProof{}, "198.51.100.10", tc.code))
				require.Zero(t, verifier.called)
			}
			token, user, err := svc.RegisterWithVerification(ctx, "user@test.com", "password", tc.code, "", "", "")
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				require.Empty(t, token)
				require.Nil(t, user)
				require.Empty(t, repo.created, "invalid proof must never create a user")
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, token)
			require.Equal(t, RoleUser, user.Role)
			require.Len(t, repo.created, 1)
		})
	}
}

type registrationPolicyFailureRepo struct {
	settingRepoStub
	emailReads int
}

func (s *registrationPolicyFailureRepo) GetValue(ctx context.Context, key string) (string, error) {
	if key == SettingKeyEmailVerifyEnabled {
		s.emailReads++
		if s.emailReads > 1 {
			return "", errors.New("email policy unavailable")
		}
	}
	return s.settingRepoStub.GetValue(ctx, key)
}

func (s *registrationPolicyFailureRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("registration policy unavailable")
}

func TestAuthService_RegisterFailsClosedAfterCaptchaEmailPolicyLookupFailure(t *testing.T) {
	repo := &userRepoStub{nextID: 7}
	svc := newAuthService(repo, map[string]string{}, &emailCacheStub{}, nil)
	settings := &registrationPolicyFailureRepo{settingRepoStub: settingRepoStub{values: map[string]string{
		SettingKeyRegistrationEnabled: "true",
		SettingKeyEmailVerifyEnabled:  "true",
	}}}
	svc.settingService = NewSettingService(settings, svc.cfg)
	ctx := context.Background()
	require.NoError(t, svc.VerifyCaptchaForRegister(ctx, CaptchaProof{}, "198.51.100.10", "111111"))
	token, user, err := svc.RegisterWithVerification(ctx, "user@test.com", "password", "111111", "", "", "")
	require.ErrorIs(t, err, ErrServiceUnavailable)
	require.Empty(t, token)
	require.Nil(t, user)
	require.Empty(t, repo.created)
}

func TestAuthService_RegisterRejectsMissingOrInvalidEmailPolicy(t *testing.T) {
	for _, raw := range []string{"missing", "", "TRUE", "invalid"} {
		t.Run(raw, func(t *testing.T) {
			repo := &userRepoStub{nextID: 7}
			svc := newAuthService(repo, map[string]string{}, &emailCacheStub{}, nil)
			values := map[string]string{SettingKeyRegistrationEnabled: "true"}
			if raw != "missing" {
				values[SettingKeyEmailVerifyEnabled] = raw
			}
			svc.settingService = NewSettingService(&settingRepoStub{values: values}, svc.cfg)
			token, user, err := svc.RegisterWithVerification(context.Background(), "user@test.com", "password", "111111", "", "", "")
			require.ErrorIs(t, err, ErrServiceUnavailable)
			require.Empty(t, token)
			require.Nil(t, user)
			require.Empty(t, repo.created)
			require.ErrorIs(t, svc.VerifyCaptchaForRegister(context.Background(), CaptchaProof{}, "198.51.100.10", "111111"), ErrServiceUnavailable)
			_, _, err = svc.RegisterWithCaptcha(context.Background(), "user@test.com", "password", "111111", "", "", "", CaptchaProof{}, "198.51.100.10")
			require.ErrorIs(t, err, ErrServiceUnavailable)
			require.Empty(t, repo.created)
		})
	}
}

type changingRegistrationPolicyRepo struct {
	settingRepoStub
	policyReads int
	emailReads  int
}

func (s *changingRegistrationPolicyRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	values, err := s.settingRepoStub.GetMultiple(ctx, keys)
	for _, key := range keys {
		if key == SettingKeyRegistrationEnabled {
			s.policyReads++
			// Simulate a change between the captcha and account-creation stages.
			s.mu.Lock()
			s.values[SettingKeyEmailVerifyEnabled] = "false"
			s.mu.Unlock()
			break
		}
	}
	return values, err
}

func (s *changingRegistrationPolicyRepo) GetValue(ctx context.Context, key string) (string, error) {
	if key == SettingKeyEmailVerifyEnabled {
		s.emailReads++
		return "", errors.New("email policy unavailable after initial read")
	}
	return s.settingRepoStub.GetValue(ctx, key)
}

func TestAuthService_RegisterWithCaptchaUsesOneValidatedPolicy(t *testing.T) {
	repo := &userRepoStub{nextID: 7}
	svc := newAuthService(repo, map[string]string{}, &emailCacheStub{data: &VerificationCodeData{Code: "654321"}}, nil)
	settings := &changingRegistrationPolicyRepo{settingRepoStub: settingRepoStub{values: map[string]string{
		SettingKeyRegistrationEnabled: "true",
		SettingKeyEmailVerifyEnabled:  "true",
	}}}
	svc.settingService = NewSettingService(settings, svc.cfg)
	token, user, err := svc.RegisterWithCaptcha(context.Background(), "user@test.com", "password", "111111", "", "", "", CaptchaProof{}, "198.51.100.10")
	require.ErrorIs(t, err, ErrInvalidVerifyCode)
	require.Empty(t, token)
	require.Nil(t, user)
	require.Empty(t, repo.created)
	require.Equal(t, 1, settings.policyReads)
	require.Zero(t, settings.emailReads, "captcha and registration must not re-read email policy separately")
}

func TestAuthService_RegisterWithCaptchaRejectsPolicyReadFailure(t *testing.T) {
	repo := &userRepoStub{nextID: 7}
	svc := newAuthService(repo, map[string]string{}, &emailCacheStub{}, nil)
	svc.settingService = NewSettingService(&settingRepoStub{err: errors.New("settings unavailable")}, svc.cfg)
	token, user, err := svc.RegisterWithCaptcha(context.Background(), "user@test.com", "password", "111111", "", "", "", CaptchaProof{}, "198.51.100.10")
	require.ErrorIs(t, err, ErrServiceUnavailable)
	require.Empty(t, token)
	require.Nil(t, user)
	require.Empty(t, repo.created)
}

func TestAuthService_RegisterWithCaptchaRejectsMissingEmailCode(t *testing.T) {
	repo := &userRepoStub{nextID: 7}
	svc := newAuthService(repo, map[string]string{
		SettingKeyRegistrationEnabled: "true",
		SettingKeyEmailVerifyEnabled:  "true",
	}, &emailCacheStub{}, nil)
	token, user, err := svc.RegisterWithCaptcha(context.Background(), "user@test.com", "password", "", "", "", "", CaptchaProof{}, "198.51.100.10")
	require.ErrorIs(t, err, ErrEmailVerifyRequired)
	require.Empty(t, token)
	require.Nil(t, user)
	require.Empty(t, repo.created)
}

func TestAuthService_SendVerifyCodeAsyncRequiresEmailService(t *testing.T) {
	svc := newAuthService(&userRepoStub{}, map[string]string{SettingKeyRegistrationEnabled: "true"}, nil, nil)
	result, err := svc.SendVerifyCodeAsync(context.Background(), "user@test.com")
	require.ErrorIs(t, err, ErrServiceUnavailable)
	require.Nil(t, result)
}

func TestAuthService_SendVerifyCodeAsyncPreflightRejectsUnconfiguredEmail(t *testing.T) {
	t.Setenv(cloudflareAuthEmailEnabledEnv, "false")
	lookupErr := errors.New("mail settings unavailable")
	for _, tc := range []struct {
		name    string
		repoErr error
		wantErr error
	}{
		{"missing SMTP configuration", nil, ErrEmailNotConfigured},
		{"SMTP lookup failure", lookupErr, lookupErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newAuthService(&userRepoStub{}, map[string]string{SettingKeyRegistrationEnabled: "true"}, &emailCacheStub{}, nil)
			svc.emailService.settingRepo = &settingRepoStub{values: map[string]string{}, err: tc.repoErr}
			// No workers: a mistakenly accepted task remains observable without
			// generating a code or attempting delivery.
			svc.emailQueueService = &EmailQueueService{taskChan: make(chan EmailTask, 1), stopChan: make(chan struct{})}
			result, err := svc.SendVerifyCodeAsync(context.Background(), "user@test.com")
			require.ErrorIs(t, err, tc.wantErr)
			require.Nil(t, result)
			require.Empty(t, svc.emailQueueService.taskChan)
		})
	}
}
