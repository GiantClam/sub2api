package service

import "context"

type registrationPolicy struct {
	enabled                   bool
	emailVerificationRequired bool
}

// registrationPolicy reads both gates in one repository query. Only an explicit,
// valid false may disable email verification when registration is enabled.
func (s *AuthService) registrationPolicy(ctx context.Context) (registrationPolicy, error) {
	if s.settingService == nil {
		return registrationPolicy{}, nil
	}
	values, err := s.settingService.settingRepo.GetMultiple(ctx, []string{
		SettingKeyRegistrationEnabled, SettingKeyEmailVerifyEnabled,
	})
	if err != nil {
		return registrationPolicy{}, ErrServiceUnavailable
	}
	if values[SettingKeyRegistrationEnabled] != "true" {
		return registrationPolicy{}, nil
	}
	emailRequired, err := parseRegistrationEmailVerification(values[SettingKeyEmailVerifyEnabled])
	if err != nil {
		return registrationPolicy{}, err
	}
	return registrationPolicy{enabled: true, emailVerificationRequired: emailRequired}, nil
}

func parseRegistrationEmailVerification(value string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, ErrServiceUnavailable
	}
}

// RegisterWithCaptcha keeps the captcha decision and email ownership check on
// the same validated policy snapshot for the public registration endpoint.
func (s *AuthService) RegisterWithCaptcha(ctx context.Context, email, password, verifyCode, promoCode, invitationCode, affiliateCode string, proof CaptchaProof, remoteIP string) (string, *User, error) {
	policy, err := s.registrationPolicy(ctx)
	if err != nil {
		return "", nil, err
	}
	if !policy.enabled {
		return "", nil, ErrRegDisabled
	}
	if err := s.verifyCaptchaForRegistration(ctx, policy.emailVerificationRequired, proof, remoteIP, verifyCode); err != nil {
		return "", nil, err
	}
	return s.registerWithPolicy(ctx, policy, email, password, verifyCode, promoCode, invitationCode, affiliateCode)
}
