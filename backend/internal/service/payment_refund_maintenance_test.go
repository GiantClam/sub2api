//go:build unit

package service

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestRefundMaintenanceRejectsBeforeSideEffects(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "true")
	t.Setenv("EASYPAY_RECONCILIATION_MAX_ORDER_ID", "")
	testRefundMaintenanceEntryPoints(t)
}

func TestRefundMaintenanceRejectsFrozenOrdersBeforeSideEffects(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "false")
	t.Setenv("EASYPAY_RECONCILIATION_MAX_ORDER_ID", "365")
	testRefundMaintenanceEntryPoints(t)
}

func testRefundMaintenanceEntryPoints(t *testing.T) {
	t.Helper()
	for _, tradeNo := range []string{"", "synthetic-refund-trade"} {
		name := "with trade number"
		if tradeNo == "" {
			name = "without trade number"
		}
		t.Run(name, func(t *testing.T) {
			for _, entry := range []string{"validate", "request", "prepare", "execute balance", "execute subscription", "gateway", "query"} {
				t.Run(entry, func(t *testing.T) {
					ctx := context.Background()
					client := newPaymentConfigServiceTestClient(t)
					order := createEasyPayRefundMaintenanceOrder(t, ctx, client, tradeNo)
					if entry == "query" {
						var err error
						order, err = client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefundPending).Save(ctx)
						require.NoError(t, err)
					}
					if entry == "execute subscription" {
						var err error
						order, err = client.PaymentOrder.UpdateOneID(order.ID).SetOrderType(payment.OrderTypeSubscription).Save(ctx)
						require.NoError(t, err)
					}
					auditCount, err := client.PaymentAuditLog.Query().Count(ctx)
					require.NoError(t, err)
					var deductions, rollbacks int
					svc := &PaymentService{
						entClient: client,
						userRepo: &mockUserRepo{
							getByIDUser: &User{ID: order.UserID, Balance: 100},
							deductAvailableBalanceFn: func(context.Context, int64, float64) (float64, error) {
								deductions++
								return 100, nil
							},
							updateBalanceFn: func(context.Context, int64, float64) error {
								rollbacks++
								return nil
							},
						},
					}
					plan := &RefundPlan{
						OrderID: order.ID, Order: order, RefundAmount: 100, GatewayAmount: 100,
						DeductionType: payment.DeductionTypeBalance, BalanceToDeduct: 100,
					}
					var callErr error
					require.NotPanics(t, func() {
						switch entry {
						case "validate":
							_, callErr = svc.validateRefundRequest(ctx, order.ID, order.UserID)
						case "request":
							callErr = svc.RequestRefund(ctx, order.ID, order.UserID, "maintenance")
						case "prepare":
							_, _, callErr = svc.PrepareRefund(ctx, order.ID, 100, "maintenance", false, true)
						case "execute balance":
							_, callErr = svc.ExecuteRefund(ctx, plan)
						case "execute subscription":
							plan.DeductionType = payment.DeductionTypeSubscription
							plan.SubDaysToDeduct, plan.SubscriptionID = 30, 1
							_, callErr = svc.ExecuteRefund(ctx, plan)
						case "gateway":
							_, callErr = svc.gwRefund(ctx, plan)
						case "query":
							_, callErr = svc.QueryAndFinalizeRefund(ctx, order.ID)
						}
					})
					require.ErrorIs(t, callErr, ErrEasyPayMaintenance)
					require.Equal(t, 503, infraerrors.Code(callErr))
					require.Zero(t, deductions)
					require.Zero(t, rollbacks)
					current, err := client.PaymentOrder.Get(ctx, order.ID)
					require.NoError(t, err)
					require.Equal(t, order.Status, current.Status)
					require.Equal(t, order.UpdatedAt, current.UpdatedAt)
					require.Equal(t, order.RefundAt, current.RefundAt)
					currentAuditCount, err := client.PaymentAuditLog.Query().Count(ctx)
					require.NoError(t, err)
					require.Equal(t, auditCount, currentAuditCount)
				})
			}
		})
	}
}

func TestRefundMaintenanceExecuteReloadsCurrentOrder(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "true")
	t.Setenv("EASYPAY_RECONCILIATION_MAX_ORDER_ID", "")
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createEasyPayRefundMaintenanceOrder(t, ctx, client, "")
	stale := *order
	stale.PaymentType = payment.TypeStripe
	stale.ProviderSnapshot = map[string]any{"provider_key": payment.TypeStripe}
	plan := &RefundPlan{OrderID: order.ID, Order: &stale, RefundAmount: 100, GatewayAmount: 100}

	result, err := (&PaymentService{entClient: client}).ExecuteRefund(ctx, plan)
	require.ErrorIs(t, err, ErrEasyPayMaintenance)
	require.Nil(t, result)
	current, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, order.Status, current.Status)
	require.Equal(t, order.UpdatedAt, current.UpdatedAt)
}

func TestRefundMaintenanceRejectsInvalidPlan(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "false")
	t.Setenv("EASYPAY_RECONCILIATION_MAX_ORDER_ID", "")
	for _, entry := range []string{"execute", "gateway"} {
		t.Run(entry, func(t *testing.T) {
			for _, plan := range []*RefundPlan{nil, {}} {
				var err error
				require.NotPanics(t, func() {
					svc := &PaymentService{}
					if entry == "execute" {
						_, err = svc.ExecuteRefund(context.Background(), plan)
					} else {
						_, err = svc.gwRefund(context.Background(), plan)
					}
				})
				require.Equal(t, 400, infraerrors.Code(err))
				require.Equal(t, "INVALID_REQUEST", infraerrors.Reason(err))
			}
		})
	}
}

func TestRefundMaintenancePreservesStripeRefund(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "true")
	t.Setenv("EASYPAY_RECONCILIATION_MAX_ORDER_ID", "365")
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "stripe-maintenance-exemption")
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetStatus(OrderStatusCompleted).SetPaymentTradeNo("").Save(ctx)
	require.NoError(t, err)
	var deducted float64
	svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{
		deductAvailableBalanceFn: func(_ context.Context, id int64, amount float64) (float64, error) {
			require.Equal(t, order.UserID, id)
			deducted += amount
			return amount, nil
		},
	}}
	plan := &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 100,
		DeductionType: payment.DeductionTypeBalance, BalanceToDeduct: 100}
	result, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 100.0, deducted)
	current, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunded, current.Status)
}

func createEasyPayRefundMaintenanceOrder(t *testing.T, ctx context.Context, client *dbent.Client, tradeNo string) *dbent.PaymentOrder {
	t.Helper()
	order := createPendingRefundOrderForTest(t, ctx, client, "refund-maintenance")
	inst, err := client.PaymentProviderInstance.Query().Only(ctx)
	require.NoError(t, err)
	_, err = client.PaymentProviderInstance.UpdateOneID(inst.ID).
		SetProviderKey(payment.TypeEasyPay).SetSupportedTypes(payment.TypeAlipay).
		SetAllowUserRefund(true).Save(ctx)
	require.NoError(t, err)
	order, err = client.PaymentOrder.UpdateOneID(order.ID).
		SetProviderKey(payment.TypeEasyPay).SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo(tradeNo).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)
	return order
}
