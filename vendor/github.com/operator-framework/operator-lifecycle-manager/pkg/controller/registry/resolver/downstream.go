package resolver

import (
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/controller/registry/resolver/runtime_constraints"
)

func init() {
	initHooks = append(initHooks, OpenShiftStepResolverInit)
}

func OpenShiftStepResolverInit(stepResolver *OperatorStepResolver) error {
	if stepResolver == nil {
		return nil
	}

	// add it to the sat resolver
	if stepResolver.satResolver.runtimeConstraintsProvider != nil {
		panic("resolver already has a runtime constraint provider defined")
	}

	stepResolver.log.Debugf("*** Helloeeeeeee!!!!")

	// create the cluster version client
	clusterVersionClient := configv1client.New(stepResolver.kubeclient.Discovery().RESTClient())

	// create the runtime constraints provider
	runtimeConstraintProvider, err := runtime_constraints.NewOpenShiftRuntimeConstraintsProvider(clusterVersionClient)
	if err != nil {
		return err
	}

	stepResolver.satResolver.runtimeConstraintsProvider = runtimeConstraintProvider
	return nil
}
