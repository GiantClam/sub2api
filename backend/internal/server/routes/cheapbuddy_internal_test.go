package routes

import "testing"

func TestConstantTimeBearerMatch(t *testing.T) {
	t.Parallel()

	const token = "test-token-value"
	for _, testCase := range []struct {
		name          string
		authorization string
		want          bool
	}{
		{name: "valid", authorization: "Bearer " + token, want: true},
		{name: "missing prefix", authorization: token, want: false},
		{name: "wrong token", authorization: "Bearer test-token-valuX", want: false},
		{name: "wrong length", authorization: "Bearer short", want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := constantTimeBearerMatch(testCase.authorization, token); got != testCase.want {
				t.Fatalf("constantTimeBearerMatch() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestCheapBuddyBillingHelpers(t *testing.T) {
	t.Parallel()

	if got := quotaToUSD(1000000, cheapBuddyDefaultQuotaPerUSD); got != 2 {
		t.Fatalf("quotaToUSD() = %v, want 2", got)
	}
	if got := cheapBuddyBillingRequestID(cheapBuddyBillingOperationCapture, "req-1"); got != "batch_image_capture:req-1" {
		t.Fatalf("capture request id = %q", got)
	}
	if !(&cheapBuddyBillingRequest{RequestID: "req-1", APIKeyID: 1, UserID: 2, Amount: 100}).valid(cheapBuddyBillingOperationReserve) {
		t.Fatal("reserve request should be valid")
	}
	if (&cheapBuddyBillingRequest{RequestID: "req-1", APIKeyID: 1, UserID: 2, HeldAmount: 100, ActualAmount: 101}).valid(cheapBuddyBillingOperationCapture) {
		t.Fatal("capture request exceeding hold should be invalid")
	}
}
