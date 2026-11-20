// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

// Package validate implements offline validation and static CEL cost checking for OpenChoreo templates.
package validate

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/openchoreo/openchoreo/internal/validation/cost"
)

// NewValidateCmd creates the offline `occ validate` command.
func NewValidateCmd() *cobra.Command {
	var (
		filePath     string
		celCostMode  string
		celCostLimit uint64
	)

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate OpenChoreo template manifests offline (schemas and static CEL cost)",
		Long: `Validate OpenChoreo template CRDs (ComponentType, ClusterComponentType, Trait,
ClusterTrait, EndpointType, ClusterEndpointType, ResourceType, ClusterResourceType,
Workflow, ClusterWorkflow) offline without a cluster connection.

Runs OpenAPI structural schema extraction and static CEL cost estimation, making it
suitable for local authoring feedback and GitOps PR CI pipelines.

Examples:
  # Validate a single template manifest
  occ validate -f samples/componenttypes/service.yaml

  # Validate all template manifests in a directory
  occ validate -f samples/

  # Emit warnings instead of failing on cost budget violations
  occ validate -f samples/ --cel-cost-mode=warn`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if filePath == "" {
				return fmt.Errorf("file or directory path is required (-f/--file)")
			}
			mode, err := parseMode(celCostMode)
			if err != nil {
				return err
			}
			return Run(cmd.Context(), Params{
				Path:         filePath,
				Mode:         mode,
				CELCostLimit: celCostLimit,
			}, cmd.OutOrStdout())
		},
	}

	cmd.Flags().StringVarP(&filePath, "file", "f", "", "Path to a YAML file or directory of manifests to validate")
	cmd.Flags().StringVar(&celCostMode, "cel-cost-mode", string(cost.ModeEnforce), "CEL cost enforcement mode (enforce, warn, off)")
	cmd.Flags().Uint64Var(&celCostLimit, "cel-cost-limit", 0, "Per-expression CEL cost limit (0 uses built-in default 1000000)")
	return cmd
}

func parseMode(raw string) (cost.Mode, error) {
	switch cost.Mode(raw) {
	case cost.ModeEnforce, cost.ModeWarn, cost.ModeOff:
		return cost.Mode(raw), nil
	default:
		return "", fmt.Errorf("invalid --cel-cost-mode %q: must be one of enforce, warn, off", raw)
	}
}
