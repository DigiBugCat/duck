// Command merge-workspace-ledger-slugs repairs duck workspace records that were
// written under a slug derived from a non-hub $HOME.
//
// Run on the hub from the duck repo:
//
//	go run ./scripts/merge-workspace-ledger-slugs.go --dry-run
//	go run ./scripts/merge-workspace-ledger-slugs.go
//
// Optional:
//
//	go run ./scripts/merge-workspace-ledger-slugs.go --base ~/.claude/projects --dry-run
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DigiBugCat/duck/internal/workspaces"
)

type sourceRecord struct {
	rec  workspaces.Record
	path string
}

func main() {
	baseFlag := flag.String("base", workspaces.DefaultBase, "Claude projects corpus root")
	dryRun := flag.Bool("dry-run", false, "print actions without writing or deleting")
	flag.Parse()

	base, err := expandHome(*baseFlag)
	if err != nil {
		die("expand base: %v", err)
	}

	records, err := readRecords(base)
	if err != nil {
		die("read records: %v", err)
	}
	if len(records) == 0 {
		fmt.Printf("no workspace records found under %s\n", base)
		return
	}

	names := make([]string, 0, len(records))
	for name := range records {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		sources := records[name]
		merged := mergeRecords(sources)
		if merged.Dir == "" {
			fmt.Printf("skip %s: merged record has empty dir\n", name)
			continue
		}
		dst := filepath.Join(base, workspaces.EncodeDir(merged.Dir), "duck", merged.Name+".json")
		data, err := json.MarshalIndent(merged, "", "  ")
		if err != nil {
			die("marshal %s: %v", name, err)
		}
		data = append(data, '\n')

		if sameFileJSON(dst, data) {
			fmt.Printf("keep %s\n", dst)
		} else if *dryRun {
			fmt.Printf("would write %s from %d record(s)\n", dst, len(sources))
		} else {
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				die("mkdir %s: %v", filepath.Dir(dst), err)
			}
			if err := os.WriteFile(dst, data, 0o644); err != nil {
				die("write %s: %v", dst, err)
			}
			fmt.Printf("wrote %s from %d record(s)\n", dst, len(sources))
		}

		for _, src := range sources {
			if samePath(src.path, dst) {
				continue
			}
			if *dryRun {
				fmt.Printf("would delete %s\n", src.path)
				continue
			}
			if err := os.Remove(src.path); err != nil && !os.IsNotExist(err) {
				die("delete %s: %v", src.path, err)
			}
			fmt.Printf("deleted %s\n", src.path)
		}
	}
}

func readRecords(base string) (map[string][]sourceRecord, error) {
	glob := filepath.Join(base, "*", "duck", "*.json")
	paths, err := filepath.Glob(glob)
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	records := map[string][]sourceRecord{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var rec workspaces.Record
		if err := json.Unmarshal(data, &rec); err != nil {
			fmt.Printf("skip corrupt %s: %v\n", path, err)
			continue
		}
		if rec.Name == "" {
			fmt.Printf("skip foreign %s: empty name\n", path)
			continue
		}
		records[rec.Name] = append(records[rec.Name], sourceRecord{rec: rec, path: path})
	}
	return records, nil
}

func mergeRecords(sources []sourceRecord) workspaces.Record {
	merged := sources[0].rec
	for _, src := range sources[1:] {
		merged = mergeRecord(merged, src.rec)
	}
	return merged
}

func mergeRecord(a, b workspaces.Record) workspaces.Record {
	out := a
	out.Name = pickString(a.Name, b.Name, a.Updated, b.Updated)
	out.Dir = pickString(a.Dir, b.Dir, a.Updated, b.Updated)
	out.Title = pickString(a.Title, b.Title, a.Updated, b.Updated)
	out.Persistent = a.Persistent || b.Persistent
	out.Created = pickTime(a.Created, b.Created, a.Updated, b.Updated)
	if b.Updated.After(a.Updated) {
		out.Updated = b.Updated
	} else {
		out.Updated = a.Updated
	}
	return out
}

func pickString(a, b string, at, bt time.Time) string {
	if a == "" {
		return b
	}
	if b == "" || a == b {
		return a
	}
	if bt.After(at) {
		return b
	}
	return a
}

func pickTime(a, b, at, bt time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() || a.Equal(b) {
		return a
	}
	if bt.After(at) {
		return b
	}
	return a
}

func sameFileJSON(path string, data []byte) bool {
	existing, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(data))
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return aa == bb
	}
	return a == b
}

func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
