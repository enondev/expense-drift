//go:build ignore

package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

func main() {
	outPath := flag.String("o", "context.xml", "output .xml file path (use '-' for stdout)")
	include := flag.String("include", "", "comma-separated path prefixes or globs (e.g. 'STATE.md,AGENTS.md,schemas/,internal/')")
	exclude := flag.String("exclude", ".git/,bin/,dist/,context.xml,.env*,.secret*,*.pem,*.key", "comma-separated path prefixes or globs to skip")
	maxFileBytes := flag.Int("max-file-bytes", 64*1024, "skip single files larger than this byte size")
	maxTotalBytes := flag.Int("max-total-bytes", 64000, "hard cap on total output bytes (0 = unlimited; use 7500 for raw text-box pastes)")
	flag.Parse()

	files, err := gitTrackedAndUnignored()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack: %v\n", err)
		os.Exit(1)
	}

	incList := splitCSV(*include)
	excList := splitCSV(*exclude)

	// Prioritize handoff & spec files first so they are never omitted
	slices.SortFunc(files, func(a, b string) int {
		pa, pb := filePriority(a), filePriority(b)
		if pa != pb {
			return pa - pb
		}
		return strings.Compare(a, b)
	})

	var buf bytes.Buffer
	buf.WriteString("<repository>\n")
	const footerReserve = 512
	packed := 0
	var omitted []string

	for _, rel := range files {
		slashRel := filepath.ToSlash(rel)
		if len(incList) > 0 && !matchesAny(slashRel, incList) {
			continue
		}
		if matchesAny(slashRel, excList) {
			continue
		}

		info, err := os.Stat(rel)
		if err != nil || info.IsDir() || info.Size() > int64(*maxFileBytes) {
			continue
		}

		data, err := os.ReadFile(rel)
		if err != nil || isBinary(data) {
			continue
		}
		// Normalize CRLF to LF so byte-length framing stays exact across OSes
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))

		header := fmt.Sprintf("<file path=%q bytes=\"%d\">\n", slashRel, len(data))
		footer := "\n</file>\n"
		needed := len(header) + len(data) + len(footer)

		if *maxTotalBytes > 0 && buf.Len()+needed+footerReserve > *maxTotalBytes {
			remaining := *maxTotalBytes - buf.Len() - len(header) - len(footer) - footerReserve - 48
			if remaining > 256 {
				trunc := truncateUTF8(data, remaining)
				marker := []byte("\n<!-- FILE TRUNCATED BY MAX-TOTAL-BYTES -->")
				payload := append(trunc, marker...)
				fmt.Fprintf(&buf, "<file path=%q bytes=\"%d\">\n", slashRel, len(payload))
				buf.Write(payload)
				buf.WriteString(footer)
				packed++
			}
			omitted = append(omitted, slashRel)
			continue
		}

		buf.WriteString(header)
		buf.Write(data)
		buf.WriteString(footer)
		packed++
	}

	if len(omitted) > 0 {
		summary := strings.Join(omitted, ", ")
		if len(summary) > footerReserve-80 {
			summary = summary[:footerReserve-84] + "..."
		}
		fmt.Fprintf(&buf, "<!-- OMITTED DUE TO %d BYTE LIMIT (%d files): %s -->\n", *maxTotalBytes, len(omitted), summary)
	}
	buf.WriteString("</repository>\n")

	if *outPath == "-" {
		os.Stdout.Write(buf.Bytes())
		return
	}
	if err := os.WriteFile(*outPath, buf.Bytes(), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "pack write: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "packed %d files (%d bytes, %d omitted) -> %s\n", packed, buf.Len(), len(omitted), *outPath)
}

func truncateUTF8(b []byte, max int) []byte {
	if len(b) <= max {
		return b
	}
	cut := b[:max]
	for len(cut) > 0 && !utf8.Valid(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

func filePriority(rel string) int {
	norm := filepath.ToSlash(rel)
	switch {
	case norm == "STATE.md":
		return 0
	case norm == "AGENTS.md":
		return 1
	case strings.HasPrefix(norm, "schemas/") || strings.HasPrefix(norm, "migrations/"):
		return 2
	case norm == "go.mod":
		return 3
	default:
		return 10
	}
}

func gitTrackedAndUnignored() ([]string, error) {
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files failed (is this a git repo?): %w", err)
	}
	var files []string
	for _, entry := range bytes.Split(out, []byte{0}) {
		if len(entry) > 0 {
			files = append(files, string(entry))
		}
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, filepath.ToSlash(p))
		}
	}
	return out
}

func matchesAny(rel string, patterns []string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range patterns {
		if strings.HasSuffix(p, "/") && strings.HasPrefix(rel, p) {
			return true
		}
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
		if ok, _ := filepath.Match(p, rel); ok {
			return true
		}
		if ok, _ := filepath.Match(p, filepath.Base(rel)); ok {
			return true
		}
	}
	return false
}

func isBinary(data []byte) bool {
	check := data
	if len(check) > 8192 {
		check = check[:8192]
	}
	return bytes.IndexByte(check, 0) != -1
}