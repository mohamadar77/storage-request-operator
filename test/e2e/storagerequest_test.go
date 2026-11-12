//go:build e2e

/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/mohamadar77/storage-request-operator/test/utils"
)

// srNamespace is the namespace the StorageRequest lifecycle tests run in. It is
// separate from the operator's own namespace so creating and deleting it does
// not disturb the manager deployment.
const srNamespace = "storagerequest-e2e"

// e2eStorageClass selects the StorageClass for the lifecycle tests. Empty means
// the cluster default (on Kind, the local-path provisioner). Override with
// E2E_STORAGE_CLASS. The class must support dynamic provisioning.
var e2eStorageClass = os.Getenv("E2E_STORAGE_CLASS")

// e2eExpandStorageClass, when set (E2E_EXPAND_STORAGE_CLASS), names a
// StorageClass with allowVolumeExpansion=true for the expansion test. That test
// is skipped when it is empty, because the default Kind provisioner
// (local-path) does not support volume expansion.
var e2eExpandStorageClass = os.Getenv("E2E_EXPAND_STORAGE_CLASS")

// storageRequestManifest renders a StorageRequest. storageClass is omitted from
// the spec when empty, so the cluster default is used.
func storageRequestManifest(name, size, onDelete, storageClass string) string {
	var sc string
	if storageClass != "" {
		sc = fmt.Sprintf("\n    storageClassName: %s", storageClass)
	}
	return fmt.Sprintf(`apiVersion: storage.base-domain.io/v1alpha1
kind: StorageRequest
metadata:
  name: %s
spec:
  storage:
    size: %s%s
    accessModes:
      - ReadWriteOnce
    volumeMode: Filesystem
  lifecycle:
    onDelete: %s
`, name, size, sc, onDelete)
}

// markerPodManifest renders a one-shot pod that mounts claim at /data and runs
// sh -c with the given script, then completes (restartPolicy Never).
func markerPodManifest(name, claim, script string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
spec:
  restartPolicy: Never
  containers:
    - name: worker
      image: busybox:1.36
      command: ["sh", "-c", %q]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: %s
`, name, script, claim)
}

var _ = Describe("StorageRequest lifecycle", Ordered, func() {
	// kubectl runs a kubectl subcommand in the test namespace.
	kubectl := func(args ...string) (string, error) {
		return utils.Run(exec.Command("kubectl", append([]string{"-n", srNamespace}, args...)...))
	}
	// apply applies a manifest from stdin and fails the test on error.
	apply := func(manifest string) {
		GinkgoHelper()
		cmd := exec.Command("kubectl", "-n", srNamespace, "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(manifest)
		out, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), out)
	}
	// jsonpath reads a single jsonpath value from an object, trimmed.
	jsonpath := func(kind, name, path string) string {
		GinkgoHelper()
		out, err := kubectl("get", kind, name, "-o", "jsonpath="+path)
		Expect(err).NotTo(HaveOccurred(), out)
		return strings.TrimSpace(out)
	}
	readyStatus := func(name string) string {
		return jsonpath("storagerequest", name,
			`{.status.conditions[?(@.type=="Ready")].status}`)
	}
	degradedReason := func(name string) string {
		return jsonpath("storagerequest", name,
			`{.status.conditions[?(@.type=="Degraded")].reason}`)
	}
	pvcMissing := func(name string) bool {
		GinkgoHelper()
		_, err := kubectl("get", "pvc", name)
		return err != nil
	}

	BeforeAll(func() {
		By("installing the CRDs")
		_, err := utils.Run(exec.Command("make", "install"))
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		_, err = utils.Run(exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage)))
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

		By("waiting for the controller-manager deployment to be Available")
		Eventually(func() error {
			_, err := utils.Run(exec.Command("kubectl", "wait", "--for=condition=Available",
				"deployment/storage-request-operator-controller-manager",
				"-n", namespace, "--timeout=150s"))
			return err
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("creating the test namespace")
		_, _ = utils.Run(exec.Command("kubectl", "create", "ns", srNamespace))
	})

	AfterAll(func() {
		By("deleting the test namespace")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", srNamespace,
			"--ignore-not-found", "--wait=false"))
		By("undeploying the controller-manager")
		_, _ = utils.Run(exec.Command("make", "undeploy", "ignore-not-found=true"))
		By("uninstalling the CRDs")
		_, _ = utils.Run(exec.Command("make", "uninstall", "ignore-not-found=true"))
	})

	Context("create, bind, and status", func() {
		It("creates a PVC and becomes Ready when the claim binds", func() {
			By("creating a StorageRequest")
			apply(storageRequestManifest("app-data", "1Gi", "Retain", e2eStorageClass))

			By("observing the PVC bind")
			Eventually(func() string {
				return jsonpath("pvc", "app-data", "{.status.phase}")
			}, 2*time.Minute, 3*time.Second).Should(Equal("Bound"))

			By("observing the request become Ready")
			Eventually(func() string {
				return readyStatus("app-data")
			}, 1*time.Minute, 3*time.Second).Should(Equal("True"))

			By("recording the PVC identity in status")
			Expect(jsonpath("storagerequest", "app-data", "{.status.pvcName}")).To(Equal("app-data"))
			Expect(jsonpath("storagerequest", "app-data", "{.status.pvcUID}")).NotTo(BeEmpty())
			Expect(jsonpath("pvc", "app-data",
				`{.metadata.labels.app\.kubernetes\.io/managed-by}`)).To(Equal("storage-request-operator"))
		})

		It("serves the volume to a pod and persists a marker file across pods", func() {
			By("writing a marker from a first pod")
			apply(markerPodManifest("marker-writer", "app-data",
				"echo hello-e2e > /data/marker && sync"))
			Eventually(func() string {
				return jsonpath("pod", "marker-writer", "{.status.phase}")
			}, 2*time.Minute, 3*time.Second).Should(Equal("Succeeded"))
			_, err := kubectl("delete", "pod", "marker-writer", "--wait=true")
			Expect(err).NotTo(HaveOccurred())

			By("reading the marker back from a second pod")
			apply(markerPodManifest("marker-reader", "app-data", "cat /data/marker"))
			Eventually(func() string {
				return jsonpath("pod", "marker-reader", "{.status.phase}")
			}, 2*time.Minute, 3*time.Second).Should(Equal("Succeeded"))
			logs, err := kubectl("logs", "marker-reader")
			Expect(err).NotTo(HaveOccurred())
			Expect(logs).To(ContainSubstring("hello-e2e"), "the marker must survive the first pod")
			_, err = kubectl("delete", "pod", "marker-reader", "--wait=true")
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("safe updates", func() {
		It("rejects an ordinary shrink without changing the PVC, and stays Ready", func() {
			By("requesting a smaller size than the bound claim")
			apply(storageRequestManifest("app-data", "500Mi", "Retain", e2eStorageClass))

			By("reporting ShrinkNotSupported while the claim still satisfies the request")
			Eventually(func() string {
				return degradedReason("app-data")
			}, 1*time.Minute, 3*time.Second).Should(Equal("ShrinkNotSupported"))
			Expect(readyStatus("app-data")).To(Equal("True"))

			By("leaving the PVC request unchanged")
			Expect(jsonpath("pvc", "app-data",
				"{.spec.resources.requests.storage}")).To(Equal("1Gi"))

			By("restoring the original size")
			apply(storageRequestManifest("app-data", "1Gi", "Retain", e2eStorageClass))
			Eventually(func() string {
				return degradedReason("app-data")
			}, 1*time.Minute, 3*time.Second).Should(Equal("AsExpected"))
		})

		It("expands a bound claim when the StorageClass allows it", func() {
			if e2eExpandStorageClass == "" {
				Skip("set E2E_EXPAND_STORAGE_CLASS to a StorageClass with allowVolumeExpansion=true")
			}
			By("creating a StorageRequest on the expandable StorageClass")
			apply(storageRequestManifest("app-expand", "1Gi", "Delete", e2eExpandStorageClass))
			Eventually(func() string {
				return jsonpath("pvc", "app-expand", "{.status.phase}")
			}, 2*time.Minute, 3*time.Second).Should(Equal("Bound"))

			By("requesting a larger size")
			apply(storageRequestManifest("app-expand", "2Gi", "Delete", e2eExpandStorageClass))

			By("observing the PVC request grow and the capacity converge")
			Eventually(func() string {
				return jsonpath("pvc", "app-expand", "{.spec.resources.requests.storage}")
			}, 1*time.Minute, 3*time.Second).Should(Equal("2Gi"))
			Eventually(func() string {
				return jsonpath("pvc", "app-expand", "{.status.capacity.storage}")
			}, 5*time.Minute, 5*time.Second).Should(Equal("2Gi"))
			Eventually(func() string {
				return readyStatus("app-expand")
			}, 1*time.Minute, 3*time.Second).Should(Equal("True"))

			By("cleaning up the expandable request")
			_, _ = kubectl("delete", "storagerequest", "app-expand", "--wait=false")
			Eventually(func() bool {
				return pvcMissing("app-expand")
			}, 2*time.Minute, 3*time.Second).Should(BeTrue())
		})
	})

	Context("finalization", func() {
		It("retains and detaches the PVC when deleted with onDelete=Retain", func() {
			By("deleting the Retain request")
			_, err := kubectl("delete", "storagerequest", "app-data", "--wait=true")
			Expect(err).NotTo(HaveOccurred())

			By("leaving the PVC in place, detached")
			Consistently(func() bool {
				return pvcMissing("app-data")
			}, 10*time.Second, 2*time.Second).Should(BeFalse(), "the PVC must survive a Retain deletion")
			Expect(jsonpath("pvc", "app-data",
				`{.metadata.annotations.storage\.base-domain\.io/orphaned-from}`)).NotTo(BeEmpty())
			Expect(jsonpath("pvc", "app-data",
				`{.metadata.labels.storage\.base-domain\.io/managed}`)).To(BeEmpty())

			By("cleaning up the retained PVC")
			_, err = kubectl("delete", "pvc", "app-data", "--wait=false")
			Expect(err).NotTo(HaveOccurred())
		})

		It("deletes the PVC when deleted with onDelete=Delete", func() {
			By("creating a Delete request and waiting for it to bind")
			apply(storageRequestManifest("app-delete", "1Gi", "Delete", e2eStorageClass))
			Eventually(func() string {
				return jsonpath("pvc", "app-delete", "{.status.phase}")
			}, 2*time.Minute, 3*time.Second).Should(Equal("Bound"))

			By("deleting the request")
			_, err := kubectl("delete", "storagerequest", "app-delete", "--wait=true")
			Expect(err).NotTo(HaveOccurred())

			By("observing the PVC be removed")
			Eventually(func() bool {
				return pvcMissing("app-delete")
			}, 2*time.Minute, 3*time.Second).Should(BeTrue(), "the PVC must be deleted")
		})
	})
})
