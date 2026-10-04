package service

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func rechargeTierFixture() RechargeTier {
	return RechargeTier{
		ID: "trial", Name: "体验包", Description: "先试一轮", OrderType: payment.OrderTypeBalance,
		Prices: CurrencyAmounts{"CNY": 3, "USD": 0.46}, Featured: true,
	}
}

func TestRechargeTiersConfigPreservesOmittedAndClearsExplicitEmpty(t *testing.T) {
	ctx := context.Background()
	repo := &paymentConfigSettingRepoStub{values: map[string]string{}}
	svc := &PaymentConfigService{settingRepo: repo}
	tiers := []RechargeTier{rechargeTierFixture()}
	require.NoError(t, svc.UpdatePaymentConfig(ctx, UpdatePaymentConfigRequest{RechargeTiers: &tiers}))
	stored := repo.values[SettingPaymentRechargeTiers]
	require.NotEmpty(t, stored)
	cfg, err := svc.GetPaymentConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, tiers, cfg.RechargeTiers)

	enabled := true
	require.NoError(t, svc.UpdatePaymentConfig(ctx, UpdatePaymentConfigRequest{Enabled: &enabled}))
	require.Equal(t, stored, repo.values[SettingPaymentRechargeTiers])
	require.NotContains(t, repo.updates, SettingPaymentRechargeTiers)

	empty := []RechargeTier{}
	require.NoError(t, svc.UpdatePaymentConfig(ctx, UpdatePaymentConfigRequest{RechargeTiers: &empty}))
	require.Equal(t, "[]", repo.values[SettingPaymentRechargeTiers])
	cfg, err = svc.GetPaymentConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, cfg.RechargeTiers)
	require.Empty(t, cfg.RechargeTiers)
}

func TestRechargeTiersConfigMissingIsEmptyAndMalformedFails(t *testing.T) {
	for _, raw := range []string{"", "[]", "null"} {
		svc := &PaymentConfigService{settingRepo: &paymentConfigSettingRepoStub{values: map[string]string{SettingPaymentRechargeTiers: raw}}}
		cfg, err := svc.GetPaymentConfig(context.Background())
		require.NoError(t, err)
		require.NotNil(t, cfg.RechargeTiers)
		require.Empty(t, cfg.RechargeTiers)
	}
	for _, raw := range []string{"{broken", `[{"id":"trial","name":"Trial","prices":{"USD":0}}]`} {
		svc := &PaymentConfigService{settingRepo: &paymentConfigSettingRepoStub{values: map[string]string{SettingPaymentRechargeTiers: raw}}}
		_, err := svc.GetPaymentConfig(context.Background())
		require.Error(t, err)
	}
}

func TestNormalizeRechargeTiersValidatesAndCopies(t *testing.T) {
	tier := rechargeTierFixture()
	tier.Name = "  体验包  "
	tier.OrderType = ""
	tier.Prices = CurrencyAmounts{" usd ": 0.46, "jpy": 300, "KWD": 1.234}
	got, err := normalizeRechargeTiers([]RechargeTier{tier})
	require.NoError(t, err)
	require.Equal(t, "体验包", got[0].Name)
	require.Equal(t, payment.OrderTypeBalance, got[0].OrderType)
	require.Equal(t, CurrencyAmounts{"USD": 0.46, "JPY": 300, "KWD": 1.234}, got[0].Prices)
	require.Equal(t, "  体验包  ", tier.Name)
	got[0].Prices["USD"] = 1
	require.Equal(t, 0.46, tier.Prices[" usd "])

	cases := []struct {
		name string
		edit func(*RechargeTier)
	}{
		{"empty id", func(tier *RechargeTier) { tier.ID = "" }},
		{"unsafe id", func(tier *RechargeTier) { tier.ID = "trial/other" }},
		{"long id", func(tier *RechargeTier) { tier.ID = strings.Repeat("a", 65) }},
		{"empty name", func(tier *RechargeTier) { tier.Name = "  " }},
		{"long unicode name", func(tier *RechargeTier) { tier.Name = strings.Repeat("中", 121) }},
		{"long description", func(tier *RechargeTier) { tier.Description = strings.Repeat("中", 2001) }},
		{"subscription", func(tier *RechargeTier) { tier.OrderType = payment.OrderTypeSubscription }},
		{"empty prices", func(tier *RechargeTier) { tier.Prices = CurrencyAmounts{} }},
		{"zero", func(tier *RechargeTier) { tier.Prices = CurrencyAmounts{"USD": 0} }},
		{"negative", func(tier *RechargeTier) { tier.Prices = CurrencyAmounts{"USD": -1} }},
		{"nan", func(tier *RechargeTier) { tier.Prices = CurrencyAmounts{"USD": math.NaN()} }},
		{"infinite", func(tier *RechargeTier) { tier.Prices = CurrencyAmounts{"USD": math.Inf(1)} }},
		{"unknown currency", func(tier *RechargeTier) { tier.Prices = CurrencyAmounts{"ZZZ": 1} }},
		{"empty currency", func(tier *RechargeTier) { tier.Prices = CurrencyAmounts{"": 1} }},
		{"duplicate normalized currency", func(tier *RechargeTier) { tier.Prices = CurrencyAmounts{"USD": 1, "usd": 2} }},
		{"usd precision", func(tier *RechargeTier) { tier.Prices = CurrencyAmounts{"USD": 0.461} }},
		{"jpy precision", func(tier *RechargeTier) { tier.Prices = CurrencyAmounts{"JPY": 300.1} }},
		{"overflow", func(tier *RechargeTier) { tier.Prices = CurrencyAmounts{"USD": 1e20} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tier := rechargeTierFixture()
			tc.edit(&tier)
			repo := &paymentConfigSettingRepoStub{values: map[string]string{}}
			svc := &PaymentConfigService{settingRepo: repo}
			tiers := []RechargeTier{tier}
			require.Error(t, svc.UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{RechargeTiers: &tiers}))
			require.Empty(t, repo.updates)
		})
	}
	_, err = normalizeRechargeTiers([]RechargeTier{rechargeTierFixture(), rechargeTierFixture()})
	require.Error(t, err)
}

func TestCheckoutRechargeTiersUsesActualCurrencyFeesAndCreditRules(t *testing.T) {
	cfg := &PaymentConfig{Enabled: true, BalanceRechargeMultiplier: 2, RechargeFeeRate: 1, RechargeTiers: []RechargeTier{rechargeTierFixture()}}
	methods := map[string]MethodLimits{
		"stripe":    {Currency: "USD", FeeRate: 99},
		"alipay":    {Currency: "CNY"},
		"airwallex": {Currency: "HKD"},
	}
	tiers := BuildCheckoutRechargeTiers(cfg, methods)
	require.Len(t, tiers, 1)
	require.Equal(t, "trial", tiers[0].ID)
	require.Equal(t, payment.OrderTypeBalance, tiers[0].OrderType)
	require.True(t, tiers[0].Featured)
	require.Equal(t, []RechargePaymentOption{
		{PaymentType: "alipay", Currency: "CNY", Amount: 3, PayAmount: 3.03, CreditedBalance: 6},
		{PaymentType: "stripe", Currency: "USD", Amount: 0.46, PayAmount: 0.47, CreditedBalance: 0.92},
	}, tiers[0].PaymentOptions)
}

func TestCheckoutRechargeTiersFiltersUnavailableAmountsAndDisabledCatalog(t *testing.T) {
	tests := []struct {
		name        string
		cfg         PaymentConfig
		methods     map[string]MethodLimits
		wantMethods []string
	}{
		{"all methods", PaymentConfig{Enabled: true}, map[string]MethodLimits{"alipay": {Currency: "CNY"}, "stripe": {Currency: "USD"}}, []string{"alipay", "stripe"}},
		{"global minimum", PaymentConfig{Enabled: true, MinAmount: 1}, map[string]MethodLimits{"alipay": {Currency: "CNY"}, "stripe": {Currency: "USD"}}, []string{"alipay"}},
		{"global maximum", PaymentConfig{Enabled: true, MaxAmount: 2}, map[string]MethodLimits{"alipay": {Currency: "CNY"}, "stripe": {Currency: "USD"}}, []string{"stripe"}},
		{"method limits use fee-inclusive payment", PaymentConfig{Enabled: true, RechargeFeeRate: 1}, map[string]MethodLimits{"alipay": {Currency: "CNY", SingleMax: 3}, "stripe": {Currency: "USD", SingleMin: 0.47}}, []string{"stripe"}},
		{"no currency fallback", PaymentConfig{Enabled: true}, map[string]MethodLimits{"alipay": {}, "stripe": {Currency: "HKD"}}, nil},
		{"no methods", PaymentConfig{Enabled: true}, nil, nil},
		{"payment disabled", PaymentConfig{}, map[string]MethodLimits{"alipay": {Currency: "CNY"}}, nil},
		{"balance disabled", PaymentConfig{Enabled: true, BalanceDisabled: true}, map[string]MethodLimits{"alipay": {Currency: "CNY"}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.RechargeTiers = []RechargeTier{rechargeTierFixture()}
			got := BuildCheckoutRechargeTiers(&tc.cfg, tc.methods)
			require.NotNil(t, got)
			if len(tc.wantMethods) == 0 {
				require.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			for i, method := range tc.wantMethods {
				require.Equal(t, method, got[0].PaymentOptions[i].PaymentType)
			}
			require.Len(t, got[0].PaymentOptions, len(tc.wantMethods))
		})
	}
	require.NotNil(t, BuildCheckoutRechargeTiers(nil, nil))
	require.Empty(t, BuildCheckoutRechargeTiers(&PaymentConfig{Enabled: true}, map[string]MethodLimits{"alipay": {Currency: "CNY"}}))
}

func TestCheckoutRechargeTiersPreservesNativeRechargePromotions(t *testing.T) {
	tier := rechargeTierFixture()
	methods := map[string]MethodLimits{"alipay": {Currency: "CNY", SingleMin: 2, SingleMax: 4}}
	for _, test := range []struct {
		mode                string
		wantPay, wantCredit float64
	}{
		{RechargeBonusModeBonus, 3.03, 7.2},
		{RechargeBonusModeDiscount, 2.43, 6},
	} {
		t.Run(test.mode, func(t *testing.T) {
			cfg := &PaymentConfig{
				Enabled: true, BalanceRechargeMultiplier: 2, RechargeFeeRate: 1,
				RechargeTiers: []RechargeTier{tier}, RechargeBonusMode: test.mode,
				RechargeBonusTiers: []RechargeBonusTier{{MinAmount: 3, BonusPercent: 20}},
			}
			got := BuildCheckoutRechargeTiers(cfg, methods)
			require.Len(t, got, 1)
			require.Equal(t, []RechargePaymentOption{{PaymentType: "alipay", Currency: "CNY", Amount: 3, PayAmount: test.wantPay, CreditedBalance: test.wantCredit}}, got[0].PaymentOptions)
		})
	}
}
