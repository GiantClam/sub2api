package routes

import (
	"crypto/subtle"
	"errors"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	cheapBuddyInternalPath       = "/api/internal/cheapbuddy/identity"
	cheapBuddyBillingReservePath = "/api/internal/cheapbuddy/billing/reserve"
	cheapBuddyBillingCapturePath = "/api/internal/cheapbuddy/billing/capture"
	cheapBuddyBillingReleasePath = "/api/internal/cheapbuddy/billing/release"
	cheapBuddyMaxBodyBytes       = 4 << 10
	cheapBuddyDefaultQuotaPerUSD = 500000.0
)

// RegisterCheapBuddyInternalRoutes exposes the private identity and billing
// bridge used by CheapBuddy Relay. The billing repository supplies the same
// transactional, idempotent balance hold semantics used by native Sub2API
// batch-image billing.
func RegisterCheapBuddyInternalRoutes(r gin.IRoutes, apiKeyService *service.APIKeyService, billingRepository service.UsageBillingRepository) {
	serviceToken := strings.TrimSpace(os.Getenv("SUB2API_RELAY_SERVICE_TOKEN"))
	if serviceToken == "" || apiKeyService == nil || billingRepository == nil {
		return
	}
	quotaPerUSD := configuredCheapBuddyQuotaPerUSD()

	r.POST(cheapBuddyInternalPath, func(c *gin.Context) {
		if !constantTimeBearerMatch(c.GetHeader("Authorization"), serviceToken) {
			c.Status(http.StatusUnauthorized)
			return
		}

		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, cheapBuddyMaxBodyBytes)
		var request struct {
			APIKey string `json:"api_key"`
		}
		if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.APIKey) == "" {
			c.Status(http.StatusBadRequest)
			return
		}

		apiKey, user, err := apiKeyService.ValidateKey(c.Request.Context(), strings.TrimSpace(request.APIKey))
		if err != nil || apiKey == nil || user == nil || apiKey.ID <= 0 || user.ID <= 0 || apiKey.UserID != user.ID {
			// Do not disclose whether a credential exists to the caller; Relay
			// maps this response to its normal API authentication failure.
			c.Status(http.StatusUnauthorized)
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"user_id":    user.ID,
			"api_key_id": apiKey.ID,
		})
	})

	registerCheapBuddyBillingRoute(r, cheapBuddyBillingReservePath, serviceToken, billingRepository, quotaPerUSD, cheapBuddyBillingOperationReserve)
	registerCheapBuddyBillingRoute(r, cheapBuddyBillingCapturePath, serviceToken, billingRepository, quotaPerUSD, cheapBuddyBillingOperationCapture)
	registerCheapBuddyBillingRoute(r, cheapBuddyBillingReleasePath, serviceToken, billingRepository, quotaPerUSD, cheapBuddyBillingOperationRelease)
}

type cheapBuddyBillingOperation string

const (
	cheapBuddyBillingOperationReserve cheapBuddyBillingOperation = "reserve"
	cheapBuddyBillingOperationCapture cheapBuddyBillingOperation = "capture"
	cheapBuddyBillingOperationRelease cheapBuddyBillingOperation = "release"
)

type cheapBuddyBillingRequest struct {
	RequestID    string `json:"request_id"`
	APIKeyID     int64  `json:"api_key_id"`
	UserID       int64  `json:"user_id"`
	Amount       int64  `json:"amount"`
	HeldAmount   int64  `json:"held_amount"`
	ActualAmount int64  `json:"actual_amount"`
	PayloadHash  string `json:"payload_hash"`
}

func registerCheapBuddyBillingRoute(
	r gin.IRoutes,
	path string,
	serviceToken string,
	billingRepository service.UsageBillingRepository,
	quotaPerUSD float64,
	operation cheapBuddyBillingOperation,
) {
	r.POST(path, func(c *gin.Context) {
		if !constantTimeBearerMatch(c.GetHeader("Authorization"), serviceToken) {
			c.Status(http.StatusUnauthorized)
			return
		}

		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, cheapBuddyMaxBodyBytes)
		var request cheapBuddyBillingRequest
		if err := c.ShouldBindJSON(&request); err != nil || !request.valid(operation) {
			c.Status(http.StatusBadRequest)
			return
		}

		batchID := strings.TrimSpace(request.RequestID)
		holdAmount := request.Amount
		actualAmount := int64(0)
		switch operation {
		case cheapBuddyBillingOperationCapture:
			holdAmount = request.HeldAmount
			actualAmount = request.ActualAmount
		}

		command := &service.BatchImageBalanceHoldCommand{
			RequestID:          cheapBuddyBillingRequestID(operation, batchID),
			APIKeyID:           request.APIKeyID,
			UserID:             request.UserID,
			BatchID:            batchID,
			HoldAmount:         quotaToUSD(float64(holdAmount), quotaPerUSD),
			ActualAmount:       quotaToUSD(float64(actualAmount), quotaPerUSD),
			RequestPayloadHash: strings.TrimSpace(request.PayloadHash),
		}

		var err error
		switch operation {
		case cheapBuddyBillingOperationReserve:
			_, err = billingRepository.ReserveBatchImageBalance(c.Request.Context(), command)
		case cheapBuddyBillingOperationCapture:
			_, err = billingRepository.CaptureBatchImageBalance(c.Request.Context(), command)
		case cheapBuddyBillingOperationRelease:
			_, err = billingRepository.ReleaseBatchImageBalance(c.Request.Context(), command)
		}
		if err != nil {
			writeCheapBuddyBillingError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"billing_request_id": command.RequestID})
	})
}

func (r cheapBuddyBillingRequest) valid(operation cheapBuddyBillingOperation) bool {
	if strings.TrimSpace(r.RequestID) == "" || r.APIKeyID <= 0 || r.UserID <= 0 {
		return false
	}
	switch operation {
	case cheapBuddyBillingOperationReserve, cheapBuddyBillingOperationRelease:
		return r.Amount > 0
	case cheapBuddyBillingOperationCapture:
		return r.HeldAmount > 0 && r.ActualAmount >= 0 && r.ActualAmount <= r.HeldAmount
	default:
		return false
	}
}

func cheapBuddyBillingRequestID(operation cheapBuddyBillingOperation, batchID string) string {
	switch operation {
	case cheapBuddyBillingOperationReserve:
		return service.BatchImageHoldRequestID(batchID)
	case cheapBuddyBillingOperationCapture:
		return service.BatchImageCaptureRequestID(batchID)
	case cheapBuddyBillingOperationRelease:
		return service.BatchImageReleaseRequestID(batchID)
	default:
		return ""
	}
}

func configuredCheapBuddyQuotaPerUSD() float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("SUB2API_RELAY_QUOTA_PER_USD")), 64)
	if err == nil && value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
		return value
	}
	return cheapBuddyDefaultQuotaPerUSD
}

func quotaToUSD(quota, quotaPerUSD float64) float64 {
	if quota <= 0 {
		return 0
	}
	return quota / quotaPerUSD
}

func writeCheapBuddyBillingError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, service.ErrBatchImageInsufficientBalance):
		status = http.StatusPaymentRequired
	case errors.Is(err, service.ErrUsageBillingRequestConflict), errors.Is(err, service.ErrBatchImageSettlementCostExceedsHold):
		status = http.StatusConflict
	case errors.Is(err, service.ErrUserNotFound), errors.Is(err, service.ErrAPIKeyNotFound):
		status = http.StatusUnauthorized
	}
	c.Status(status)
}

func constantTimeBearerMatch(authorization, expectedToken string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		return false
	}
	providedToken := strings.TrimSpace(strings.TrimPrefix(authorization, prefix))
	if len(providedToken) != len(expectedToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(providedToken), []byte(expectedToken)) == 1
}
