package pqmigrate

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// Config options for the library
type Config struct {
	AllInOneTx      bool      // Perform all database operations in the same transaction
	FS              *embed.FS // Support for embedded migrations in application
	BaseDirectory   string    // Directory where the sql files are stored
	DBUrl           string    // Postgresql url `psql://<user>:<pwd>@<host>:<port>/<db_name>`
	Logger          Logger    // If set the logger will be used instead of standard out
	MigrationsTable string    // Name of migrations table in database
	Debug           bool      // Show debug info
	DryRun          bool      // Perform all database operations but don't commit

	// AllowNoTransaction permits migrations whose first line is
	// `-- pqmigrate:no-transaction`. Such a migration runs on its own
	// connection outside any transaction, after the transactional migrations
	// of the same call have been committed (see README). When false, a call
	// that would run one fails before running anything. DryRun with a
	// no-transaction migration in the run always fails, as it can't be
	// rolled back.
	AllowNoTransaction bool

	// AfterTxCommit, when non-nil, is called by MigrateUp and Sync once the
	// transactional migrations of the run have been committed and before the
	// deferred no-transaction ones start. It is called only when the run has
	// both kinds (otherwise the caller's own code after the call, or Finish,
	// is the point right after the commit), and not on dry runs. Use it for
	// work that must follow the schema change without waiting for a long
	// no-transaction build, such as telling a connection pooler to reconnect.
	AfterTxCommit func()

	// LockKey, when non-zero, serializes migrators on a session-level
	// Postgres advisory lock with this key. MigrateUp, MigrateDown,
	// MigrateUpFile, MigrateDownFile and Sync take it before reading migration
	// state (waiting, by polling, while another session holds it) and hold it
	// through the call, including its no-transaction migrations. When the call
	// returns with the transaction still open (AllInOneTx, or an error left it
	// open) the lock is held until Finish has committed it, so the next
	// migrator sees the recorded rows; otherwise it is released before the
	// call returns. Always call Finish. 0 disables locking.
	//
	// The wait has no timeout, and the lock is tied to one database session:
	// DBUrl must point at Postgres directly or at a session-pooled pooler.
	// Behind transaction pooling (e.g. PgBouncer pool_mode=transaction) the
	// lock statements can run on different server connections, so the lock
	// does not exclude anyone and can leak; leave LockKey at 0 there.
	LockKey int64
}

func (c Config) filePath(fileName string) string {
	if c.FS != nil {
		return fileName
	}

	return filepath.Join(c.BaseDirectory, fileName)
}

func (c Config) fileContents(fp string) ([]byte, error) {
	if c.FS != nil {
		return c.FS.ReadFile(fp)
	}
	return os.ReadFile(fp)
}

func (c Config) readDir() ([]fs.DirEntry, error) {
	if c.FS != nil {
		return c.FS.ReadDir(".")
	}

	return os.ReadDir(c.BaseDirectory)
}
