//go:build ignore

package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxInputBytes      = 10 * 1024 * 1024 // 10 MB total input ceiling
	maxSingleFileBytes = 1024 * 1024      // 1 MB per-file allocation cap
)

var (
	sizedHeaderRe = regexp.MustCompile(`<file\s+path="([^"]+)"\s+bytes="(\d+)">\n`)
	legacyBlockRe = regexp.MustCompile(`(?s)<file\s+path="([^"]+)">\n(.*?)\n</file>`)
)

type parsedFile struct {
	relPath string
	content []byte
}

func main() {
	inPath := flag.String("i", "-", "input .xml file path (use '-' for stdin)")
	dryRun := flag.Bool("dry-run", false, "print target paths without writing files")
	flag.Parse()

	var raw []byte
	var err error
	if *inPath != "" && *inPath != "-" {
		raw, err = os.ReadFile(*inPath)
	} else {
		raw, err = io.ReadAll(io.LimitReader(os.Stdin, maxInputBytes))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "unpack read: %v\n", err)
		os.Exit(1)
	}

	// Normalize CRLF -> LF so Windows clipboard pastes match exact LF byte counts
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))

	files, err := parseFiles(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unpack parse: %v\n", err)
		os.Exit(1)
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "unpack: no <file path=\"...\"> blocks found in input")
		os.Exit(1)
	}

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "unpack cwd: %v\n", err)
		os.Exit(1)
	}

	written := 0
	for _, f := range files {
		rel := filepath.FromSlash(strings.TrimSpace(f.relPath))
		if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "..") {
			fmt.Fprintf(os.Stderr, "unpack: blocked unsafe path %q\n", f.relPath)
			os.Exit(1)
		}
		abs := filepath.Join(root, rel)
		if !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
			fmt.Fprintf(os.Stderr, "unpack: blocked traversal path %q\n", f.relPath)
			os.Exit(1)
		}
		if *dryRun {
			fmt.Printf("[dry-run] would write %s (%d bytes)\n", rel, len(f.content))
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "unpack mkdir %s: %v\n", rel, err)
			os.Exit(1)
		}
		realDir, err := filepath.EvalSymlinks(filepath.Dir(abs))
		realRoot, rootErr := filepath.EvalSymlinks(root)
		if err != nil || rootErr != nil || !strings.HasPrefix(realDir+string(os.PathSeparator), realRoot+string(os.PathSeparator)) {
			fmt.Fprintf(os.Stderr, "unpack: blocked symlink escape for %q\n", f.relPath)
			os.Exit(1)
		}
		out := f.content
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		if err := os.WriteFile(abs, out, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "unpack write %s: %v\n", rel, err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s (%d bytes)\n", rel, len(out))
		written++
	}

	if !*dryRun {
		fmt.Printf("unpacked %d files successfully\n", written)
	}
}

func parseFiles(raw []byte) ([]parsedFile, error) {
	var results []parsedFile
	cursor := 0
	for cursor < len(raw) {
		loc := sizedHeaderRe.FindSubmatchIndex(raw[cursor:])
		if loc == nil {
			break
		}
		pathBytes := raw[cursor+loc[2] : cursor+loc[3]]
		sizeBytes := raw[cursor+loc[4] : cursor+loc[5]]
		n, err := strconv.Atoi(string(sizeBytes))
		if err != nil || n < 0 || n > maxSingleFileBytes {
			return nil, fmt.Errorf("invalid byte length %q for %s", sizeBytes, pathBytes)
		}
		bodyStart := cursor + loc[1]
		bodyEnd := bodyStart + n
		if bodyEnd > len(raw) {
			return nil, fmt.Errorf("truncated block for %s: expected %d bytes, got %d", pathBytes, n, len(raw)-bodyStart)
		}
		results = append(results, parsedFile{
			relPath: string(pathBytes),
			content: bytes.Clone(raw[bodyStart:bodyEnd]),
		})
		cursor = bodyEnd
	}
	if len(results) > 0 {
		return results, nil
	}

	for _, m := range legacyBlockRe.FindAllSubmatch(raw, -1) {
		results = append(results, parsedFile{
			relPath: string(m[1]),
			content: bytes.Clone(m[2]),
		})
	}
	return results, nil
}