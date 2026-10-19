package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestAccountRepositoryRecoverExpiredOpenAI403CooldownsIsAtomicAndGuarded(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	var query string
	var args []any
	repo := newAccountRepositoryWithSQL(nil, captureQuerySQL{db: db, captured: &query, args: &args}, nil)
	now := time.Now()
	platforms := pq.Array([]string{service.PlatformOpenAI, service.PlatformKimi, service.PlatformZhipu, service.PlatformDeepseek, service.PlatformMiniMax, service.PlatformOpenCodeGo})
	mock.ExpectQuery("WITH updated AS").WithArgs(now, "OpenAI 403 temporary cooldown%", service.StatusActive,
		service.SchedulerOutboxEventAccountChanged, platforms).WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(17))
	updated, err := repo.RecoverExpiredOpenAI403Cooldowns(context.Background(), now)
	require.NoError(t, err)
	require.EqualValues(t, 1, updated)
	normalized := normalizeSQLWhitespace(query)
	require.Contains(t, normalized, "UPDATE accounts AS a")
	require.Contains(t, normalized, "a.deleted_at IS NULL")
	require.Contains(t, normalized, "a.platform = ANY($5)")
	require.Contains(t, normalized, "a.status = $3")
	require.Contains(t, normalized, "a.schedulable IS TRUE")
	require.Contains(t, normalized, "a.temp_unschedulable_until <= $1")
	require.Contains(t, normalized, "a.temp_unschedulable_reason LIKE $2")
	require.Contains(t, normalized, "a.error_message = a.temp_unschedulable_reason")
	require.Contains(t, normalized, "a.expires_at > $1")
	require.Contains(t, normalized, "temp_unschedulable_until = NULL")
	require.Contains(t, normalized, "temp_unschedulable_reason = NULL")
	require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
	require.NotContains(t, normalized, "schedulable = true", "recovery must never enable a manually paused account")
	require.NotContains(t, normalized, "status = 'active'", "persistent errors must never be bulk reset")
	require.NotContains(t, strings.Split(normalized, "WHERE")[0], "rate_limit_reset_at =")
	require.Len(t, args, 5)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryRecoverExpiredOpenAI403CooldownsHandlesEmptyAndFailedWrites(t *testing.T) {
	for _, scenario := range []string{"empty", "query_error", "row_error"} {
		t.Run(scenario, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			repo := newAccountRepositoryWithSQL(nil, db, nil)
			expectation := mock.ExpectQuery("WITH updated AS")
			switch scenario {
			case "empty":
				expectation.WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
			case "query_error":
				expectation.WillReturnError(errors.New("outbox insert failed"))
			case "row_error":
				expectation.WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(17).RowError(0, errors.New("read failed")))
			}
			updated, err := repo.RecoverExpiredOpenAI403Cooldowns(context.Background(), time.Now())
			require.Zero(t, updated)
			if scenario == "empty" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
