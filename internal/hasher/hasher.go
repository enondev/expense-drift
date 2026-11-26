package hasher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"expensedrift/internal/model"
)

// separator joins components of the composite audit hash.
const separator = "|"

// RawContentHash returns the SHA-256 hex digest of normalized raw page content.
// NormalizeRaw strips nonce/csrf attributes, HTML comments, and collapses
// whitespace so dynamically injected attributes do not change the hash.
func RawContentHash(raw string) string {
	sum := sha256.Sum256([]byte(model.NormalizeRaw(raw)))
	return hex.EncodeToString(sum[:])
}

// CanonicalJSON returns deterministic JSON bytes for a normalized BillingRuleSet.
// The ruleset must be Normalize()'d before calling so slices are sorted.
func CanonicalJSON(ruleset *model.BillingRuleSet) ([]byte, error) {
	ruleset.Normalize()
	return json.Marshal(ruleset)
}

// SemanticSchemaHash returns the SHA-256 hex digest over the canonicalized
// BillingRuleSet JSON. Only fires a billing_diffs alert when this changes,
// eliminating CSS-redesign false positives.
func SemanticSchemaHash(ruleset *model.BillingRuleSet) (string, error) {
	b, err := CanonicalJSON(ruleset)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// CompositeAuditHash returns the SHA-256 hex digest chaining the previous
// composite hash into a tamper-evident per-target audit chain.
// Formula: SHA256(vendor_id|prev_composite_hash|raw|semantic|timestamp)
func CompositeAuditHash(vendorID, prevComposite, rawHash, semHash, timestamp string) string {
	components := []string{vendorID, prevComposite, rawHash, semHash, timestamp}
	sum := sha256.Sum256([]byte(strings.Join(components, separator)))
	return hex.EncodeToString(sum[:])
}

// Cascade computes all three hashes in order and returns them. prevComposite
// may be empty for the first snapshot of a target.
func Cascade(vendorID, prevComposite, rawContent, timestamp string, ruleset *model.BillingRuleSet) (raw, sem, comp string, err error) {
	raw = RawContentHash(rawContent)
	sem, err = SemanticSchemaHash(ruleset)
	if err != nil {
		return "", "", "", err
	}
	comp = CompositeAuditHash(vendorID, prevComposite, raw, sem, timestamp)
	return raw, sem, comp, nil
}

// ValidateCascadeRecomputation recomputes the cascade and returns whether the
// stored hashes match, used for hash-chain recovery.
func ValidateCascadeRecomputation(vendorID, prevComposite, rawContent, timestamp, storedRaw, storedSem, storedComp string, ruleset *model.BillingRuleSet) (bool, error) {
	raw, sem, comp, err := Cascade(vendorID, prevComposite, rawContent, timestamp, ruleset)
	if err != nil {
		return false, err
	}
	return raw == storedRaw && sem == storedSem && comp == storedComp, nil
}
