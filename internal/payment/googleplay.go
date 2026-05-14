package payment

import (
	"context"
	"fmt"
	"time"

	"be-miawai/internal/config"
	// Requires integration with google.golang.org/api/androidpublisher/v3 in the future
)

// GooglePlayService handles Android subscription verification.
// For now, it uses a mock implementation pending Play Console setup.
type GooglePlayService struct {
	Config config.Config
}

type VerifyPurchaseResponse struct {
	IsValid          bool
	Status           string
	CurrentPeriodEnd *time.Time
}

func (s *GooglePlayService) VerifyPurchaseToken(ctx context.Context, productID string, purchaseToken string) (*VerifyPurchaseResponse, error) {
	// For Sandbox/Dev testing without Play Console, we will accept any token starting with "mock_"
	if len(purchaseToken) > 5 && purchaseToken[:5] == "mock_" {
		expiry := time.Now().AddDate(0, 1, 0)
		if productID == "miaw_pro_yearly_idr_590000" {
			expiry = time.Now().AddDate(1, 0, 0)
		}
		return &VerifyPurchaseResponse{
			IsValid:          true,
			Status:           "active",
			CurrentPeriodEnd: &expiry,
		}, nil
	}

	// TODO: Integrate google.golang.org/api/androidpublisher/v3
	return nil, fmt.Errorf("Google Play Console is not fully configured yet. Use a mock token starting with 'mock_' for testing.")
}
