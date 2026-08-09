package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var migrationName = regexp.MustCompile(`^([0-9]{6})_([a-z0-9][a-z0-9_-]*)\.(up|down)\.sql$`)

type migration struct {
	Version    int
	VersionKey string
	Name       string
	UpDigest   [sha256.Size]byte
	DownDigest [sha256.Size]byte
	hasUp      bool
	hasDown    bool
}

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

	plan, err := buildPlan(*directory)
	if err != nil {
		fmt.Fprintf(stderr, "migration plan invalid: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "DRY RUN: validated migration plan; no database connection was attempted and no SQL was executed.")
	for _, item := range plan {
		fmt.Fprintf(stdout, "%s_%s up_sha256=%s down_sha256=%s\n",
			item.VersionKey,
			item.Name,
			hex.EncodeToString(item.UpDigest[:]),
			hex.EncodeToString(item.DownDigest[:]),
		)
	}
	fmt.Fprintf(stdout, "validated %d migration pair(s)\n", len(plan))
	return 0
}

func buildPlan(directory string) ([]migration, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read migration directory: %w", err)
	}

	byVersion := map[int]*migration{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".sql") {
			continue
		}
		parts := migrationName.FindStringSubmatch(name)
		if parts == nil {
			return nil, fmt.Errorf("non-canonical migration filename %q", name)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("migration %q must be a regular file, not a symlink", name)
		}

		version, err := strconv.Atoi(parts[1])
		if err != nil || version < 1 {
			return nil, fmt.Errorf("invalid migration version %q", parts[1])
		}
		item, exists := byVersion[version]
		if exists && item.Name != parts[2] {
			return nil, fmt.Errorf("duplicate migration version %s", parts[1])
		}
		if !exists {
			item = &migration{Version: version, VersionKey: parts[1], Name: parts[2]}
			byVersion[version] = item
		}

		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, err)
		}
		if len(strings.TrimSpace(string(content))) == 0 {
			return nil, fmt.Errorf("migration %q is empty", name)
		}
		digest := sha256.Sum256(content)
		switch parts[3] {
		case "up":
			if item.hasUp {
				return nil, fmt.Errorf("duplicate up migration for version %s", parts[1])
			}
			item.UpDigest = digest
			item.hasUp = true
		case "down":
			if item.hasDown {
				return nil, fmt.Errorf("duplicate down migration for version %s", parts[1])
			}
			item.DownDigest = digest
			item.hasDown = true
		default:
			return nil, errors.New("unreachable migration direction")
		}
	}

	if len(byVersion) == 0 {
		return nil, errors.New("no migrations found")
	}
	versions := make([]int, 0, len(byVersion))
	for version := range byVersion {
		versions = append(versions, version)
	}
	sort.Ints(versions)

	plan := make([]migration, 0, len(versions))
	for index, version := range versions {
		expected := index + 1
		item := byVersion[version]
		if version != expected {
			return nil, fmt.Errorf("expected migration version %06d, found %s", expected, item.VersionKey)
		}
		if !item.hasUp {
			return nil, fmt.Errorf("missing up migration for version %s", item.VersionKey)
		}
		if !item.hasDown {
			return nil, fmt.Errorf("missing down migration for version %s", item.VersionKey)
		}
		plan = append(plan, *item)
	}
	return plan, nil
}
