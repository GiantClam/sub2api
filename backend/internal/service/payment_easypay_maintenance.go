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
	if !strings.EqualFold(strings.TrimSpace(providerKey), payment.TypeEasyPay) {
		return nil
	}
	enabled, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("EASYPAY_MAINTENANCE_ENABLED")))
	_, validCutoff := easyPayReconciliationCutoff()
	if enabled || !validCutoff {
		return ErrEasyPayMaintenance
	}
	return nil
}

func easyPayReconciliationCutoff() (int64, bool) {
	raw := strings.TrimSpace(os.Getenv("EASYPAY_RECONCILIATION_MAX_ORDER_ID"))
	if raw == "" {
		return 0, true
	}
	maxID, err := strconv.ParseInt(raw, 10, 64)
	return maxID, err == nil && maxID > 0
}

// Keep pre-recovery orders frozen independently of the emergency global pause.
// IDs are assigned by the database; new orders cannot reuse a quarantined ID.
func checkEasyPayOrderMaintenance(providerKey string, orderID int64) error {
	if err := CheckEasyPayMaintenance(providerKey); err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(providerKey), payment.TypeEasyPay) {
		return nil
	}
	maxID, _ := easyPayReconciliationCutoff()
	if maxID > 0 && orderID <= maxID {
		return ErrEasyPayMaintenance
	}
	return nil
}

func (s *PaymentService) checkOrderEasyPayMaintenance(ctx context.Context, order *dbent.PaymentOrder) error {
	if order == nil || checkEasyPayOrderMaintenance(payment.TypeEasyPay, order.ID) == nil {
		return nil
	}
	if snapshot := psOrderProviderSnapshot(order); snapshot != nil && snapshot.ProviderKey != "" {
		return checkEasyPayOrderMaintenance(snapshot.ProviderKey, order.ID)
	}
	if key := strings.TrimSpace(psStringValue(order.ProviderKey)); key != "" {
		return checkEasyPayOrderMaintenance(key, order.ID)
	}
	inst, err := s.getOrderProviderInstance(ctx, order)
	if err != nil {
		return err
	}
	if inst != nil {
		return checkEasyPayOrderMaintenance(inst.ProviderKey, order.ID)
	}
	if err := checkEasyPayOrderMaintenance(paymentOrderFallbackProviderKey(s.registry, order), order.ID); err != nil {
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
