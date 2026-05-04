//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/operator-framework/operator-lifecycle-manager/test/e2e/ctx"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	lcResourceTimeout         = 5 * time.Minute
	lcWgetTimeout             = 2 * time.Minute
	lcCatalogImage            = "quay.io/olmtest/lifecycle-catalog:v1"
	lcCatalogNoLifecycleImage = "quay.io/olmtest/lifecycle-catalog-no-lifecycle:v1"
	lcNamespace               = "openshift-marketplace"
	lcControllerNamespace     = "openshift-operator-lifecycle-manager"
	lcAPIServiceName          = "lifecycle-controller-api"
	lcAPIPort                 = 9443
)

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

func isPodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// lifecycleAPIURL builds the URL for the lifecycle API using the controller's API service.
func lifecycleAPIURL(version, namespace, catalogName, pkg string) string {
	return fmt.Sprintf("https://%s.%s.svc:%d/api/%s/%s/%s/lifecycles/%s",
		lcAPIServiceName, lcControllerNamespace, lcAPIPort, version, namespace, catalogName, pkg)
}

// runWgetJob creates a one-shot Job that runs wget and returns the HTTP status code and body.
func runWgetJob(namespace, url string, automountToken bool, extraArgs ...string) (int, string) {
	c := ctx.Ctx().KubeClient()
	jobName := genName("wget-")

	args := []string{"-O", "/dev/stdout", "-q"}
	args = append(args, extraArgs...)
	args = append(args, url)

	var backoffLimit int32
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: namespace},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &automountToken,
					Containers: []corev1.Container{{
						Name:    "wget",
						Image:   "busybox:latest",
						Command: []string{"wget"},
						Args:    args,
					}},
					RestartPolicy: corev1.RestartPolicyNever,
				},
			},
		},
	}

	_, err := c.KubernetesInterface().BatchV1().Jobs(namespace).Create(context.Background(), job, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to create wget job %s", jobName)

	var succeeded bool
	Eventually(func() bool {
		j, err := c.KubernetesInterface().BatchV1().Jobs(namespace).Get(context.Background(), jobName, metav1.GetOptions{})
		if err != nil {
			return false
		}
		succeeded = j.Status.Succeeded > 0
		return succeeded || j.Status.Failed > 0
	}, lcWgetTimeout, 5*time.Second).Should(BeTrue(), "wget job %s did not complete", jobName)

	pods, err := c.KubernetesInterface().CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: fmt.Sprintf("job-name=%s", jobName),
	})
	Expect(err).NotTo(HaveOccurred(), "failed to list pods for job %s", jobName)
	Expect(pods.Items).NotTo(BeEmpty(), "no pods found for job %s", jobName)

	logs, err := c.KubernetesInterface().CoreV1().Pods(namespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{}).DoRaw(context.Background())
	Expect(err).NotTo(HaveOccurred(), "failed to get logs for job pod %s", pods.Items[0].Name)

	if succeeded {
		return 200, string(logs)
	}

	logStr := string(logs)
	for _, code := range []int{401, 403, 404, 503} {
		if strings.Contains(logStr, fmt.Sprintf("%d", code)) {
			return code, logStr
		}
	}
	return 0, logStr
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

		It("should serve lifecycle data for a known package", func() {
			url := lifecycleAPIURL("v1alpha1", lcNamespace, catalogName, "test-lifecycle-operator")

			var status int
			var body string
			Eventually(func() int {
				status, body = runWgetJob(lcNamespace, url, true, "--no-check-certificate")
				return status
			}, lcResourceTimeout, 10*time.Second).Should(Equal(200), "expected 200 for known package, got body: %s", body)

			var result map[string]any
			Expect(json.Unmarshal([]byte(body), &result)).To(Succeed(), "response should be valid JSON")
			Expect(result["package"]).To(Equal("test-lifecycle-operator"))
			Expect(result["status"]).To(Equal("active"))
		})

		It("should return 404 for an unknown package", func() {
			url := lifecycleAPIURL("v1alpha1", lcNamespace, catalogName, "nonexistent")

			var status int
			Eventually(func() int {
				status, _ = runWgetJob(lcNamespace, url, true, "--no-check-certificate")
				return status
			}, lcResourceTimeout, 10*time.Second).Should(Equal(404))
		})

		It("should return 404 for an unknown version", func() {
			url := lifecycleAPIURL("v99", lcNamespace, catalogName, "test-lifecycle-operator")

			var status int
			Eventually(func() int {
				status, _ = runWgetJob(lcNamespace, url, true, "--no-check-certificate")
				return status
			}, lcResourceTimeout, 10*time.Second).Should(Equal(404))
		})
	})

	Context("CatalogSource deletion", func() {
		It("should return 404 after CatalogSource is deleted", func() {
			catalogName := genName("lc-del-catalog-")
			_, cleanup := createCatalogSourceForLifecycle(catalogName, lcNamespace, lcCatalogImage)

			By("waiting for catalog pod and lifecycle data to be served")
			waitForCatalogPodRunning(lcNamespace, catalogName)

			url := lifecycleAPIURL("v1alpha1", lcNamespace, catalogName, "test-lifecycle-operator")
			Eventually(func() int {
				status, _ := runWgetJob(lcNamespace, url, true, "--no-check-certificate")
				return status
			}, lcResourceTimeout, 10*time.Second).Should(Equal(200))

			By("deleting the CatalogSource")
			cleanup()

			By("verifying lifecycle API returns 404")
			Eventually(func() int {
				status, _ := runWgetJob(lcNamespace, url, true, "--no-check-certificate")
				return status
			}, lcResourceTimeout, 10*time.Second).Should(Equal(404))
		})
	})

	Context("catalog without lifecycle data", func() {
		It("should return 404 for API requests", func() {
			catalogName := genName("lc-nolc-catalog-")
			_, cleanup := createCatalogSourceForLifecycle(catalogName, lcNamespace, lcCatalogNoLifecycleImage)
			defer cleanup()

			By("waiting for catalog pod to be running")
			waitForCatalogPodRunning(lcNamespace, catalogName)

			url := lifecycleAPIURL("v1alpha1", lcNamespace, catalogName, "test-no-lifecycle-operator")

			// Wait for the controller to have pulled the image (negative cache), then verify 404
			Eventually(func() int {
				status, _ := runWgetJob(lcNamespace, url, true, "--no-check-certificate")
				return status
			}, lcResourceTimeout, 10*time.Second).Should(Equal(404), "expected 404 for catalog without lifecycle data")
		})
	})
})
