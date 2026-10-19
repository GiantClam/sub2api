//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestEasyPayMaintenanceWebhookReturns503(t *testing.T) {
	t.Setenv("EASYPAY_MAINTENANCE_ENABLED", "true")
	gin.SetMode(gin.TestMode)
	h := NewPaymentWebhookHandler(&service.PaymentService{}, payment.NewRegistry())
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			w := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(w)
			ctx.Request = httptest.NewRequest(method, "/api/v1/payment/webhook/easypay", nil)
			h.EasyPayNotify(ctx)
			require.Equal(t, http.StatusServiceUnavailable, w.Code)
			require.NotEqual(t, "success", w.Body.String())
		})
	}
}
