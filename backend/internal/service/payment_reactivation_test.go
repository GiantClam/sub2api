package service

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestCalculatePaymentReactivationBonusRoundsAndCaps(t *testing.T) {
	tests := []struct {
		name     string
		credited float64
		want     float64
	}{
		{name: "ten percent of credited balance", credited: 12.34, want: 1.23},
		{name: "rounds half cents upward", credited: 40.05, want: 4.01},
		{name: "caps at five USD", credited: 75, want: 5},
		{name: "zero balance", credited: 0, want: 0},
		{name: "negative balance", credited: -10, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, calculatePaymentReactivationBonus(tt.credited))
		})
	}
}

func TestPaymentReactivationEmailEventsAreOptionalAndPreviewable(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)
	infos := make(map[string]NotificationEmailEventInfo)
	for _, info := range svc.ListEventInfos() {
		infos[info.Event] = info
	}

	for _, event := range []string{
		NotificationEmailEventPaymentReactivation,
		NotificationEmailEventPaymentReactivationFollowup,
	} {
		info, ok := infos[event]
		require.True(t, ok, "event %s should be registered", event)
		require.True(t, info.Optional, "promotional email should honor user preferences")
		require.Contains(t, info.Placeholders, "recharge_url")
		require.Contains(t, info.Placeholders, "reward_cap")

		preview, err := svc.PreviewTemplate(ctx, NotificationEmailPreviewInput{Event: event, Locale: "zh"})
		require.NoError(t, err)
		require.NotEmpty(t, preview.Subject)
		require.NotEmpty(t, preview.HTML)
	}
}

func TestPaymentReactivationPurchaseURLPrefersFrontendURL(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	require.NoError(t, repo.Set(ctx, SettingKeyAPIBaseURL, "https://api.example.com"))
	require.NoError(t, repo.Set(ctx, SettingKeyFrontendURL, "https://app.example.com/"))

	svc := NewNotificationEmailService(repo, nil)
	require.Equal(t, "https://app.example.com/purchase", paymentReactivationPurchaseURL(ctx, svc))
}

func TestPaymentReactivationEligiblePendingOrderAcceptsStripeIntent(t *testing.T) {
	order := &dbent.PaymentOrder{
		OrderType:      payment.OrderTypeBalance,
		Status:         OrderStatusPending,
		PaymentTradeNo: "pi_test_intent",
	}
	require.True(t, paymentReactivationEligiblePendingOrder(order))
}

func TestPaymentReactivationEligiblePendingOrderRequiresCheckoutArtifact(t *testing.T) {
	order := &dbent.PaymentOrder{
		OrderType: payment.OrderTypeBalance,
		Status:    OrderStatusPending,
	}
	require.False(t, paymentReactivationEligiblePendingOrder(order))

	order.Status = OrderStatusCompleted
	order.PaymentTradeNo = "pi_test_intent"
	require.False(t, paymentReactivationEligiblePendingOrder(order))
}
