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

func TestEasyPayMaintenanceBlocksPersistedOrderEntryPoints(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "true")
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusCompleted, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetProviderSnapshot(map[string]any{"provider_key": payment.TypeEasyPay}).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client}
	for name, call := range map[string]func() error{
		"notification": func() error {
			return svc.HandlePaymentNotification(ctx, &payment.PaymentNotification{
				OrderID: order.OutTradeNo, Amount: order.PayAmount, Status: payment.NotificationStatusSuccess,
			}, payment.TypeEasyPay)
		},
		"balance retry":      func() error { return svc.ExecuteBalanceFulfillment(ctx, order.ID) },
		"subscription retry": func() error { return svc.ExecuteSubscriptionFulfillment(ctx, order.ID) },
		"authenticated verify": func() error {
			_, err := svc.VerifyOrderByOutTradeNo(ctx, order.OutTradeNo, order.UserID)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) { require.Equal(t, 503, infraerrors.Code(call())) })
	}
	current, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, order.Status, current.Status)
}

func TestEasyPayMaintenanceBlocksPendingQueryAndResume(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "true")
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPending, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetProviderKey(payment.TypeEasyPay).Save(ctx)
	require.NoError(t, err)
	resume := NewPaymentResumeService([]byte("synthetic-maintenance-signing-key"))
	token, err := resume.CreateToken(ResumeTokenClaims{OrderID: order.ID, UserID: order.UserID,
		ProviderKey: payment.TypeEasyPay, PaymentType: order.PaymentType,
		CanonicalReturnURL: "https://site.invalid/payment/result"})
	require.NoError(t, err)
	svc := &PaymentService{entClient: client, resumeService: resume}
	_, err = svc.VerifyOrderByOutTradeNo(ctx, order.OutTradeNo, order.UserID)
	require.Equal(t, 503, infraerrors.Code(err))
	_, err = svc.GetPublicOrderByResumeToken(ctx, token)
	require.Equal(t, 503, infraerrors.Code(err))
	_, err = svc.getOrderProvider(ctx, order)
	require.Equal(t, 503, infraerrors.Code(err), "scheduled reconciliation must not acquire a query provider")
}

func TestEasyPayMaintenancePreventsStatusAndFulfillmentMutations(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "true")
	for _, status := range []string{OrderStatusPending, OrderStatusPaid, OrderStatusFailed, OrderStatusRecharging} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, status, time.Now())
			inst, err := client.PaymentProviderInstance.Create().
				SetProviderKey(payment.TypeEasyPay).SetName("disabled-easypay").
				SetConfig("{}").SetSupportedTypes(payment.TypeAlipay).SetEnabled(false).Save(ctx)
			require.NoError(t, err)
			order, err = client.PaymentOrder.UpdateOneID(order.ID).SetProviderSnapshot(map[string]any{
				"provider_key": payment.TypeEasyPay, "provider_instance_id": strconv.FormatInt(inst.ID, 10),
			}).Save(ctx)
			require.NoError(t, err)
			svc := &PaymentService{entClient: client}
			// Even a caller labelling a pinned EasyPay order as another provider cannot advance it.
			err = svc.HandlePaymentNotification(ctx, &payment.PaymentNotification{
				OrderID: order.OutTradeNo, Amount: order.PayAmount, Status: payment.NotificationStatusSuccess,
			}, payment.TypeStripe)
			require.Equal(t, 503, infraerrors.Code(err))
			require.Equal(t, 503, infraerrors.Code(svc.ExecuteBalanceFulfillment(ctx, order.ID)))
			require.Equal(t, 503, infraerrors.Code(svc.ExecuteSubscriptionFulfillment(ctx, order.ID)))
			require.Equal(t, 503, infraerrors.Code(svc.RetryFulfillment(ctx, order.ID)))
			_, err = svc.getOrderProvider(ctx, order)
			require.Equal(t, 503, infraerrors.Code(err))
			current, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, status, current.Status)
			require.Equal(t, order.PaymentTradeNo, current.PaymentTradeNo)
			count, err := client.PaymentAuditLog.Query().Count(ctx)
			require.NoError(t, err)
			require.Zero(t, count, "paused retry must not record a fulfillment attempt")
			user, err := client.User.Get(ctx, order.UserID)
			require.NoError(t, err)
			require.Zero(t, user.Balance)
		})
	}
}

func TestEasyPayMaintenanceDefaultsOffAndOnlyMatchesEasyPay(t *testing.T) {
	for _, value := range []string{"", "false", "0"} {
		t.Setenv("EASYPAY_MAINTENANCE_ENABLED", value)
		require.NoError(t, CheckEasyPayMaintenance(payment.TypeEasyPay))
	}
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "true")
	require.Equal(t, 503, infraerrors.Code(CheckEasyPayMaintenance(" EasyPay ")))
	require.NoError(t, CheckEasyPayMaintenance(payment.TypeStripe))
	require.NoError(t, CheckEasyPayMaintenance(payment.TypeWxpay))
}

func TestEasyPayMaintenanceResolvesDisabledLegacyInstance(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "true")
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeEasyPay).SetName("disabled-legacy-easypay").
		SetConfig("{}").SetSupportedTypes(payment.TypeAlipay).SetEnabled(false).Save(ctx)
	require.NoError(t, err)
	instanceID := strconv.FormatInt(inst.ID, 10)
	svc := &PaymentService{entClient: client}
	for name, order := range map[string]*dbent.PaymentOrder{
		"pinned column": {PaymentType: payment.TypeAlipay, ProviderInstanceID: &instanceID},
		"legacy unique": {PaymentType: payment.TypeAlipay},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, 503, infraerrors.Code(svc.checkOrderEasyPayMaintenance(ctx, order)))
		})
	}
}

func TestEasyPayMaintenanceBlocksWebhookProviderBeforeLookup(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "true")
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	registry := payment.NewRegistry()
	registry.Register(webhookProviderTestDouble{key: payment.TypeEasyPay, types: []payment.PaymentType{payment.TypeAlipay}})
	svc := &PaymentService{entClient: client, registry: registry, providersLoaded: true}
	_, err := svc.GetWebhookProviders(ctx, payment.TypeEasyPay, "")
	require.Equal(t, 503, infraerrors.Code(err))
}

func TestEasyPayMaintenancePreservesStripeQueryAndNotification(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "true")
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPending, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetPaymentType(payment.TypeStripe).Save(ctx)
	require.NoError(t, err)
	provider := &paymentOrderLifecycleQueryProvider{key: payment.TypeStripe,
		resp: &payment.QueryOrderResponse{Status: payment.ProviderStatusPending}}
	registry := payment.NewRegistry()
	registry.Register(provider)
	svc := &PaymentService{entClient: client, registry: registry, providersLoaded: true}
	_, err = svc.VerifyOrderByOutTradeNo(ctx, order.OutTradeNo, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 1, provider.queryCalls)
	svc.loadBalancer = maintenanceCreateLoadBalancer{key: payment.TypeStripe}
	selection, err := svc.selectCreateOrderInstance(ctx, CreateOrderRequest{PaymentType: payment.TypeStripe}, &PaymentConfig{}, 100)
	require.NoError(t, err)
	require.Equal(t, payment.TypeStripe, selection.ProviderKey, "Stripe remains selectable for new orders")
	_, err = client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, svc.HandlePaymentNotification(ctx, &payment.PaymentNotification{
		OrderID: order.OutTradeNo, Amount: order.PayAmount, Status: payment.NotificationStatusSuccess,
	}, payment.TypeStripe))
}

type maintenanceCreateLoadBalancer struct{ key string }

func (b maintenanceCreateLoadBalancer) GetInstanceConfig(context.Context, int64) (map[string]string, error) {
	return map[string]string{}, nil
}
func (b maintenanceCreateLoadBalancer) SelectInstance(context.Context, string, payment.PaymentType, payment.Strategy, float64) (*payment.InstanceSelection, error) {
	return &payment.InstanceSelection{ProviderKey: b.key, SupportedTypes: payment.TypeAlipay}, nil
}

func TestEasyPayMaintenanceRejectsOrderBeforePersistence(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "true")
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	settings := &paymentConfigSettingRepoStub{values: map[string]string{SettingPaymentEnabled: "true"}}
	svc := &PaymentService{entClient: client,
		configService: &PaymentConfigService{settingRepo: settings, entClient: client},
		userRepo:      &mockUserRepo{getByIDUser: &User{ID: 1, Email: "payer@site.invalid", Status: StatusActive}},
		loadBalancer:  maintenanceCreateLoadBalancer{key: payment.TypeEasyPay}}
	_, err := svc.CreateOrder(ctx, CreateOrderRequest{UserID: 1, Amount: 100, PaymentType: payment.TypeAlipay})
	require.Equal(t, 503, infraerrors.Code(err))
	count, err := client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}

var _ payment.LoadBalancer = maintenanceCreateLoadBalancer{}

func TestEasyPayMaintenanceRejectsAmbiguousLegacyFulfillment(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "true")
	for _, method := range []string{payment.TypeAlipay, payment.TypeWxpay, "legacycoin"} {
		t.Run(method, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPaid, time.Now())
			order, err := client.PaymentOrder.UpdateOneID(order.ID).SetPaymentType(method).Save(ctx)
			require.NoError(t, err)
			for _, key := range []string{payment.TypeEasyPay, method} {
				_, err := client.PaymentProviderInstance.Create().SetProviderKey(key).
					SetName(key).SetConfig("{}").SetSupportedTypes(method).
					SetEnabled(key != payment.TypeEasyPay).Save(ctx)
				require.NoError(t, err)
			}
			svc := &PaymentService{entClient: client, registry: payment.NewRegistry()}
			require.Equal(t, 503, infraerrors.Code(svc.checkOrderEasyPayMaintenance(ctx, order)))
			require.Equal(t, 503, infraerrors.Code(svc.RetryFulfillment(ctx, order.ID)))
			require.Equal(t, 503, infraerrors.Code(svc.ExecuteBalanceFulfillment(ctx, order.ID)))
			require.Equal(t, 503, infraerrors.Code(svc.ExecuteSubscriptionFulfillment(ctx, order.ID)))
			_, err = svc.getOrderProvider(ctx, order)
			require.Equal(t, 503, infraerrors.Code(err))
			current, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusPaid, current.Status)
			count, err := client.PaymentAuditLog.Query().Count(ctx)
			require.NoError(t, err)
			require.Zero(t, count)
		})
	}
}

type maintenanceColdRegistryLoadBalancer struct {
	maintenanceCreateLoadBalancer
	config map[string]string
}

func (b maintenanceColdRegistryLoadBalancer) GetInstanceConfig(context.Context, int64) (map[string]string, error) {
	return b.config, nil
}

func TestEasyPayMaintenanceChecksActualProviderAfterColdRegistryLoad(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "true")
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	// Deliberately inconsistent legacy config: the initialized provider must be
	// checked even when the visible method was provisionally treated as Stripe.
	_, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeEasyPay).
		SetName("cold-easypay").SetConfig("{}").SetSupportedTypes(payment.TypeWxpay).SetEnabled(true).Save(ctx)
	require.NoError(t, err)
	cfg := validEasyPayProviderConfig(t)
	cfg["customMethods"] = `[{"type":"stripe","upstreamType":"legacycoin","displayName":"Legacy coin"}]`
	svc := &PaymentService{entClient: client, registry: payment.NewRegistry(),
		loadBalancer: maintenanceColdRegistryLoadBalancer{config: cfg}}
	prov, err := svc.getOrderProvider(ctx, &dbent.PaymentOrder{PaymentType: payment.TypeStripe})
	require.True(t, svc.providersLoaded, "exercise the cold registry fallback")
	require.Equal(t, 503, infraerrors.Code(err))
	require.Nil(t, prov, "reconciliation must not receive the EasyPay query provider")
}
