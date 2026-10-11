package cost_test

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/cel-go/checker"
	"gopkg.in/yaml.v3"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel/model"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/pipeline/component/context"
	"github.com/openchoreo/openchoreo/internal/schema"
	"github.com/openchoreo/openchoreo/internal/template"
	"github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/component/cost"
	"github.com/openchoreo/openchoreo/internal/validation/component/decltype"
)

func TestE0d_DumpAllAbove10k(t *testing.T) {
	samplesDir := "../../../../samples"
	absPath, err := filepath.Abs(samplesDir)
	if err != nil {
		t.Fatalf("failed to resolve samples dir: %v", err)
	}

	var yamlFiles []string
	_ = filepath.Walk(absPath, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			yamlFiles = append(yamlFiles, path)
		}
		return nil
	})

	type ExpItem struct {
		Component  string
		ResourceID string
		Expression string
		Cost       uint64
		IsSat      bool
		Category   string
		Drivers    string
	}

	var above10k []ExpItem

	for _, file := range yamlFiles {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		docs := strings.Split(string(content), "\n---")
		for _, doc := range docs {
			var ct rawComponentTypeDoc
			if err := yaml.Unmarshal([]byte(doc), &ct); err != nil || (ct.Kind != "ComponentType" && ct.Kind != "ClusterComponentType") {
				continue
			}

			var paramStruct *apiextschema.Structural
			if len(ct.Spec.Parameters.OpenAPIV3Schema) > 0 {
				paramStruct, _ = schema.OpenAPIV3ToStructural(ct.Spec.Parameters.OpenAPIV3Schema)
			}
			var envConfigStruct *apiextschema.Structural
			if len(ct.Spec.EnvironmentConfigs.OpenAPIV3Schema) > 0 {
				envConfigStruct, _ = schema.OpenAPIV3ToStructural(ct.Spec.EnvironmentConfigs.OpenAPIV3Schema)
			}

			validator, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{
				ParametersSchema:         paramStruct,
				EnvironmentConfigsSchema: envConfigStruct,
			})
			if err != nil {
				continue
			}
			baseEnv := validator.GetBaseEnv()

			roots := make(map[string]*apiservercel.DeclType)
			if paramStruct != nil {
				roots["parameters"] = model.SchemaDeclType(paramStruct, false)
			} else {
				roots["parameters"] = apiservercel.NewObjectType("Parameters", map[string]*apiservercel.DeclField{})
			}
			if envConfigStruct != nil {
				roots["environmentConfigs"] = model.SchemaDeclType(envConfigStruct, false)
			} else {
				roots["environmentConfigs"] = apiservercel.NewObjectType("EnvironmentConfigs", map[string]*apiservercel.DeclField{})
			}
			schemaBasedFields := map[string]bool{"parameters": true, "environmentConfigs": true}
			reflectedFields := decltype.ExtractFields(reflect.TypeFor[context.ComponentContext](), schemaBasedFields)
			for _, rf := range reflectedFields {
				roots[rf.Name] = rf.DeclType
			}
			coster := cost.NewDeclTypeCostEstimator(roots)

			checkRecord := func(resID, expr string, costVal uint64) {
				if costVal > 10000 {
					cat := "platform-owned"
					drivers := "workload.endpoints"
					if strings.Contains(expr, "parameters.") {
						cat = "parameters"
						drivers = "parameters"
					} else if strings.Contains(expr, "environmentConfigs.") {
						cat = "environmentConfigs"
						drivers = "environmentConfigs"
					} else if strings.Contains(expr, "gateway.") {
						drivers = "gateway.ingress"
					} else if strings.Contains(expr, "envConfig.") {
						drivers = "envConfig.envs (configurations)"
					} else if strings.Contains(expr, "secretEnv.") {
						drivers = "secretEnv.envs (configurations)"
					}

					above10k = append(above10k, ExpItem{
						Component:  ct.Metadata.Name,
						ResourceID: resID,
						Expression: expr,
						Cost:       costVal,
						IsSat:      costVal == math.MaxUint64,
						Category:   cat,
						Drivers:    drivers,
					})
				}
			}

			// Validations
			allRules := append(ct.Spec.Validations, ct.Spec.PreRenderValidations...)
			for _, r := range allRules {
				if exprs, err := template.FindCELExpressions(r.Rule); err == nil {
					for _, expr := range exprs {
						if ast, issues := baseEnv.Compile(expr.InnerExpr); issues == nil || issues.Err() == nil {
							if c, err := checker.Cost(ast.NativeRep(), coster); err == nil {
								checkRecord("validation", expr.InnerExpr, c.Max)
							}
						}
					}
				}
			}

			// Resources
			for _, res := range ct.Spec.Resources {
				resEnv := baseEnv
				if res.IncludeWhen != "" {
					if ex, err := template.FindCELExpressions(res.IncludeWhen); err == nil {
						for _, e := range ex {
							if ast, issues := baseEnv.Compile(e.InnerExpr); issues == nil || issues.Err() == nil {
								if c, err := checker.Cost(ast.NativeRep(), coster); err == nil {
									checkRecord(res.ID+"/includeWhen", e.InnerExpr, c.Max)
								}
							}
						}
					}
				}
				if res.ForEach != "" {
					if ex, err := template.FindCELExpressions(res.ForEach); err == nil {
						for _, e := range ex {
							if ast, issues := baseEnv.Compile(e.InnerExpr); issues == nil || issues.Err() == nil {
								if c, err := checker.Cost(ast.NativeRep(), coster); err == nil {
									checkRecord(res.ID+"/forEach", e.InnerExpr, c.Max)
								}
							}
						}
					}
					if extEnv, err := validator.ResourceEnvironment(res.ForEach, res.Var); err == nil && extEnv != nil {
						resEnv = extEnv
					}
				}
				var bodyExprs []string
				if res.Template != nil {
					collectExpressions(res.Template, &bodyExprs)
				}
				for _, expr := range bodyExprs {
					if ast, issues := resEnv.Compile(expr); issues == nil || issues.Err() == nil {
						if c, err := checker.Cost(ast.NativeRep(), coster); err == nil {
							checkRecord(res.ID, expr, c.Max)
						}
					}
				}
			}
		}
	}

	t.Logf("TOTAL EXPRESSIONS ABOVE 10,000: %d", len(above10k))
	for i, item := range above10k {
		costStr := fmt.Sprintf("%d", item.Cost)
		if item.IsSat {
			costStr = "MaxUint64"
		}
		t.Logf("[%2d] %-30s | %-12s | Cat: %-15s | Drivers: %-25s | Expr: %s",
			i+1, item.Component+"/"+item.ResourceID, costStr, item.Category, item.Drivers, item.Expression)
	}
}
