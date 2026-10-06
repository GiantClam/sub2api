package service

import (
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

func balanceLedgerEnabled(cfg *PaymentConfig) bool {
	return cfg != nil && cfg.BalanceUSDToCNYRate > 0
}

func BalanceLedgerCurrency(cfg *PaymentConfig) string {
	if balanceLedgerEnabled(cfg) {
		return "USD"
	}
	return "legacy"
}

func PaymentOrderLedgerCurrency(order *dbent.PaymentOrder) string {
	if order != nil && order.ProviderSnapshot["ledger_currency"] == "USD" {
		return "USD"
	}
	return "legacy"
}

func paymentProviderSelectionCurrency(selection *payment.InstanceSelection) string {
	if selection == nil {
		return payment.DefaultPaymentCurrency
	}
	return paymentProviderConfigCurrency(selection.ProviderKey, selection.Config)
}

// Fixed packages promise one USD credit, independent of native payment rounding.
func balanceLedgerRechargeAmount(cfg *PaymentConfig, amount float64, currency string) decimal.Decimal {
	if balanceLedgerEnabled(cfg) {
		for _, tier := range cfg.RechargeTiers {
			if price, ok := tier.Prices[currency]; ok && tier.CreditedBalance != nil && decimal.NewFromFloat(price).Equal(decimal.NewFromFloat(amount)) {
				return decimal.NewFromFloat(*tier.CreditedBalance)
			}
		}
	}
	return balanceLedgerPaymentAmount(cfg, amount, currency)
}

func validateBalanceLedgerCurrency(cfg *PaymentConfig, currency string) error {
	if balanceLedgerEnabled(cfg) && currency != "USD" && currency != "CNY" {
		return infraerrors.BadRequest("UNSUPPORTED_BALANCE_CURRENCY", "USD balance settlement supports only USD and CNY payments")
	}
	return nil
}

func balanceLedgerPaymentAmount(cfg *PaymentConfig, amount float64, currency string) decimal.Decimal {
	value := decimal.NewFromFloat(amount)
	if balanceLedgerEnabled(cfg) && currency == "CNY" {
		value = value.Div(decimal.NewFromFloat(cfg.BalanceUSDToCNYRate))
	}
	return value
}

func balanceLedgerPrecision(cfg *PaymentConfig) int32 {
	if balanceLedgerEnabled(cfg) {
		return 8
	}
	return 2
}

// Freeze only money metadata, never provider credentials. Fulfillment uses order.Amount.
func addPaymentLedgerSnapshot(snapshot map[string]any, cfg *PaymentConfig, orderType, currency string, original, payBase, credited, bonus float64) map[string]any {
	if orderType != payment.OrderTypeBalance || !balanceLedgerEnabled(cfg) {
		return snapshot
	}
	result := make(map[string]any, len(snapshot)+8)
	for key, value := range snapshot {
		result[key] = value
	}
	result["currency"] = currency
	result["ledger_currency"] = "USD"
	result["ledger_version"] = 2
	result["balance_usd_to_cny_rate"] = cfg.BalanceUSDToCNYRate
	result["original_payment_amount"] = original
	result["payment_base_amount"] = payBase
	result["payment_base_usd"] = balanceLedgerPaymentAmount(cfg, payBase, currency).Round(8).InexactFloat64()
	result["credited_balance"] = credited
	result["bonus_balance"] = bonus
	return result
}

func paymentOrderLedgerAmountTolerance(order *dbent.PaymentOrder) float64 {
	if order != nil && order.ProviderSnapshot["ledger_currency"] == "USD" {
		return 1e-8
	}
	return paymentAmountToleranceForCurrency(PaymentOrderCurrency(order))
}

func calculateGatewayRefundAmountForOrder(order *dbent.PaymentOrder, refundAmount float64) float64 {
	if order.ProviderSnapshot["ledger_currency"] != "USD" {
		return calculateGatewayRefundAmount(order.Amount, order.PayAmount, refundAmount, PaymentOrderCurrency(order))
	}
	if order.Amount <= 0 || order.PayAmount <= 0 || refundAmount <= 0 {
		return 0
	}
	// Wallet amounts use eight decimals; native currency tolerance must not turn
	// a nearly-full USD refund into a full CNY payment refund.
	return decimal.NewFromFloat(order.PayAmount).Mul(decimal.NewFromFloat(refundAmount)).
		Div(decimal.NewFromFloat(order.Amount)).Round(int32(payment.CurrencyMaxFractionDigits(PaymentOrderCurrency(order)))).InexactFloat64()
}

// Daily limits are USD when FX is enabled. Legacy orders use their original payment
// currency and the configured rate for limit accounting only; their credits never change.
func paymentOrderDailyLimitAmountUSD(order *dbent.PaymentOrder, rate float64) float64 {
	if order.OrderType != payment.OrderTypeBalance {
		return order.Amount
	}
	if value, ok := order.ProviderSnapshot["payment_base_usd"].(float64); ok {
		return value
	}
	currency := strings.ToUpper(PaymentOrderCurrency(order))
	if currency == "CNY" {
		return decimal.NewFromFloat(order.PayAmount).Div(decimal.NewFromFloat(rate)).Round(8).InexactFloat64()
	}
	return order.PayAmount
}
