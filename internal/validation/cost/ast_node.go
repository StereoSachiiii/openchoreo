// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"github.com/google/cel-go/checker"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/types"
)

// PathAstNode implements checker.AstNode for path-based and typed size lookups.
type PathAstNode struct {
	PathParts []string
	NodeType  *types.Type
}

var _ checker.AstNode = PathAstNode{}

// Path returns the property path segments of the node.
func (p PathAstNode) Path() []string { return p.PathParts }

// Type returns the CEL type of the node.
func (p PathAstNode) Type() *types.Type { return p.NodeType }

// Expr returns nil because PathAstNode does not wrap a CEL AST expression.
func (p PathAstNode) Expr() celast.Expr { return nil }

// ComputedSize returns nil as size is computed by the cost estimator.
func (p PathAstNode) ComputedSize() *checker.SizeEstimate { return nil }

// NewPathAstNode creates an AstNode representing the given property path.
func NewPathAstNode(path ...string) checker.AstNode {
	return PathAstNode{PathParts: path}
}

// NewTypedAstNode creates an AstNode representing an un-pathed node with a known CEL type.
func NewTypedAstNode(t *types.Type, path ...string) checker.AstNode {
	return PathAstNode{PathParts: path, NodeType: t}
}
