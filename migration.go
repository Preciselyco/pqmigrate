package pqmigrate

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type migration struct {
	Version uint64 `db:"version"`
	Name    string `db:"name"`
	Up      string `db:"up"`
	Down    string `db:"down"`
}

// noTxDirective marks a migration file that must run outside a transaction
// when it is the file's first line.
const noTxDirective = "-- pqmigrate:no-transaction"

// hasNoTxDirective reports whether contents starts with the no-transaction
// directive line. Trailing whitespace on that line is allowed.
func hasNoTxDirective(contents string) bool {
	firstLine := contents
	if i := strings.IndexByte(contents, '\n'); i >= 0 {
		firstLine = contents[:i]
	}
	return strings.TrimRight(firstLine, " \t\r") == noTxDirective
}

// noTx reports whether the migration runs outside a transaction in the given
// direction. It is derived from the migration text, so it works the same for
// migrations loaded from disk, from a squash file and from the migrations
// table (which stores the text including the directive).
func (m *migration) noTx(md migrateDirection) bool {
	if md == migrateDown {
		return hasNoTxDirective(m.Down)
	}
	return hasNoTxDirective(m.Up)
}

func (m *migration) fileName(md migrateDirection) string {
	return m.Name + "." + string(md) + ".sql"
}

// migrationSplitNoTx splits migs, keeping their order, into the ones that run
// in a transaction and the ones that run outside one in direction md.
func migrationSplitNoTx(migs []*migration, md migrateDirection) (tx, noTx []*migration) {
	for _, m := range migs {
		if m.noTx(md) {
			noTx = append(noTx, m)
		} else {
			tx = append(tx, m)
		}
	}
	return tx, noTx
}

// migrationCheckNoTx returns an error naming the first migration in migs that
// would run outside a transaction in direction md when that is not allowed:
// Config.AllowNoTransaction is off, or Config.DryRun is on (a statement run
// outside a transaction cannot be rolled back).
func (ctx *PQMigrate) migrationCheckNoTx(migs []*migration, md migrateDirection) error {
	for _, m := range migs {
		if !m.noTx(md) {
			continue
		}
		if !ctx.config.AllowNoTransaction {
			return fmt.Errorf("migration %s is marked %q but Config.AllowNoTransaction is false", m.fileName(md), noTxDirective)
		}
		if ctx.config.DryRun {
			return fmt.Errorf("migration %s is marked %q and cannot be dry-run", m.fileName(md), noTxDirective)
		}
	}
	return nil
}

var (
	reMigrationName   = regexp.MustCompilePOSIX(`^[a-z0-9][a-z0-9_]+$`)
	migrationFileRegx = regexp.MustCompilePOSIX(`^[0-9]{10}[^.]+\.(up|down).sql$`)
	migrationRegx     = regexp.MustCompilePOSIX(`^([0-9]{10}[^.]+)`)
	squashFileRegx    = regexp.MustCompilePOSIX(squashFileName + "$")
)

type byVersion []*migration

func (a byVersion) Len() int           { return len(a) }
func (a byVersion) Less(i, j int) bool { return a[i].Version < a[j].Version }
func (a byVersion) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

type byVersionReversed []*migration

func (a byVersionReversed) Len() int           { return len(a) }
func (a byVersionReversed) Less(i, j int) bool { return a[j].Version < a[i].Version }
func (a byVersionReversed) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

func (ctx *PQMigrate) migrationGetVersion(fileName string) (uint64, error) {
	ctx.dbg("migrationGetVersion", fileName)
	fs := strings.Split(fileName, "_")
	return strconv.ParseUint(fs[0], 10, 64)
}

var errNotMigrationFile = fmt.Errorf("not a migration file")

func (ctx *PQMigrate) migrationSquash(m *migration) *migration {
	return &migration{
		Version: m.Version,
		Name:    m.Name,
		Up:      base64.StdEncoding.EncodeToString([]byte(m.Up)),
		Down:    base64.StdEncoding.EncodeToString([]byte(m.Down)),
	}
}

func (ctx *PQMigrate) migrationSquashAll(migrations []*migration) ([]byte, []string, error) {
	b := make([]byte, 0)
	buf := bytes.NewBuffer(b)
	migrationFileNames := make([]string, 0)
	for _, mig := range migrations {
		nMig := ctx.migrationSquash(mig)
		buf.WriteString(nMig.Name)
		buf.WriteString(squashSep)
		buf.WriteString(nMig.Up)
		buf.WriteString(squashSep)
		buf.WriteString(nMig.Down)
		buf.WriteString(squashLineSep)
		migrationFileNames = append(migrationFileNames, nMig.Name+".down.sql")
		migrationFileNames = append(migrationFileNames, nMig.Name+".up.sql")
	}
	return buf.Bytes(), migrationFileNames, nil
}

func (ctx *PQMigrate) migrationGetSquashed(line string) (*migration, error) {
	migration := &migration{}
	contents := strings.Split(line, squashSep)
	if len(contents) != 3 {
		ctx.dbg("UnSquash", "wrong number of fields")
		return nil, fmt.Errorf("corrupt squash file")
	}
	migration.Name = contents[0]
	upBytes, err := base64.StdEncoding.DecodeString(contents[1])
	if err != nil {
		ctx.dbg("UnSquash", err)
		return nil, fmt.Errorf("corrupt squash file")
	}
	downBytes, err := base64.StdEncoding.DecodeString(contents[2])
	if err != nil {
		return nil, fmt.Errorf("corrupt squash file")
	}
	migration.Up = string(upBytes)
	migration.Down = string(downBytes)
	version, err := ctx.migrationGetVersion(migration.Name)
	if err != nil {
		return nil, fmt.Errorf("corrupt squash file")
	}
	migration.Version = version
	return migration, nil
}

func (ctx *PQMigrate) migrationGetAllSquashed(fileName string) ([]*migration, error) {
	migrations := make([]*migration, 0)
	fc, err := ctx.fileGetContents(fileName)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(fc, squashLineSep) {
		if line == "" {
			continue
		}
		m, err := ctx.migrationGetSquashed(line)
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, m)
	}
	return migrations, nil
}

func (ctx *PQMigrate) migrationGetSpecific(fileName string) (*migration, error) {
	ctx.dbg("migrationGetSpecific", fileName)
	sm := migrationFileRegx.FindStringSubmatch(fileName)
	if len(sm) != 2 {
		return nil, errNotMigrationFile
	}
	ctx.dbg("migrationGetSpecific", sm)
	direction := sm[1]
	version, err := ctx.migrationGetVersion(fileName)
	if err != nil {
		ctx.dbg("migrationGetSpecific", err)
		return nil, err
	}
	var upContents string
	var downContents string
	var upFileName string
	var downFileName string
	migrationName := migrationRegx.FindString(fileName)
	if migrationName == "" {
		return nil, errNotMigrationFile
	}
	if direction == "up" {
		upFileName = fileName
		downFileName = migrationName + ".down.sql"
	} else {
		downFileName = fileName
		upFileName = migrationName + ".up.sql"
	}
	upContents, err = ctx.fileGetContents(upFileName)
	if err != nil {
		return nil, err
	}
	downContents, err = ctx.fileGetContents(downFileName)
	if err != nil {
		return nil, err
	}
	return &migration{
		Name:    migrationName,
		Version: version,
		Up:      upContents,
		Down:    downContents,
	}, nil
}

func (ctx *PQMigrate) migrationGetAll() ([]*migration, error) {
	ctx.dbg("migrationGetAll")
	files, err := ctx.config.readDir()
	if err != nil {
		ctx.dbg("migrationGetAll", err)
		return nil, err
	}
	migMap := map[uint64]*migration{}
	for _, fo := range files {
		if fo.IsDir() {
			continue
		}
		if squashFileRegx.MatchString(fo.Name()) {
			migrations, err := ctx.migrationGetAllSquashed(fo.Name())
			if err != nil {
				return nil, err
			}
			for _, migration := range migrations {
				migMap[migration.Version] = migration
			}
			continue
		}
		sm := migrationFileRegx.FindStringSubmatch(fo.Name())
		if len(sm) != 2 {
			continue
		}
		ctx.dbg("migrationGetAll", sm)
		version, err := ctx.migrationGetVersion(fo.Name())
		if err != nil {
			ctx.dbg("migrationGetAll", err)
			return nil, err
		}
		if _, found := migMap[version]; found {
			continue
		}
		m, err := ctx.migrationGetSpecific(fo.Name())
		if err != nil {
			return nil, err
		}
		migMap[m.Version] = m
	}
	migrations := make([]*migration, 0)
	for _, m := range migMap {
		migrations = append(migrations, m)
	}
	ctx.dbg("migrationGetAll", "sorting migrations")
	sort.Sort(byVersion(migrations))
	return migrations, nil
}

func (ctx *PQMigrate) migrationSuperSet(vs1, vs2 []*migration) (r1 []*migration) {
	ctx.dbg("migrationSuperSet")
	for _, i := range vs1 {
		found := false
		for _, j := range vs2 {
			if i.Version == j.Version {
				found = true
				continue
			}
		}
		if !found {
			r1 = append(r1, i)
		}
	}
	return
}

func (ctx *PQMigrate) migrationCreate(name string, noTx bool) error {
	ctx.dbg("migrationCreate", name, noTx)
	if ok := reMigrationName.MatchString(name); !ok {
		return fmt.Errorf("invalid migration name, must match the regexp: ^[a-z0-9_]+$")
	}
	epoch := strconv.FormatInt(time.Now().Unix(), 10)
	down := epoch + "_" + name + ".down.sql"
	up := epoch + "_" + name + ".up.sql"
	contents := []byte("")
	if noTx {
		contents = []byte(noTxDirective + "\n")
	}
	ctx.logger.Print(fmt.Sprintf("creating %s", down))
	if err := ctx.fileWriteContents(down, contents); err != nil {
		return err
	}
	ctx.logger.Print(fmt.Sprintf("creating %s", up))
	return ctx.fileWriteContents(up, contents)
}

type migrationSet struct {
	items map[uint64]*migration
}

func newMigrationSet() *migrationSet {
	return &migrationSet{
		items: make(map[uint64]*migration),
	}
}

func (s *migrationSet) add(items ...*migration) {
	for _, m := range items {
		s.items[m.Version] = m
	}
}

func (s *migrationSet) rem(items ...*migration) {
	for _, m := range items {
		delete(s.items, m.Version)
	}
}

func (s *migrationSet) has(item *migration) bool {
	_, found := s.items[item.Version]
	return found
}

func (s *migrationSet) itemsSlice() []*migration {
	rs := make([]*migration, 0)
	for _, m := range s.items {
		rs = append(rs, m)
	}
	sort.Sort(byVersion(rs))
	return rs
}

func migrationSliceIntersection(a, b []*migration) []*migration {
	sa := newMigrationSet()
	sb := newMigrationSet()
	sa.add(a...)
	for _, m := range b {
		if sa.has(m) {
			sb.add(m)
		}
	}
	items := sb.itemsSlice()
	sort.Sort(byVersion(items))
	return items
}

func migrationSliceUnion(a, b []*migration) []*migration {
	s := newMigrationSet()
	s.add(a...)
	s.add(b...)
	items := s.itemsSlice()
	sort.Sort(byVersion(items))
	return items
}

func migrationSliceDifference(a, b []*migration) []*migration {
	s := newMigrationSet()
	s.add(a...)
	s.rem(b...)
	items := s.itemsSlice()
	sort.Sort(byVersion(items))
	return items
}

func migrationSliceSymmetricDifference(a, b []*migration) []*migration {
	return migrationSliceUnion(migrationSliceDifference(a, b), migrationSliceDifference(b, a))
}
