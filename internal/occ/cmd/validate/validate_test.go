// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package validate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openchoreo/openchoreo/internal/validation/cost"
)

const validBoundedCT = `
apiVersion: openchoreo.dev/v1alpha1
kind: ComponentType
metadata:
  name: web-service
spec:
  workloadType: deployment
  parameters:
    openAPIV3Schema:
      type: object
      properties:
        replicas:
          type: integer
        tags:
          type: array
          maxItems: 10
          items:
            type: string
  resources:
    - id: cm
      template:
        apiVersion: v1
        kind: ConfigMap
        metadata:
          name: ${metadata.name}
        data:
          count: ${string(parameters.tags.map(t, t).size())}
`

const overBudgetUnboundedCT = `
apiVersion: openchoreo.dev/v1alpha1
kind: ComponentType
metadata:
  name: expensive-service
spec:
  workloadType: deployment
  parameters:
    openAPIV3Schema:
      type: object
      properties:
        tags:
          type: array
          items:
            type: string
  resources:
    - id: cm
      template:
        apiVersion: v1
        kind: ConfigMap
        metadata:
          name: ${metadata.name}
        data:
          count: ${string(parameters.tags.map(x, parameters.tags.filter(y, y == x)).size())}
`

const nonTemplateAndWorkflowMultiDoc = `
apiVersion: v1
kind: Namespace
metadata:
  name: default
---
apiVersion: openchoreo.dev/v1alpha1
kind: Workflow
metadata:
  name: build-wf
spec:
  parameters:
    openAPIV3Schema:
      type: object
      properties:
        image:
          type: string
  runTemplate:
    apiVersion: batch/v1
    kind: Job
    spec:
      template:
        spec:
          containers:
            - name: build
              image: ${parameters.image}
`

func TestValidate_SingleFile_BoundedPasses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ct.yaml")
	require.NoError(t, os.WriteFile(path, []byte(validBoundedCT), 0o600))

	var out bytes.Buffer
	err := Run(context.Background(), Params{Path: path, Mode: cost.ModeEnforce}, &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "OK   ComponentType/web-service")
	assert.Contains(t, out.String(), "Validated 1 template(s) successfully")
}

func TestValidate_EnforceRejectsOverBudget_WarnAdmits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(overBudgetUnboundedCT), 0o600))

	var enforceOut bytes.Buffer
	err := Run(context.Background(), Params{Path: path, Mode: cost.ModeEnforce}, &enforceOut)
	require.Error(t, err)
	assert.Contains(t, enforceOut.String(), "FAIL ComponentType/expensive-service")
	assert.Contains(t, enforceOut.String(), "maxItems")

	var warnOut bytes.Buffer
	err = Run(context.Background(), Params{Path: path, Mode: cost.ModeWarn}, &warnOut)
	require.NoError(t, err)
	assert.Contains(t, warnOut.String(), "WARN ComponentType/expensive-service")
	assert.Contains(t, warnOut.String(), "OK   ComponentType/expensive-service")
}

func TestValidate_DirectoryAndMultiDocSkipsNonTemplates(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(validBoundedCT), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.yml"), []byte(nonTemplateAndWorkflowMultiDoc), 0o600))

	var out bytes.Buffer
	err := Run(context.Background(), Params{Path: dir, Mode: cost.ModeEnforce}, &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "OK   ComponentType/web-service")
	assert.Contains(t, out.String(), "OK   Workflow/build-wf")
	assert.Contains(t, out.String(), "Validated 2 template(s) successfully")
}

func TestNewValidateCmd_FlagsAndExecution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ct.yaml")
	require.NoError(t, os.WriteFile(path, []byte(validBoundedCT), 0o600))

	cmd := NewValidateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"-f", path, "--cel-cost-mode=enforce"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "Validated 1 template(s) successfully")
}
