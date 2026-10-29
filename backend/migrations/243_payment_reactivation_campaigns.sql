CREATE TABLE IF NOT EXISTS payment_reactivation_campaigns (
    campaign_id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    source_order_id BIGINT NOT NULL UNIQUE REFERENCES payment_orders(id) ON DELETE CASCADE,
    initial_announcement_id BIGINT REFERENCES announcements(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    initial_email_sent_at TIMESTAMPTZ,
    initial_email_attempts INTEGER NOT NULL DEFAULT 0,
    initial_email_next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    followup_email_sent_at TIMESTAMPTZ,
    followup_email_attempts INTEGER NOT NULL DEFAULT 0,
    followup_email_next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reward_order_id BIGINT UNIQUE REFERENCES payment_orders(id),
    reward_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    reward_granted_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT payment_reactivation_campaign_reward_check CHECK (
        reward_amount >= 0 AND
        ((reward_order_id IS NULL AND reward_granted_at IS NULL AND reward_amount = 0) OR
         (reward_order_id IS NOT NULL AND reward_amount > 0))
    )
);

CREATE INDEX IF NOT EXISTS idx_payment_reactivation_initial_due
    ON payment_reactivation_campaigns(initial_email_next_attempt_at, campaign_id)
    WHERE initial_email_sent_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_payment_reactivation_followup_due
    ON payment_reactivation_campaigns(followup_email_next_attempt_at, initial_email_sent_at, campaign_id)
    WHERE followup_email_sent_at IS NULL;

COMMENT ON TABLE payment_reactivation_campaigns IS 'One-time first balance recharge reminder and bonus state per user';
