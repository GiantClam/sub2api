//go:build unit

package service

import (
	"context"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestEasyPayQuarantineAllowsNewOrdersAndStripe(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "false")
	t.Setenv("EASYPAY_RECONCILIATION_MAX_ORDER_ID", "365")
	svc := &PaymentService{}
	easyPay := payment.TypeEasyPay
	stripe := payment.TypeStripe
	for _, tc := range []struct {
		name    string
		order   *dbent.PaymentOrder
		blocked bool
	}{
		{"old snapshot", &dbent.PaymentOrder{ID: 365, ProviderSnapshot: map[string]any{"provider_key": easyPay}}, true},
		{"old column", &dbent.PaymentOrder{ID: 100, ProviderKey: &easyPay}, true},
		{"unidentified legacy", &dbent.PaymentOrder{ID: 365, PaymentType: payment.TypeAlipay}, true},
		{"new easypay", &dbent.PaymentOrder{ID: 366, ProviderKey: &easyPay}, false},
		{"old stripe", &dbent.PaymentOrder{ID: 1, ProviderKey: &stripe}, false},
		{"legacy stripe", &dbent.PaymentOrder{ID: 1, PaymentType: payment.TypeStripe}, false},
		{"nil order", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.checkOrderEasyPayMaintenance(context.Background(), tc.order)
			if tc.blocked {
				require.Equal(t, 503, infraerrors.Code(err))
			} else {
				require.NoError(t, err)
			}
		})
	}
	// New order selection remains available while only historical orders are frozen.
	require.NoError(t, CheckEasyPayMaintenance(easyPay))
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "true")
	require.Equal(t, 503, infraerrors.Code(svc.checkOrderEasyPayMaintenance(context.Background(), &dbent.PaymentOrder{ID: 366, ProviderKey: &easyPay})))
}

func TestEasyPayQuarantineBlocksPersistedSettlementBeforeWrites(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "false")
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPaid, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetProviderKey(payment.TypeEasyPay).Save(ctx)
	require.NoError(t, err)
	t.Setenv("EASYPAY_RECONCILIATION_MAX_ORDER_ID", strconv.FormatInt(order.ID, 10))
	svc := &PaymentService{entClient: client}
	for _, call := range []func() error{
		func() error {
			return svc.HandlePaymentNotification(ctx, &payment.PaymentNotification{OrderID: order.OutTradeNo, Amount: order.PayAmount, Status: payment.NotificationStatusSuccess}, payment.TypeEasyPay)
		},
		func() error { return svc.ExecuteBalanceFulfillment(ctx, order.ID) },
		func() error { return svc.ExecuteSubscriptionFulfillment(ctx, order.ID) },
		func() error { return svc.RetryFulfillment(ctx, order.ID) },
		func() error { _, err := svc.getOrderProvider(ctx, order); return err },
	} {
		require.Equal(t, 503, infraerrors.Code(call()))
	}
	current, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPaid, current.Status)
	count, err := client.PaymentAuditLog.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
	user, err := client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Zero(t, user.Balance)
}

func TestEasyPayQuarantineInvalidCutoffFailsClosed(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "false")
	svc := &PaymentService{}
	easyPay, stripe := payment.TypeEasyPay, payment.TypeStripe
	for _, value := range []string{"invalid", "-1", "0", "99999999999999999999999"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("EASYPAY_RECONCILIATION_MAX_ORDER_ID", value)
			require.Equal(t, 503, infraerrors.Code(CheckEasyPayMaintenance(easyPay)), "invalid recovery settings must reject new orders too")
			require.Equal(t, 503, infraerrors.Code(svc.checkOrderEasyPayMaintenance(context.Background(), &dbent.PaymentOrder{ID: 400, ProviderKey: &easyPay})))
			require.NoError(t, svc.checkOrderEasyPayMaintenance(context.Background(), &dbent.PaymentOrder{ID: 1, ProviderKey: &stripe}))
		})
	}
	t.Setenv("EASYPAY_RECONCILIATION_MAX_ORDER_ID", "")
	require.NoError(t, svc.checkOrderEasyPayMaintenance(context.Background(), &dbent.PaymentOrder{ID: 1, ProviderKey: &easyPay}))
}

func TestEasyPayQuarantineChecksActualProviderAfterColdRegistryLoad(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "false")
	t.Setenv("EASYPAY_RECONCILIATION_MAX_ORDER_ID", "365")
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	_, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeEasyPay).
		SetName("cold-easypay").SetConfig("{}").SetSupportedTypes(payment.TypeWxpay).SetEnabled(true).Save(ctx)
	require.NoError(t, err)
	cfg := validEasyPayProviderConfig(t)
	cfg["customMethods"] = `[{"type":"stripe","upstreamType":"legacycoin","displayName":"Legacy coin"}]`
	svc := &PaymentService{entClient: client, registry: payment.NewRegistry(),
		loadBalancer: maintenanceColdRegistryLoadBalancer{config: cfg}}
	prov, err := svc.getOrderProvider(ctx, &dbent.PaymentOrder{ID: 365, PaymentType: payment.TypeStripe})
	require.True(t, svc.providersLoaded)
	require.Equal(t, 503, infraerrors.Code(err))
	require.Nil(t, prov)
}
