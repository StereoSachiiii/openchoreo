// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package template

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"sync"

	"github.com/openchoreo/openchoreo/internal/limits"
)

// maxForEachItems bounds how many items a single forEach may expand to. Each item
// drives a full render, so an unbounded tenant-supplied collection amplifies work far
// beyond what the per-expression CEL cost limit can see.
const maxForEachItems = int(limits.MaxForEachItemsPerLoop)

// maxForEachItemsPerRender bounds the sum of every forEach expansion in one pipeline
// render. The per-loop cap prevents one large fan-out; this cap prevents many legal
// loops with cheap or static bodies from recreating the same workload in total.
const maxForEachItemsPerRender = int(limits.MaxForEachItemsPerRender)

type forEachBudget struct {
	mu    sync.Mutex
	spent int
}

type forEachBudgetKey struct{}

// WithForEachBudget returns a context carrying a render-scoped cumulative expansion
// budget. An existing budget is preserved so nested pipeline work shares the same total.
func WithForEachBudget(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(forEachBudgetKey{}).(*forEachBudget); ok {
		return ctx
	}
	return context.WithValue(ctx, forEachBudgetKey{}, &forEachBudget{})
}

// CheckForEachLimit rejects a forEach result whose item count exceeds maxForEachItems.
// Callers must invoke it before copying the result or fanning out sub-renders over it, so
// an oversized collection costs no more than the length check.
func CheckForEachLimit(ctx context.Context, n int) error {
	if n > maxForEachItems {
		return fmt.Errorf("%w: forEach expanded to %d items, exceeding the limit of %d",
			ErrExpansionLimitExceeded, n, maxForEachItems)
	}
	if ctx == nil {
		return nil
	}

	budget, ok := ctx.Value(forEachBudgetKey{}).(*forEachBudget)
	if !ok {
		return nil
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.spent > maxForEachItemsPerRender-n {
		budget.spent = maxForEachItemsPerRender + 1
		return fmt.Errorf("%w: forEach expansions would exceed the cumulative limit of %d items in one render",
			ErrExpansionLimitExceeded, maxForEachItemsPerRender)
	}
	budget.spent += n
	return nil
}

// ToIterableItems converts a forEach evaluation result to a slice of items.
// Arrays are returned as-is. Maps are converted to sorted slice of {key, value} entries.
//
// Supported input types:
//   - []any - returned as-is
//   - []map[string]any - converted to []any
//   - map[string]any - converted to sorted slice of {"key": k, "value": v} entries
//
// For maps, the keys are sorted alphabetically to ensure deterministic iteration order.
//
// Results with more than maxForEachItems entries are rejected, and contexts created by
// WithForEachBudget also enforce the cumulative per-render cap.
func ToIterableItems(ctx context.Context, result any) ([]any, error) {
	// Step 1: count the items. Unsupported shapes are rejected here.
	n, err := forEachLen(result)
	if err != nil {
		return nil, err
	}

	// Step 2: enforce the per-loop and cumulative caps BEFORE allocating anything,
	// so an oversized collection costs no more than the length check.
	if err := CheckForEachLimit(ctx, n); err != nil {
		return nil, err
	}

	// Step 3: normalize to []any.
	switch v := result.(type) {
	case []any:
		return v, nil
	case []map[string]any:
		return sliceOfMapsToItems(v), nil
	case map[string]any:
		return mapToSortedEntries(v), nil
	}
	return nil, nil // unreachable: forEachLen already rejected other types
}

// forEachLen returns the number of items a forEach result will expand to.
func forEachLen(result any) (int, error) {
	switch v := result.(type) {
	case []any:
		return len(v), nil
	case []map[string]any:
		return len(v), nil
	case map[string]any:
		return len(v), nil
	default:
		return 0, fmt.Errorf("forEach must evaluate to array or map, got %T", result)
	}
}

// sliceOfMapsToItems widens []map[string]any to []any.
func sliceOfMapsToItems(v []map[string]any) []any {
	items := make([]any, len(v))
	for i, m := range v {
		items[i] = m
	}
	return items
}

// mapToSortedEntries turns a map into {"key", "value"} entries ordered by key,
// so iteration (and therefore rendered output) is deterministic across runs.
func mapToSortedEntries(v map[string]any) []any {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	items := make([]any, len(keys))
	for i, k := range keys {
		items[i] = map[string]any{"key": k, "value": v[k]}
	}
	return items
}

// RenderForEach evaluates a forEach CEL expression, bounds its size against both the
// per-loop limit and the cumulative render budget, and produces iteration contexts.
// varName defaults to "item" if empty.
func (e *Engine) RenderForEach(
	ctx context.Context,
	forEach string,
	varName string,
	celContext map[string]any,
) ([]map[string]any, error) {
	result, err := e.Render(ctx, forEach, celContext)
	if err != nil {
		return nil, err
	}

	items, err := ToIterableItems(ctx, result)
	if err != nil {
		return nil, err
	}

	if varName == "" {
		varName = "item"
	}

	contexts := make([]map[string]any, len(items))
	for i, item := range items {
		itemContext := maps.Clone(celContext)
		itemContext[varName] = item
		contexts[i] = itemContext
	}

	return contexts, nil
}
