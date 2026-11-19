// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package renderer

import (
	"context"

	"github.com/openchoreo/openchoreo/internal/limits"
	"github.com/openchoreo/openchoreo/internal/template"
)

// WithForEachBudget forwards to template.WithForEachBudget for backwards compatibility.
const maxForEachItems = int(limits.MaxForEachItemsPerLoop)
const maxForEachItemsPerRender = int(limits.MaxForEachItemsPerRender)

func WithForEachBudget(ctx context.Context) context.Context {
	return template.WithForEachBudget(ctx)
}

// ToIterableItems forwards to template.ToIterableItems for backwards compatibility.
func ToIterableItems(ctx context.Context, result any) ([]any, error) {
	return template.ToIterableItems(ctx, result)
}
