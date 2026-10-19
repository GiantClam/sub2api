//go:build cooldownintegration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func cooldownSQLDatabase(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("SUB2API_COOLDOWN_TEST_DSN")
	if dsn == "" {
		t.Skip("SUB2API_COOLDOWN_TEST_DSN must point to an isolated local cooldown_test database")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "localhost"}, u.Hostname())
	require.Equal(t, "/cooldown_test", u.Path)
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("cooldown_%d", time.Now().UnixNano())
	_, err = admin.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE") })
	query := u.Query()
	query.Set("search_path", schema)
	query.Set("application_name", schema)
	u.RawQuery = query.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE accounts (
		id bigint PRIMARY KEY, platform text NOT NULL DEFAULT 'openai',
		status text NOT NULL DEFAULT 'active', schedulable boolean NOT NULL DEFAULT true,
		deleted_at timestamptz, temp_unschedulable_until timestamptz,
		temp_unschedulable_reason text, error_message text,
		auto_pause_on_expired boolean NOT NULL DEFAULT true, expires_at timestamptz,
		rate_limit_reset_at timestamptz, overload_until timestamptz,
		credentials jsonb NOT NULL DEFAULT '{"keep":"credentials"}',
		extra jsonb NOT NULL DEFAULT '{"keep":"metadata"}',
		updated_at timestamptz NOT NULL DEFAULT '2026-01-01T00:00:00Z'
	);
	CREATE TABLE scheduler_outbox (
		id bigserial PRIMARY KEY, event_type text NOT NULL, account_id bigint,
		group_id bigint, payload jsonb
	)`)
	require.NoError(t, err)
	return db, admin
}

func insertExpiredCooldown(t *testing.T, db *sql.DB, id int64, now time.Time) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO accounts (id, temp_unschedulable_until, temp_unschedulable_reason)
		VALUES ($1, $2, $3)`, id, now.Add(-time.Minute), service.OpenAI403CooldownReasonPrefix+": retry")
	require.NoError(t, err)
}

func cooldownRow(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var row string
	require.NoError(t, db.QueryRow("SELECT to_jsonb(a)::text FROM accounts a WHERE id=$1", id).Scan(&row))
	return row
}

func TestAccountCooldownSQLRecoveryBoundariesAndIdempotence(t *testing.T) {
	db, _ := cooldownSQLDatabase(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for id := int64(1); id <= 14; id++ {
		insertExpiredCooldown(t, db, id, now)
	}
	changes := []string{
		`UPDATE accounts SET platform='deepseek' WHERE id=2`,
		`UPDATE accounts SET error_message=temp_unschedulable_reason WHERE id=2`,
		`UPDATE accounts SET temp_unschedulable_until=NOW()+INTERVAL '1 hour' WHERE id=3`,
		`UPDATE accounts SET schedulable=false WHERE id=4`,
		`UPDATE accounts SET status='disabled' WHERE id=5`,
		`UPDATE accounts SET status='error', error_message='Unauthorized (401)', schedulable=false WHERE id=6`,
		`UPDATE accounts SET status='error', error_message='Access forbidden (403): Insufficient account balance | consecutive_403=3/3', schedulable=false WHERE id=7`,
		`UPDATE accounts SET deleted_at=NOW() WHERE id=8`,
		`UPDATE accounts SET expires_at=NOW()-INTERVAL '1 hour' WHERE id=9`,
		`UPDATE accounts SET error_message='Insufficient quota (402)' WHERE id=10`,
		`UPDATE accounts SET rate_limit_reset_at=NOW()+INTERVAL '1 hour', overload_until=NOW()+INTERVAL '2 hours' WHERE id=11`,
		`UPDATE accounts SET temp_unschedulable_reason='stream_timeout' WHERE id=12`,
		`UPDATE accounts SET platform='anthropic' WHERE id=13`,
		`UPDATE accounts SET temp_unschedulable_until=NULL WHERE id=14`,
	}
	for _, change := range changes {
		_, err := db.Exec(change)
		require.NoError(t, err)
	}
	unchanged := make(map[int64]string)
	for _, id := range []int64{3, 4, 5, 6, 7, 8, 9, 10, 12, 13, 14} {
		unchanged[id] = cooldownRow(t, db, id)
	}
	repo := newAccountRepositoryWithSQL(nil, db, nil)
	count, err := repo.RecoverExpiredOpenAI403Cooldowns(context.Background(), now)
	require.NoError(t, err)
	require.EqualValues(t, 3, count)
	for id, before := range unchanged {
		require.Equal(t, before, cooldownRow(t, db, id), "protected account %d", id)
	}
	for _, id := range []int64{1, 2, 11} {
		var cleared, preserved bool
		err := db.QueryRow(`SELECT temp_unschedulable_until IS NULL AND temp_unschedulable_reason IS NULL
			AND COALESCE(error_message, '')='', status='active' AND schedulable=true
			AND credentials='{"keep":"credentials"}'::jsonb AND extra='{"keep":"metadata"}'::jsonb
			FROM accounts WHERE id=$1`, id).Scan(&cleared, &preserved)
		require.NoError(t, err)
		require.True(t, cleared)
		require.True(t, preserved)
	}
	var independentLimitsPreserved bool
	require.NoError(t, db.QueryRow(`SELECT rate_limit_reset_at>NOW() AND overload_until>NOW()
		FROM accounts WHERE id=11`).Scan(&independentLimitsPreserved))
	require.True(t, independentLimitsPreserved)
	var queued int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM scheduler_outbox
		WHERE event_type='account_changed' AND account_id IN (1,2,11)`).Scan(&queued))
	require.Equal(t, 3, queued)
	count, err = repo.RecoverExpiredOpenAI403Cooldowns(context.Background(), now)
	require.NoError(t, err)
	require.Zero(t, count)
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduler_outbox").Scan(&queued))
	require.Equal(t, 3, queued)
}

func TestAccountCooldownSQLRecoveryRollsBackWhenOutboxFails(t *testing.T) {
	db, _ := cooldownSQLDatabase(t)
	now := time.Now().UTC()
	insertExpiredCooldown(t, db, 1, now)
	before := cooldownRow(t, db, 1)
	_, err := db.Exec("ALTER TABLE scheduler_outbox ADD CONSTRAINT reject_recovery CHECK (false)")
	require.NoError(t, err)
	repo := newAccountRepositoryWithSQL(nil, db, nil)
	count, err := repo.RecoverExpiredOpenAI403Cooldowns(context.Background(), now)
	require.Error(t, err)
	require.Zero(t, count)
	require.Equal(t, before, cooldownRow(t, db, 1), "outbox failure must roll back the state update")
}

func TestAccountCooldownSQLRecoveryRechecksConcurrentChanges(t *testing.T) {
	for _, change := range []struct {
		name string
		sql  string
	}{
		{"manual_pause", "UPDATE accounts SET schedulable=false WHERE id=1"},
		{"new_unauthorized", "UPDATE accounts SET status='error', error_message='Unauthorized (401)' WHERE id=1"},
		{"extended_cooldown", "UPDATE accounts SET temp_unschedulable_until=NOW()+INTERVAL '1 hour' WHERE id=1"},
	} {
		t.Run(change.name, func(t *testing.T) {
			db, admin := cooldownSQLDatabase(t)
			now := time.Now().UTC()
			insertExpiredCooldown(t, db, 1, now)
			tx, err := db.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			_, err = tx.Exec(change.sql)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			type result struct {
				count int64
				err   error
			}
			done := make(chan result, 1)
			go func() {
				repo := newAccountRepositoryWithSQL(nil, db, nil)
				count, err := repo.RecoverExpiredOpenAI403Cooldowns(ctx, now)
				done <- result{count, err}
			}()
			var applicationName string
			require.NoError(t, tx.QueryRow("SHOW application_name").Scan(&applicationName))
			deadline := time.Now().Add(5 * time.Second)
			locked := false
			for time.Now().Before(deadline) {
				err = admin.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
					WHERE application_name=$1 AND wait_event_type='Lock')`, applicationName).Scan(&locked)
				require.NoError(t, err)
				if locked {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			require.True(t, locked, "recovery must be observed waiting on the concurrent row update")
			require.NoError(t, tx.Commit())
			before := cooldownRow(t, db, 1)
			got := <-done
			require.NoError(t, got.err)
			require.Zero(t, got.count)
			require.Equal(t, before, cooldownRow(t, db, 1))
			var queued int
			require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduler_outbox").Scan(&queued))
			require.Zero(t, queued)
		})
	}
}
