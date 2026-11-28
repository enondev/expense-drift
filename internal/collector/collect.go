// Package collector contains the shared fetch → hash → compare → extract → store
// pipeline used by both cmd/collector (batch worker) and cmd/server (on-demand trigger).
package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"expensedrift/internal/differ"
	"expensedrift/internal/extractor"
	"expensedrift/internal/fetcher"
	"expensedrift/internal/hasher"
	"expensedrift/internal/model"
	"expensedrift/internal/storage"
)

// CollectionSummary summarizes a collection run.
type CollectionSummary struct {
	TargetsChecked    int     `json:"targets_checked"`
	RawUnchanged      int     `json:"raw_unchanged"`
	SemanticUnchanged int     `json:"semantic_unchanged"`
	DiffsDetected     int     `json:"diffs_detected"`
	Errors            int     `json:"errors"`
	DurationMS        int64   `json:"duration_ms"`
}

// Run executes the collection pipeline for all active watch targets (or a scoped
// subset when vendorID/targetID is set). Returns a summary of results.
func Run(ctx context.Context, log *slog.Logger, d1 *storage.D1Client, kv *storage.KVClient, fc *fetcher.Fetcher, ext *extractor.Extractor, vendorID, targetID string, forceSemantic, dryRun bool) (*CollectionSummary, error) {
	start := time.Now()

	// Read manifest:v1 from KV (single read at startup).
	manifest, err := kv.ReadManifest()
	if err != nil {
		log.Warn("KV manifest read failed, starting empty", "error", err)
		manifest = &storage.Manifest{Targets: make(map[string]storage.TargetHashes)}
	}

	// Load watch targets from D1.
	targets, err := loadWatchTargets(ctx, d1)
	if err != nil {
		return nil, fmt.Errorf("load watch targets: %w", err)
	}

	// Filter by vendor/target if scoped.
	targets = filterTargets(targets, vendorID, targetID)

	var summary CollectionSummary
	summary.TargetsChecked = len(targets)

	log.Info("collection run starting",
		"targets_checked", summary.TargetsChecked,
		"vendor", vendorID,
		"target", targetID,
		"force_semantic", forceSemantic,
		"dry_run", dryRun,
	)

	// Verify D1 connectivity before processing targets.
	if err := CheckD1Connection(ctx, d1); err != nil {
		return nil, fmt.Errorf("d1 connectivity check failed: %w", err)
	}

	// Process each target.
	for _, tgt := range targets {
		if err := processTarget(ctx, log, d1, kv, fc, ext, manifest, tgt, &summary, forceSemantic, dryRun); err != nil {
			log.Error("failed to process target", "target_id", tgt.ID, "error", err)
			summary.Errors++
			continue
		}
		// Update next_check_at based on cooldown so this target won't be re-fetched
		// until its cooldown has elapsed.
		if !dryRun {
			if err := updateTargetCheckTime(ctx, d1, tgt.ID, tgt.CooldownSeconds); err != nil {
				log.Warn("failed to update next_check_at", "target_id", tgt.ID, "error", err)
			}
		}
	}

	// Write manifest:v1 to KV (single write at completion).
	if !dryRun {
		if err := kv.WriteManifest(manifest); err != nil {
			return nil, fmt.Errorf("write manifest: %w", err)
		}
	}

	summary.DurationMS = time.Since(start).Milliseconds()

	log.Info("collection run complete",
		"targets_checked", summary.TargetsChecked,
		"raw_unchanged", summary.RawUnchanged,
		"semantic_unchanged", summary.SemanticUnchanged,
		"diffs_detected", summary.DiffsDetected,
		"errors", summary.Errors,
		"duration_ms", summary.DurationMS,
	)

	return &summary, nil
}

// watchTarget is a local copy of a D1 watch target row.
type watchTarget struct {
	ID              string
	VendorID        string
	SurfaceType     string
	URL             string
	ExtractSelector string
	LastRawHash     string
	LastSemHash     string
	LastCompHash    string
	CooldownSeconds int
}

// loadWatchTargets fetches all active watch targets from D1 whose cooldown has expired.
func loadWatchTargets(ctx context.Context, d1 *storage.D1Client) ([]watchTarget, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := d1.Query(storage.Statement{
		SQL: `SELECT wt.id, wt.vendor_id, wt.surface_type, wt.url, wt.extract_selector,
				 wt.fetch_tier, wt.last_raw_hash, wt.last_semantic_hash, wt.last_composite_hash,
				 wt.cooldown_seconds
			  FROM watch_targets wt
			  JOIN vendors v ON v.id = wt.vendor_id
			  WHERE v.status = 'active'
			    AND wt.next_check_at <= ?`,
		Params: []interface{}{now},
	})
	if err != nil {
		return nil, err
	}

	var targets []watchTarget
	for _, r := range rows {
		for _, row := range r.Results {
			tgt := watchTarget{
				ID:              strOrEmpty(row["id"]),
				VendorID:        strOrEmpty(row["vendor_id"]),
				SurfaceType:     strOrEmpty(row["surface_type"]),
				URL:             strOrEmpty(row["url"]),
				ExtractSelector: strOrEmpty(row["extract_selector"]),
				LastRawHash:     strOrEmpty(row["last_raw_hash"]),
				LastSemHash:     strOrEmpty(row["last_semantic_hash"]),
				LastCompHash:    strOrEmpty(row["last_composite_hash"]),
				CooldownSeconds: strOrEmptyInt(row["cooldown_seconds"]),
			}
			targets = append(targets, tgt)
		}
	}
	return targets, nil
}

// CheckD1Connection verifies the D1 endpoint is reachable before processing targets.
func CheckD1Connection(ctx context.Context, d1 *storage.D1Client) error {
	_, err := d1.Query(storage.Statement{
		SQL: `SELECT 1 AS connected FROM vendors WHERE id = id LIMIT 1`,
	})
	return err
}

// updateTargetCheckTime records the next check time for a target after processing.
func updateTargetCheckTime(ctx context.Context, d1 *storage.D1Client, targetID string, cooldownSeconds int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	nextCheck := time.Now().UTC().Add(time.Duration(cooldownSeconds) * time.Second).Format(time.RFC3339)
	_, err := d1.Query(storage.Statement{
		SQL: `UPDATE watch_targets
			  SET last_checked_at = ?,
			      next_check_at = ?
			  WHERE id = ?`,
		Params: []interface{}{now, nextCheck, targetID},
	})
	return err
}

func strOrEmpty(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func strOrEmptyInt(v interface{}) int {
	if s, ok := v.(string); ok && s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return 3600 // default 1 hour cooldown
}

func filterTargets(targets []watchTarget, vendorID, targetID string) []watchTarget {
	if vendorID == "" && targetID == "" {
		return targets
	}
	var out []watchTarget
	for _, t := range targets {
		if vendorID != "" && t.VendorID != vendorID {
			continue
		}
		if targetID != "" && t.ID != targetID {
			continue
		}
		out = append(out, t)
	}
	return out
}

// processTarget runs the full pipeline for a single watch target.
func processTarget(ctx context.Context, log *slog.Logger, d1 *storage.D1Client, kv *storage.KVClient, fc *fetcher.Fetcher, ext *extractor.Extractor, manifest *storage.Manifest, tgt watchTarget, summary *CollectionSummary, forceSemantic, dryRun bool) error {
	cached, hasCached := manifest.Targets[tgt.ID]

	// Step 1: Fetch the page.
	res := fc.Fetch(ctx, tgt.URL)
	if res.Err != nil {
		log.Error("fetch failed", "target_id", tgt.ID, "url", tgt.URL, "error", res.Err)
		return fmt.Errorf("fetch: %w", res.Err)
	}
	log.Debug("fetch complete", "target_id", tgt.ID, "url", tgt.URL, "status_code", res.StatusCode, "body_bytes", len(res.RawBody))

	// Step 2: Compute raw content hash.
	rawHash := hasher.RawContentHash(string(res.RawBody))

	// Step 3: Check if raw unchanged (and not forcing semantic).
	if hasCached && cached.Raw == rawHash && !forceSemantic {
		summary.RawUnchanged++
		log.Debug("raw content unchanged, skipping", "target_id", tgt.ID, "raw_hash", rawHash)
		return nil
	}

	// Step 4: Extract billing rules.
	ruleset, extractorModel, err := ext.Extract(ctx, res.Distilled, res.NextData, strings.Join(res.JSONLD, "\n"))
	if err != nil {
		log.Error("extraction failed", "target_id", tgt.ID, "url", tgt.URL, "error", err)
		return fmt.Errorf("extract: %w", err)
	}
	ruleset.VendorID = tgt.VendorID
	ruleset.SurfaceType = model.SurfaceType(tgt.SurfaceType)

	// Step 5: Compute semantic schema hash.
	semHash, err := hasher.SemanticSchemaHash(ruleset)
	if err != nil {
		log.Error("semantic hash failed", "target_id", tgt.ID, "error", err)
		return fmt.Errorf("semantic hash: %w", err)
	}

	// Step 6: Check if semantic unchanged.
	if hasCached && cached.Sem == semHash && !forceSemantic {
		summary.SemanticUnchanged++
		log.Debug("semantic schema unchanged, skipping", "target_id", tgt.ID, "semantic_hash", semHash)
		return nil
	}

	// Step 7: Compute composite audit hash (chains prev composite).
	timestamp := time.Now().UTC().Format(time.RFC3339)
	compHash := hasher.CompositeAuditHash(tgt.VendorID, cached.Comp, rawHash, semHash, timestamp)

	// Step 8: Store snapshot in D1.
	var snapshotID string
	if !dryRun {
		snapshotID = fmt.Sprintf("snap-%d", time.Now().UnixNano())
		canonicalJSON, _ := json.Marshal(ruleset)
		if _, err := d1.Query(storage.Statement{
			SQL: `INSERT INTO snapshots (id, target_id, vendor_id, raw_content_hash, semantic_schema_hash,
					 composite_audit_hash, prev_composite_hash, canonical_json, extractor_model, captured_at)
				  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			Params: []interface{}{
				snapshotID, tgt.ID, tgt.VendorID, rawHash, semHash, compHash, cached.Comp,
				string(canonicalJSON), extractorModel, timestamp,
			},
		}); err != nil {
			log.Error("failed to store snapshot", "target_id", tgt.ID, "error", err)
			return fmt.Errorf("store snapshot: %w", err)
		}
		log.Info("snapshot stored", "target_id", tgt.ID, "snapshot_id", snapshotID, "raw_hash", rawHash, "semantic_hash", semHash, "composite_hash", compHash, "extractor", extractorModel)
	}

	// Step 9: Compare with previous snapshot to detect drift.
	if hasCached && cached.Sem != "" {
		prevRuleset, err := loadPreviousSnapshot(ctx, d1, tgt.ID)
		if err == nil && prevRuleset != nil {
			result, changed := differ.Compare(prevRuleset, ruleset)
			if changed {
				summary.DiffsDetected++
				log.Warn("billing drift detected", "target_id", tgt.ID, "vendor_id", tgt.VendorID,
					"drift_type", result.DriftType, "severity", result.Severity, "summary", result.Summary)
				if !dryRun {
					if err := storeBillingDiff(ctx, d1, tgt.VendorID, tgt.ID, snapshotID, result); err != nil {
						log.Error("failed to store billing diff", "target_id", tgt.ID, "error", err)
						summary.Errors++
					}
				}
			}
		}
	}

	// Step 10: Update manifest entry.
	manifest.Targets[tgt.ID] = storage.TargetHashes{
		Raw: rawHash,
		Sem: semHash,
		Comp: compHash,
	}

	return nil
}

// loadPreviousSnapshot loads the most recent previous snapshot's ruleset for differ comparison.
func loadPreviousSnapshot(ctx context.Context, d1 *storage.D1Client, targetID string) (*model.BillingRuleSet, error) {
	rows, err := d1.Query(storage.Statement{
		SQL: `SELECT canonical_json FROM snapshots WHERE target_id = ? ORDER BY captured_at DESC LIMIT 1 OFFSET 1`,
		Params: []interface{}{targetID},
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || len(rows[0].Results) == 0 {
		return nil, nil
	}
	rawJSON, _ := rows[0].Results[0]["canonical_json"].(string)
	var ruleset model.BillingRuleSet
	if err := json.Unmarshal([]byte(rawJSON), &ruleset); err != nil {
		return nil, err
	}
	return &ruleset, nil
}

// storeBillingDiff persists a detected billing diff to D1.
// from_snapshot_id is nil (SQL NULL) when there is no previous snapshot, since
// an empty string "" would violate the foreign key constraint under
// PRAGMA foreign_keys = ON (from_snapshot_id REFERENCES snapshots(id)).
func storeBillingDiff(ctx context.Context, d1 *storage.D1Client, vendorID, targetID, snapshotID string, result model.BillingDiffResult) error {
	diffID := fmt.Sprintf("diff-%d", time.Now().UnixNano())
	timestamp := time.Now().UTC().Format(time.RFC3339)
	patchJSON, _ := json.Marshal(result.PatchOps)

	driftType := string(result.DriftType)
	if driftType == "" {
		driftType = "tos_trap"
	}

	_, err := d1.Query(storage.Statement{
		SQL: `INSERT INTO billing_diffs (id, vendor_id, target_id, from_snapshot_id, to_snapshot_id,
				 drift_type, severity, summary, diff_patch_json, acknowledged, created_at)
			  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		Params: []interface{}{
			diffID, vendorID, targetID, nil, snapshotID, driftType, result.Severity,
			result.Summary, string(patchJSON), timestamp,
		},
	})
	return err
}
