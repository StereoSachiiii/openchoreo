// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package template

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestToIterableItems(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    []any
		wantErr bool
	}{
		{
			name:    "array of strings",
			input:   []any{"a", "b", "c"},
			want:    []any{"a", "b", "c"},
			wantErr: false,
		},
		{
			name:    "array of maps",
			input:   []any{map[string]any{"port": 80}, map[string]any{"port": 443}},
			want:    []any{map[string]any{"port": 80}, map[string]any{"port": 443}},
			wantErr: false,
		},
		{
			name: "slice of map[string]any",
			input: []map[string]any{
				{"name": "http", "port": 80},
				{"name": "https", "port": 443},
			},
			want: []any{
				map[string]any{"name": "http", "port": 80},
				map[string]any{"name": "https", "port": 443},
			},
			wantErr: false,
		},
		{
			name: "map with string keys",
			input: map[string]any{
				"beta":  "second",
				"alpha": "first",
			},
			want: []any{
				map[string]any{"key": "alpha", "value": "first"},
				map[string]any{"key": "beta", "value": "second"},
			},
			wantErr: false,
		},
		{
			name:    "empty array",
			input:   []any{},
			want:    []any{},
			wantErr: false,
		},
		{
			name:    "empty map",
			input:   map[string]any{},
			want:    []any{},
			wantErr: false,
		},
		{
			name:    "nil input",
			input:   nil,
			want:    nil,
			wantErr: true,
		},
		{
			name:    "string input (invalid)",
			input:   "not an array or map",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "integer input (invalid)",
			input:   42,
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ToIterableItems(t.Context(), tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ToIterableItems() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ToIterableItems() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestToIterableItemsRejectsOversizedForEach(t *testing.T) {
	buildSliceAny := func(n int) any {
		out := make([]any, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	buildSliceMap := func(n int) any {
		out := make([]map[string]any, n)
		for i := range out {
			out[i] = map[string]any{"index": i}
		}
		return out
	}
	buildMap := func(n int) any {
		out := make(map[string]any, n)
		for i := 0; i < n; i++ {
			out[fmt.Sprintf("k%04d", i)] = i
		}
		return out
	}

	cases := []struct {
		name  string
		build func(int) any
	}{
		{"slice-of-any", buildSliceAny},
		{"slice-of-map", buildSliceMap},
		{"map-of-any", buildMap},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ToIterableItems(t.Context(), tc.build(maxForEachItems))
			if err != nil {
				t.Fatalf("ToIterableItems() with %d items: unexpected error = %v", maxForEachItems, err)
			}
			if len(got) != maxForEachItems {
				t.Fatalf("ToIterableItems() returned %d items, want %d", len(got), maxForEachItems)
			}

			over := maxForEachItems + 1
			got, err = ToIterableItems(t.Context(), tc.build(over))
			if err == nil {
				t.Fatalf("ToIterableItems() with %d items: expected error, got %d items", over, len(got))
			}
			want := fmt.Sprintf("forEach expanded to %d items, exceeding the limit of %d", over, maxForEachItems)
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("ToIterableItems() error = %v, want it to contain %q", err, want)
			}
			if !IsTerminalRenderError(err) {
				t.Errorf("ToIterableItems() cap breach must classify as terminal, got %v", err)
			}
			if got != nil {
				t.Errorf("ToIterableItems() returned %d items alongside the error, want nil", len(got))
			}
		})
	}
}

func TestToIterableItemsRejectsCumulativeExpansion(t *testing.T) {
	ctx := WithForEachBudget(t.Context())
	items := make([]any, maxForEachItems)

	// Fill up to maxForEachItemsPerRender
	var total int
	for total+maxForEachItems <= maxForEachItemsPerRender {
		if _, err := ToIterableItems(ctx, items); err != nil {
			t.Fatalf("expansion at total %d unexpectedly exceeded limit: %v", total, err)
		}
		total += maxForEachItems
	}
	if rem := maxForEachItemsPerRender - total; rem > 0 {
		remItems := make([]any, rem)
		if _, err := ToIterableItems(ctx, remItems); err != nil {
			t.Fatalf("remainder expansion unexpectedly failed: %v", err)
		}
	}

	_, err := ToIterableItems(ctx, []any{"one more"})
	if err == nil {
		t.Fatalf("ToIterableItems() allowed expansion past cumulative cap of %d", maxForEachItemsPerRender)
	}
	want := fmt.Sprintf("exceed the cumulative limit of %d", maxForEachItemsPerRender)
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("ToIterableItems() error = %v, want it to contain %q", err, want)
	}
	if !IsTerminalRenderError(err) {
		t.Errorf("cumulative cap breach must classify as terminal, got %v", err)
	}
}

func TestRenderForEach(t *testing.T) {
	e := NewEngine()
	inputs := map[string]any{
		"items": []any{"apple", "banana"},
	}

	contexts, err := e.RenderForEach(t.Context(), "${items}", "fruit", inputs)
	if err != nil {
		t.Fatalf("RenderForEach failed: %v", err)
	}
	if len(contexts) != 2 {
		t.Fatalf("expected 2 contexts, got %d", len(contexts))
	}
	if contexts[0]["fruit"] != "apple" || contexts[1]["fruit"] != "banana" {
		t.Errorf("unexpected contexts: %v", contexts)
	}
}
