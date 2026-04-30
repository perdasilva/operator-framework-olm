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
	"github.com/operator-framework/operator-lifecycle-manager/test/e2e/ctx"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	lsWgetTimeout = 2 * time.Minute
)

// runWgetJob creates a one-shot Job that runs wget and returns the HTTP status
// code and response body by inspecting pod logs.
func runWgetJob(namespace, image, url string, automountToken bool, extraArgs ...string) (int, string) {
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
						Image:   image,
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
	}, lsWgetTimeout, 5*time.Second).Should(BeTrue(), "wget job %s did not complete", jobName)

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

// getLifecycleServerImage gets the image used by the lifecycle-server container
// from the Deployment created by the controller.
func getLifecycleServerImage(namespace, catalogName string) string {
	c := ctx.Ctx().KubeClient()
	name := lcResourceName(catalogName)
	dep, err := c.KubernetesInterface().AppsV1().Deployments(namespace).Get(context.Background(), name, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to get lifecycle-server Deployment")
	Expect(dep.Spec.Template.Spec.Containers).NotTo(BeEmpty(), "Deployment has no containers")
	return dep.Spec.Template.Spec.Containers[0].Image
}

// getLifecycleServerPodIP returns the IP of a running lifecycle-server pod for the given catalog.
func getLifecycleServerPodIP(namespace, catalogName string) string {
	c := ctx.Ctx().KubeClient()
	var podIP string
	Eventually(func() string {
		pods, err := c.KubernetesInterface().CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s,%s=%s", lcAppLabelKey, lcAppLabelVal, lcCatalogNameLabelKey, catalogName),
		})
		if err != nil || len(pods.Items) == 0 {
			return ""
		}
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" && isPodReady(&pod) {
				podIP = pod.Status.PodIP
				return podIP
			}
		}
		return ""
	}, lcResourceTimeout, 5*time.Second).ShouldNot(BeEmpty(), "no running lifecycle-server pod found for %s", catalogName)
	return podIP
}

// waitForLifecycleServerReady waits until the lifecycle-server Deployment has ready replicas.
func waitForLifecycleServerReady(namespace, catalogName string) {
	c := ctx.Ctx().KubeClient()
	name := lcResourceName(catalogName)
	Eventually(func() bool {
		dep, err := c.KubernetesInterface().AppsV1().Deployments(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		return dep.Status.ReadyReplicas > 0
	}, lcResourceTimeout, 5*time.Second).Should(BeTrue(), "lifecycle-server Deployment %s not ready", name)
}

var _ = Describe("Lifecycle Server", func() {
	Context("with lifecycle data", func() {
		var (
			catalogName string
			cleanup     func()
		)

		BeforeEach(func() {
			catalogName = genName("ls-catalog-")
			_, cleanup = createCatalogSourceForLifecycle(catalogName, lcNamespace, lcCatalogImage)

			By("waiting for catalog pod and lifecycle-server resources")
			waitForCatalogPodRunning(lcNamespace, catalogName)
			waitForLifecycleResources(lcNamespace, catalogName)
			waitForLifecycleServerReady(lcNamespace, catalogName)
		})

		AfterEach(func() {
			if cleanup != nil {
				cleanup()
			}
		})

		It("should return lifecycle data for a known package", func() {
			image := getLifecycleServerImage(lcNamespace, catalogName)
			svcName := lcResourceName(catalogName)
			url := fmt.Sprintf("https://%s.%s.svc:8443/api/v1alpha1/lifecycles/test-lifecycle-operator", svcName, lcNamespace)

			status, body := runWgetJob(lcNamespace, image, url, true, "--no-check-certificate")
			Expect(status).To(Equal(200), "expected 200 for known package, got body: %s", body)

			var result map[string]any
			Expect(json.Unmarshal([]byte(body), &result)).To(Succeed(), "response should be valid JSON")
			Expect(result["package"]).To(Equal("test-lifecycle-operator"), "unexpected package in response")
			Expect(result["status"]).To(Equal("active"), "unexpected status in response")
		})

		It("should return 404 for an unknown package", func() {
			image := getLifecycleServerImage(lcNamespace, catalogName)
			svcName := lcResourceName(catalogName)
			url := fmt.Sprintf("https://%s.%s.svc:8443/api/v1alpha1/lifecycles/nonexistent", svcName, lcNamespace)

			status, _ := runWgetJob(lcNamespace, image, url, true, "--no-check-certificate")
			Expect(status).To(Equal(404), "expected 404 for unknown package")
		})

		It("should return 404 for an unknown version", func() {
			image := getLifecycleServerImage(lcNamespace, catalogName)
			svcName := lcResourceName(catalogName)
			url := fmt.Sprintf("https://%s.%s.svc:8443/api/v99/lifecycles/test-lifecycle-operator", svcName, lcNamespace)

			status, _ := runWgetJob(lcNamespace, image, url, true, "--no-check-certificate")
			Expect(status).To(Equal(404), "expected 404 for unknown version")
		})
	})

	Context("without lifecycle data", func() {
		It("should return 503 for API requests", func() {
			catalogName := genName("ls-nolc-catalog-")
			_, cleanup := createCatalogSourceForLifecycle(catalogName, lcNamespace, lcCatalogNoLifecycleImage)
			defer cleanup()

			waitForCatalogPodRunning(lcNamespace, catalogName)
			waitForLifecycleResources(lcNamespace, catalogName)
			waitForLifecycleServerReady(lcNamespace, catalogName)

			image := getLifecycleServerImage(lcNamespace, catalogName)
			svcName := lcResourceName(catalogName)
			url := fmt.Sprintf("https://%s.%s.svc:8443/api/v1alpha1/lifecycles/test-no-lifecycle-operator", svcName, lcNamespace)

			status, _ := runWgetJob(lcNamespace, image, url, true, "--no-check-certificate")
			Expect(status).To(Equal(503), "expected 503 when no lifecycle data is loaded")
		})
	})

	Context("health endpoints", func() {
		var (
			catalogName string
			cleanup     func()
		)

		BeforeEach(func() {
			catalogName = genName("ls-health-catalog-")
			_, cleanup = createCatalogSourceForLifecycle(catalogName, lcNamespace, lcCatalogImage)

			waitForCatalogPodRunning(lcNamespace, catalogName)
			waitForLifecycleResources(lcNamespace, catalogName)
			waitForLifecycleServerReady(lcNamespace, catalogName)
		})

		AfterEach(func() {
			if cleanup != nil {
				cleanup()
			}
		})

		It("should return 200 on /healthz", func() {
			image := getLifecycleServerImage(lcNamespace, catalogName)
			podIP := getLifecycleServerPodIP(lcNamespace, catalogName)

			healthURL := fmt.Sprintf("http://%s:8081/healthz", podIP)
			status, body := runWgetJob(lcNamespace, image, healthURL, true)
			Expect(status).To(Equal(200), "expected 200 on /healthz")
			Expect(body).To(ContainSubstring("ok"), "expected 'ok' in healthz response")
		})

		It("should return 200 on /readyz when lifecycle data is loaded", func() {
			image := getLifecycleServerImage(lcNamespace, catalogName)
			podIP := getLifecycleServerPodIP(lcNamespace, catalogName)

			readyzURL := fmt.Sprintf("http://%s:8081/readyz", podIP)
			status, body := runWgetJob(lcNamespace, image, readyzURL, true)
			Expect(status).To(Equal(200), "expected 200 on /readyz with lifecycle data loaded")
			Expect(body).To(ContainSubstring("ok"), "expected 'ok' in readyz response")
		})
	})

	Context("security", func() {
		It("should reject unauthenticated requests", func() {
			catalogName := genName("ls-sec-catalog-")
			_, cleanup := createCatalogSourceForLifecycle(catalogName, lcNamespace, lcCatalogImage)
			defer cleanup()

			waitForCatalogPodRunning(lcNamespace, catalogName)
			waitForLifecycleResources(lcNamespace, catalogName)
			waitForLifecycleServerReady(lcNamespace, catalogName)

			image := getLifecycleServerImage(lcNamespace, catalogName)
			svcName := lcResourceName(catalogName)
			url := fmt.Sprintf("https://%s.%s.svc:8443/api/v1alpha1/lifecycles/test-lifecycle-operator", svcName, lcNamespace)

			By("making request without service account token")
			status, body := runWgetJob(lcNamespace, image, url, false, "--no-check-certificate")
			Expect(status).To(SatisfyAny(Equal(401), Equal(403)),
				"expected 401 or 403 for unauthenticated request, got %d, body: %s", status, body)
		})
	})
})
