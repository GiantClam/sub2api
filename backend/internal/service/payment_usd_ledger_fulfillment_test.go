//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestUSDLedgerFulfillmentRetainsFrozenCreditAndIgnoresReplay(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
	user, err := client.User.Create().SetEmail("usd-fulfillment@example.com").SetPasswordHash("test").Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().SetUserID(user.ID).SetUserEmail(user.Email).
		SetUserName("USD verification").SetAmount(2.30769231).SetPayAmount(15).SetFeeRate(0).
		SetRechargeCode("USD-FROZEN-FULFILLMENT").SetOutTradeNo("usd_frozen_fulfillment").
		SetPaymentTradeNo("").SetClientIP("127.0.0.1").SetSrcHost("api.example.com").
		SetPaymentType(payment.TypeAlipay).SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusPaid).SetExpiresAt(time.Now().Add(time.Hour)).
		SetProviderSnapshot(map[string]any{"ledger_currency": "USD", "currency": "CNY", "balance_usd_to_cny_rate": 6.5, "provider_key": payment.TypeAlipay}).Save(ctx)
	require.NoError(t, err)
	settings := &paymentFulfillmentSettingRepoStub{values: map[string]string{SettingBalanceUSDToCNYRate: "7"}}
	cfgService := &PaymentConfigService{settingRepo: settings}
	cfg, err := cfgService.GetPaymentConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, 7.0, cfg.BalanceUSDToCNYRate)

	credits, credited := 0, 0.0
	userRepo := &mockUserRepo{getByIDUser: &User{ID: user.ID, Balance: 0}}
	userRepo.updateBalanceFn = func(_ context.Context, id int64, amount float64) error {
		require.Equal(t, user.ID, id)
		credits++
		credited += amount
		return nil
	}
	redeemRepo := &paymentFulfillmentRedeemRepo{}
	cache := &paymentFulfillmentRedeemCacheStub{}
	redeemService := NewRedeemService(redeemRepo, userRepo, nil, cache, nil, client, nil, nil)
	svc := &PaymentService{entClient: client, configService: cfgService, redeemService: redeemService, userRepo: userRepo}
	require.NoError(t, svc.ExecuteBalanceFulfillment(ctx, order.ID))
	require.NoError(t, svc.ExecuteBalanceFulfillment(ctx, order.ID))
	notification := &payment.PaymentNotification{TradeNo: "usd-replay", OrderID: order.OutTradeNo, Amount: 15, Status: payment.NotificationStatusSuccess}
	require.NoError(t, svc.HandlePaymentNotification(ctx, notification, payment.TypeAlipay))
	require.NoError(t, svc.HandlePaymentNotification(ctx, notification, payment.TypeAlipay))
	require.Equal(t, 1, credits)
	require.Equal(t, 2.30769231, credited)
	require.Equal(t, 1, redeemRepo.createCalls)
	require.Len(t, redeemRepo.useCalls, 1)
	require.Equal(t, 2.30769231, redeemRepo.codesByCode[order.RechargeCode].Value)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.Equal(t, 6.5, reloaded.ProviderSnapshot["balance_usd_to_cny_rate"])
}

func TestUSDLedgerRefundToleranceUsesWalletPrecision(t *testing.T) {
	order := &dbent.PaymentOrder{Amount: 2.30769231, PayAmount: 15, ProviderSnapshot: map[string]any{"ledger_currency": "USD", "currency": "CNY"}}
	require.Equal(t, "USD", BalanceLedgerCurrency(&PaymentConfig{BalanceUSDToCNYRate: 6.5}))
	require.Equal(t, "legacy", BalanceLedgerCurrency(nil))
	require.Equal(t, 1e-8, paymentOrderLedgerAmountTolerance(order))
	// A refund smaller than the full credit must remain proportional, even when
	// its USD difference is smaller than the native CNY payment tolerance.
	require.Equal(t, 14.97, calculateGatewayRefundAmountForOrder(order, order.Amount-0.005))
	delete(order.ProviderSnapshot, "ledger_currency")
	require.Greater(t, paymentOrderLedgerAmountTolerance(order), 1e-8)
}
