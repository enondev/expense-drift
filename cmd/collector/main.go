// Command collector is the headless batch worker for ExpenseDrift.
// It runs scheduled collection runs via GitHub Actions / local cron,
// performing fetch → hash → compare → extract → store for each watch target.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"expensedrift/internal/collector"
	"expensedrift/internal/extractor"
	"expensedrift/internal/fetcher"
	"expensedrift/internal/storage"
)

// CLI flags.
var (
	seedOnly        bool
	dryRun          bool
	rebuildManifest bool
	vendorID        string
	targetID        string
	forceSemantic   bool
	seedsFile       string
)

func init() {
	flag.BoolVar(&seedOnly, "seed-only", false, "Only load seed vendors/targets into D1")
	flag.BoolVar(&dryRun, "dry-run", false, "Do not write to D1/KV")
	flag.BoolVar(&rebuildManifest, "rebuild-manifest", false, "Rebuild KV manifest:v1 from D1 snapshots")
	flag.StringVar(&vendorID, "vendor", "", "Scope collection to a single vendor slug")
	flag.StringVar(&targetID, "target", "", "Scope collection to a single target ID")
	flag.BoolVar(&forceSemantic, "force-semantic", false, "Bypass raw_content_hash check and re-run semantic extraction")
	flag.StringVar(&seedsFile, "seeds", "schemas/saas_seeds.json", "Path to seeds JSON file")
}

func main() {
	flag.Parse()

	cfg := storage.FromEnv()
	if cfg.CFAccountID == "" || cfg.CFAPIToken == "" || cfg.CFD1DatabaseID == "" {
		fmt.Fprintln(os.Stderr, "error: CF_ACCOUNT_ID, CF_API_TOKEN, and CF_D1_DATABASE_ID are required")
		os.Exit(1)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	d1 := storage.NewD1Client(cfg)
	kv := storage.NewKVClient(cfg)
	fc := fetcher.New()
	ext := extractor.New(nil)

	ctx := context.Background()
	start := time.Now()

	// Verify D1 connectivity before any collection work.
	if err := collector.CheckD1Connection(ctx, d1); err != nil {
		log.Error("d1 connectivity check failed", "error", err)
		os.Exit(1)
	}
	log.Info("d1 connectivity verified")

	var summary *collector.CollectionSummary
	var err error

	switch {
	case seedOnly:
		err = runSeedOnly(ctx, log, d1, kv)
	case rebuildManifest:
		err = runRebuildManifest(ctx, log, d1, kv)
	default:
		summary, err = collector.Run(ctx, log, d1, kv, fc, ext, vendorID, targetID, forceSemantic, dryRun)
	}

	if err != nil {
		log.Error("collector run failed", "error", err)
		os.Exit(1)
	}

	if summary != nil {
		summary.DurationMS = time.Since(start).Milliseconds()
		out, _ := json.MarshalIndent(summary, "", "  ")
		fmt.Println(string(out))

		log.Info("collector run complete",
			"targets_checked", summary.TargetsChecked,
			"raw_unchanged", summary.RawUnchanged,
			"semantic_unchanged", summary.SemanticUnchanged,
			"diffs_detected", summary.DiffsDetected,
			"errors", summary.Errors,
			"duration_ms", summary.DurationMS,
		)
	} else {
		log.Info("collector run complete", "mode", "seed-only-or-rebuild-manifest", "duration_ms", time.Since(start).Milliseconds())
	}
}

// runSeedOnly loads vendors and watch targets from the seeds JSON file into D1.
func runSeedOnly(ctx context.Context, log *slog.Logger, d1 *storage.D1Client, kv *storage.KVClient) error {
	f, err := os.Open(seedsFile)
	if err != nil {
		return fmt.Errorf("open seeds file %q: %w", seedsFile, err)
	}
	defer f.Close()

	var doc struct {
		Vendors []seedVendor `json:"vendors"`
	}
	if err := json.NewDecoder(f).Decode(&doc); err != nil {
		return fmt.Errorf("decode seeds file %q: %w", seedsFile, err)
	}

	var stmts []storage.Statement
	for _, v := range doc.Vendors {
		stmts = append(stmts, storage.Statement{
			SQL: `INSERT INTO vendors (id, name, category, homepage_url, status)
				  VALUES (?, ?, ?, ?, 'active')
				  ON CONFLICT(id) DO UPDATE SET name=excluded.name, category=excluded.category, homepage_url=excluded.homepage_url`,
			Params: []interface{}{v.ID, v.Name, v.Category, v.HomepageURL},
		})
		for _, t := range v.Targets {
			targetID := t.ID
			if targetID == "" {
				targetID = v.ID + ":" + strings.ToLower(strings.ReplaceAll(t.SurfaceType, "_", "-"))
			}
			stmts = append(stmts, storage.Statement{
				SQL: `INSERT INTO watch_targets (id, vendor_id, surface_type, url, extract_selector, fetch_tier, next_check_at)
					  VALUES (?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
					  ON CONFLICT(id) DO UPDATE SET vendor_id=excluded.vendor_id, surface_type=excluded.surface_type, url=excluded.url, extract_selector=excluded.extract_selector, fetch_tier=excluded.fetch_tier, next_check_at=excluded.next_check_at`,
				Params: []interface{}{targetID, v.ID, t.SurfaceType, t.URL, t.ExtractSelector, t.FetchTier},
			})
		}
	}

	if len(stmts) > 0 {
		if _, err := d1.Query(stmts...); err != nil {
			return fmt.Errorf("seed D1: %w", err)
		}
		log.Info("seeded vendors and targets", "vendor_count", len(doc.Vendors), "target_count", len(stmts)-len(doc.Vendors))
	}

	// Rebuild manifest from freshly seeded targets.
	if err := runRebuildManifest(ctx, log, d1, kv); err != nil {
		return fmt.Errorf("rebuild manifest: %w", err)
	}

	return nil
}

// runRebuildManifest reads the latest snapshot per target from D1 and writes KV manifest:v1.
func runRebuildManifest(ctx context.Context, log *slog.Logger, d1 *storage.D1Client, kv *storage.KVClient) error {
	rows, err := d1.Query(storage.Statement{
		SQL: `SELECT s.target_id, s.raw_content_hash, s.semantic_schema_hash, s.composite_audit_hash
			  FROM snapshots s
			  INNER JOIN (
			    SELECT target_id, MAX(captured_at) AS max_captured
			    FROM snapshots
			    GROUP BY target_id
			  ) latest ON s.target_id = latest.target_id AND s.captured_at = latest.max_captured`,
	})
	if err != nil {
		return fmt.Errorf("query snapshots: %w", err)
	}

	manifest := &storage.Manifest{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Targets:   make(map[string]storage.TargetHashes),
	}
	for _, r := range rows {
		for _, row := range r.Results {
			targetID := strOrEmpty(row["target_id"])
			manifest.Targets[targetID] = storage.TargetHashes{
				Raw: strOrEmpty(row["raw_content_hash"]),
				Sem: strOrEmpty(row["semantic_schema_hash"]),
				Comp: strOrEmpty(row["composite_audit_hash"]),
			}
		}
	}

	if err := kv.WriteManifest(manifest); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	log.Info("rebuilt manifest", "target_count", len(manifest.Targets))
	return nil
}

// seedVendor mirrors internal/seeds for local parsing.
type seedVendor struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Category    string        `json:"category"`
	HomepageURL string        `json:"homepage_url"`
	Targets     []seedTarget  `json:"targets"`
}

// seedTarget mirrors internal/seeds for local parsing.
type seedTarget struct {
	ID            string   `json:"id"`
	SurfaceType   string   `json:"surface_type"`
	URL           string   `json:"url"`
	ExtractSelector *string `json:"extract_selector,omitempty"`
	FetchTier     int      `json:"fetch_tier"`
}

func strOrEmpty(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
