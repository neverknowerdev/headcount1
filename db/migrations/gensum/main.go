// Command gensum rewrites db/migrations/<dialect>/atlas.sum from the .up.sql
// files of both dialects. Run it from the repository root after adding or
// changing a migration:
//
//	go run ./db/migrations/gensum
//
// With -check it writes nothing and exits non-zero if a checksum file is stale.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ariga.io/atlas/sql/migrate"
)

func main() {
	check := flag.Bool("check", false, "verify the checked-in atlas.sum files instead of rewriting them")
	flag.Parse()
	stale := false
	for _, dialect := range []string{"sqlite", "postgres"} {
		dir := filepath.Join("db", "migrations", dialect)
		entries, err := os.ReadDir(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var names []string
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".up.sql") {
				names = append(names, entry.Name())
			}
		}
		sort.Strings(names)
		var files []migrate.File
		for _, name := range names {
			body, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			files = append(files, migrate.NewLocalFile(name, body))
		}
		sum, err := migrate.NewHashFile(files)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		out, err := sum.MarshalText()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		sumPath := filepath.Join(dir, "atlas.sum")
		if *check {
			existing, _ := os.ReadFile(sumPath)
			if string(existing) != string(out) {
				fmt.Fprintf(os.Stderr, "%s is stale; run: go run ./db/migrations/gensum\n", sumPath)
				stale = true
			}
			continue
		}
		if err := os.WriteFile(sumPath, out, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s (%d migrations)\n", sumPath, len(names))
	}
	if stale {
		os.Exit(1)
	}
}
