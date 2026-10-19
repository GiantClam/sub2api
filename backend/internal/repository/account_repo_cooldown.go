package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// RecoverExpiredOpenAI403Cooldowns clears only expired system-owned cooldowns.
// It never changes status or schedulable: legacy errors have no provenance that
// can distinguish an automatic disable from an administrator's manual pause.
func (r *accountRepository) RecoverExpiredOpenAI403Cooldowns(ctx context.Context, now time.Time) (int64, error) {
	rows, err := r.sql.QueryContext(ctx, `
		WITH updated AS (
			UPDATE accounts AS a
			SET temp_unschedulable_until = NULL,
				temp_unschedulable_reason = NULL,
				error_message = CASE WHEN a.error_message = a.temp_unschedulable_reason THEN NULL ELSE a.error_message END,
				updated_at = NOW()
			WHERE a.deleted_at IS NULL
				AND a.platform = ANY($5)
				AND a.status = $3
				AND a.schedulable IS TRUE
				AND a.temp_unschedulable_until <= $1
				AND a.temp_unschedulable_reason LIKE $2
				AND (a.error_message IS NULL OR a.error_message = '' OR a.error_message = a.temp_unschedulable_reason)
				AND (a.auto_pause_on_expired IS NOT TRUE OR a.expires_at IS NULL OR a.expires_at > $1)
			RETURNING a.id
		), queued AS (
			INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
			SELECT $4, updated.id, NULL, NULL FROM updated
			RETURNING account_id
		)
		SELECT account_id FROM queued
	`, now, service.OpenAI403CooldownReasonPrefix+"%", service.StatusActive, service.SchedulerOutboxEventAccountChanged,
		pq.Array([]string{service.PlatformOpenAI, service.PlatformKimi, service.PlatformZhipu, service.PlatformDeepseek, service.PlatformMiniMax, service.PlatformOpenCodeGo}))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var accountIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		accountIDs = append(accountIDs, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, id := range accountIDs {
		r.syncSchedulerAccountSnapshotDetached(ctx, id)
	}
	return int64(len(accountIDs)), nil
}
