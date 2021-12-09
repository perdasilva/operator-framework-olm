package resolver

import (
	"fmt"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/controller/registry/resolver/runtime_constraints"
)

func init() {
	fmt.Println("Resolver init")
	runtimeConstraintProvider, err := runtime_constraints.NewFromEnv()
	if err != nil {
		panic(err)
	}

	// Configure the sat solver to always include the cluster runtime constraints
	fmt.Println("***** Setting cluster runtime constraints provider")
	resolverOptions = []SatSolverOption{
		WithRuntimeConstraintsProvider(runtimeConstraintProvider),
	}
}
