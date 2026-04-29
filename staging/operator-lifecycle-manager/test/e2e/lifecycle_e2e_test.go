//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	operatorsv1 "github.com/operator-framework/api/pkg/operators/v1"
	"github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/api/client/clientset/versioned"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/lib/operatorclient"
	"github.com/operator-framework/operator-lifecycle-manager/test/e2e/ctx"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	lifecycleServerLabel       = "olm-lifecycle-server"
	lifecycleCatalogNameLabel  = "olm.lifecycle-server/catalog-name"
	lifecycleAppLabel          = "app"
	lifecycleCRBName           = "operator-lifecycle-manager-lifecycle-server"
	lifecycleTestCatalogImage  = "quay.io/perdasilva/olm:rhops3"
	lifecycleDeploymentTimeout = 5 * time.Minute
	lifecycleCleanupTimeout    = 2 * time.Minute
)

func lifecycleResourceName(csName string) string {
	return csName + "-lifecycle-server"
}

func createLifecycleCatalogSource(c operatorclient.ClientInterface, crc versioned.Interface, name, namespace, image string) (*v1alpha1.CatalogSource, cleanupFunc) {
	catalogSource := &v1alpha1.CatalogSource{
		TypeMeta: metav1.TypeMeta{
			Kind:       v1alpha1.CatalogSourceKind,
			APIVersion: v1alpha1.CatalogSourceCRDAPIVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1alpha1.CatalogSourceSpec{
			SourceType:  v1alpha1.SourceTypeGrpc,
			Image:       image,
			DisplayName: "Lifecycle Test Catalog",
			Publisher:   "OLM e2e",
			GrpcPodConfig: &v1alpha1.GrpcPodConfig{
				SecurityContextConfig: v1alpha1.Restricted,
			},
		},
	}

	ctx.Ctx().Logf("Creating lifecycle catalog source %s in namespace %s...", name, namespace)
	created, err := crc.OperatorsV1alpha1().CatalogSources(namespace).Create(context.Background(), catalogSource, metav1.CreateOptions{})
	Expect(err).ShouldNot(HaveOccurred())
	ctx.Ctx().Logf("Lifecycle catalog source %s created", name)

	return created, buildCatalogSourceCleanupFunc(c, crc, namespace, created)
}

var _ = Describe("Lifecycle Server", Label("LifecycleServer"), func() {
	var (
		generatedNamespace corev1.Namespace
		c                  operatorclient.ClientInterface
		crc                versioned.Interface
	)

	BeforeEach(func() {
		namespaceName := genName("openshift-lifecycle-e2e-")
		og := operatorsv1.OperatorGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-operatorgroup", namespaceName),
				Namespace: namespaceName,
			},
		}
		generatedNamespace = SetupGeneratedTestNamespaceWithOperatorGroup(namespaceName, og)
		c = ctx.Ctx().KubeClient()
		crc = ctx.Ctx().OperatorClient()
	})

	AfterEach(func() {
		TeardownNamespace(generatedNamespace.GetName())
	})

	It("provisions lifecycle server for a catalog source", func() {
		catalogName := genName("lifecycle-catsrc-")
		ns := generatedNamespace.GetName()

		By("creating a catalog source with lifecycle data")
		_, cleanup := createLifecycleCatalogSource(c, crc, catalogName, ns, lifecycleTestCatalogImage)
		defer cleanup()

		By("waiting for the catalog source to be synced")
		_, err := fetchCatalogSourceOnStatus(crc, catalogName, ns, catalogSourceRegistryPodSynced())
		Expect(err).ShouldNot(HaveOccurred())

		resName := lifecycleResourceName(catalogName)

		By("waiting for the lifecycle-server Deployment to become available")
		Eventually(func(g Gomega) {
			var deploy appsv1.Deployment
			err := ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: resName, Namespace: ns}, &deploy)
			g.Expect(err).ShouldNot(HaveOccurred())

			for _, cond := range deploy.Status.Conditions {
				if cond.Type == appsv1.DeploymentAvailable {
					g.Expect(cond.Status).To(Equal(corev1.ConditionTrue), "Deployment should be Available")
					return
				}
			}
			g.Expect(false).To(BeTrue(), "Deployment Available condition not found")
		}).WithTimeout(lifecycleDeploymentTimeout).WithPolling(5 * time.Second).Should(Succeed())

		By("verifying ServiceAccount exists")
		var sa corev1.ServiceAccount
		err = ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: resName, Namespace: ns}, &sa)
		Expect(err).ShouldNot(HaveOccurred())

		By("verifying Service exists with port 8443")
		var svc corev1.Service
		err = ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: resName, Namespace: ns}, &svc)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(svc.Spec.Ports).To(HaveLen(1))
		Expect(svc.Spec.Ports[0].Port).To(Equal(int32(8443)))

		By("verifying NetworkPolicy exists")
		var np networkingv1.NetworkPolicy
		err = ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: resName, Namespace: ns}, &np)
		Expect(err).ShouldNot(HaveOccurred())

		By("verifying ClusterRoleBinding includes the lifecycle-server SA")
		var crb rbacv1.ClusterRoleBinding
		err = ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: lifecycleCRBName}, &crb)
		Expect(err).ShouldNot(HaveOccurred())

		found := false
		for _, subj := range crb.Subjects {
			if subj.Name == resName && subj.Namespace == ns {
				found = true
				break
			}
		}
		Expect(found).To(BeTrue(), "ClusterRoleBinding should include the lifecycle-server SA")
	})

	It("cleans up resources when CatalogSource is deleted", func() {
		catalogName := genName("lifecycle-cleanup-")
		ns := generatedNamespace.GetName()

		By("creating a catalog source")
		created, _ := createLifecycleCatalogSource(c, crc, catalogName, ns, lifecycleTestCatalogImage)

		By("waiting for the catalog source to be synced")
		_, err := fetchCatalogSourceOnStatus(crc, catalogName, ns, catalogSourceRegistryPodSynced())
		Expect(err).ShouldNot(HaveOccurred())

		resName := lifecycleResourceName(catalogName)

		By("waiting for lifecycle-server resources to exist")
		Eventually(func(g Gomega) {
			var deploy appsv1.Deployment
			g.Expect(ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: resName, Namespace: ns}, &deploy)).To(Succeed())
		}).WithTimeout(lifecycleDeploymentTimeout).WithPolling(5 * time.Second).Should(Succeed())

		By("deleting the CatalogSource")
		err = crc.OperatorsV1alpha1().CatalogSources(ns).Delete(context.Background(), created.Name, metav1.DeleteOptions{})
		Expect(err).ShouldNot(HaveOccurred())

		By("verifying all lifecycle-server resources are cleaned up")
		Eventually(func(g Gomega) {
			var deploy appsv1.Deployment
			err := ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: resName, Namespace: ns}, &deploy)
			g.Expect(client.IgnoreNotFound(err)).To(Succeed())
			g.Expect(err).To(HaveOccurred(), "Deployment should be deleted")

			var svc corev1.Service
			err = ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: resName, Namespace: ns}, &svc)
			g.Expect(client.IgnoreNotFound(err)).To(Succeed())
			g.Expect(err).To(HaveOccurred(), "Service should be deleted")

			var sa corev1.ServiceAccount
			err = ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: resName, Namespace: ns}, &sa)
			g.Expect(client.IgnoreNotFound(err)).To(Succeed())
			g.Expect(err).To(HaveOccurred(), "ServiceAccount should be deleted")

			var np networkingv1.NetworkPolicy
			err = ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: resName, Namespace: ns}, &np)
			g.Expect(client.IgnoreNotFound(err)).To(Succeed())
			g.Expect(err).To(HaveOccurred(), "NetworkPolicy should be deleted")
		}).WithTimeout(lifecycleCleanupTimeout).WithPolling(5 * time.Second).Should(Succeed())

		By("verifying ClusterRoleBinding no longer includes the deleted SA")
		Eventually(func(g Gomega) {
			var crb rbacv1.ClusterRoleBinding
			err := ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: lifecycleCRBName}, &crb)
			g.Expect(err).ShouldNot(HaveOccurred())

			for _, subj := range crb.Subjects {
				g.Expect(subj.Name).NotTo(Equal(resName), "ClusterRoleBinding should not include the deleted SA")
			}
		}).WithTimeout(lifecycleCleanupTimeout).WithPolling(5 * time.Second).Should(Succeed())
	})

	It("runs lifecycle server pods with restricted security context", func() {
		catalogName := genName("lifecycle-hardened-")
		ns := generatedNamespace.GetName()

		By("creating a catalog source")
		_, cleanup := createLifecycleCatalogSource(c, crc, catalogName, ns, lifecycleTestCatalogImage)
		defer cleanup()

		By("waiting for the catalog source to be synced")
		_, err := fetchCatalogSourceOnStatus(crc, catalogName, ns, catalogSourceRegistryPodSynced())
		Expect(err).ShouldNot(HaveOccurred())

		resName := lifecycleResourceName(catalogName)

		By("waiting for lifecycle-server Deployment to exist")
		var deploy appsv1.Deployment
		Eventually(func(g Gomega) {
			g.Expect(ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: resName, Namespace: ns}, &deploy)).To(Succeed())
		}).WithTimeout(lifecycleDeploymentTimeout).WithPolling(5 * time.Second).Should(Succeed())

		podSpec := deploy.Spec.Template.Spec

		By("verifying pod security context")
		Expect(podSpec.SecurityContext).NotTo(BeNil())
		Expect(podSpec.SecurityContext.RunAsNonRoot).NotTo(BeNil())
		Expect(*podSpec.SecurityContext.RunAsNonRoot).To(BeTrue())
		Expect(podSpec.SecurityContext.SeccompProfile).NotTo(BeNil())
		Expect(podSpec.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

		By("verifying container security context")
		Expect(podSpec.Containers).NotTo(BeEmpty())
		container := podSpec.Containers[0]
		Expect(container.SecurityContext).NotTo(BeNil())
		Expect(container.SecurityContext.AllowPrivilegeEscalation).NotTo(BeNil())
		Expect(*container.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
		Expect(container.SecurityContext.ReadOnlyRootFilesystem).NotTo(BeNil())
		Expect(*container.SecurityContext.ReadOnlyRootFilesystem).To(BeTrue())
		Expect(container.SecurityContext.Capabilities).NotTo(BeNil())
		Expect(container.SecurityContext.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")))
	})

	It("handles multiple catalog sources independently", func() {
		ns := generatedNamespace.GetName()
		catalogName1 := genName("lifecycle-multi-a-")
		catalogName2 := genName("lifecycle-multi-b-")

		By("creating two catalog sources")
		_, cleanup1 := createLifecycleCatalogSource(c, crc, catalogName1, ns, lifecycleTestCatalogImage)
		defer cleanup1()
		_, cleanup2 := createLifecycleCatalogSource(c, crc, catalogName2, ns, lifecycleTestCatalogImage)
		defer cleanup2()

		By("waiting for both catalog sources to be synced")
		_, err := fetchCatalogSourceOnStatus(crc, catalogName1, ns, catalogSourceRegistryPodSynced())
		Expect(err).ShouldNot(HaveOccurred())
		_, err = fetchCatalogSourceOnStatus(crc, catalogName2, ns, catalogSourceRegistryPodSynced())
		Expect(err).ShouldNot(HaveOccurred())

		resName1 := lifecycleResourceName(catalogName1)
		resName2 := lifecycleResourceName(catalogName2)

		By("waiting for both lifecycle-server Deployments to exist")
		Eventually(func(g Gomega) {
			var deploy1, deploy2 appsv1.Deployment
			g.Expect(ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: resName1, Namespace: ns}, &deploy1)).To(Succeed())
			g.Expect(ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: resName2, Namespace: ns}, &deploy2)).To(Succeed())
		}).WithTimeout(lifecycleDeploymentTimeout).WithPolling(5 * time.Second).Should(Succeed())

		By("deleting the first CatalogSource")
		err = crc.OperatorsV1alpha1().CatalogSources(ns).Delete(context.Background(), catalogName1, metav1.DeleteOptions{})
		Expect(err).ShouldNot(HaveOccurred())

		By("verifying only the first's resources are cleaned up")
		Eventually(func(g Gomega) {
			var deploy appsv1.Deployment
			err := ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: resName1, Namespace: ns}, &deploy)
			g.Expect(err).To(HaveOccurred(), "First Deployment should be deleted")
		}).WithTimeout(lifecycleCleanupTimeout).WithPolling(5 * time.Second).Should(Succeed())

		By("verifying the second's resources still exist")
		var deploy2 appsv1.Deployment
		err = ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: resName2, Namespace: ns}, &deploy2)
		Expect(err).ShouldNot(HaveOccurred())

		By("verifying ClusterRoleBinding has exactly one subject remaining")
		Eventually(func(g Gomega) {
			var crb rbacv1.ClusterRoleBinding
			err := ctx.Ctx().Client().Get(context.Background(), types.NamespacedName{Name: lifecycleCRBName}, &crb)
			g.Expect(err).ShouldNot(HaveOccurred())

			hasSecond := false
			for _, subj := range crb.Subjects {
				g.Expect(subj.Name).NotTo(Equal(resName1), "Deleted SA should not be in CRB")
				if subj.Name == resName2 && subj.Namespace == ns {
					hasSecond = true
				}
			}
			g.Expect(hasSecond).To(BeTrue(), "Second SA should still be in CRB")
		}).WithTimeout(lifecycleCleanupTimeout).WithPolling(5 * time.Second).Should(Succeed())
	})
})
