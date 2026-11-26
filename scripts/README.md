# Repository Context Packing & Unpacking Workflow

Zero-dependency Go utilities (`//go:build ignore`) for packing repository context into paste-safe `<repository>` XML bundles and unpacking AI-generated file outputs directly to disk.

---

## 1. `scripts/pack.go` — Serialize Repo Context

Collects Git-tracked and unignored files (via `git ls-files`), prioritizes `STATE.md`, `AGENTS.md`, `schemas/`, and `migrations/`, skips binary files, and enforces a hard byte ceiling (`--max-total-bytes`) so pastes never truncate in chat inputs.

### Common Commands

**Default paste-safe bundle (`<= 32 KB`) to stdout:**
```bash
go run scripts/pack.go
```

**Pack specific folders/files for a focused session:**
```bash
go run scripts/pack.go --include "STATE.md,AGENTS.md,internal/storage/" --max-total-bytes 28000
```

**Write to a file instead of stdout:**
```bash
go run scripts/pack.go -o context.xml
```

### Pipe Directly to Clipboard

- **Windows (PowerShell):**
  ```powershell
  go run scripts/pack.go | Set-Clipboard
  ```
- **macOS:**
  ```bash
  go run scripts/pack.go | pbcopy
  ```
- **Linux (Wayland / X11):**
  ```bash
  go run scripts/pack.go | wl-copy
  # or: go run scripts/pack.go | xclip -selection clipboard
  ```

---

## 2. `scripts/unpack.go` — Apply `<repository>` XML Output to Disk

Parses `<file path="...">...</file>` blocks from `stdin` (or `-i <file>`), blocks path-traversal attempts (`../`), creates missing parent directories, and writes files atomically.

### Preview Changes First (`--dry-run`)

- **Windows (PowerShell):**
  ```powershell
  Get-Clipboard -Raw | go run scripts/unpack.go --dry-run
  ```
- **macOS / Linux:**
  ```bash
  pbpaste | go run scripts/unpack.go --dry-run
  # or: wl-paste | go run scripts/unpack.go --dry-run
  ```

### Write Files & Verify Build

- **Windows (PowerShell):**
  ```powershell
  Get-Clipboard -Raw | go run scripts/unpack.go && go test -race ./...
  ```
- **macOS:**
  ```bash
  pbpaste | go run scripts/unpack.go && go test -race ./...
  ```
- **Linux (Wayland / X11):**
  ```bash
  wl-paste | go run scripts/unpack.go && go test -race ./...
  # or: xclip -o -selection clipboard | go run scripts/unpack.go && go test -race ./...
  ```

---

## 3. Flags Reference

| Script | Flag | Default | Description |
| :--- | :--- | :--- | :--- |
| `pack.go` | `--include` | `""` (all) | Comma-separated path prefixes or globs to include |
| `pack.go` | `--exclude` | `.git/,bin/,dist/,context.xml` | Comma-separated path prefixes or globs to skip |
| `pack.go` | `--max-file-bytes` | `65536` (64 KB) | Skip individual files exceeding this size |
| `pack.go` | `--max-total-bytes` | `32000` (32 KB) | Hard cap on total XML output (`0` = unlimited) |
| `pack.go` | `-o` | `""` (`stdout`) | Write packed XML to file path |
| `unpack.go` | `-i` | `""` (`stdin`) | Read XML input from file path instead of `stdin` |
| `unpack.go` | `--dry-run` | `false` | List target paths and byte sizes without writing |