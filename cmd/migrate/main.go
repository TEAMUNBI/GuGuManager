package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/gugumanager/gugumanager/internal/migrations"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	directory := flags.String("dir", "migrations", "directory containing canonical migration pairs")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: migrate [-dir migrations] plan")
		fmt.Fprintln(stderr, "plan validates migration files and prints a dry-run manifest; it never connects to a database")
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 || flags.Arg(0) != "plan" {
		flags.Usage()
		return 2
	}

	plan, err := migrations.LoadMigrations(*directory)
	if err != nil {
		fmt.Fprintf(stderr, "migration plan invalid: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "DRY RUN: validated migration plan; no database connection was attempted and no SQL was executed.")
	for _, item := range plan {
		upDigest := sha256.Sum256(item.Up)
		downDigest := sha256.Sum256(item.Down)
		fmt.Fprintf(stdout, "%s_%s up_sha256=%s down_sha256=%s\n",
			item.VersionKey,
			item.Name,
			hex.EncodeToString(upDigest[:]),
			hex.EncodeToString(downDigest[:]),
		)
	}
	fmt.Fprintf(stdout, "validated %d migration pair(s)\n", len(plan))
	return 0
}
