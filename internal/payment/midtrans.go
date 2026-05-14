package payment

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"be-miawai/internal/config"
)

type MidtransService struct {
	Config config.Config
}

type CreateSnapTransactionRequest struct {
	TransactionDetails struct {
		OrderID  string `json:"order_id"`
		GrossAmt int    `json:"gross_amount"`
	} `json:"transaction_details"`
	CustomerDetails struct {
		FirstName string `json:"first_name"`
		Email     string `json:"email"`
	} `json:"customer_details"`
	ItemDetails []struct {
		ID       string `json:"id"`
		Price    int    `json:"price"`
		Quantity int    `json:"quantity"`
		Name     string `json:"name"`
	} `json:"item_details"`
	Callbacks *struct {
		Finish   string `json:"finish,omitempty"`
		Unfinish string `json:"unfinish,omitempty"`
		Error    string `json:"error,omitempty"`
	} `json:"callbacks,omitempty"`
}

type CreateSnapTransactionResponse struct {
	Token       string `json:"token"`
	RedirectURL string `json:"redirect_url"`
}

type TransactionStatusResponse struct {
	OrderID           string `json:"order_id"`
	TransactionStatus string `json:"transaction_status"`
	FraudStatus       string `json:"fraud_status"`
	StatusCode        string `json:"status_code"`
	GrossAmount       string `json:"gross_amount"`
	SignatureKey      string `json:"signature_key"`
}

func (s *MidtransService) CreateSnapTransaction(orderID string, productID string, amount int, userEmail string, userName string, returnURL string) (*CreateSnapTransactionResponse, error) {
	if strings.TrimSpace(s.Config.MidtransServerKey) == "" {
		return nil, errors.New("MIDTRANS_SERVER_KEY is not configured")
	}

	reqData := CreateSnapTransactionRequest{}
	reqData.TransactionDetails.OrderID = orderID
	reqData.TransactionDetails.GrossAmt = amount
	reqData.CustomerDetails.Email = userEmail
	reqData.CustomerDetails.FirstName = userName

	productName := "Miaw Pro"
	if productID == "miaw_pro_monthly_idr_59000" {
		productName = "Miaw Pro Monthly"
	} else if productID == "miaw_pro_yearly_idr_590000" {
		productName = "Miaw Pro Yearly"
	} else if productID == "miaw_credit_usd_5_idr_25000" {
		productName = "Miaw Credit $5"
	} else if productID == "miaw_credit_usd_10_idr_50000" {
		productName = "Miaw Credit $10"
	} else if productID == "miaw_credit_usd_20_idr_100000" {
		productName = "Miaw Credit $20"
	}

	reqData.ItemDetails = []struct {
		ID       string `json:"id"`
		Price    int    `json:"price"`
		Quantity int    `json:"quantity"`
		Name     string `json:"name"`
	}{
		{
			ID:       productID,
			Price:    amount,
			Quantity: 1,
			Name:     productName,
		},
	}
	if trimmed := strings.TrimSpace(returnURL); trimmed != "" {
		reqData.Callbacks = &struct {
			Finish   string `json:"finish,omitempty"`
			Unfinish string `json:"unfinish,omitempty"`
			Error    string `json:"error,omitempty"`
		}{
			Finish:   trimmed,
			Unfinish: trimmed,
			Error:    trimmed,
		}
	}

	bodyBytes, err := json.Marshal(reqData)
	if err != nil {
		return nil, err
	}

	url := "https://app.sandbox.midtrans.com/snap/v1/transactions"
	if s.Config.MidtransIsProduction {
		url = "https://app.midtrans.com/snap/v1/transactions"
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(s.Config.MidtransServerKey, "")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("midtrans error: %s", string(respBody))
	}

	var snapResp CreateSnapTransactionResponse
	if err := json.Unmarshal(respBody, &snapResp); err != nil {
		return nil, err
	}

	return &snapResp, nil
}

func (s *MidtransService) GetTransactionStatus(orderID string) (*TransactionStatusResponse, error) {
	if strings.TrimSpace(s.Config.MidtransServerKey) == "" {
		return nil, errors.New("MIDTRANS_SERVER_KEY is not configured")
	}

	baseURL := "https://api.sandbox.midtrans.com/v2"
	if s.Config.MidtransIsProduction {
		baseURL = "https://api.midtrans.com/v2"
	}

	req, err := http.NewRequest("GET", baseURL+"/"+orderID+"/status", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(s.Config.MidtransServerKey, "")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("midtrans status error: %s", string(respBody))
	}

	var status TransactionStatusResponse
	if err := json.Unmarshal(respBody, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (s *MidtransService) VerifySignatureKey(orderID string, statusCode string, grossAmount string, signatureKey string) bool {
	// sha512(order_id + status_code + gross_amount + server_key)
	data := orderID + statusCode + grossAmount + s.Config.MidtransServerKey
	hash := sha512.Sum512([]byte(data))
	calculatedSignature := hex.EncodeToString(hash[:])
	return calculatedSignature == signatureKey
}
