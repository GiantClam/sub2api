package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/payment"
)

const (
	paymentReactivationInitialDelay  = 10 * time.Minute
	paymentReactivationFollowupDelay = 24 * time.Hour
	paymentReactivationRewardCap     = 5.0
	paymentReactivationBatchSize     = 100
	paymentReactivationRetryBase     = 5 * time.Minute
	paymentReactivationRetryMax      = time.Hour
	paymentReactivationRewardType    = "campaign_reactivation"
)

// calculatePaymentReactivationBonus returns a two-decimal, half-up 10% bonus
// capped at the campaign maximum. The input is the order's credited USD amount,
// which already includes the normal tier bonus.
func calculatePaymentReactivationBonus(creditedAmount float64) float64 {
	if creditedAmount <= 0 || math.IsNaN(creditedAmount) || math.IsInf(creditedAmount, 0) {
		return 0
	}
	bonus := math.Round(creditedAmount*0.10*100+1e-9) / 100
	if bonus > paymentReactivationRewardCap {
		return paymentReactivationRewardCap
	}
	return bonus
}

type paymentReactivationCampaign struct {
	ID                       int64
	UserID                   int64
	SourceOrderID            int64
	InitialAnnouncementID    sql.NullInt64
	CreatedAt                time.Time
	InitialEmailSentAt       sql.NullTime
	InitialEmailAttempts     int
	InitialEmailNextAttempt  time.Time
	FollowupEmailSentAt      sql.NullTime
	FollowupEmailAttempts    int
	FollowupEmailNextAttempt time.Time
	RewardOrderID            sql.NullInt64
	RewardAmount             float64
	RewardGrantedAt          sql.NullTime
}

func (s *PaymentService) ProcessPaymentReactivationReminders(ctx context.Context, now time.Time) error {
	if s == nil || s.entClient == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}

	var firstErr error
	if err := s.processPaymentReactivationInitialReminders(ctx, now); err != nil {
		firstErr = err
	}
	if err := s.processPaymentReactivationFollowups(ctx, now); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := s.ProcessPaymentReactivationRewards(ctx, now); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *PaymentService) ProcessPaymentReactivationRewards(ctx context.Context, _ time.Time) error {
	if s == nil || s.entClient == nil {
		return nil
	}
	ids, err := s.listPaymentReactivationRewardCampaigns(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, campaignID := range ids {
		if err := s.settlePaymentReactivationCampaign(ctx, campaignID); err != nil {
			slog.Warn("first recharge reward settlement failed", "campaign_id", campaignID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (s *PaymentService) processPaymentReactivationInitialReminders(ctx context.Context, now time.Time) error {
	orders, err := s.entClient.PaymentOrder.Query().
		Where(
			paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
			paymentorder.StatusEQ(OrderStatusPending),
			paymentorder.CreatedAtLTE(now.Add(-paymentReactivationInitialDelay)),
			paymentorder.Or(
				paymentorder.And(paymentorder.PayURLNotNil(), paymentorder.PayURLNEQ("")),
				paymentorder.And(paymentorder.QrCodeNotNil(), paymentorder.QrCodeNEQ("")),
				paymentorder.And(paymentorder.QrCodeImgNotNil(), paymentorder.QrCodeImgNEQ("")),
			),
		).
		Order(dbent.Asc(paymentorder.FieldCreatedAt)).
		Limit(paymentReactivationBatchSize).
		All(ctx)
	if err != nil {
		return fmt.Errorf("list first recharge reminder orders: %w", err)
	}

	var firstErr error
	for _, order := range orders {
		campaignID, created, err := s.enrollPaymentReactivationCampaign(ctx, order.ID, now)
		if err != nil {
			slog.Warn("first recharge campaign enrollment failed", "order_id", order.ID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if created {
			if err := s.sendPaymentReactivationInitialEmail(ctx, campaignID, now); err != nil {
				slog.Warn("first recharge reminder email failed", "campaign_id", campaignID, "error", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func (s *PaymentService) enrollPaymentReactivationCampaign(ctx context.Context, orderID int64, now time.Time) (int64, bool, error) {
	order, err := s.entClient.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return 0, false, err
	}
	if !paymentReactivationEligiblePendingOrder(order) {
		return 0, false, nil
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("begin first recharge campaign transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)

	hasPaid, err := paymentReactivationUserHasPaidOrder(txCtx, tx.Client(), order.UserID)
	if err != nil {
		return 0, false, fmt.Errorf("check first recharge eligibility: %w", err)
	}
	if hasPaid {
		return 0, false, nil
	}

	campaignID, inserted, err := insertPaymentReactivationCampaign(txCtx, tx.Client(), order.UserID, order.ID, now)
	if err != nil {
		return 0, false, err
	}
	if !inserted {
		return 0, false, nil
	}

	checkoutURL := paymentReactivationOrderURL(ctx, s.notificationEmailService, order)
	announcement, err := tx.Announcement.Create().
		SetTitle("Complete your first recharge and get 10% extra").
		SetContent(paymentReactivationAnnouncementContent(checkoutURL, false)).
		SetTitleZh("完成首次充值，额外赠送 10% 余额").
		SetTitleEn("Complete your first recharge and get 10% extra").
		SetContentZh(paymentReactivationAnnouncementContent(checkoutURL, true)).
		SetContentEn(paymentReactivationAnnouncementContent(checkoutURL, false)).
		SetStatus(domain.AnnouncementStatusActive).
		SetNotifyMode(domain.AnnouncementNotifyModePopup).
		SetTargeting(domain.AnnouncementTargeting{UserIDs: []int64{order.UserID}}).
		Save(txCtx)
	if err != nil {
		return 0, false, fmt.Errorf("create first recharge announcement: %w", err)
	}
	if err := updatePaymentReactivationAnnouncement(txCtx, tx.Client(), campaignID, announcement.ID); err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit first recharge campaign: %w", err)
	}
	return campaignID, true, nil
}

func paymentReactivationEligiblePendingOrder(order *dbent.PaymentOrder) bool {
	if order == nil || order.OrderType != payment.OrderTypeBalance || order.Status != OrderStatusPending {
		return false
	}
	return paymentOrderCheckoutURL(order) != "" || paymentOrderCheckoutQR(order) != ""
}

func paymentReactivationUserHasPaidOrder(ctx context.Context, client *dbent.Client, userID int64) (bool, error) {
	return client.PaymentOrder.Query().
		Where(
			paymentorder.UserIDEQ(userID),
			paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
			paymentorder.PaidAtNotNil(),
			paymentorder.StatusNotIn(OrderStatusPending, OrderStatusFailed, OrderStatusCancelled, OrderStatusExpired),
			paymentorder.PaymentTypeNEQ(paymentReactivationRewardType),
		).
		Exist(ctx)
}

func paymentReactivationFirstSuccessfulOrder(ctx context.Context, client *dbent.Client, userID int64) (*dbent.PaymentOrder, error) {
	orders, err := client.PaymentOrder.Query().
		Where(
			paymentorder.UserIDEQ(userID),
			paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
			paymentorder.PaidAtNotNil(),
			paymentorder.StatusEQ(OrderStatusCompleted),
			paymentorder.PaymentTypeNEQ(paymentReactivationRewardType),
		).
		Order(dbent.Asc(paymentorder.FieldPaidAt), dbent.Asc(paymentorder.FieldID)).
		Limit(1).
		All(ctx)
	if err != nil {
		return nil, err
	}
	if len(orders) == 0 {
		return nil, nil
	}
	return orders[0], nil
}

func (s *PaymentService) processPaymentReactivationFollowups(ctx context.Context, now time.Time) error {
	ids, err := listPaymentReactivationFollowupCampaigns(ctx, s.entClient, now)
	if err != nil {
		return err
	}
	var firstErr error
	for _, campaignID := range ids {
		if err := s.sendPaymentReactivationFollowup(ctx, campaignID, now); err != nil {
			slog.Warn("first recharge follow-up email failed", "campaign_id", campaignID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (s *PaymentService) sendPaymentReactivationInitialEmail(ctx context.Context, campaignID int64, now time.Time) error {
	campaign, err := loadPaymentReactivationCampaign(ctx, s.entClient, campaignID, false)
	if err != nil {
		return err
	}
	order, err := s.entClient.PaymentOrder.Get(ctx, campaign.SourceOrderID)
	if err != nil {
		return err
	}
	if order.Status != OrderStatusPending {
		return nil
	}
	if paid, err := paymentReactivationUserHasPaidOrder(ctx, s.entClient, order.UserID); err != nil {
		return err
	} else if paid {
		return nil
	}

	claimed, err := claimPaymentReactivationEmail(ctx, s.entClient, campaignID, false, now)
	if err != nil || !claimed {
		return err
	}
	return s.sendClaimedPaymentReactivationEmail(ctx, campaign, order, false)
}

func (s *PaymentService) sendPaymentReactivationFollowup(ctx context.Context, campaignID int64, now time.Time) error {
	campaign, err := loadPaymentReactivationCampaign(ctx, s.entClient, campaignID, false)
	if err != nil {
		return err
	}
	if campaign.RewardOrderID.Valid || campaign.RewardGrantedAt.Valid {
		return nil
	}
	if paid, err := paymentReactivationUserHasPaidOrder(ctx, s.entClient, campaign.UserID); err != nil {
		return err
	} else if paid {
		return s.settlePaymentReactivationCampaign(ctx, campaignID)
	}

	claimed, err := claimPaymentReactivationEmail(ctx, s.entClient, campaignID, true, now)
	if err != nil || !claimed {
		return err
	}
	order, err := s.entClient.PaymentOrder.Get(ctx, campaign.SourceOrderID)
	if err != nil && !dbent.IsNotFound(err) {
		return err
	}
	if order == nil {
		order = &dbent.PaymentOrder{UserID: campaign.UserID}
	}
	return s.sendClaimedPaymentReactivationEmail(ctx, campaign, order, true)
}

func (s *PaymentService) sendClaimedPaymentReactivationEmail(ctx context.Context, campaign *paymentReactivationCampaign, order *dbent.PaymentOrder, followup bool) error {
	if campaign == nil || s.notificationEmailService == nil {
		return fmt.Errorf("first recharge notification email service is not configured")
	}
	email := strings.TrimSpace(order.UserEmail)
	name := strings.TrimSpace(order.UserName)
	if email == "" && s.userRepo != nil {
		user, err := s.userRepo.GetByID(ctx, campaign.UserID)
		if err != nil {
			return err
		}
		email = strings.TrimSpace(user.Email)
		if name == "" {
			name = strings.TrimSpace(user.Username)
		}
	}
	if email == "" {
		return markPaymentReactivationEmailSent(ctx, s.entClient, campaign.ID, followup)
	}

	checkoutURL := paymentReactivationOrderURL(ctx, s.notificationEmailService, order)
	event := NotificationEmailEventPaymentReactivation
	reminderKey := "initial"
	if followup {
		event = NotificationEmailEventPaymentReactivationFollowup
		reminderKey = "followup"
		checkoutURL = paymentReactivationPurchaseURL(ctx, s.notificationEmailService)
	}
	err := s.notificationEmailService.Send(ctx, NotificationEmailSendInput{
		Event:          event,
		RecipientEmail: email,
		RecipientName:  firstNonEmpty(name, email),
		UserID:         campaign.UserID,
		SourceType:     "payment_reactivation",
		SourceID:       strconv.FormatInt(campaign.ID, 10),
		ReminderKey:    reminderKey,
		Variables: map[string]string{
			"recharge_url": checkoutURL,
			"reward_cap":   fmt.Sprintf("%.2f USD", paymentReactivationRewardCap),
		},
	})
	if err != nil {
		return err
	}
	return markPaymentReactivationEmailSent(ctx, s.entClient, campaign.ID, followup)
}

func (s *PaymentService) applyPaymentReactivationBonus(ctx context.Context, order *dbent.PaymentOrder) error {
	if s == nil || order == nil || order.OrderType != payment.OrderTypeBalance || s.redeemService == nil {
		return nil
	}
	campaign, err := loadPaymentReactivationCampaignByUser(ctx, s.entClient, order.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.settlePaymentReactivationCampaign(ctx, campaign.ID)
}

func (s *PaymentService) settlePaymentReactivationCampaign(ctx context.Context, campaignID int64) error {
	if campaignID <= 0 || s == nil || s.entClient == nil || s.redeemService == nil {
		return nil
	}
	// Re-read and lock the campaign in a transaction. The reward order is a
	// durable reservation, so a crash between reservation and redemption is
	// resumed by the next maintenance pass without issuing a second reward.
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin first recharge reward transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	campaign, err := loadPaymentReactivationCampaign(txCtx, tx.Client(), campaignID, true)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if campaign.RewardGrantedAt.Valid {
		return nil
	}

	locked := campaign

	rewardOrderID := int64(0)
	rewardAmount := locked.RewardAmount
	if locked.RewardOrderID.Valid {
		rewardOrderID = locked.RewardOrderID.Int64
	} else {
		firstOrder, err := paymentReactivationFirstSuccessfulOrder(txCtx, tx.Client(), locked.UserID)
		if err != nil {
			return fmt.Errorf("find first successful recharge: %w", err)
		}
		if firstOrder == nil {
			return nil
		}
		rewardAmount = calculatePaymentReactivationBonus(firstOrder.Amount)
		if rewardAmount <= 0 {
			return nil
		}
		rewardCode := paymentReactivationRewardCode(locked.ID)
		rewardOrder, err := tx.PaymentOrder.Create().
			SetUserID(locked.UserID).
			SetUserEmail(firstOrder.UserEmail).
			SetUserName(firstOrder.UserName).
			SetAmount(rewardAmount).
			SetPayAmount(0).
			SetFeeRate(0).
			SetBonusAmount(rewardAmount).
			SetRechargeCode(rewardCode).
			SetOutTradeNo(paymentReactivationRewardTradeNo(locked.ID)).
			SetPaymentType(paymentReactivationRewardType).
			SetPaymentTradeNo(paymentReactivationRewardTradeNo(locked.ID)).
			SetOrderType(payment.OrderTypeBalance).
			SetStatus(OrderStatusRecharging).
			SetExpiresAt(time.Now()).
			SetClientIP("system").
			SetSrcHost("payment-reactivation").
			SetUserNotes("First successful recharge 10% reward").
			Save(txCtx)
		if err != nil {
			return fmt.Errorf("create first recharge reward order: %w", err)
		}
		rewardOrderID = rewardOrder.ID
		if err := reservePaymentReactivationReward(txCtx, tx.Client(), locked.ID, rewardOrderID, rewardAmount); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit first recharge reward reservation: %w", err)
	}

	if err := s.ensurePaymentReactivationRewardCode(ctx, locked.ID, rewardAmount); err != nil {
		return err
	}
	if err := s.redeemPaymentReactivationReward(ctx, locked.UserID, locked.ID, rewardAmount); err != nil {
		return err
	}
	granted, err := markPaymentReactivationRewardGranted(ctx, s.entClient, locked.ID, rewardOrderID, rewardAmount)
	if err != nil {
		return err
	}
	if granted {
		s.writeAuditLog(ctx, rewardOrderID, "FIRST_RECHARGE_REWARD_GRANTED", "system", map[string]any{
			"campaignID":   locked.ID,
			"sourceUserID": locked.UserID,
			"rewardAmount": rewardAmount,
		})
	}
	return nil
}

func (s *PaymentService) ensurePaymentReactivationRewardCode(ctx context.Context, campaignID int64, amount float64) error {
	code := paymentReactivationRewardCode(campaignID)
	existing, err := s.redeemService.GetByCode(ctx, code)
	if err != nil && !errors.Is(err, ErrRedeemCodeNotFound) {
		return err
	}
	if existing == nil {
		if err := s.redeemService.CreateCode(ctx, &RedeemCode{Code: code, Type: RedeemTypeBalance, Value: amount, Status: StatusUnused, Notes: "First successful recharge 10% reward"}); err != nil {
			lookup, lookupErr := s.redeemService.GetByCode(ctx, code)
			if lookupErr != nil {
				return err
			}
			existing = lookup
		}
	}
	if existing != nil && (existing.Type != RedeemTypeBalance || math.Abs(existing.Value-amount) > 1e-8) {
		return fmt.Errorf("first recharge reward code %s has unexpected value or type", code)
	}
	return nil
}

func (s *PaymentService) redeemPaymentReactivationReward(ctx context.Context, userID, campaignID int64, amount float64) error {
	code := paymentReactivationRewardCode(campaignID)
	used, err := s.redeemService.redeemForPaymentFulfillment(ctx, userID, code)
	if err == nil {
		if math.Abs(used.Value-amount) > 1e-8 {
			return fmt.Errorf("redeemed first recharge reward amount mismatch: expected %.8f, got %.8f", amount, used.Value)
		}
		return nil
	}
	if !errors.Is(err, ErrRedeemCodeUsed) {
		return err
	}
	existing, lookupErr := s.redeemService.GetByCode(ctx, code)
	if lookupErr != nil {
		return err
	}
	if existing.UsedBy == nil || *existing.UsedBy != userID || math.Abs(existing.Value-amount) > 1e-8 {
		return fmt.Errorf("first recharge reward code %s was used by another user or has a different amount", code)
	}
	return nil
}

func paymentReactivationRewardCode(campaignID int64) string {
	return fmt.Sprintf("PROMO-FIRST-%d", campaignID)
}

func paymentReactivationRewardTradeNo(campaignID int64) string {
	return fmt.Sprintf("PROMO-FIRST-%d", campaignID)
}

func paymentReactivationOrderURL(ctx context.Context, notificationService *NotificationEmailService, order *dbent.PaymentOrder) string {
	if url := paymentOrderCheckoutURL(order); url != "" {
		return url
	}
	return paymentReactivationPurchaseURL(ctx, notificationService)
}

func paymentOrderCheckoutURL(order *dbent.PaymentOrder) string {
	if order == nil || order.PayURL == nil {
		return ""
	}
	return strings.TrimSpace(*order.PayURL)
}

func paymentOrderCheckoutQR(order *dbent.PaymentOrder) string {
	if order == nil {
		return ""
	}
	if order.QrCode != nil && strings.TrimSpace(*order.QrCode) != "" {
		return strings.TrimSpace(*order.QrCode)
	}
	if order.QrCodeImg != nil {
		return strings.TrimSpace(*order.QrCodeImg)
	}
	return ""
}

func paymentReactivationPurchaseURL(ctx context.Context, notificationService *NotificationEmailService) string {
	if notificationService == nil || notificationService.settingRepo == nil {
		return "/purchase"
	}
	base := ""
	if frontendURL, err := notificationService.settingRepo.GetValue(ctx, SettingKeyFrontendURL); err == nil {
		base = strings.TrimRight(strings.TrimSpace(frontendURL), "/")
	}
	if base == "" {
		return "/purchase"
	}
	return base + "/purchase"
}

func paymentReactivationAnnouncementContent(checkoutURL string, chinese bool) string {
	if chinese {
		return fmt.Sprintf("你还没有完成首次充值。首次成功充值后，按实际到账 USD 余额额外赠送 10%%，最高 5.00 USD。\n\n[立即完成充值](%s)", checkoutURL)
	}
	return fmt.Sprintf("Your first recharge is still waiting for payment. Complete your first successful recharge to receive 10%% extra credited USD balance, capped at 5.00 USD.\n\n[Continue to recharge](%s)", checkoutURL)
}

func paymentReactivationRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := paymentReactivationRetryBase
	for i := 1; i < attempt && delay < paymentReactivationRetryMax; i++ {
		delay *= 2
	}
	if delay > paymentReactivationRetryMax {
		return paymentReactivationRetryMax
	}
	return delay
}

func insertPaymentReactivationCampaign(ctx context.Context, client *dbent.Client, userID, orderID int64, _ time.Time) (int64, bool, error) {
	query := `
INSERT INTO payment_reactivation_campaigns
    (user_id, source_order_id, created_at, initial_email_next_attempt_at, followup_email_next_attempt_at, updated_at)
VALUES ($1, $2, NOW(), NOW(), NOW(), NOW())
ON CONFLICT (user_id) DO NOTHING
RETURNING campaign_id`
	var rows entsql.Rows
	if err := client.Driver().Query(ctx, query, []any{userID, orderID}, &rows); err != nil {
		return 0, false, fmt.Errorf("insert first recharge campaign: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	var campaignID int64
	if err := rows.Scan(&campaignID); err != nil {
		return 0, false, err
	}
	return campaignID, true, nil
}

func updatePaymentReactivationAnnouncement(ctx context.Context, client *dbent.Client, campaignID, announcementID int64) error {
	var result entsql.Result
	if err := client.Driver().Exec(ctx, `UPDATE payment_reactivation_campaigns SET initial_announcement_id = $1, updated_at = NOW() WHERE campaign_id = $2`, []any{announcementID, campaignID}, &result); err != nil {
		return fmt.Errorf("link first recharge announcement: %w", err)
	}
	return nil
}

func loadPaymentReactivationCampaign(ctx context.Context, client *dbent.Client, campaignID int64, forUpdate bool) (*paymentReactivationCampaign, error) {
	query := `SELECT campaign_id, user_id, source_order_id, initial_announcement_id, created_at,
initial_email_sent_at, initial_email_attempts, initial_email_next_attempt_at,
followup_email_sent_at, followup_email_attempts, followup_email_next_attempt_at,
reward_order_id, reward_amount, reward_granted_at
FROM payment_reactivation_campaigns WHERE campaign_id = $1`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var rows entsql.Rows
	if err := client.Driver().Query(ctx, query, []any{campaignID}, &rows); err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	var campaign paymentReactivationCampaign
	if err := rows.Scan(
		&campaign.ID,
		&campaign.UserID,
		&campaign.SourceOrderID,
		&campaign.InitialAnnouncementID,
		&campaign.CreatedAt,
		&campaign.InitialEmailSentAt,
		&campaign.InitialEmailAttempts,
		&campaign.InitialEmailNextAttempt,
		&campaign.FollowupEmailSentAt,
		&campaign.FollowupEmailAttempts,
		&campaign.FollowupEmailNextAttempt,
		&campaign.RewardOrderID,
		&campaign.RewardAmount,
		&campaign.RewardGrantedAt,
	); err != nil {
		return nil, err
	}
	return &campaign, nil
}

func loadPaymentReactivationCampaignByUser(ctx context.Context, client *dbent.Client, userID int64) (*paymentReactivationCampaign, error) {
	query := `SELECT campaign_id FROM payment_reactivation_campaigns WHERE user_id = $1`
	var rows entsql.Rows
	if err := client.Driver().Query(ctx, query, []any{userID}, &rows); err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	var campaignID int64
	if err := rows.Scan(&campaignID); err != nil {
		return nil, err
	}
	return loadPaymentReactivationCampaign(ctx, client, campaignID, false)
}

func listPaymentReactivationFollowupCampaigns(ctx context.Context, client *dbent.Client, now time.Time) ([]int64, error) {
	return listPaymentReactivationCampaignIDs(ctx, client, `
SELECT campaign_id FROM payment_reactivation_campaigns
WHERE followup_email_sent_at IS NULL AND reward_order_id IS NULL
  AND created_at <= $1 AND followup_email_next_attempt_at <= $2
ORDER BY campaign_id LIMIT $3`, now.Add(-paymentReactivationFollowupDelay), now, paymentReactivationBatchSize)
}

func (s *PaymentService) listPaymentReactivationRewardCampaigns(ctx context.Context) ([]int64, error) {
	return listPaymentReactivationCampaignIDs(ctx, s.entClient, `
SELECT campaign_id FROM payment_reactivation_campaigns
WHERE reward_granted_at IS NULL
ORDER BY campaign_id LIMIT $1`, paymentReactivationBatchSize)
}

func listPaymentReactivationCampaignIDs(ctx context.Context, client *dbent.Client, query string, args ...any) ([]int64, error) {
	var rows entsql.Rows
	if err := client.Driver().Query(ctx, query, args, &rows); err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0, paymentReactivationBatchSize)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func claimPaymentReactivationEmail(ctx context.Context, client *dbent.Client, campaignID int64, followup bool, now time.Time) (bool, error) {
	query := `UPDATE payment_reactivation_campaigns
SET initial_email_attempts = initial_email_attempts + 1, initial_email_next_attempt_at = $2, updated_at = NOW()
WHERE campaign_id = $1 AND initial_email_sent_at IS NULL AND initial_email_next_attempt_at <= $3 AND reward_order_id IS NULL`
	args := []any{campaignID, now.Add(paymentReactivationRetryDelay(1)), now}
	if followup {
		query = `UPDATE payment_reactivation_campaigns
SET followup_email_attempts = followup_email_attempts + 1, followup_email_next_attempt_at = $2, updated_at = NOW()
WHERE campaign_id = $1 AND followup_email_sent_at IS NULL AND followup_email_next_attempt_at <= $3
  AND created_at <= $4 AND reward_order_id IS NULL`
		args = []any{campaignID, now.Add(paymentReactivationRetryDelay(1)), now, now.Add(-paymentReactivationFollowupDelay)}
	}
	var result entsql.Result
	if err := client.Driver().Exec(ctx, query, args, &result); err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

func markPaymentReactivationEmailSent(ctx context.Context, client *dbent.Client, campaignID int64, followup bool) error {
	column := "initial_email_sent_at"
	if followup {
		column = "followup_email_sent_at"
	}
	var result entsql.Result
	if err := client.Driver().Exec(ctx, fmt.Sprintf(`UPDATE payment_reactivation_campaigns SET %s = NOW(), updated_at = NOW() WHERE campaign_id = $1 AND %s IS NULL`, column, column), []any{campaignID}, &result); err != nil {
		return err
	}
	return nil
}

func reservePaymentReactivationReward(ctx context.Context, client *dbent.Client, campaignID, rewardOrderID int64, amount float64) error {
	var result entsql.Result
	if err := client.Driver().Exec(ctx, `UPDATE payment_reactivation_campaigns SET reward_order_id = $1, reward_amount = $2, updated_at = NOW() WHERE campaign_id = $3 AND reward_order_id IS NULL`, []any{rewardOrderID, amount, campaignID}, &result); err != nil {
		return fmt.Errorf("reserve first recharge reward: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("first recharge reward reservation was lost")
	}
	return nil
}

func markPaymentReactivationRewardGranted(ctx context.Context, client *dbent.Client, campaignID, rewardOrderID int64, amount float64) (bool, error) {
	tx, err := client.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	if updated, err := tx.PaymentOrder.Update().
		Where(paymentorder.IDEQ(rewardOrderID), paymentorder.StatusIn(OrderStatusRecharging, OrderStatusFailed, OrderStatusPaid)).
		SetStatus(OrderStatusCompleted).
		SetCompletedAt(time.Now()).
		Save(txCtx); err != nil {
		return false, err
	} else if updated == 0 {
		current, getErr := tx.PaymentOrder.Get(txCtx, rewardOrderID)
		if getErr != nil || current.Status != OrderStatusCompleted {
			if getErr != nil {
				return false, getErr
			}
			return false, fmt.Errorf("first recharge reward order %d is not completable", rewardOrderID)
		}
	}
	var result entsql.Result
	if err := tx.Client().Driver().Exec(ctx, `UPDATE payment_reactivation_campaigns SET reward_granted_at = NOW(), reward_amount = $2, updated_at = NOW() WHERE campaign_id = $1 AND reward_order_id = $3 AND reward_granted_at IS NULL`, []any{campaignID, amount, rewardOrderID}, &result); err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return updated > 0, nil
}
