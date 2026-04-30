package controllers

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func testScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(operatorsv1alpha1.AddToScheme(scheme))
	return scheme
}

func testClientBuilder() *fake.ClientBuilder {
	return fake.NewClientBuilder().WithScheme(testScheme())
}

func testReconciler(cl client.Client, cache *LifecycleCache) *LifecycleReconciler {
	return &LifecycleReconciler{
		Client:                     cl,
		Log:                        logr.Discard(),
		Cache:                      cache,
		Puller:                     nil, // Tests that need pulling inject a mock
		CatalogSourceLabelSelector: labels.Everything(),
		CatalogSourceFieldSelector: fields.Everything(),
	}
}

func newCatalogSource(name, namespace string, labelMap map[string]string) *operatorsv1alpha1.CatalogSource {
	return &operatorsv1alpha1.CatalogSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labelMap,
		},
		Spec: operatorsv1alpha1.CatalogSourceSpec{
			SourceType: operatorsv1alpha1.SourceTypeGrpc,
			Image:      "quay.io/test/catalog:latest",
		},
	}
}

func catalogPod(csName, namespace, nodeName, imgID string, phase corev1.PodPhase) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csName + "-pod",
			Namespace: namespace,
			Labels: map[string]string{
				catalogLabelKey: csName,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{Name: "registry"},
			},
		},
		Status: corev1.PodStatus{
			Phase: phase,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:    "registry",
					ImageID: imgID,
				},
			},
		},
	}
	if phase == corev1.PodRunning {
		pod.Status.Conditions = []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionTrue},
		}
	}
	return pod
}

// --- Pure function tests ---

func TestImageID(t *testing.T) {
	tt := []struct {
		name     string
		pod      *corev1.Pod
		expected string
	}{
		{
			name: "extract-content init container returns its ImageID",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					InitContainerStatuses: []corev1.ContainerStatus{
						{Name: "extract-content", ImageID: "sha256:abc123"},
					},
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "registry", ImageID: "sha256:def456"},
					},
				},
			},
			expected: "sha256:abc123",
		},
		{
			name: "no extract-content falls back to first container",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					InitContainerStatuses: []corev1.ContainerStatus{
						{Name: "other-init", ImageID: "sha256:other"},
					},
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "registry", ImageID: "sha256:def456"},
					},
				},
			},
			expected: "sha256:def456",
		},
		{
			name: "no container statuses returns empty",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{},
			},
			expected: "",
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, imageID(tc.pod))
		})
	}
}

// --- matchesCatalogSource tests ---

func TestMatchesCatalogSource(t *testing.T) {
	tt := []struct {
		name          string
		labelSelector string
		fieldSelector string
		cs            *operatorsv1alpha1.CatalogSource
		expected      bool
	}{
		{
			name:     "everything selectors match all",
			cs:       newCatalogSource("test", "test-ns", nil),
			expected: true,
		},
		{
			name:          "label selector matches",
			labelSelector: "env=prod",
			cs:            newCatalogSource("test", "test-ns", map[string]string{"env": "prod"}),
			expected:      true,
		},
		{
			name:          "label selector does not match",
			labelSelector: "env=prod",
			cs:            newCatalogSource("test", "test-ns", map[string]string{"env": "dev"}),
			expected:      false,
		},
		{
			name:          "field selector matches namespace",
			fieldSelector: "metadata.namespace=test-ns",
			cs:            newCatalogSource("test", "test-ns", nil),
			expected:      true,
		},
		{
			name:          "field selector does not match namespace",
			fieldSelector: "metadata.namespace=other-ns",
			cs:            newCatalogSource("test", "test-ns", nil),
			expected:      false,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			labelSel, err := labels.Parse(tc.labelSelector)
			require.NoError(t, err)
			fieldSel, err := fields.ParseSelector(tc.fieldSelector)
			require.NoError(t, err)

			r := testReconciler(nil, nil)
			r.CatalogSourceLabelSelector = labelSel
			r.CatalogSourceFieldSelector = fieldSel

			require.Equal(t, tc.expected, r.matchesCatalogSource(tc.cs))
		})
	}
}

// --- Reconcile tests ---

func TestReconcile_CatalogSourceNotFound_DeletesCache(t *testing.T) {
	cache := NewLifecycleCache()
	cache.Set("test-ns", "nonexistent", "sha256:old", nil)

	cl := testClientBuilder().Build()
	r := testReconciler(cl, cache)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "test-ns"},
	})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)

	_, found := cache.Get("test-ns", "nonexistent")
	require.False(t, found, "cache entry should be deleted when CatalogSource is not found")
}

func TestReconcile_CatalogSourceDoesNotMatchSelectors_DeletesCache(t *testing.T) {
	cache := NewLifecycleCache()
	cache.Set("test-ns", "test-catalog", "sha256:old", nil)

	cs := newCatalogSource("test-catalog", "test-ns", map[string]string{"env": "dev"})
	cl := testClientBuilder().WithObjects(cs).Build()

	labelSel, _ := labels.Parse("env=prod")
	r := testReconciler(cl, cache)
	r.CatalogSourceLabelSelector = labelSel

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-catalog", Namespace: "test-ns"},
	})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)

	_, found := cache.Get("test-ns", "test-catalog")
	require.False(t, found, "cache entry should be deleted when selectors don't match")
}

func TestReconcile_NoPodRunning_NoError(t *testing.T) {
	cache := NewLifecycleCache()
	cs := newCatalogSource("test-catalog", "test-ns", nil)
	cl := testClientBuilder().WithObjects(cs).Build()

	r := testReconciler(cl, cache)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-catalog", Namespace: "test-ns"},
	})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)
	require.Equal(t, 0, cache.Len())
}

func TestReconcile_CacheHit_SkipsPull(t *testing.T) {
	cache := NewLifecycleCache()
	cache.Set("test-ns", "test-catalog", "sha256:abc123", nil)

	cs := newCatalogSource("test-catalog", "test-ns", nil)
	pod := catalogPod("test-catalog", "test-ns", "worker-1", "sha256:abc123", corev1.PodRunning)
	cl := testClientBuilder().WithObjects(cs, pod).Build()

	r := testReconciler(cl, cache)
	// Puller is nil — if it tried to pull, it would panic

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-catalog", Namespace: "test-ns"},
	})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)
}

// --- getCatalogImageRef tests ---

func TestGetCatalogImageRef(t *testing.T) {
	ctx := context.Background()

	tt := []struct {
		name          string
		pods          []*corev1.Pod
		expectedImage string
		expectErr     bool
	}{
		{
			name:          "no pods returns empty",
			pods:          nil,
			expectedImage: "",
		},
		{
			name: "running pod with digest",
			pods: []*corev1.Pod{
				catalogPod("test-catalog", "test-ns", "worker-1", "sha256:abc123", corev1.PodRunning),
			},
			expectedImage: "sha256:abc123",
		},
		{
			name: "pending pod is skipped",
			pods: []*corev1.Pod{
				catalogPod("test-catalog", "test-ns", "worker-1", "sha256:abc123", corev1.PodPending),
			},
			expectedImage: "",
		},
		{
			name: "running pod with empty imageID is skipped",
			pods: []*corev1.Pod{
				catalogPod("test-catalog", "test-ns", "worker-1", "", corev1.PodRunning),
			},
			expectedImage: "",
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			builder := testClientBuilder()
			for _, p := range tc.pods {
				builder = builder.WithObjects(p)
			}
			cl := builder.Build()

			r := testReconciler(cl, nil)
			cs := newCatalogSource("test-catalog", "test-ns", nil)

			image, err := r.getCatalogImageRef(ctx, cs)
			if tc.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expectedImage, image)
		})
	}
}

func TestGetCatalogImageRef_ListError(t *testing.T) {
	listErr := fmt.Errorf("simulated list error")
	cl := testClientBuilder().
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
				return listErr
			},
		}).
		Build()

	r := testReconciler(cl, nil)
	cs := newCatalogSource("test-catalog", "test-ns", nil)

	_, err := r.getCatalogImageRef(context.Background(), cs)
	require.Error(t, err)
	require.ErrorIs(t, err, listErr)
}

// --- catalogPodPredicate tests ---

func TestCatalogPodPredicate(t *testing.T) {
	pred := catalogPodPredicate()

	basePod := func() *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "test-ns",
				Labels:    map[string]string{catalogLabelKey: "test-catalog"},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "registry", ImageID: "sha256:abc123"},
				},
			},
		}
	}

	t.Run("create events always pass", func(t *testing.T) {
		require.True(t, pred.Create(event.CreateEvent{Object: basePod()}))
	})

	t.Run("delete events always pass", func(t *testing.T) {
		require.True(t, pred.Delete(event.DeleteEvent{Object: basePod()}))
	})

	t.Run("update: phase change passes", func(t *testing.T) {
		old := basePod()
		new := basePod()
		new.Status.Phase = corev1.PodSucceeded
		require.True(t, pred.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: new}))
	})

	t.Run("update: imageID change passes", func(t *testing.T) {
		old := basePod()
		new := basePod()
		new.Status.ContainerStatuses[0].ImageID = "sha256:def456"
		require.True(t, pred.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: new}))
	})

	t.Run("update: no relevant change filtered", func(t *testing.T) {
		old := basePod()
		new := basePod()
		new.Annotations = map[string]string{"foo": "bar"}
		require.False(t, pred.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: new}))
	})
}

// --- mapPodToCatalogSource tests ---

func TestMapPodToCatalogSource(t *testing.T) {
	tt := []struct {
		name     string
		obj      client.Object
		expected []reconcile.Request
	}{
		{
			name: "pod with catalog label enqueues request",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod", Namespace: "olm",
					Labels: map[string]string{catalogLabelKey: "my-catalog"},
				},
			},
			expected: []reconcile.Request{
				{NamespacedName: types.NamespacedName{Name: "my-catalog", Namespace: "olm"}},
			},
		},
		{
			name: "pod without catalog label is ignored",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod", Namespace: "olm",
				},
			},
			expected: nil,
		},
		{
			name:     "non-pod object is ignored",
			obj:      &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "olm"}},
			expected: nil,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, mapPodToCatalogSource(context.Background(), tc.obj))
		})
	}
}

// --- isDigest tests ---

func TestIsDigest(t *testing.T) {
	require.True(t, isDigest("quay.io/test/image@sha256:abc123"))
	require.False(t, isDigest("quay.io/test/image:latest"))
	require.False(t, isDigest("quay.io/test/image"))
}

// --- Cache tests ---

func TestLifecycleCache(t *testing.T) {
	c := NewLifecycleCache()

	_, found := c.Get("ns", "name")
	require.False(t, found)

	c.Set("ns", "name", "sha256:abc", nil)
	entry, found := c.Get("ns", "name")
	require.True(t, found)
	require.Equal(t, "sha256:abc", entry.ImageDigest)
	require.Nil(t, entry.Data)
	require.Equal(t, 1, c.Len())

	c.Delete("ns", "name")
	_, found = c.Get("ns", "name")
	require.False(t, found)
	require.Equal(t, 0, c.Len())
}
