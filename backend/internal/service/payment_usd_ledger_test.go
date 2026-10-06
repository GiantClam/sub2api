//go:build unit

package service

import (
	"context"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestUSDLedgerQuotesAndFixedTier(t *testing.T) {
	credit := 2.30769231
	cfg := &PaymentConfig{BalanceUSDToCNYRate: 6.5, BalanceRechargeMultiplier: 1}
	require.Equal(t, 10.0, quoteRechargeBonus(cfg, 65, "CNY").Credited)
	require.Equal(t, 10.0, quoteRechargeBonus(cfg, 10, "USD").Credited)
	require.Equal(t, credit, quoteRechargeBonus(cfg, 15, "CNY").Credited)
	cfg.RechargeTiers = []RechargeTier{{ID: "standard", Prices: CurrencyAmounts{"CNY": 15, "USD": 2.31}, CreditedBalance: &credit}}
	require.Equal(t, credit, quoteRechargeBonus(cfg, 15, "CNY").Credited)
	require.Equal(t, credit, quoteRechargeBonus(cfg, 2.31, "USD").Credited)
	require.Equal(t, 20.0, quoteRechargeBonus(cfg, 20, "USD").Credited)
	cfg.RechargeBonusTiers = []RechargeBonusTier{{MinAmount: 2, BonusPercent: 10}}
	require.Equal(t, quoteRechargeBonus(cfg, 15, "CNY").Credited, quoteRechargeBonus(cfg, 2.31, "USD").Credited)
}

func TestUSDLedgerFrozenOrderPrecisionAndRefund(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().SetEmail("usd-ledger@example.com").SetPasswordHash("test").Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client}
	cfg := &PaymentConfig{BalanceUSDToCNYRate: 6.5}
	order, err := svc.createOrderInTx(ctx, CreateOrderRequest{UserID: user.ID, Amount: 15, OrderType: payment.OrderTypeBalance}, &User{ID: user.ID}, nil, cfg, 2.30769231, 15, 0, 15, 0, &payment.InstanceSelection{ProviderKey: payment.TypeAlipay, Config: map[string]string{}})
	require.NoError(t, err)
	require.Equal(t, "USD", PaymentOrderLedgerCurrency(order))
	require.Equal(t, 6.5, order.ProviderSnapshot["balance_usd_to_cny_rate"])
	cfg.BalanceUSDToCNYRate = 7
	persisted, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, 2.30769231, persisted.Amount)
	require.Equal(t, 6.5, persisted.ProviderSnapshot["balance_usd_to_cny_rate"])
	require.Equal(t, 15.0, calculateGatewayRefundAmountForOrder(persisted, persisted.Amount))
	require.Equal(t, 7.5, calculateGatewayRefundAmountForOrder(persisted, persisted.Amount/2))
	require.Equal(t, "legacy", PaymentOrderLedgerCurrency(&dbent.PaymentOrder{Amount: 15}))
}

func TestUSDLedgerValidationAndDailyLimits(t *testing.T) {
	cfg := &PaymentConfig{BalanceUSDToCNYRate: 6.5}
	require.NoError(t, validateBalanceLedgerCurrency(cfg, "CNY"))
	require.NoError(t, validateBalanceLedgerCurrency(cfg, "USD"))
	require.Error(t, validateBalanceLedgerCurrency(cfg, "EUR"))
	cny := &dbent.PaymentOrder{OrderType: payment.OrderTypeBalance, PayAmount: 65, ProviderSnapshot: map[string]any{"currency": "CNY"}}
	usd := &dbent.PaymentOrder{OrderType: payment.OrderTypeBalance, PayAmount: 10, ProviderSnapshot: map[string]any{"currency": "USD"}}
	require.Equal(t, 10.0, paymentOrderDailyLimitAmountUSD(cny, 6.5))
	require.Equal(t, 10.0, paymentOrderDailyLimitAmountUSD(usd, 6.5))
	frozen := &dbent.PaymentOrder{OrderType: payment.OrderTypeBalance, PayAmount: 65, ProviderSnapshot: map[string]any{"payment_base_usd": 10.0}}
	require.Equal(t, 10.0, paymentOrderDailyLimitAmountUSD(frozen, 7))
	a, b := 2.3, 3.0
	_, err := normalizeRechargeTiers([]RechargeTier{{ID: "one", Name: "One", Prices: CurrencyAmounts{"CNY": 15}, CreditedBalance: &a}, {ID: "two", Name: "Two", Prices: CurrencyAmounts{"CNY": 15}, CreditedBalance: &b}})
	require.Error(t, err)
	_, err = normalizeRechargeTiers([]RechargeTier{{ID: "one", Name: "One", Prices: CurrencyAmounts{"CNY": 15}, CreditedBalance: &a}, {ID: "two", Name: "Two", Prices: CurrencyAmounts{"CNY": 15}}})
	require.Error(t, err)
}

func TestUSDLedgerGrandfatheredCurrencyWithoutProviderSnapshot(t *testing.T) {
	order := &dbent.PaymentOrder{OrderType: payment.OrderTypeBalance, Amount: 15, PayAmount: 2.31, ProviderSnapshot: map[string]any{"ledger_currency": "USD", "original_payment_currency": "USD", "ledger_version": 1}}
	require.Nil(t, psOrderProviderSnapshot(order))
	require.Equal(t, "USD", PaymentOrderCurrency(order))
	require.Equal(t, 2.31, calculateGatewayRefundAmountForOrder(order, 15))
	require.Equal(t, 2.31, paymentOrderDailyLimitAmountUSD(order, 6.5))
	order.ProviderSnapshot["schema_version"] = 1
	order.ProviderSnapshot["provider_key"] = "stripe"
	require.NotNil(t, psOrderProviderSnapshot(order))
	require.Equal(t, "USD", PaymentOrderCurrency(order))
}
