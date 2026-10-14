// admin performs explicit deployment and management operations without starting
// HTTP or changing the schema. ModelDev management also verifies Redis sessions.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	klog "github.com/go-kratos/kratos/v2/log"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go-wind-admin/app/admin/service/cmd/server/assets"
	dbbootstrap "go-wind-admin/sql/bootstrap"
)

func main() {
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "modeldev-") {
		// Kratos's config reader logs raw source bytes on decode failures. This
		// standalone management process reports only the command's bounded
		// errors and persisted audit result, even before bootstrap is available.
		klog.SetLogger(klog.NewStdLogger(io.Discard))
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	return runAdmin(context.Background(), os.Args[1:], os.Stdout)
}

func runAdmin(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: admin <init|check|sync-apis|permissions-align|modeldev-pause> [--username admin] [--password-file PATH] [--dry-run]; connection: ANI_DATABASE_DSN")
	}
	command := args[0]
	if command == "modeldev-pause" {
		return runModelDevPause(ctx, args[1:], stdout)
	}
	switch command {
	case "modeldev-enable", "modeldev-import-release", "modeldev-import-csv", "modeldev-inspect", "modeldev-reconcile", "modeldev-cleanup-plan", "modeldev-cleanup-apply":
		return runModelDevManagement(ctx, command, args[1:], stdout)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	username := flags.String("username", "admin", "first platform administrator username (init only)")
	passwordFile := flags.String("password-file", "", "file containing the first administrator password (init only)")
	dryRun := flags.Bool("dry-run", false, "show API/permission changes and roll back (sync-apis, permissions-align)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if command != "init" && command != "check" && command != "sync-apis" && command != "permissions-align" {
		return fmt.Errorf("unknown command %q", command)
	}
	if *dryRun && command != "sync-apis" && command != "permissions-align" {
		return fmt.Errorf("--dry-run applies to sync-apis and permissions-align only")
	}
	var invalidFlag string
	flags.Visit(func(f *flag.Flag) {
		if command != "init" && (f.Name == "username" || f.Name == "password-file") {
			invalidFlag = f.Name
		}
	})
	if invalidFlag != "" {
		return fmt.Errorf("--%s applies to init only", invalidFlag)
	}
	dsn := os.Getenv("ANI_DATABASE_DSN")
	if dsn == "" {
		return fmt.Errorf("ANI_DATABASE_DSN is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("invalid database connection configuration")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		return fmt.Errorf("database connection failed; check ANI_DATABASE_DSN and PostgreSQL availability")
	}
	if command == "check" {
		if err = dbbootstrap.Check(ctx, db); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "PASS: platform administrator and required API grants are present")
		return nil
	}
	if command == "permissions-align" {
		tx, terr := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: *dryRun})
		if terr != nil {
			return terr
		}
		defer tx.Rollback()
		result, aerr := dbbootstrap.AlignPermissions(ctx, tx, *dryRun)
		if aerr != nil {
			return aerr
		}
		for _, change := range result.Changes {
			fmt.Fprintln(stdout, change)
		}
		if *dryRun {
			fmt.Fprintf(stdout, "DRY RUN: no changes written (matched=%d insert=%d disable=%d link=%d unlink=%d)\n",
				result.Matched, result.Inserted, result.Disabled, result.Linked, result.Unlinked)
			return nil
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "PASS: permissions aligned incrementally (matched=%d insert=%d disable=%d link=%d unlink=%d). Reload running instances to refresh authorization policies.\n",
			result.Matched, result.Inserted, result.Disabled, result.Linked, result.Unlinked)
		return nil
	}
	catalog, err := dbbootstrap.Catalog(assets.OpenApiData)
	if err != nil {
		return err
	}
	if command == "init" {
		var password string
		if *passwordFile != "" {
			b, e := os.ReadFile(*passwordFile)
			if e != nil {
				return e
			}
			password = strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
		}
		created, e := dbbootstrap.Initialize(ctx, db, catalog, *username, password)
		if e != nil {
			return e
		}
		if e = dbbootstrap.Check(ctx, db); e != nil {
			return e
		}
		if created {
			fmt.Fprintln(stdout, "PASS: initial seed and administrator committed")
		} else {
			fmt.Fprintln(stdout, "UNCHANGED: initialization already completed; existing data preserved")
		}
		return nil
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: *dryRun})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changes, err := dbbootstrap.SyncAPIs(ctx, tx, catalog, *dryRun)
	if err != nil {
		return err
	}
	for _, change := range changes {
		fmt.Fprintln(stdout, change)
	}
	if *dryRun {
		fmt.Fprintln(stdout, "DRY RUN: no changes written")
		return nil
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "PASS: API catalog synchronized; permissions unchanged. Reload running instances to refresh authorization policies.")
	return nil
}
