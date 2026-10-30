//go:build unit

package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type checkoutSettingRepo struct {
	service.SettingRepository
	values map[string]string
}

func (repo *checkoutSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	return repo.values[key], nil
}

func (repo *checkoutSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		values[key] = repo.values[key]
	}
	return values, nil
}

func checkoutHandlerFixture(t *testing.T, tiers string, overrides ...map[string]string) (*PaymentHandler, *sql.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := sql.Open("sqlite", "file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).SetName("Alipay").SetConfig(`{}`).
		SetSupportedTypes(payment.TypeAlipay).SetEnabled(true).Save(context.Background())
	require.NoError(t, err)
	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeStripe).SetName("Stripe").SetConfig(`{"currency":"USD"}`).
		SetSupportedTypes("card").SetEnabled(true).Save(context.Background())
	require.NoError(t, err)
	repo := &checkoutSettingRepo{values: map[string]string{
		service.SettingPaymentEnabled: "true", service.SettingMinRechargeAmount: "0",
		service.SettingPaymentRechargeTiers: tiers, service.SettingRechargeFeeRate: "1",
	}}
	for _, values := range overrides {
		for key, value := range values {
			repo.values[key] = value
		}
	}
	return NewPaymentHandler(nil, service.NewPaymentConfigService(client, repo, nil)), db
}

func callCheckoutHandler(handler *PaymentHandler) *httptest.ResponseRecorder {
	writer := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(writer)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/payment/checkout-info", nil)
	handler.GetCheckoutInfo(ctx)
	return writer
}

func TestCheckoutInfoReturnsRechargeCatalogSeparatelyFromSubscriptions(t *testing.T) {
	handler, _ := checkoutHandlerFixture(t, `[{"id":"trial","name":"体验包","description":"余额充值","order_type":"balance","prices":{"CNY":3,"USD":0.46}}]`)
	writer := callCheckoutHandler(handler)
	require.Equal(t, http.StatusOK, writer.Code, writer.Body.String())
	var body struct {
		Data checkoutInfoResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(writer.Body.Bytes(), &body))
	require.NotNil(t, body.Data.Plans)
	require.Empty(t, body.Data.Plans)
	require.Len(t, body.Data.RechargeTiers, 1)
	require.Equal(t, payment.OrderTypeBalance, body.Data.RechargeTiers[0].OrderType)
	require.Equal(t, []service.RechargePaymentOption{
		{PaymentType: "alipay", Currency: "CNY", Amount: 3, PayAmount: 3.03, CreditedBalance: 3},
		{PaymentType: "stripe", Currency: "USD", Amount: 0.46, PayAmount: 0.47, CreditedBalance: 0.46},
	}, body.Data.RechargeTiers[0].PaymentOptions)
}

func TestCheckoutInfoMissingRechargeCatalogReturnsArray(t *testing.T) {
	handler, _ := checkoutHandlerFixture(t, "")
	writer := callCheckoutHandler(handler)
	require.Equal(t, http.StatusOK, writer.Code)
	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(writer.Body.Bytes(), &body))
	require.JSONEq(t, "[]", string(body.Data["recharge_tiers"]))
	require.JSONEq(t, "[]", string(body.Data["recharge_fixed_credits"]))
}

func TestCheckoutInfoExposesFixedCreditWithoutGatewayOption(t *testing.T) {
	handler, _ := checkoutHandlerFixture(t, `[{"id":"standard","name":"Standard","prices":{"USD":2.31},"credited_balance":2.30769231}]`, map[string]string{
		service.SettingBalanceUSDToCNYRate: "6.5",
		service.SettingMinRechargeAmount:   "10",
	})
	writer := callCheckoutHandler(handler)
	require.Equal(t, http.StatusOK, writer.Code, writer.Body.String())
	var body struct {
		Data checkoutInfoResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(writer.Body.Bytes(), &body))
	require.Empty(t, body.Data.RechargeTiers)
	require.Equal(t, []service.RechargeFixedCredit{{Currency: "USD", Amount: 2.31, CreditedBalanceBaseUSD: 2.30769231}}, body.Data.RechargeFixedCredits)
}

func TestCheckoutInfoPropagatesSubscriptionCatalogReadErrors(t *testing.T) {
	handler, db := checkoutHandlerFixture(t, "")
	_, err := db.Exec("DROP TABLE subscription_plans")
	require.NoError(t, err)
	writer := callCheckoutHandler(handler)
	require.Equal(t, http.StatusInternalServerError, writer.Code)
	require.NotContains(t, writer.Body.String(), `"plans":[]`)
}
