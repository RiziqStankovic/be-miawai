package database

import (
	"context"
	"time"

	"be-miawai/internal/models"
)

func (s *Store) GetAdminOverview(ctx context.Context, since time.Time, limit int) (models.AdminOverview, error) {
	if limit <= 0 {
		limit = 50
	}

	var overview models.AdminOverview
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&overview.TotalUsers); err != nil {
		return models.AdminOverview{}, err
	}
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*)
		 FROM users
		 WHERE subscription_status IN ('active', 'trialing')
		   AND (entitled_until IS NULL OR entitled_until > NOW())`,
	).Scan(&overview.ActiveSubscriptions); err != nil {
		return models.AdminOverview{}, err
	}
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*)
		 FROM users
		 WHERE subscription_status = 'trialing'
		   AND (entitled_until IS NULL OR entitled_until > NOW())`,
	).Scan(&overview.TrialSubscriptions); err != nil {
		return models.AdminOverview{}, err
	}
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COALESCE(SUM(amount), 0)
		 FROM payment_transactions
		 WHERE status IN ('settlement', 'capture', 'paid', 'success')`,
	).Scan(&overview.PaymentTotalAmount); err != nil {
		return models.AdminOverview{}, err
	}
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*), COALESCE(SUM(image_count), 0), COALESCE(SUM(research_count), 0),
		        COALESCE(SUM(token_input), 0), COALESCE(SUM(token_output), 0)
		 FROM user_usage_events
		 WHERE created_at >= $1`,
		since,
	).Scan(
		&overview.TotalPromptCount,
		&overview.TotalImageCount,
		&overview.TotalResearchCount,
		&overview.TotalTokenInput,
		&overview.TotalTokenOutput,
	); err != nil {
		return models.AdminOverview{}, err
	}

	usageRows, err := s.db.QueryContext(
		ctx,
		`SELECT u.id, u.email, u.name, u.plan, u.subscription_status, u.entitled_until, u.created_at,
		        COUNT(e.id), COALESCE(SUM(e.image_count), 0), COALESCE(SUM(e.research_count), 0),
		        COALESCE(SUM(e.token_input), 0), COALESCE(SUM(e.token_output), 0)
		 FROM users u
		 LEFT JOIN user_usage_events e ON e.user_id = u.id AND e.created_at >= $1
		 GROUP BY u.id, u.email, u.name, u.plan, u.subscription_status, u.entitled_until, u.created_at
		 ORDER BY COALESCE(SUM(e.token_input + e.token_output), 0) DESC, u.created_at DESC
		 LIMIT $2`,
		since,
		limit,
	)
	if err != nil {
		return models.AdminOverview{}, err
	}
	defer usageRows.Close()
	for usageRows.Next() {
		var usage models.AdminUserUsage
		if err := usageRows.Scan(
			&usage.UserID,
			&usage.Email,
			&usage.Name,
			&usage.Plan,
			&usage.SubscriptionStatus,
			&usage.EntitledUntil,
			&usage.CreatedAt,
			&usage.PromptCount,
			&usage.ImageCount,
			&usage.ResearchCount,
			&usage.TokenInput,
			&usage.TokenOutput,
		); err != nil {
			return models.AdminOverview{}, err
		}
		overview.UsageByUser = append(overview.UsageByUser, usage)
	}
	if err := usageRows.Err(); err != nil {
		return models.AdminOverview{}, err
	}

	paymentRows, err := s.db.QueryContext(
		ctx,
		`SELECT p.id, p.user_id, u.email, p.order_id, p.platform, p.product_id, p.amount, p.currency, p.status, p.created_at, p.updated_at
		 FROM payment_transactions p
		 JOIN users u ON u.id = p.user_id
		 ORDER BY p.updated_at DESC
		 LIMIT $1`,
		limit,
	)
	if err != nil {
		return models.AdminOverview{}, err
	}
	defer paymentRows.Close()
	for paymentRows.Next() {
		var payment models.AdminPaymentSummary
		if err := paymentRows.Scan(
			&payment.ID,
			&payment.UserID,
			&payment.Email,
			&payment.OrderID,
			&payment.Platform,
			&payment.ProductID,
			&payment.Amount,
			&payment.Currency,
			&payment.Status,
			&payment.CreatedAt,
			&payment.UpdatedAt,
		); err != nil {
			return models.AdminOverview{}, err
		}
		overview.RecentPayments = append(overview.RecentPayments, payment)
	}
	if err := paymentRows.Err(); err != nil {
		return models.AdminOverview{}, err
	}

	return overview, nil
}
