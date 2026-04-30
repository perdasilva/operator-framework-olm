//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/operator-framework/operator-lifecycle-manager/test/e2e/ctx"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	lcAppLabelKey             = "app"
	lcAppLabelVal             = "olm-lifecycle-server"
	lcCatalogNameLabelKey     = "olm.lifecycle-server/catalog-name"
	lcCRBName                 = "operator-lifecycle-manager-lifecycle-server"
	lcResourceTimeout         = 5 * time.Minute
	lcCleanupTimeout          = 2 * time.Minute
	lcCatalogImage            = "quay.io/olmtest/lifecycle-catalog:v1"
	lcCatalogNoLifecycleImage = "quay.io/olmtest/lifecycle-catalog-no-lifecycle:v1"
	lcNamespace               = "openshift-marketplace"
)

// lcResourceName mirrors the controller's resourceName logic for test assertions.
func lcResourceName(csName string) string {
	return csName + "-lifecycle-server"
}

// createCatalogSourceForLifecycle creates a CatalogSource in openshift-marketplace.
func createCatalogSourceForLifecycle(name, namespace, image string) (*v1alpha1.CatalogSource, func()) {
	crc := ctx.Ctx().OperatorClient()
	cs := &v1alpha1.CatalogSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1alpha1.CatalogSourceSpec{
			SourceType:  v1alpha1.SourceTypeGrpc,
			Image:       image,
			DisplayName: name,
			Publisher:   "lifecycle-e2e",
			GrpcPodConfig: &v1alpha1.GrpcPodConfig{
				SecurityContextConfig: v1alpha1.Restricted,
			},
		},
	}

	created, err := crc.OperatorsV1alpha1().CatalogSources(namespace).Create(context.Background(), cs, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to create CatalogSource %s/%s", namespace, name)

	cleanup := func() {
		_ = crc.OperatorsV1alpha1().CatalogSources(namespace).Delete(context.Background(), name, metav1.DeleteOptions{})
	}
	return created, cleanup
}

// waitForCatalogPodRunning waits until the CatalogSource has a running and ready pod.
func waitForCatalogPodRunning(namespace, catalogName string) {
	c := ctx.Ctx().KubeClient()
	Eventually(func() bool {
		pods, err := c.KubernetesInterface().CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
			LabelSelector: fmt.Sprintf("olm.catalogSource=%s", catalogName),
		})
		if err != nil || len(pods.Items) == 0 {
			return false
		}
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning && isPodReady(&pod) {
				return true
			}
		}
		return false
	}, lcResourceTimeout, 5*time.Second).Should(BeTrue(), "catalog pod for %s did not reach Running/Ready", catalogName)
}

// isPodReady returns true if the pod has a Ready condition set to True.
func isPodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// waitForLifecycleResources waits until all lifecycle-server resources exist for a CatalogSource.
func waitForLifecycleResources(namespace, catalogName string) {
	c := ctx.Ctx().KubeClient()
	name := lcResourceName(catalogName)

	By(fmt.Sprintf("waiting for lifecycle-server resources for %s/%s", namespace, catalogName))

	Eventually(func() error {
		_, err := c.KubernetesInterface().AppsV1().Deployments(namespace).Get(context.Background(), name, metav1.GetOptions{})
		return err
	}, lcResourceTimeout, 5*time.Second).Should(Succeed(), "Deployment %s not created", name)

	Eventually(func() error {
		_, err := c.KubernetesInterface().CoreV1().Services(namespace).Get(context.Background(), name, metav1.GetOptions{})
		return err
	}, lcResourceTimeout, 5*time.Second).Should(Succeed(), "Service %s not created", name)

	Eventually(func() error {
		_, err := c.KubernetesInterface().CoreV1().ServiceAccounts(namespace).Get(context.Background(), name, metav1.GetOptions{})
		return err
	}, lcResourceTimeout, 5*time.Second).Should(Succeed(), "ServiceAccount %s not created", name)

	Eventually(func() error {
		_, err := c.KubernetesInterface().NetworkingV1().NetworkPolicies(namespace).Get(context.Background(), name, metav1.GetOptions{})
		return err
	}, lcResourceTimeout, 5*time.Second).Should(Succeed(), "NetworkPolicy %s not created", name)
}

// verifyLifecycleResourcesDeleted waits until all lifecycle-server resources are gone.
func verifyLifecycleResourcesDeleted(namespace, catalogName string) {
	c := ctx.Ctx().KubeClient()
	name := lcResourceName(catalogName)

	By(fmt.Sprintf("verifying lifecycle-server resources deleted for %s/%s", namespace, catalogName))

	Eventually(func() bool {
		_, err := c.KubernetesInterface().AppsV1().Deployments(namespace).Get(context.Background(), name, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}, lcCleanupTimeout, 5*time.Second).Should(BeTrue(), "Deployment %s not deleted", name)

	Eventually(func() bool {
		_, err := c.KubernetesInterface().CoreV1().Services(namespace).Get(context.Background(), name, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}, lcCleanupTimeout, 5*time.Second).Should(BeTrue(), "Service %s not deleted", name)

	Eventually(func() bool {
		_, err := c.KubernetesInterface().CoreV1().ServiceAccounts(namespace).Get(context.Background(), name, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}, lcCleanupTimeout, 5*time.Second).Should(BeTrue(), "ServiceAccount %s not deleted", name)

	Eventually(func() bool {
		_, err := c.KubernetesInterface().NetworkingV1().NetworkPolicies(namespace).Get(context.Background(), name, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}, lcCleanupTimeout, 5*time.Second).Should(BeTrue(), "NetworkPolicy %s not deleted", name)
}

// crbContainsSubject checks if the ClusterRoleBinding includes a subject for the given SA.
func crbContainsSubject(crb *rbacv1.ClusterRoleBinding, saName, saNamespace string) bool {
	for _, s := range crb.Subjects {
		if s.Kind == "ServiceAccount" && s.Name == saName && s.Namespace == saNamespace {
			return true
		}
	}
	return false
}

var _ = Describe("Lifecycle Controller", func() {
	Context("with a CatalogSource containing lifecycle data", func() {
		var (
			catalogName string
			cleanup     func()
		)

		BeforeEach(func() {
			catalogName = genName("lc-catalog-")
			_, cleanup = createCatalogSourceForLifecycle(catalogName, lcNamespace, lcCatalogImage)

			By("waiting for catalog pod to be running")
			waitForCatalogPodRunning(lcNamespace, catalogName)
		})

		AfterEach(func() {
			if cleanup != nil {
				cleanup()
			}
		})

		It("should create lifecycle-server resources", func() {
			waitForLifecycleResources(lcNamespace, catalogName)

			By("verifying Deployment labels and container")
			c := ctx.Ctx().KubeClient()
			name := lcResourceName(catalogName)
			dep, err := c.KubernetesInterface().AppsV1().Deployments(lcNamespace).Get(context.Background(), name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred(), "failed to get lifecycle-server Deployment")
			Expect(dep.Labels[lcAppLabelKey]).To(Equal(lcAppLabelVal), "unexpected app label on Deployment")
			Expect(dep.Labels[lcCatalogNameLabelKey]).To(Equal(catalogName), "unexpected catalog-name label on Deployment")
			Expect(dep.Spec.Template.Spec.Containers).NotTo(BeEmpty(), "Deployment has no containers")
			Expect(dep.Spec.Template.Spec.Containers[0].Name).To(Equal("lifecycle-server"), "unexpected container name")

			By("verifying ClusterRoleBinding includes the ServiceAccount")
			Eventually(func() bool {
				crb, err := c.KubernetesInterface().RbacV1().ClusterRoleBindings().Get(context.Background(), lcCRBName, metav1.GetOptions{})
				if err != nil {
					return false
				}
				return crbContainsSubject(crb, name, lcNamespace)
			}, lcResourceTimeout, 5*time.Second).Should(BeTrue(), "ClusterRoleBinding should include lifecycle-server SA")
		})
	})

	Context("CatalogSource deletion", func() {
		It("should clean up lifecycle-server resources when CatalogSource is deleted", func() {
			catalogName := genName("lc-del-catalog-")
			_, cleanup := createCatalogSourceForLifecycle(catalogName, lcNamespace, lcCatalogImage)

			By("waiting for catalog pod and lifecycle-server resources")
			waitForCatalogPodRunning(lcNamespace, catalogName)
			waitForLifecycleResources(lcNamespace, catalogName)

			By("deleting the CatalogSource")
			cleanup()

			By("verifying lifecycle-server resources are cleaned up")
			verifyLifecycleResourcesDeleted(lcNamespace, catalogName)

			By("verifying ClusterRoleBinding no longer includes the ServiceAccount")
			c := ctx.Ctx().KubeClient()
			name := lcResourceName(catalogName)
			Eventually(func() bool {
				crb, err := c.KubernetesInterface().RbacV1().ClusterRoleBindings().Get(context.Background(), lcCRBName, metav1.GetOptions{})
				if err != nil {
					return apierrors.IsNotFound(err)
				}
				return !crbContainsSubject(crb, name, lcNamespace)
			}, lcCleanupTimeout, 5*time.Second).Should(BeTrue(), "ClusterRoleBinding should not include deleted SA")
		})
	})

	Context("catalog without lifecycle data", func() {
		It("should still create lifecycle-server resources", func() {
			catalogName := genName("lc-nolc-catalog-")
			_, cleanup := createCatalogSourceForLifecycle(catalogName, lcNamespace, lcCatalogNoLifecycleImage)
			defer cleanup()

			By("waiting for catalog pod to be running")
			waitForCatalogPodRunning(lcNamespace, catalogName)

			By("verifying lifecycle-server resources are created")
			waitForLifecycleResources(lcNamespace, catalogName)
		})
	})

	Context("multiple CatalogSources", func() {
		It("should create independent resources for each CatalogSource", func() {
			catalog1 := genName("lc-multi-a-")
			catalog2 := genName("lc-multi-b-")

			_, cleanup1 := createCatalogSourceForLifecycle(catalog1, lcNamespace, lcCatalogImage)
			defer cleanup1()
			_, cleanup2 := createCatalogSourceForLifecycle(catalog2, lcNamespace, lcCatalogNoLifecycleImage)
			defer cleanup2()

			By("waiting for both catalog pods")
			waitForCatalogPodRunning(lcNamespace, catalog1)
			waitForCatalogPodRunning(lcNamespace, catalog2)

			By("verifying resources for first catalog")
			waitForLifecycleResources(lcNamespace, catalog1)

			By("verifying resources for second catalog")
			waitForLifecycleResources(lcNamespace, catalog2)

			By("verifying ClusterRoleBinding includes both ServiceAccounts")
			c := ctx.Ctx().KubeClient()
			name1 := lcResourceName(catalog1)
			name2 := lcResourceName(catalog2)
			Eventually(func() bool {
				crb, err := c.KubernetesInterface().RbacV1().ClusterRoleBindings().Get(context.Background(), lcCRBName, metav1.GetOptions{})
				if err != nil {
					return false
				}
				return crbContainsSubject(crb, name1, lcNamespace) && crbContainsSubject(crb, name2, lcNamespace)
			}, lcResourceTimeout, 5*time.Second).Should(BeTrue(), "ClusterRoleBinding should include both SAs")

			By("verifying Deployments have different catalog-name labels")
			dep1, err := c.KubernetesInterface().AppsV1().Deployments(lcNamespace).Get(context.Background(), name1, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			dep2, err := c.KubernetesInterface().AppsV1().Deployments(lcNamespace).Get(context.Background(), name2, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(dep1.Labels[lcCatalogNameLabelKey]).To(Equal(catalog1), "first Deployment should reference first catalog")
			Expect(dep2.Labels[lcCatalogNameLabelKey]).To(Equal(catalog2), "second Deployment should reference second catalog")
		})
	})
})
