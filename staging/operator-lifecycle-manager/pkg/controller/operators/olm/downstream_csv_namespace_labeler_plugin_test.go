package olm

import (
	"context"
	"errors"
	"testing"

	"github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/lib/kubestate"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/typed/core/v1/fake"
	clienttesting "k8s.io/client-go/testing"
)

func NewFakeCSVNamespaceLabelerPlugin(t *testing.T, options ...fakeOperatorOption) *csvNamespaceLabelerPlugin {
	// the plug in is already injected as part of the operator start up
	// see pkg/controller/operators/olm/downstream_plugins.go
	operator, err := NewFakeOperator(context.Background(), options...)
	if err != nil {
		t.Fatalf("error creating fake operator: %s", err)
	}

	// extract initialised fake plug-in
	for _, plugin := range operator.plugins {
		if _, ok := plugin.(*csvNamespaceLabelerPlugin); ok {
			return plugin.(*csvNamespaceLabelerPlugin)
		}
	}

	t.Fatalf("csv namespace labeler plugin not found")
	return nil
}

func NewCsvInNamespace(namespace string) *v1alpha1.ClusterServiceVersion {
	return &v1alpha1.ClusterServiceVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-csv",
			Namespace: namespace,
		},
	}
}

func NewCopiedCsvInNamespace(namespace string) *v1alpha1.ClusterServiceVersion {
	csv := NewCsvInNamespace(namespace)
	csv.SetLabels(map[string]string{
		v1alpha1.CopiedLabelKey: "true",
	})
	return csv
}

func NewNamespace(name string) *v1.Namespace {
	return &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

func NewLabeledNamespace(name string, labelValue string) *v1.Namespace {
	ns := NewNamespace(name)
	ns.SetLabels(map[string]string{
		NamespaceLabelSyncerLabelKey: labelValue,
	})
	return ns
}

func Test_SyncIgnoresDeletionEvent(t *testing.T) {
	// Sync ignores deletion events
	namespace := "test-namespace"
	plugin := NewFakeCSVNamespaceLabelerPlugin(t, withK8sObjs(NewNamespace(namespace)))
	event := kubestate.NewResourceEvent(kubestate.ResourceDeleted, NewCsvInNamespace(namespace))
	assert.Nil(t, plugin.Sync(context.Background(), event))

	ns, err := plugin.operator.opClient.KubernetesInterface().CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
	assert.NoError(t, err)
	assert.NotContains(t, ns.GetLabels(), NamespaceLabelSyncerLabelKey)
}

func Test_SyncIgnoresCopiedCsvs(t *testing.T) {
	// Sync ignores copied csvs
	namespace := "openshift-test"
	plugin := NewFakeCSVNamespaceLabelerPlugin(t, withK8sObjs(NewNamespace(namespace)))
	event := kubestate.NewResourceEvent(kubestate.ResourceAdded, NewCopiedCsvInNamespace(namespace))
	assert.Nil(t, plugin.Sync(context.Background(), event))

	ns, err := plugin.operator.opClient.KubernetesInterface().CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
	assert.NoError(t, err)
	assert.NotContains(t, ns.GetLabels(), NamespaceLabelSyncerLabelKey)
}

func Test_SyncIgnoresNonOpenshiftNamespaces(t *testing.T) {
	// Sync ignores non-openshift namespaces
	namespace := "test-namespace"
	plugin := NewFakeCSVNamespaceLabelerPlugin(t, withK8sObjs(NewNamespace(namespace)))
	event := kubestate.NewResourceEvent(kubestate.ResourceAdded, NewCopiedCsvInNamespace(namespace))
	assert.Nil(t, plugin.Sync(context.Background(), event))

	ns, err := plugin.operator.opClient.KubernetesInterface().CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
	assert.NoError(t, err)
	assert.NotContains(t, ns.GetLabels(), NamespaceLabelSyncerLabelKey)
}

func Test_SyncIgnoresPayloadOpenshiftNamespacesExceptOperators(t *testing.T) {
	// Sync ignores payload openshift namespaces, except openshift-operators
	// openshift-monitoring sync -> no label
	plugin := NewFakeCSVNamespaceLabelerPlugin(t, withK8sObjs(NewNamespace("openshift-monitoring"), NewNamespace("openshift-operators")))
	event := kubestate.NewResourceEvent(kubestate.ResourceAdded, NewCsvInNamespace("openshift-monitoring"))
	assert.Nil(t, plugin.Sync(context.Background(), event))

	ns, err := plugin.operator.opClient.KubernetesInterface().CoreV1().Namespaces().Get(context.Background(), "openshift-monitoring", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.NotContains(t, ns.GetLabels(), NamespaceLabelSyncerLabelKey)

	// openshift-operators sync -> label added
	event = kubestate.NewResourceEvent(kubestate.ResourceAdded, NewCsvInNamespace("openshift-operators"))
	assert.Nil(t, plugin.Sync(context.Background(), event))
	ns, err = plugin.operator.opClient.KubernetesInterface().CoreV1().Namespaces().Get(context.Background(), "openshift-operators", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Equal(t, "true", ns.GetLabels()[NamespaceLabelSyncerLabelKey])
}

func Test_SyncIgnoresAlreadyLabeledNonPayloadOpenshiftNamespaces(t *testing.T) {
	// Sync ignores non-payload openshift namespaces that are already labeled
	labelValues := []string{"true", "false", " ", "", "gibberish"}
	namespace := "openshift-test"

	for _, labelValue := range labelValues {

		plugin := NewFakeCSVNamespaceLabelerPlugin(t, withK8sObjs(NewLabeledNamespace(namespace, labelValue)))
		event := kubestate.NewResourceEvent(kubestate.ResourceUpdated, NewCsvInNamespace(namespace))
		assert.Nil(t, plugin.Sync(context.Background(), event))

		ns, err := plugin.operator.opClient.KubernetesInterface().CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Equal(t, labelValue, ns.GetLabels()[NamespaceLabelSyncerLabelKey])
	}
}

func Test_SyncLabelsNonPayloadUnlabeledOpenshiftNamespaces(t *testing.T) {
	// Sync will label non-labeled non-payload openshift- namespaces independent of event type (except deletion, tested separately)
	eventTypes := []kubestate.ResourceEventType{kubestate.ResourceUpdated, kubestate.ResourceAdded}
	namespace := "openshift-test"

	for _, eventType := range eventTypes {
		plugin := NewFakeCSVNamespaceLabelerPlugin(t, withK8sObjs(NewNamespace(namespace)))
		event := kubestate.NewResourceEvent(eventType, NewCsvInNamespace(namespace))
		assert.Nil(t, plugin.Sync(context.Background(), event))

		ns, err := plugin.operator.opClient.KubernetesInterface().CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
		assert.NoError(t, err)
		assert.Contains(t, ns.GetLabels(), NamespaceLabelSyncerLabelKey)
	}
}

func Test_SyncFailsIfEventResourceIsNotCSV(t *testing.T) {
	// Sync fails if resource is not a csv
	plugin := NewFakeCSVNamespaceLabelerPlugin(t)
	event := kubestate.NewResourceEvent(kubestate.ResourceAdded, NewNamespace("some-namespaces"))
	assert.Error(t, plugin.Sync(context.Background(), event))
}

func Test_SyncFailsIfNamespaceNotFound(t *testing.T) {
	// Sync fails if the namespace is not found
	plugin := NewFakeCSVNamespaceLabelerPlugin(t)
	event := kubestate.NewResourceEvent(kubestate.ResourceAdded, NewCsvInNamespace("openshift-test"))
	assert.Error(t, plugin.Sync(context.Background(), event))
}

func Test_SyncFailsIfCSVCannotBeUpdated(t *testing.T) {
	// Sync fails if the namespace cannot be updated
	namespace := "openshift-test"
	plugin := NewFakeCSVNamespaceLabelerPlugin(t, withK8sObjs(NewNamespace(namespace)))
	event := kubestate.NewResourceEvent(kubestate.ResourceAdded, NewCsvInNamespace(namespace))
	updateNsError := func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, &v1.Namespace{}, errors.New("error updating namespace")
	}
	plugin.operator.opClient.KubernetesInterface().CoreV1().(*fake.FakeCoreV1).PrependReactor("update", "namespaces", updateNsError)
	assert.Error(t, plugin.Sync(context.Background(), event))
}
