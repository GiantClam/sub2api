package service

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
	"golang.org/x/text/currency"
)

const SettingPaymentRechargeTiers = "PAYMENT_RECHARGE_TIERS"

// RechargeTier is a display preset for the existing balance-order flow.
// Prices are explicit order amounts in each payment currency, never FX estimates.
type RechargeTier struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	OrderType       string          `json:"order_type"`
	Prices          CurrencyAmounts `json:"prices"`
	Featured        bool            `json:"featured,omitempty"`
	CreditedBalance *float64        `json:"credited_balance,omitempty"`
}

type RechargePaymentOption struct {
	PaymentType     string  `json:"payment_type"`
	Currency        string  `json:"currency"`
	Amount          float64 `json:"amount"`
	PayAmount       float64 `json:"pay_amount"`
	CreditedBalance float64 `json:"credited_balance"`
}

type CheckoutRechargeTier struct {
	ID             string                  `json:"id"`
	Name           string                  `json:"name"`
	Description    string                  `json:"description"`
	OrderType      string                  `json:"order_type"`
	Featured       bool                    `json:"featured"`
	PaymentOptions []RechargePaymentOption `json:"payment_options"`
}

func parseRechargeTiers(raw string) ([]RechargeTier, error) {
	if strings.TrimSpace(raw) == "" {
		return []RechargeTier{}, nil
	}
	var tiers []RechargeTier
	if err := json.Unmarshal([]byte(raw), &tiers); err != nil {
		return nil, infraerrors.InternalServer("PAYMENT_RECHARGE_TIERS_INVALID", "recharge tier configuration is invalid").WithCause(err)
	}
	normalized, err := normalizeRechargeTiers(tiers)
	if err != nil {
		return nil, infraerrors.InternalServer("PAYMENT_RECHARGE_TIERS_INVALID", "recharge tier configuration is invalid").WithCause(err)
	}
	return normalized, nil
}

func normalizeRechargeTiers(tiers []RechargeTier) ([]RechargeTier, error) {
	if len(tiers) > 100 {
		return nil, invalidRechargeTier("at most 100 recharge tiers are allowed")
	}
	result := make([]RechargeTier, 0, len(tiers))
	seen := make(map[string]bool, len(tiers))
	creditsByPrice := make(map[string]string)
	for _, tier := range tiers {
		tier.ID = strings.TrimSpace(tier.ID)
		tier.Name = strings.TrimSpace(tier.Name)
		tier.Description = strings.TrimSpace(tier.Description)
		if !validRechargeTierID(tier.ID) || seen[tier.ID] {
			return nil, invalidRechargeTier("recharge tier IDs must be unique, 1-64 lowercase letters, digits, hyphens or underscores")
		}
		seen[tier.ID] = true
		if !utf8.ValidString(tier.Name) || tier.Name == "" || utf8.RuneCountInString(tier.Name) > 120 {
			return nil, invalidRechargeTier("recharge tier names must contain 1-120 characters")
		}
		if !utf8.ValidString(tier.Description) || utf8.RuneCountInString(tier.Description) > 2000 {
			return nil, invalidRechargeTier("recharge tier descriptions must contain at most 2000 characters")
		}
		if tier.OrderType != "" && tier.OrderType != payment.OrderTypeBalance {
			return nil, invalidRechargeTier("recharge tier order_type must be balance")
		}
		tier.OrderType = payment.OrderTypeBalance
		prices, err := normalizeRechargeTierPrices(tier.Prices)
		if err != nil {
			return nil, err
		}
		tier.Prices = prices
		if tier.CreditedBalance != nil {
			credit := *tier.CreditedBalance
			if math.IsNaN(credit) || math.IsInf(credit, 0) || credit <= 0 || !decimal.NewFromFloat(credit).Equal(decimal.NewFromFloat(credit).Round(8)) {
				return nil, invalidRechargeTier("USD credited_balance must be positive, finite and have at most eight decimals")
			}
		}
		promise := "automatic"
		if tier.CreditedBalance != nil {
			promise = decimal.NewFromFloat(*tier.CreditedBalance).String()
		}
		for code, amount := range prices {
			key := code + ":" + decimal.NewFromFloat(amount).String()
			if existing, ok := creditsByPrice[key]; ok && existing != promise {
				return nil, invalidRechargeTier("the same currency price cannot promise different USD balances")
			}
			creditsByPrice[key] = promise
		}
		result = append(result, tier)
	}
	return result, nil
}

func validRechargeTierID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}

func normalizeRechargeTierPrices(prices CurrencyAmounts) (CurrencyAmounts, error) {
	if len(prices) == 0 {
		return nil, invalidRechargeTier("recharge tiers require at least one currency price")
	}
	result := make(CurrencyAmounts, len(prices))
	for raw, amount := range prices {
		code := strings.ToUpper(strings.TrimSpace(raw))
		if _, err := currency.ParseISO(code); err != nil || code == "XXX" || code == "XTS" {
			return nil, invalidRechargeTier("recharge tier prices require recognized ISO currency codes")
		}
		if _, exists := result[code]; exists {
			return nil, invalidRechargeTier("recharge tier currencies must be unique")
		}
		if err := validateRechargeTierAmount(amount, code); err != nil {
			return nil, err
		}
		result[code] = amount
	}
	return result, nil
}

func validateRechargeTierAmount(amount float64, code string) error {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return invalidRechargeTier("recharge tier prices must be positive finite amounts")
	}
	if err := validateCreateOrderAmountCurrency(amount, code); err != nil {
		return invalidRechargeTier(fmt.Sprintf("recharge tier prices exceed the allowed precision for %s", code))
	}
	minorAmount := decimal.NewFromFloat(amount).Shift(int32(payment.CurrencyMinorUnit(code)))
	if minorAmount.GreaterThan(decimal.NewFromInt(1<<63 - 1)) {
		return invalidRechargeTier("recharge tier prices exceed the supported payment amount")
	}
	return nil
}

func invalidRechargeTier(message string) error {
	return infraerrors.BadRequest("INVALID_RECHARGE_TIERS", message)
}

// BuildCheckoutRechargeTiers quotes only configured currencies and visible methods.
// Order creation and settlement retain their existing authority and validations.
func BuildCheckoutRechargeTiers(cfg *PaymentConfig, methods map[string]MethodLimits) []CheckoutRechargeTier {
	result := []CheckoutRechargeTier{}
	if cfg == nil || !cfg.Enabled || cfg.BalanceDisabled {
		return result
	}
	methodNames := make([]string, 0, len(methods))
	for method := range methods {
		methodNames = append(methodNames, method)
	}
	sort.Strings(methodNames)
	for _, tier := range cfg.RechargeTiers {
		options := make([]RechargePaymentOption, 0, len(methodNames))
		for _, method := range methodNames {
			limits := methods[method]
			code := strings.ToUpper(strings.TrimSpace(limits.Currency))
			amount, ok := tier.Prices[code]
			if validateBalanceLedgerCurrency(cfg, code) != nil {
				continue
			}
			if !ok || validateRechargeTierAmount(amount, code) != nil || !withinRechargeRange(amount, cfg.MinAmount, cfg.MaxAmount) {
				continue
			}
			quote := quoteRechargeBonus(cfg, amount, code)
			_, payAmount, err := calculateCreateOrderPayAmountForOrderType(quote.PayBase, cfg.RechargeFeeRate, code, payment.OrderTypeBalance, cfg.SubscriptionUSDToCNYRate)
			if err != nil || validateRechargeTierAmount(payAmount, code) != nil || !withinRechargeRange(payAmount, limits.SingleMin, limits.SingleMax) {
				continue
			}
			options = append(options, RechargePaymentOption{
				PaymentType: method, Currency: code, Amount: amount, PayAmount: payAmount,
				CreditedBalance: quote.Credited,
			})
		}
		if len(options) > 0 {
			result = append(result, CheckoutRechargeTier{
				ID: tier.ID, Name: tier.Name, Description: tier.Description,
				OrderType: payment.OrderTypeBalance, Featured: tier.Featured, PaymentOptions: options,
			})
		}
	}
	return result
}

func withinRechargeRange(amount, minimum, maximum float64) bool {
	return (minimum <= 0 || amount >= minimum) && (maximum <= 0 || amount <= maximum)
}

func marshalRechargeTiers(tiers []RechargeTier) (string, error) {
	normalized, err := normalizeRechargeTiers(tiers)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("serialize recharge tiers: %w", err)
	}
	return string(raw), nil
}
