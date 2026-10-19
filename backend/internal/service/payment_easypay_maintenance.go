package service

import (
	"context"
	"os"
	"strconv"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var ErrEasyPayMaintenance = infraerrors.ServiceUnavailable("EASYPAY_MAINTENANCE", "EasyPay payments are temporarily unavailable")

// CheckEasyPayMaintenance is independent of instance Enabled: historical orders
// can still resolve disabled provider instances through their persisted snapshots.
func CheckEasyPayMaintenance(providerKey string) error {
	enabled, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("EASYPAY_MAINTENANCE_ENABLED")))
	if enabled && strings.EqualFold(strings.TrimSpace(providerKey), payment.TypeEasyPay) {
		return ErrEasyPayMaintenance
	}
	return nil
}

func (s *PaymentService) checkOrderEasyPayMaintenance(ctx context.Context, order *dbent.PaymentOrder) error {
	if CheckEasyPayMaintenance(payment.TypeEasyPay) == nil || order == nil {
		return nil
	}
	if snapshot := psOrderProviderSnapshot(order); snapshot != nil && snapshot.ProviderKey != "" {
		return CheckEasyPayMaintenance(snapshot.ProviderKey)
	}
	if key := strings.TrimSpace(psStringValue(order.ProviderKey)); key != "" {
		return CheckEasyPayMaintenance(key)
	}
	inst, err := s.getOrderProviderInstance(ctx, order)
	if err != nil {
		return err
	}
	if inst != nil {
		return CheckEasyPayMaintenance(inst.ProviderKey)
	}
	if err := CheckEasyPayMaintenance(paymentOrderFallbackProviderKey(s.registry, order)); err != nil {
		return err
	}
	// Without a historical binding, alipay/wxpay and custom methods cannot
	// exclude EasyPay. Registry fallback is not evidence of their original
	// provider, especially before lazy initialization. Keep these orders frozen.
	if payment.GetBasePaymentType(strings.TrimSpace(order.PaymentType)) != payment.TypeStripe {
		return ErrEasyPayMaintenance
	}
	return nil
}
