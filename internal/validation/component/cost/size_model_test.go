// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/openchoreo/openchoreo/internal/validation/component/cost"
)

func TestSizeModel_TruthTable(t *testing.T) {
	known10 := cost.SizeBound{Max: 10, State: cost.StateKnown, Origin: cost.OriginEnforced}
	known20 := cost.SizeBound{Max: 20, State: cost.StateKnown, Origin: cost.OriginEnforced}
	knownZeroDeclared := cost.SizeBound{Max: 0, State: cost.StateKnown, Origin: cost.OriginDeclared}
	knownZeroDefault := cost.SizeBound{Max: 0, State: cost.StateKnown, Origin: cost.OriginDefault}
	unknown := cost.SizeBound{Max: math.MaxUint64, State: cost.StateUnknown, Origin: cost.OriginDefault}

	t.Run("Addition: KNOWN + KNOWN = KNOWN", func(t *testing.T) {
		res := cost.AddBounds(known10, known20)
		assert.Equal(t, cost.StateKnown, res.State)
		assert.Equal(t, uint64(30), res.Max)
	})

	t.Run("Addition: KNOWN + UNKNOWN = UNKNOWN", func(t *testing.T) {
		res := cost.AddBounds(known10, unknown)
		assert.Equal(t, cost.StateUnknown, res.State)
	})

	t.Run("Multiplication: KNOWN * KNOWN = KNOWN", func(t *testing.T) {
		res := cost.MultiplyBounds(known10, known20)
		assert.Equal(t, cost.StateKnown, res.State)
		assert.Equal(t, uint64(200), res.Max)
	})

	t.Run("Multiplication: KNOWN(0, DECLARED) * UNKNOWN = KNOWN(0)", func(t *testing.T) {
		res := cost.MultiplyBounds(knownZeroDeclared, unknown)
		assert.Equal(t, cost.StateKnown, res.State)
		assert.Equal(t, uint64(0), res.Max)
	})

	t.Run("Multiplication: KNOWN(0, DEFAULT) * UNKNOWN = UNKNOWN", func(t *testing.T) {
		res := cost.MultiplyBounds(knownZeroDefault, unknown)
		assert.Equal(t, cost.StateUnknown, res.State)
	})

	t.Run("Multiplication: KNOWN(>0) * UNKNOWN = UNKNOWN", func(t *testing.T) {
		res := cost.MultiplyBounds(known10, unknown)
		assert.Equal(t, cost.StateUnknown, res.State)
	})

	t.Run("Clamping: values >= 2^63 become UNKNOWN", func(t *testing.T) {
		huge := cost.SizeBound{Max: 1 << 62, State: cost.StateKnown, Origin: cost.OriginEnforced}
		res := cost.AddBounds(huge, huge) // 2^63
		assert.Equal(t, cost.StateUnknown, res.State)
	})
}
