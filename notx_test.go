package pqmigrate_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Preciselyco/pqmigrate"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const noTx = "-- pqmigrate:no-transaction\n"

// newDB creates a throwaway database on the server at $KVASIR_TESTDB_URL and
// returns its url; it is dropped when the test ends.
func newDB(t *testing.T) string {
	t.Helper()
	base := os.Getenv("KVASIR_TESTDB_URL")
	if base == "" {
		t.Skip("KVASIR_TESTDB_URL must point at a Postgres server whose role can create databases")
	}
	admin, err := sql.Open("postgres", base)
	require.NoError(t, err)
	t.Cleanup(func() { admin.Close() })

	suffix := make([]byte, 6)
	_, err = rand.Read(suffix)
	require.NoError(t, err)
	name := "pqmigrate_dev1375_" + hex.EncodeToString(suffix)
	_, err = admin.Exec("create database " + pq.QuoteIdentifier(name))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec("drop database if exists " + pq.QuoteIdentifier(name) + " with (force)")
		assert.NoError(t, err)
	})

	u, err := url.Parse(base)
	require.NoError(t, err)
	u.Path = "/" + name
	return u.String()
}

func openDB(t *testing.T, dbURL string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", dbURL)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

type mig struct {
	name, up, down string
}

func writeMigrations(t *testing.T, migs ...mig) string {
	t.Helper()
	dir := t.TempDir()
	for _, m := range migs {
		require.NoError(t, os.WriteFile(filepath.Join(dir, m.name+".up.sql"), []byte(m.up), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, m.name+".down.sql"), []byte(m.down), 0o644))
	}
	return dir
}

func newMigrator(dbURL, dir string, configure func(*pqmigrate.Config)) *pqmigrate.PQMigrate {
	cfg := pqmigrate.Config{
		AllInOneTx:         true,
		AllowNoTransaction: true,
		BaseDirectory:      dir,
		DBUrl:              dbURL,
		MigrationsTable:    "pqmigrate",
	}
	if configure != nil {
		configure(&cfg)
	}
	return pqmigrate.New(cfg)
}

func recorded(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRow(`select to_regclass('pqmigrate') is not null`).Scan(&exists))
	names := []string{}
	if !exists {
		return names
	}
	rows, err := db.Query(`select name from pqmigrate order by version`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		names = append(names, n)
	}
	require.NoError(t, rows.Err())
	return names
}

func relationExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRow(`select to_regclass($1) is not null`, name).Scan(&exists))
	return exists
}

func indexValid(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var valid bool
	require.NoError(t, db.QueryRow(`select indisvalid from pg_index where indexrelid = to_regclass($1)`, name).Scan(&valid))
	return valid
}

func randomLockKey(t *testing.T) int64 {
	t.Helper()
	n, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	require.NoError(t, err)
	return n.Int64() + 1
}

var (
	createT = mig{"1000000001_create_t", "create table t (a int, b int);", "drop table t;"}
	indexA  = mig{"1000000002_index_a", noTx + "create index concurrently t_a_idx on t (a);", noTx + "drop index concurrently t_a_idx;"}
	createU = mig{"1000000003_create_u", "create table u (a int);", "drop table u;"}
)

func TestTransactionalOnly(t *testing.T) {
	t.Run("all in one transaction commits in Finish", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		m := newMigrator(dbURL, writeMigrations(t, createT, createU), nil)

		n, err := m.MigrateUp(-1)
		require.NoError(t, err)
		assert.Equal(t, 2, n)
		assert.False(t, relationExists(t, db, "t"))

		require.NoError(t, m.Finish())
		assert.True(t, relationExists(t, db, "t"))
		assert.True(t, relationExists(t, db, "u"))
		assert.Equal(t, []string{createT.name, createU.name}, recorded(t, db))
	})

	t.Run("a create index concurrently not marked no-transaction fails in the transaction", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		unmarked := mig{"1000000002_index_a", "-- comment\n" + noTx + "create index concurrently t_a_idx on t (a);", ""}
		m := newMigrator(dbURL, writeMigrations(t, createT, unmarked), func(c *pqmigrate.Config) { c.AllInOneTx = false })

		_, err := m.MigrateUp(-1)
		assert.ErrorContains(t, err, "cannot run inside a transaction block")
		_ = m.Finish()
		assert.Equal(t, []string{createT.name}, recorded(t, db))
	})
}

func TestMigrateUpNoTransaction(t *testing.T) {
	t.Run("defers a no-transaction migration after the transactional ones", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		indexFirst := mig{"1000000001_index_a", noTx + "create index concurrently t_a_idx on t (a);", noTx + "drop index concurrently t_a_idx;"}
		createLater := mig{"1000000002_create_t", "create table t (a int);", "drop table t;"}
		m := newMigrator(dbURL, writeMigrations(t, indexFirst, createLater), nil)

		n, err := m.MigrateUp(-1)
		require.NoError(t, err)
		assert.Equal(t, 2, n)
		assert.True(t, relationExists(t, db, "t"), "transactional work is committed before Finish")
		assert.True(t, indexValid(t, db, "t_a_idx"))
		assert.Equal(t, []string{indexFirst.name, createLater.name}, recorded(t, db))
		assert.NoError(t, m.Finish())
		assert.NoError(t, m.Finish())
	})

	t.Run("runs create index concurrently with all in one transaction", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		m := newMigrator(dbURL, writeMigrations(t, createT, indexA, createU), nil)

		n, err := m.MigrateUp(-1)
		require.NoError(t, err)
		assert.Equal(t, 3, n)
		require.NoError(t, m.Finish())
		assert.True(t, indexValid(t, db, "t_a_idx"))
		assert.True(t, relationExists(t, db, "u"))
		assert.Equal(t, []string{createT.name, indexA.name, createU.name}, recorded(t, db))
	})

	t.Run("accepts trailing whitespace on the directive", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		spaced := mig{indexA.name, "-- pqmigrate:no-transaction  \t\r\ncreate index concurrently t_a_idx on t (a);", ""}
		m := newMigrator(dbURL, writeMigrations(t, createT, spaced), nil)

		_, err := m.MigrateUp(-1)
		require.NoError(t, err)
		require.NoError(t, m.Finish())
		assert.True(t, indexValid(t, db, "t_a_idx"))
	})

	t.Run("steps select the first pending migrations by version", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		m := newMigrator(dbURL, writeMigrations(t, createT, indexA, createU), nil)

		n, err := m.MigrateUp(2)
		require.NoError(t, err)
		assert.Equal(t, 2, n)
		require.NoError(t, m.Finish())
		assert.True(t, indexValid(t, db, "t_a_idx"))
		assert.False(t, relationExists(t, db, "u"))
		assert.Equal(t, []string{createT.name, indexA.name}, recorded(t, db))
	})

	t.Run("a failed no-transaction migration leaves the transactional ones committed and itself unrecorded", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		broken := mig{"1000000002_index_missing", noTx + "create index concurrently t_x_idx on t (missing);", ""}
		m := newMigrator(dbURL, writeMigrations(t, createT, broken, createU), nil)

		_, err := m.MigrateUp(-1)
		assert.ErrorContains(t, err, "1000000002_index_missing.up.sql")
		assert.True(t, relationExists(t, db, "t"))
		assert.True(t, relationExists(t, db, "u"))
		assert.Equal(t, []string{createT.name, createU.name}, recorded(t, db))
		assert.NoError(t, m.Finish())
	})

	t.Run("refuses without AllowNoTransaction before running anything", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		m := newMigrator(dbURL, writeMigrations(t, createT, indexA), func(c *pqmigrate.Config) { c.AllowNoTransaction = false })

		_, err := m.MigrateUp(-1)
		assert.ErrorContains(t, err, "1000000002_index_a.up.sql")
		assert.ErrorContains(t, err, "AllowNoTransaction")
		assert.NoError(t, m.Finish())
		assert.False(t, relationExists(t, db, "t"))
		assert.Empty(t, recorded(t, db))
	})

	t.Run("runs without AllowNoTransaction when the no-transaction migration is outside the steps", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		m := newMigrator(dbURL, writeMigrations(t, createT, indexA), func(c *pqmigrate.Config) { c.AllowNoTransaction = false })

		n, err := m.MigrateUp(1)
		require.NoError(t, err)
		assert.Equal(t, 1, n)
		require.NoError(t, m.Finish())
		assert.Equal(t, []string{createT.name}, recorded(t, db))
	})

	t.Run("refuses a dry run before running anything", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		m := newMigrator(dbURL, writeMigrations(t, createT, indexA), func(c *pqmigrate.Config) { c.DryRun = true })

		_, err := m.MigrateUp(-1)
		assert.ErrorContains(t, err, "dry-run")
		assert.NoError(t, m.Finish())
		assert.False(t, relationExists(t, db, "t"))
		assert.Empty(t, recorded(t, db))
	})
}

func TestMigrateDownNoTransaction(t *testing.T) {
	t.Run("runs no-transaction downs before transactional ones", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		dir := writeMigrations(t, createT, indexA, createU)
		up := newMigrator(dbURL, dir, nil)
		_, err := up.MigrateUp(-1)
		require.NoError(t, err)
		require.NoError(t, up.Finish())

		down := newMigrator(dbURL, dir, nil)
		n, err := down.MigrateDown(1)
		require.NoError(t, err)
		assert.Equal(t, 1, n)
		require.NoError(t, down.Finish())
		assert.False(t, relationExists(t, db, "u"))
		assert.True(t, relationExists(t, db, "t_a_idx"))

		up = newMigrator(dbURL, dir, nil)
		_, err = up.MigrateUp(-1)
		require.NoError(t, err)
		require.NoError(t, up.Finish())

		down = newMigrator(dbURL, dir, nil)

		n, err = down.MigrateDown(-1)
		require.NoError(t, err, "drop index concurrently must run before drop table t")
		assert.Equal(t, 3, n)
		require.NoError(t, down.Finish())
		assert.False(t, relationExists(t, db, "t"))
		assert.False(t, relationExists(t, db, "u"))
		assert.Empty(t, recorded(t, db))
	})

	t.Run("refuses a no-transaction down without AllowNoTransaction", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		dir := writeMigrations(t, createT, indexA)
		up := newMigrator(dbURL, dir, nil)
		_, err := up.MigrateUp(-1)
		require.NoError(t, err)
		require.NoError(t, up.Finish())

		down := newMigrator(dbURL, dir, func(c *pqmigrate.Config) { c.AllowNoTransaction = false })
		_, err = down.MigrateDown(-1)
		assert.ErrorContains(t, err, "1000000002_index_a.down.sql")
		assert.NoError(t, down.Finish())
		assert.Equal(t, []string{createT.name, indexA.name}, recorded(t, db))
	})
}

func TestNoTransactionFailure(t *testing.T) {
	t.Run("a failing no-transaction migration after a successful one leaves only the first recorded", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		indexB := mig{"1000000003_index_b", noTx + "create index concurrently t_b_idx on t (nosuchcolumn);", noTx + "drop index concurrently t_b_idx;"}
		m := newMigrator(dbURL, writeMigrations(t, createT, indexA, indexB), nil)

		_, err := m.MigrateUp(-1)
		assert.ErrorContains(t, err, "1000000003_index_b.up.sql")
		assert.NoError(t, m.Finish())
		assert.True(t, indexValid(t, db, "t_a_idx"))
		assert.False(t, relationExists(t, db, "t_b_idx"))
		assert.Equal(t, []string{createT.name, indexA.name}, recorded(t, db))
	})
}

func TestMigrateFileNoTransaction(t *testing.T) {
	t.Run("commits the open transaction and runs a no-transaction up file", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		m := newMigrator(dbURL, writeMigrations(t, createT, indexA), nil)

		require.NoError(t, m.MigrateUpFile(createT.name+".up.sql"))
		assert.False(t, relationExists(t, db, "t"))
		require.NoError(t, m.MigrateUpFile(indexA.name+".up.sql"))
		assert.True(t, indexValid(t, db, "t_a_idx"))
		assert.Equal(t, []string{createT.name, indexA.name}, recorded(t, db))
		require.NoError(t, m.Finish())
	})

	t.Run("runs a no-transaction down file", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		m := newMigrator(dbURL, writeMigrations(t, createT, indexA), nil)
		_, err := m.MigrateUp(-1)
		require.NoError(t, err)

		require.NoError(t, m.MigrateDownFile(indexA.name+".down.sql"))
		require.NoError(t, m.Finish())
		assert.False(t, relationExists(t, db, "t_a_idx"))
		assert.Equal(t, []string{createT.name}, recorded(t, db))
	})

	t.Run("refuses a no-transaction file without AllowNoTransaction", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		m := newMigrator(dbURL, writeMigrations(t, createT, indexA), func(c *pqmigrate.Config) { c.AllowNoTransaction = false })

		assert.ErrorContains(t, m.MigrateUpFile(indexA.name+".up.sql"), "AllowNoTransaction")
		assert.NoError(t, m.Finish())
		assert.Empty(t, recorded(t, db))
	})
}

func TestLockKey(t *testing.T) {
	result := func(m *pqmigrate.PQMigrate) <-chan error {
		done := make(chan error, 1)
		go func() {
			_, err := m.MigrateUp(-1)
			if finishErr := m.Finish(); err == nil {
				err = finishErr
			}
			done <- err
		}()
		return done
	}

	t.Run("a second migrator waits until the first has committed", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		dir := writeMigrations(t, createT, createU)
		key := randomLockKey(t)
		lockKey := func(c *pqmigrate.Config) { c.LockKey = key }

		first := newMigrator(dbURL, dir, lockKey)
		n, err := first.MigrateUp(-1)
		require.NoError(t, err)
		assert.Equal(t, 2, n)

		second := result(newMigrator(dbURL, dir, lockKey))
		select {
		case err := <-second:
			t.Fatalf("second migrator finished while the lock was held: %v", err)
		case <-time.After(1500 * time.Millisecond):
		}

		require.NoError(t, first.Finish())
		select {
		case err := <-second:
			assert.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("second migrator did not finish after the lock was released")
		}
		assert.Equal(t, []string{createT.name, createU.name}, recorded(t, db))
	})

	t.Run("a waiting migrator does not block create index concurrently in the lock holder", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		dir := writeMigrations(t, createT, indexA)
		key := randomLockKey(t)
		lockKey := func(c *pqmigrate.Config) { c.LockKey = key }

		first := newMigrator(dbURL, dir, lockKey)
		require.NoError(t, first.MigrateUpFile(createT.name+".up.sql"))

		second := result(newMigrator(dbURL, dir, lockKey))
		time.Sleep(time.Second)

		indexed := make(chan error, 1)
		go func() { indexed <- first.MigrateUpFile(indexA.name + ".up.sql") }()
		select {
		case err := <-indexed:
			require.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("create index concurrently hung while another migrator waited for the lock")
		}
		require.NoError(t, first.Finish())

		select {
		case err := <-second:
			assert.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("second migrator did not finish after the lock was released")
		}
		assert.True(t, indexValid(t, db, "t_a_idx"))
		assert.Equal(t, []string{createT.name, indexA.name}, recorded(t, db))
	})

	t.Run("is released at the end of a call that left nothing open", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		key := randomLockKey(t)
		m := newMigrator(dbURL, writeMigrations(t, createT, indexA), func(c *pqmigrate.Config) { c.LockKey = key })

		_, err := m.MigrateUp(-1)
		require.NoError(t, err)

		conn, err := db.Conn(context.Background())
		require.NoError(t, err)
		defer conn.Close()
		var locked bool
		require.NoError(t, conn.QueryRowContext(context.Background(), `select pg_try_advisory_lock($1)`, key).Scan(&locked))
		assert.True(t, locked)
		_, err = conn.ExecContext(context.Background(), `select pg_advisory_unlock($1)`, key)
		require.NoError(t, err)
		assert.NoError(t, m.Finish())
	})
}

func TestAfterTxCommit(t *testing.T) {
	run := func(t *testing.T, migs ...mig) (calls int, sawT bool) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		m := newMigrator(dbURL, writeMigrations(t, migs...), func(c *pqmigrate.Config) {
			c.AfterTxCommit = func() {
				calls++
				sawT = relationExists(t, db, "t")
			}
		})
		_, err := m.MigrateUp(-1)
		require.NoError(t, err)
		require.NoError(t, m.Finish())
		return calls, sawT
	}

	t.Run("is called once, after the transactional part is committed and before the no-transaction part", func(t *testing.T) {
		calls, sawT := run(t, createT, indexA)
		assert.Equal(t, 1, calls)
		assert.True(t, sawT)
	})

	t.Run("is not called without a no-transaction migration", func(t *testing.T) {
		calls, _ := run(t, createT, createU)
		assert.Equal(t, 0, calls)
	})

	t.Run("is not called without a transactional migration", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		_, err := db.Exec("create table t (a int, b int)")
		require.NoError(t, err)
		calls := 0
		m := newMigrator(dbURL, writeMigrations(t, indexA), func(c *pqmigrate.Config) { c.AfterTxCommit = func() { calls++ } })
		_, err = m.MigrateUp(-1)
		require.NoError(t, err)
		require.NoError(t, m.Finish())
		assert.Equal(t, 0, calls)
	})
}
