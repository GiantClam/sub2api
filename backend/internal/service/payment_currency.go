package service

import (
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func paymentProviderConfigCurrency(providerKey string, cfg map[string]string) string {
	switch strings.TrimSpace(providerKey) {
	case payment.TypeStripe, payment.TypeAirwallex:
		currency, err := payment.NormalizePaymentCurrency(cfg["currency"])
		if err == nil {
			return currency
		}
	}
	return payment.DefaultPaymentCurrency
}

func PaymentOrderCurrency(order *dbent.PaymentOrder) string {
	if snapshot := psOrderProviderSnapshot(order); snapshot != nil && strings.TrimSpace(snapshot.Currency) != "" {
		if currency, err := payment.NormalizePaymentCurrency(snapshot.Currency); err == nil {
			return currency
		}
	}
	// Historical money metadata must not fabricate a provider instance snapshot.
	if order != nil {
		if raw, ok := order.ProviderSnapshot["original_payment_currency"].(string); ok {
			if currency, err := payment.NormalizePaymentCurrency(raw); err == nil {
				return currency
			}
		}
	}
	return payment.DefaultPaymentCurrency
}
