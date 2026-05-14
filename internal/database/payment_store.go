package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"be-miawai/internal/models"
)

func (s *Store) CreatePaymentTransaction(ctx context.Context, txn models.PaymentTransaction) error {
	raw, err := json.Marshal(txn.RawPayload)
	if err != nil {
		raw = []byte("{}")
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO payment_transactions (
		   id, user_id, order_id, platform, product_id, amount, currency, status, raw_payload
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		newID("ptx"),
		txn.UserID,
		txn.OrderID,
		txn.Platform,
		txn.ProductID,
		txn.Amount,
		txn.Currency,
		txn.Status,
		raw,
	)
	return err
}

func (s *Store) UpdatePaymentTransactionStatus(ctx context.Context, orderID string, status string) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE payment_transactions
		 SET status = $2, updated_at = NOW()
		 WHERE order_id = $1`,
		orderID,
		status,
	)
	return err
}

func (s *Store) GetPaymentByOrderID(ctx context.Context, orderID string) (models.PaymentTransaction, error) {
	var txn models.PaymentTransaction
	var rawPayload []byte
	err := s.db.QueryRowContext(
		ctx,
		`SELECT id, user_id, order_id, platform, product_id, amount, currency, status, raw_payload, created_at, updated_at
		 FROM payment_transactions
		 WHERE order_id = $1`,
		orderID,
	).Scan(
		&txn.ID,
		&txn.UserID,
		&txn.OrderID,
		&txn.Platform,
		&txn.ProductID,
		&txn.Amount,
		&txn.Currency,
		&txn.Status,
		&rawPayload,
		&txn.CreatedAt,
		&txn.UpdatedAt,
	)
	if err != nil {
		return models.PaymentTransaction{}, err
	}

	if len(rawPayload) > 0 {
		_ = json.Unmarshal(rawPayload, &txn.RawPayload)
	}
	return txn, nil
}

func (s *Store) GetSettledCreditPurchaseUSD(ctx context.Context, userID string) (float64, error) {
	var total float64
	err := s.db.QueryRowContext(
		ctx,
		`SELECT COALESCE(SUM(
		    CASE product_id
		      WHEN 'miaw_credit_usd_5_idr_25000' THEN 5
		      WHEN 'miaw_credit_usd_10_idr_50000' THEN 10
		      WHEN 'miaw_credit_usd_20_idr_100000' THEN 20
		      ELSE 0
		    END
		  ), 0)
		 FROM payment_transactions
		 WHERE user_id = $1
		   AND status IN ('settlement', 'capture', 'paid', 'success')`,
		userID,
	).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total, nil
}

func (s *Store) IncrementDailyUsage(ctx context.Context, userID string, tokenInput int, tokenOutput int, imageCount int, researchCount int) error {
	usageDate := dailyUsageDate()
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO user_daily_usage (user_id, usage_date, prompt_count, image_count, research_count, token_input, token_output)
		 VALUES ($1, $2, 1, $3, $4, $5, $6)
		 ON CONFLICT (user_id, usage_date)
		 DO UPDATE SET prompt_count = user_daily_usage.prompt_count + 1,
		               image_count = user_daily_usage.image_count + EXCLUDED.image_count,
		               research_count = user_daily_usage.research_count + EXCLUDED.research_count,
		               token_input = user_daily_usage.token_input + EXCLUDED.token_input,
		               token_output = user_daily_usage.token_output + EXCLUDED.token_output`,
		userID,
		usageDate,
		imageCount,
		researchCount,
		tokenInput,
		tokenOutput,
	)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO user_usage_events (id, user_id, token_input, token_output, image_count, research_count)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		newID("use"),
		userID,
		tokenInput,
		tokenOutput,
		imageCount,
		researchCount,
	)
	return err
}

func (s *Store) GetDailyUsage(ctx context.Context, userID string) (models.UserDailyUsage, error) {
	var usage models.UserDailyUsage
	usageDate := dailyUsageDate()
	err := s.db.QueryRowContext(
		ctx,
		`SELECT user_id, usage_date, prompt_count, image_count, research_count, token_input, token_output
		 FROM user_daily_usage
		 WHERE user_id = $1 AND usage_date = $2`,
		userID,
		usageDate,
	).Scan(
		&usage.UserID,
		&usage.UsageDate,
		&usage.PromptCount,
		&usage.ImageCount,
		&usage.ResearchCount,
		&usage.TokenInput,
		&usage.TokenOutput,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.UserDailyUsage{
				UserID:        userID,
				UsageDate:     usageDate,
				PromptCount:   0,
				ImageCount:    0,
				ResearchCount: 0,
				TokenInput:    0,
				TokenOutput:   0,
			}, nil
		}
		return models.UserDailyUsage{}, err
	}
	return usage, nil
}

func dailyUsageDate() time.Time {
	now := time.Now().In(time.FixedZone("WIB", 7*60*60))
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

func (s *Store) GetUsageWindow(ctx context.Context, userID string, since time.Time) (models.UsageWindow, error) {
	var usage models.UsageWindow
	err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*), COALESCE(SUM(image_count), 0), COALESCE(SUM(research_count), 0),
		        COALESCE(SUM(token_input), 0), COALESCE(SUM(token_output), 0)
		 FROM user_usage_events
		 WHERE user_id = $1 AND created_at >= $2`,
		userID,
		since,
	).Scan(
		&usage.PromptCount,
		&usage.ImageCount,
		&usage.ResearchCount,
		&usage.TokenInput,
		&usage.TokenOutput,
	)
	if err != nil {
		return models.UsageWindow{}, err
	}
	return usage, nil
}
