package runtime_constraints

import (
	"encoding/json"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/controller/registry/resolver/cache"
	"github.com/operator-framework/operator-registry/pkg/registry"
	"io/ioutil"
	"os"
)

const (
	RuntimeConstraintEnvVarName = "RUNTIME_CONSTRAINTS"
)

type OCPRuntimeConstraintProvider struct {
	// the path to the constraints json file
	constraintsFile string
	constraints     []cache.Predicate
}

func NewFromEnv() (RuntimeConstraintsProvider, error) {
	runtimeConstraintsFilePath, isSet := os.LookupEnv(RuntimeConstraintEnvVarName)
	if !isSet {
		return nil, nil
	}
	return New(runtimeConstraintsFilePath)
}

func New(path string) (RuntimeConstraintsProvider, error) {
	provider := &OCPRuntimeConstraintProvider{
		constraintsFile: path,
		constraints:     nil,
	}
	if err := provider.load(); err != nil {
		return nil, err
	}
	return provider, nil
}

func (o *OCPRuntimeConstraintProvider) Constraints() ([]cache.Predicate, error) {
	return o.constraints, nil
}

func (o *OCPRuntimeConstraintProvider) load() error {
	propertiesFile, err := readRuntimeConstraintsYaml(o.constraintsFile)
	if err != nil {
		return nil
	}

	// Using package type to test with
	// We may only want to allow the generic constraint types once they are readym
	o.constraints = make([]cache.Predicate, 0)
	for _, property := range propertiesFile.Properties {
		rawMessage := []byte(property.Value)
		switch property.Type {
		case registry.PackageType:
			dep := registry.PackageDependency{}
			err := json.Unmarshal(rawMessage, &dep)
			if err != nil {
				return nil
			}
			o.constraints = append(o.constraints, cache.PkgPredicate(dep.PackageName))
		case registry.LabelType:
			dep := registry.LabelDependency{}
			err := json.Unmarshal(rawMessage, &dep)
			if err != nil {
				return nil
			}
			o.constraints = append(o.constraints, cache.LabelPredicate(dep.Label))
		}
	}
	return nil
}

func readRuntimeConstraintsYaml(yamlPath string) (*registry.PropertiesFile, error) {
	// Read file
	yamlFile, err := ioutil.ReadFile(yamlPath)
	if err != nil {
		return nil, err
	}

	// Parse yaml
	var propertiesFile = &registry.PropertiesFile{}
	err = json.Unmarshal(yamlFile, propertiesFile)
	if err != nil {
		return nil, err
	}

	return propertiesFile, nil
}
