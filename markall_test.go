package pqmigrate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Preciselyco/pqmigrate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const latestSchema = `create table pqmigrate (
	version bigint not null primary key,
	name text not null default '',
	up text not null default '',
	down text not null default ''
);
create table t (a int, b int);
create index t_a_idx on t (a);
create table u (a int);`

func writeSchema(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schema.sql"), []byte(latestSchema), 0o644))
}

func TestMarkAllMigrated(t *testing.T) {
	t.Run("records every migration without running it, in the schema load's transaction", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		dir := writeMigrations(t, createU, createT, indexA)
		writeSchema(t, dir)
		m := newMigrator(dbURL, dir, nil)

		require.NoError(t, m.LoadDBSchema("schema.sql", nil))
		n, err := m.MarkAllMigrated()
		require.NoError(t, err)
		assert.Equal(t, 3, n)
		assert.False(t, relationExists(t, db, "t"), "nothing is committed before Finish")

		require.NoError(t, m.Finish())
		assert.Equal(t, []string{createT.name, indexA.name, createU.name}, recorded(t, db))

		var up, down string
		require.NoError(t, db.QueryRow(`select up, down from pqmigrate where name = $1`, indexA.name).Scan(&up, &down))
		assert.Equal(t, indexA.up, up)
		assert.Equal(t, indexA.down, down)
	})

	t.Run("leaves nothing to migrate up afterwards", func(t *testing.T) {
		dbURL := newDB(t)
		dir := writeMigrations(t, createT, indexA, createU)
		writeSchema(t, dir)
		m := newMigrator(dbURL, dir, nil)
		require.NoError(t, m.LoadDBSchema("schema.sql", nil))
		_, err := m.MarkAllMigrated()
		require.NoError(t, err)
		require.NoError(t, m.Finish())

		up := newMigrator(dbURL, dir, nil)
		n, err := up.MigrateUp(-1)
		require.NoError(t, err)
		assert.Equal(t, 0, n)
		require.NoError(t, up.Finish())
	})

	t.Run("a later migration runs on top of the marked ones", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		dir := writeMigrations(t, createT, indexA, createU)
		writeSchema(t, dir)
		m := newMigrator(dbURL, dir, nil)
		require.NoError(t, m.LoadDBSchema("schema.sql", nil))
		_, err := m.MarkAllMigrated()
		require.NoError(t, err)
		require.NoError(t, m.Finish())

		createV := mig{"1000000004_create_v", "create table v (a int);", "drop table v;"}
		require.NoError(t, os.WriteFile(filepath.Join(dir, createV.name+".up.sql"), []byte(createV.up), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, createV.name+".down.sql"), []byte(createV.down), 0o644))
		up := newMigrator(dbURL, dir, nil)
		n, err := up.MigrateUp(-1)
		require.NoError(t, err)
		assert.Equal(t, 1, n)
		require.NoError(t, up.Finish())
		assert.True(t, relationExists(t, db, "v"))
		assert.Equal(t, []string{createT.name, indexA.name, createU.name, createV.name}, recorded(t, db))
	})

	t.Run("commits on its own without all in one transaction", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		m := newMigrator(dbURL, writeMigrations(t, createT, createU), func(c *pqmigrate.Config) { c.AllInOneTx = false })

		n, err := m.MarkAllMigrated()
		require.NoError(t, err)
		assert.Equal(t, 2, n)
		assert.Equal(t, []string{createT.name, createU.name}, recorded(t, db))
		require.NoError(t, m.Finish())
	})

	t.Run("refuses a database that already records migrations", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		first := newMigrator(dbURL, writeMigrations(t, createT), nil)
		_, err := first.MigrateUp(-1)
		require.NoError(t, err)
		require.NoError(t, first.Finish())

		m := newMigrator(dbURL, writeMigrations(t, createT, createU), nil)
		_, err = m.MarkAllMigrated()
		assert.ErrorContains(t, err, "already records 1 migration(s)")
		_ = m.Finish()
		assert.Equal(t, []string{createT.name}, recorded(t, db))
		assert.False(t, relationExists(t, db, "u"))
	})

	t.Run("dry run records nothing", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		m := newMigrator(dbURL, writeMigrations(t, createT, createU), func(c *pqmigrate.Config) { c.DryRun = true })

		n, err := m.MarkAllMigrated()
		require.NoError(t, err)
		assert.Equal(t, 2, n)
		require.NoError(t, m.Finish())
		assert.Empty(t, recorded(t, db))
	})

	t.Run("holds the migration lock until Finish with all in one transaction", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		key := randomLockKey(t)
		m := newMigrator(dbURL, writeMigrations(t, createT), func(c *pqmigrate.Config) { c.LockKey = key })

		_, err := m.MarkAllMigrated()
		require.NoError(t, err)
		var free bool
		require.NoError(t, db.QueryRow(`select pg_try_advisory_lock($1)`, key).Scan(&free))
		assert.False(t, free)

		require.NoError(t, m.Finish())
		require.NoError(t, db.QueryRow(`select pg_try_advisory_lock($1)`, key).Scan(&free))
		assert.True(t, free)
		_, err = db.Exec(`select pg_advisory_unlock($1)`, key)
		require.NoError(t, err)
	})
}

func TestMigrateUpAfterDeletingMarkedMigrations(t *testing.T) {
	t.Run("ignores recorded migrations whose files are gone and runs only the new ones", func(t *testing.T) {
		dbURL := newDB(t)
		db := openDB(t, dbURL)
		dir := writeMigrations(t, createT, indexA, createU)
		writeSchema(t, dir)
		m := newMigrator(dbURL, dir, nil)
		require.NoError(t, m.LoadDBSchema("schema.sql", nil))
		_, err := m.MarkAllMigrated()
		require.NoError(t, err)
		require.NoError(t, m.Finish())

		createV := mig{"1000000004_create_v", "create table v (a int);", "drop table v;"}
		pruned := writeMigrations(t, createU, createV)
		up := newMigrator(dbURL, pruned, nil)
		n, err := up.MigrateUp(-1)
		require.NoError(t, err)
		assert.Equal(t, 1, n)
		require.NoError(t, up.Finish())
		assert.True(t, relationExists(t, db, "v"))
		assert.Equal(t, []string{createT.name, indexA.name, createU.name, createV.name}, recorded(t, db))
	})
}
