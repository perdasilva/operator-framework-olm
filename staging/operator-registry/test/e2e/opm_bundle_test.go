package e2e_test

import (
	"bytes"
	"context"
	"encoding/pem"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"golang.org/x/mod/sumdb/dirhash"

	libimage "github.com/operator-framework/operator-registry/internal/testutil/image"
)

var _ = Describe("opm alpha bundle", func() {
	// out captures opm command output
	var out bytes.Buffer

	BeforeEach(func() {
		// Reset the command's output buffer
		out = bytes.Buffer{}
		opm.SetOut(&out)
		opm.SetErr(&out)
	})

	Context("for an invalid bundle", func() {
		var (
			bundleRef      string
			bundleChecksum string
			tmpDir         string
			rootCA         string
			server         *httptest.Server
		)

		BeforeEach(func() {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Spin up an in-process docker registry with a set of preconfigured test images
			var (
				// Directory containing the docker registry filesystem
				goldenFiles = "../../pkg/image/testdata/golden"
			)
			server := libimage.RunDockerRegistry(ctx, goldenFiles)
			serverURL, err := url.Parse(server.URL)
			Expect(err).NotTo(HaveOccurred())

			caFile, err := os.CreateTemp("", "ca")
			Expect(err).NotTo(HaveOccurred())

			Expect(pem.Encode(caFile, &pem.Block{
				Type:  "CERTIFICATE",
				Bytes: server.Certificate().Raw,
			})).To(Succeed())
			rootCA = caFile.Name()
			caFile.Close()

			// Create a bundle ref using the local registry host name and the namespace/name of a bundle we already know the content of
			bundleRef = serverURL.Host + imageDomain + "/kiali@sha256:a1bec450c104ceddbb25b252275eb59f1f1e6ca68e0ced76462042f72f7057d8"

			// Generate a checksum of the expected content for the bundle under test
			bundleChecksum, err = dirhash.HashDir(filepath.Join(goldenFiles, "bundles/kiali"), "", dirhash.DefaultHash)
			Expect(err).ToNot(HaveOccurred())

			// Set up a temporary directory that we can use for testing
			tmpDir, err = os.MkdirTemp("", "opm-alpha-bundle-")
			Expect(err).ToNot(HaveOccurred())
		})

		AfterEach(func() {
			server.Close()
			if CurrentGinkgoTestDescription().Failed {
				// Skip additional cleanup
				return
			}

			Expect(os.RemoveAll(rootCA)).To(Succeed())
			Expect(os.RemoveAll(tmpDir)).To(Succeed())
		})

		// Note(tflannag): See https://github.com/operator-framework/operator-registry/issues/609.
		PIt("fails to unpack", func() {
			unpackDir := filepath.Join(tmpDir, "unpacked")
			opm.SetArgs([]string{
				"alpha",
				"bundle",
				"unpack",
				"--root-ca",
				rootCA,
				"--out",
				unpackDir,
				bundleRef,
			})

			Expect(opm.Execute()).ToNot(Succeed())
			result, err := io.ReadAll(&out)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(result)).To(ContainSubstring("bundle content validation failed"))
		})

		PIt("unpacks successfully", func() {
			By("setting --skip-validation")

			unpackDir := filepath.Join(tmpDir, "unpacked")
			opm.SetArgs([]string{
				"alpha",
				"bundle",
				"unpack",
				"--root-ca",
				rootCA,
				"--out",
				unpackDir,
				bundleRef,
				"--skip-validation",
			})

			Expect(opm.Execute()).To(Succeed())
			result, err := io.ReadAll(&out)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(ContainSubstring("bundle content validation failed"))

			checksum, err := dirhash.HashDir(unpackDir, "", dirhash.DefaultHash)
			Expect(err).ToNot(HaveOccurred())
			Expect(checksum).To(Equal(bundleChecksum))
		})
	})
})
