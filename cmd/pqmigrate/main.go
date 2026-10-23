package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Preciselyco/pqmigrate"
	"github.com/docopt/docopt-go"
	"github.com/fatih/color"
	"github.com/joho/godotenv"
)

const (
	version = "v1.5.0"
)

var arguments = map[string]interface{}{}
var logger = newCmdLogger()
var bw = false

const (
	argURL        = "--url"
	argDIR        = "--dir"
	argName       = "--name"
	argSteps      = "--steps"
	argBW         = "--bw"
	argDryRun     = "-d"
	argNoTx       = "--no-tx"
	argHelpConfig = "--help-config"
	argVersion    = "--version"

	confirmY       = "y"
	confirmPainful = "yes-i-am-really-really-sure"
)

var cmds = map[string]struct {
	f func() error
	d string
}{
	"create-db":   {f: createDbCMD, d: "initializing db"},
	"drop-db":     {f: dropDbCMD, d: "dropping database"},
	"up":          {f: upCMD, d: "migrating up"},
	"down":        {f: downCMD, d: "migrating down"},
	"sync":        {f: syncCMD, d: "syncing database and filesystem"},
	"replace":     {f: replaceCMD, d: "replacing migration"},
	"create":      {f: createCMD, d: "creating migration files"},
	"dump-schema": {f: dumpSchemaCMD, d: "dumping database schema"},
	"dump-full":   {f: dumpFullCMD, d: "dumping database and content"},
	"load-schema": {f: loadSchemaCMD, d: "loading database schema"},
	"load-dump":   {f: loadDumpCMD, d: "loading database dump"},
	"seed":        {f: seedCMD, d: "seeding database"},
	"squash":      {f: squashCMD, d: "squashing migrations"},
	"unsquash":    {f: unSquashCMD, d: "unsquashing migrations"},
}

func main() {
	usage := `pqmigrate

Usage:
  pqmigrate create-db [--url=<url>] [--bw]
  pqmigrate drop-db [--url=<url>] [--bw]
  pqmigrate up [--url=<url>] [--dir=<dir>] [--steps=<steps>] [--bw] [-d] [--name=<name>]
  pqmigrate down [--url=<url>] [--dir=<dir>] [--steps=<steps>] [--bw] [-d] [--name=<name>]
  pqmigrate sync [--url=<url>] [--dir=<dir>] [--bw] [-d]
  pqmigrate replace [--url=<url>] [--dir=<dir>] [--bw] [-d] --name=<name>
  pqmigrate create <name> [--dir=<dir>] [--bw] [-d] [--no-tx]
  pqmigrate dump-schema [--dir=<dir>] [--name=<name>] [--bw]
  pqmigrate dump-full [--dir=<dir>] [--name=<name>] [--bw]
  pqmigrate load-schema [--dir=<dir>] [--name=<name>] [--bw] [-d]
  pqmigrate load-dump <name> [--dir=<dir>] [--name=<name>] [--bw] [-d]
  pqmigrate seed [--dir=<dir>] [--name=<name>] [--bw] [-d]
  pqmigrate squash [--dir=<dir>] [--bw]
  pqmigrate unsquash [--dir=<dir>] [--bw]
  pqmigrate -h | --help
  pqmigrate --help-config
  pqmigrate --version

Options:
  -h --help        Show help.
  --help-config	   Show configuration options. [default: false]
  --version        Show version. [default: false]
  --url=<url>      Postgres url
  --dir=<dir>      Directory where migrations files are stores. [default: migrations/]
  --steps=<steps>  Max steps to migrate [default: 1].
  --bw             No colour (black and white). [default false]
  -d               Dry run, test migrations but rollback changes. [default: false]
  --no-tx          Create a migration that runs outside a transaction. [default: false]
`
	configOptions := `
Configuration

pqmigrate supports loading environment variables from a file named ".env" in the directory
where the command is run. 

Supported environment keys:
 PQM_ALL_IN_ONE_TX: Bool. If specified all db operations will be performed in a single transaction.
 PQM_BASE_DIRECTORY: Relative path to directory containing migrations. Defaults to "migrations".
 PQM_DATABASE_URL: String on format "psql://<username>:<password>@<host>[:<port>]/<database>"
 PQM_DEBUG: Bool. Defaults to false.
 PQM_MIGRATIONS_TABLE: String. Name of migrations table in the database. Defaults to "pqmigrate".
`
	arguments, _ = docopt.ParseDoc(usage)
	bw = arguments[argBW].(bool)
	if arguments[argHelpConfig].(bool) {
		fmt.Println(configOptions)
		return
	}
	godotenv.Load()
	if v, ok := arguments[argVersion].(bool); ok && v {
		logger.Ok(fmt.Sprintf("Version: %s", version))
		return
	}
	for k, cmd := range cmds {
		if c, ok := arguments[k].(bool); c && ok {
			if len(cmd.d) > 0 {
				logger.Inf(cmd.d + "...")
			}
			if err := cmd.f(); err != nil {
				logger.Error(err)
				os.Exit(1)
			}
		}
	}
	logger.Ok("done.")
}

type cmdLogger struct{}

func newCmdLogger() *cmdLogger {
	return &cmdLogger{}
}

func (l *cmdLogger) Printf(format string, args ...interface{}) {
	fmt.Printf(format, args...)
}

func (l *cmdLogger) printFEach(c func(string, ...interface{}), format string, args ...interface{}) {
	for _, v := range args {
		if bw || c == nil {
			l.Printf(format, v)
		} else {
			c(format, v)
		}
	}
}

func (l *cmdLogger) Print(args ...interface{}) {
	l.printFEach(nil, "%v\n", args...)
}

func (l *cmdLogger) Error(args ...interface{}) {
	l.printFEach(color.Red, "ERROR: %v\n", args...)
}

func (l *cmdLogger) Warn(args ...interface{}) {
	l.printFEach(color.Yellow, "%v\n", args...)
}

func (l *cmdLogger) Inf(args ...interface{}) {
	l.printFEach(color.Cyan, "%v\n", args...)
}

func (l *cmdLogger) Ok(args ...interface{}) {
	l.printFEach(color.Green, "%v\n", args...)
}

func (l *cmdLogger) DBG(args ...interface{}) {
	if os.Getenv("PQM_DEBUG") == "true" {
		l.printFEach(color.Magenta, "DBG: %v\n", args...)
	}
}

func getSteps() int {
	if sVal, ok := arguments[argSteps].(string); ok {
		steps, err := strconv.Atoi(sVal)
		if err != nil {
			// gratuitous goto
			goto return_one
		}
		return steps
	}
return_one:
	return 1
}

func getEnvOrDefaultBool(envKey string, def bool) bool {
	v, err := strconv.ParseBool(os.Getenv(envKey))
	if err != nil {
		return def
	}
	return v
}

func confirmCB(expected string, simple bool) func(prompt string) bool {
	return func(prompt string) bool {
		reader := bufio.NewReader(os.Stdin)
		replacement := ""
		replacer := strings.NewReplacer(
			"\r\n", replacement,
			"\r", replacement,
			"\n", replacement,
			"\v", replacement,
			"\f", replacement,
			"\u0085", replacement,
			"\u2028", replacement,
			"\u2029", replacement,
		)
		if simple {
			logger.Warn(fmt.Sprintf("%s: [y/N] ", prompt))
			text, _ := reader.ReadString('\n')
			if text == "" {
				return false
			}
			return replacer.Replace(strings.ToLower(text)) == expected
		}
		logger.Warn(fmt.Sprintf("%s: [%s/N] ", prompt, expected))
		text, _ := reader.ReadString('\n')
		return replacer.Replace(text) == expected
	}
}

func getConfig() (pqmigrate.Config, error) {
	logger.DBG(fmt.Sprintf("%+v", arguments))
	cfg := pqmigrate.Config{}
	cfg.DBUrl = os.Getenv("PQM_DATABASE_URL")
	if u, ok := arguments[argURL].(string); ok {
		cfg.DBUrl = u
	}
	if cfg.DBUrl == "" {
		return cfg, fmt.Errorf("no database url provided")
	}
	cfg.BaseDirectory = os.Getenv("PQM_BASE_DIRECTORY")
	if d, ok := arguments[argDIR].(string); ok {
		var fullDir string
		var err error
		fullDir, err = filepath.Abs(d)
		if err != nil {
			return cfg, fmt.Errorf("could not get full path dir: %v", err)
		}
		cfg.BaseDirectory = fullDir
	}
	cfg.Debug = getEnvOrDefaultBool("PQM_DEBUG", false)
	cfg.AllInOneTx = getEnvOrDefaultBool("PQM_ALL_IN_ONE_TX", false)
	cfg.AllowNoTransaction = true
	cfg.Logger = logger
	cfg.MigrationsTable = os.Getenv("PQM_MIGRATIONS_TABLE")
	cfg.DryRun = false
	if dr, ok := arguments[argDryRun].(bool); ok {
		cfg.DryRun = dr
	}
	logger.DBG(fmt.Sprintf("%+v", cfg))
	return cfg, nil
}

func getConfigOrDie() pqmigrate.Config {
	cfg, err := getConfig()
	if err != nil {
		logger.Error(err)
		os.Exit(-1)
	}
	return cfg
}

func getArgStringOrNil(key string) *string {
	if d, found := arguments[key]; found {
		if dVal, ok := d.(string); ok {
			val := new(string)
			*val = dVal
			return val
		}
	}
	return nil
}

func createDbCMD() error {
	ctx := pqmigrate.New(getConfigOrDie())
	return ctx.CreateDB(confirmCB(confirmY, true))
}

func dropDbCMD() error {
	ctx := pqmigrate.New(getConfigOrDie())
	return ctx.DropDB(confirmCB(confirmPainful, false))
}

func createCMD() error {
	ctx := pqmigrate.New(getConfigOrDie())
	name := getArgStringOrNil("<name>")
	if name == nil {
		return fmt.Errorf("<name> required")
	}
	if noTx, ok := arguments[argNoTx].(bool); ok && noTx {
		return ctx.CreateMigrationNoTx(*name)
	}
	return ctx.CreateMigration(*name)
}

func upCMD() error {
	steps := getSteps()
	ctx := pqmigrate.New(getConfigOrDie())
	fileName := getArgStringOrNil(argName)
	if fileName != nil {
		if err := ctx.MigrateUpFile(*fileName); err != nil {
			return err
		}
		return ctx.Finish()
	}
	if _, err := ctx.MigrateUp(steps); err != nil {
		return err
	}
	return ctx.Finish()
}

func downCMD() error {
	steps := getSteps()
	ctx := pqmigrate.New(getConfigOrDie())
	fileName := getArgStringOrNil(argName)
	if fileName != nil {
		if err := ctx.MigrateDownFile(*fileName); err != nil {
			return err
		}
		return ctx.Finish()
	}
	if _, err := ctx.MigrateDown(steps); err != nil {
		return err
	}
	return ctx.Finish()
}

func syncCMD() error {
	ctx := pqmigrate.New(getConfigOrDie())
	if err := ctx.Sync(confirmCB(confirmY, true)); err != nil {
		return err
	}
	return ctx.Finish()
}

func replaceCMD() error {
	ctx := pqmigrate.New(getConfigOrDie())
	fileName := getArgStringOrNil(argName)
	if fileName == nil {
		return fmt.Errorf("please supply a filename to replace")
	}
	if err := ctx.Replace(*fileName, confirmCB(confirmY, true)); err != nil {
		return err
	}
	return ctx.Finish()
}

func dumpSchemaCMD() error {
	fileName := getArgStringOrNil(argName)
	ctx := pqmigrate.New(getConfigOrDie())
	return ctx.DumpDBSchemaToFile(fileName)
}

func dumpFullCMD() error {
	fileName := getArgStringOrNil(argName)
	ctx := pqmigrate.New(getConfigOrDie())
	return ctx.DumpDBFull(fileName)
}

func loadSchemaCMD() error {
	cfg := getConfigOrDie()
	cfg.AllInOneTx = false
	ctx := pqmigrate.New(cfg)
	fileName := "schema.sql"
	if fn := getArgStringOrNil(argName); fn != nil {
		fileName = *fn
	}
	if err := ctx.LoadDBSchema(fileName, confirmCB(confirmY, true)); err != nil {
		return err
	}
	return ctx.Finish()
}

func loadDumpCMD() error {
	ctx := pqmigrate.New(getConfigOrDie())
	dumpName := getArgStringOrNil("<name>")
	if dumpName == nil {
		return fmt.Errorf("<name> required")
	}
	if err := ctx.LoadFullDump(*dumpName); err != nil {
		return err
	}
	return ctx.Finish()
}

func seedCMD() error {
	ctx := pqmigrate.New(getConfigOrDie())
	fileName := "seeds.sql"
	if fn := getArgStringOrNil(argName); fn != nil {
		fileName = *fn
	}
	if err := ctx.MigrateFromFile(fileName); err != nil {
		return err
	}
	return ctx.Finish()
}

func squashCMD() error {
	ctx := pqmigrate.New(getConfigOrDie())
	return ctx.Squash(confirmCB(confirmY, true))
}

func unSquashCMD() error {
	ctx := pqmigrate.New(getConfigOrDie())
	return ctx.UnSquash(confirmCB(confirmY, true))
}
