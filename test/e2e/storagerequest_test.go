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
// E2E_STORAGE_CLASS. The class must support dynamic provisioning. A consumer pod
// drives binding, so a WaitForFirstConsumer class (the Kind default) works.
var e2eStorageClass = os.Getenv("E2E_STORAGE_CLASS")

// e2eExpandStorageClass, when set (E2E_EXPAND_STORAGE_CLASS), names a
// StorageClass with allowVolumeExpansion=true for the expansion test. That test
// is skipped when it is empty, because the default Kind provisioner (local-path)
// does not support volume expansion.
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

// oneShotPodManifest renders a pod that mounts claim at /data and runs sh -c
// with the given script, then completes (restartPolicy Never). Scheduling it is
// what binds a WaitForFirstConsumer claim.
func oneShotPodManifest(name, claim, script string) string {
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

// sleeperPodManifest renders a pod that mounts claim and idles, keeping the
// claim bound and mounted (needed for node-side filesystem resize) until the
// pod is deleted.
func sleeperPodManifest(name, claim string) string {
	return oneShotPodManifest(name, claim, "sleep 3600")
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
	// get reads a single jsonpath value WITHOUT asserting, so it is safe to call
	// inside an Eventually poller (a failed assertion in a plain poller aborts
	// the spec instead of retrying).
	get := func(kind, name, path string) (string, error) {
		out, err := kubectl("get", kind, name, "-o", "jsonpath="+path)
		return strings.TrimSpace(out), err
	}
	// mustGet reads a jsonpath value from an object that is expected to exist.
	mustGet := func(kind, name, path string) string {
		GinkgoHelper()
		out, err := get(kind, name, path)
		Expect(err).NotTo(HaveOccurred(), out)
		return out
	}
	// eventuallyValue polls get until the value matches, retrying on errors
	// (e.g. the object not existing yet).
	eventuallyValue := func(kind, name, path, want string, timeout time.Duration) {
		GinkgoHelper()
		Eventually(func(g Gomega) {
			v, err := get(kind, name, path)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(v).To(Equal(want))
		}, timeout, 3*time.Second).Should(Succeed())
	}
	// expectPVCGone polls until the PVC is confirmed absent. It distinguishes
	// NotFound (empty output, exit 0) from a transient error, so a blip is never
	// read as a successful deletion.
	expectPVCGone := func(name string, timeout time.Duration) {
		GinkgoHelper()
		Eventually(func(g Gomega) {
			out, err := kubectl("get", "pvc", name, "--ignore-not-found", "-o", "name")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(BeEmpty())
		}, timeout, 3*time.Second).Should(Succeed())
	}
	deletePod := func(name string) {
		GinkgoHelper()
		_, err := kubectl("delete", "pod", name, "--ignore-not-found", "--wait=true")
		Expect(err).NotTo(HaveOccurred())
	}

	BeforeAll(func() {
		By("installing the CRDs")
		_, err := utils.Run(exec.Command("make", "install"))
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		_, err = utils.Run(exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage)))
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

		By("waiting for the controller-manager deployment to be Available")
		_, err = utils.Run(exec.Command("kubectl", "wait", "--for=condition=Available",
			"deployment/storage-request-operator-controller-manager",
			"-n", namespace, "--timeout=180s"))
		Expect(err).NotTo(HaveOccurred(), "controller-manager did not become Available")

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
		It("binds the PVC once a consumer schedules it and becomes Ready", func() {
			By("creating a StorageRequest")
			apply(storageRequestManifest("app-data", "1Gi", "Retain", e2eStorageClass))

			By("scheduling a pod that writes a marker, which also binds the claim")
			apply(oneShotPodManifest("marker-writer", "app-data", "echo hello-e2e > /data/marker && sync"))
			eventuallyValue("pod", "marker-writer", "{.status.phase}", "Succeeded", 3*time.Minute)

			By("observing the PVC bound and the request Ready")
			eventuallyValue("pvc", "app-data", "{.status.phase}", "Bound", 1*time.Minute)
			eventuallyValue("storagerequest", "app-data",
				`{.status.conditions[?(@.type=="Ready")].status}`, "True", 1*time.Minute)

			By("recording the PVC identity in status")
			Expect(mustGet("storagerequest", "app-data", "{.status.pvcName}")).To(Equal("app-data"))
			Expect(mustGet("storagerequest", "app-data", "{.status.pvcUID}")).NotTo(BeEmpty())
			Expect(mustGet("pvc", "app-data",
				`{.metadata.labels.app\.kubernetes\.io/managed-by}`)).To(Equal("storage-request-operator"))

			deletePod("marker-writer")
		})

		It("persists the marker file across pods", func() {
			By("reading the marker back from a second pod")
			apply(oneShotPodManifest("marker-reader", "app-data", "cat /data/marker"))
			eventuallyValue("pod", "marker-reader", "{.status.phase}", "Succeeded", 2*time.Minute)

			logs, err := kubectl("logs", "marker-reader")
			Expect(err).NotTo(HaveOccurred())
			Expect(logs).To(ContainSubstring("hello-e2e"), "the marker must survive the first pod")

			deletePod("marker-reader")
		})
	})

	Context("safe updates", func() {
		It("rejects an ordinary shrink without changing the PVC, and stays Ready", func() {
			By("requesting a smaller size than the bound claim")
			apply(storageRequestManifest("app-data", "500Mi", "Retain", e2eStorageClass))

			By("reporting ShrinkNotSupported while the claim still satisfies the request")
			eventuallyValue("storagerequest", "app-data",
				`{.status.conditions[?(@.type=="Degraded")].reason}`, "ShrinkNotSupported", 1*time.Minute)
			Expect(mustGet("storagerequest", "app-data",
				`{.status.conditions[?(@.type=="Ready")].status}`)).To(Equal("True"))

			By("leaving the PVC request unchanged")
			Expect(mustGet("pvc", "app-data", "{.spec.resources.requests.storage}")).To(Equal("1Gi"))

			By("restoring the original size")
			apply(storageRequestManifest("app-data", "1Gi", "Retain", e2eStorageClass))
			eventuallyValue("storagerequest", "app-data",
				`{.status.conditions[?(@.type=="Degraded")].reason}`, "AsExpected", 1*time.Minute)
		})

		It("expands a bound claim when the StorageClass allows it", func() {
			if e2eExpandStorageClass == "" {
				Skip("set E2E_EXPAND_STORAGE_CLASS to a StorageClass with allowVolumeExpansion=true")
			}
			By("creating a StorageRequest on the expandable StorageClass with a consumer")
			apply(storageRequestManifest("app-expand", "1Gi", "Delete", e2eExpandStorageClass))
			apply(sleeperPodManifest("app-expand-consumer", "app-expand"))
			eventuallyValue("pod", "app-expand-consumer", "{.status.phase}", "Running", 3*time.Minute)
			eventuallyValue("pvc", "app-expand", "{.status.phase}", "Bound", 1*time.Minute)

			By("requesting a larger size")
			apply(storageRequestManifest("app-expand", "2Gi", "Delete", e2eExpandStorageClass))

			By("observing the PVC request grow and the capacity converge")
			eventuallyValue("pvc", "app-expand", "{.spec.resources.requests.storage}", "2Gi", 1*time.Minute)
			eventuallyValue("pvc", "app-expand", "{.status.capacity.storage}", "2Gi", 5*time.Minute)
			eventuallyValue("storagerequest", "app-expand",
				`{.status.conditions[?(@.type=="Ready")].status}`, "True", 1*time.Minute)

			By("cleaning up the expandable request")
			deletePod("app-expand-consumer")
			_, _ = kubectl("delete", "storagerequest", "app-expand", "--wait=false")
			expectPVCGone("app-expand", 2*time.Minute)
		})
	})

	Context("finalization", func() {
		It("retains and detaches the PVC when deleted with onDelete=Retain", func() {
			By("deleting the Retain request")
			_, err := kubectl("delete", "storagerequest", "app-data", "--wait=true")
			Expect(err).NotTo(HaveOccurred())

			By("leaving the PVC in place, detached")
			Consistently(func(g Gomega) {
				out, err := kubectl("get", "pvc", "app-data", "--ignore-not-found", "-o", "name")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).NotTo(BeEmpty())
			}, 10*time.Second, 2*time.Second).Should(Succeed(), "the PVC must survive a Retain deletion")
			Expect(mustGet("pvc", "app-data",
				`{.metadata.annotations.storage\.base-domain\.io/orphaned-from}`)).NotTo(BeEmpty())
			Expect(mustGet("pvc", "app-data",
				`{.metadata.labels.storage\.base-domain\.io/managed}`)).To(BeEmpty())

			By("cleaning up the retained PVC")
			_, err = kubectl("delete", "pvc", "app-data", "--wait=false")
			Expect(err).NotTo(HaveOccurred())
		})

		It("deletes the PVC when deleted with onDelete=Delete", func() {
			By("creating a Delete request and binding it with a one-shot consumer")
			apply(storageRequestManifest("app-delete", "1Gi", "Delete", e2eStorageClass))
			apply(oneShotPodManifest("app-delete-binder", "app-delete", "true"))
			eventuallyValue("pod", "app-delete-binder", "{.status.phase}", "Succeeded", 3*time.Minute)
			eventuallyValue("pvc", "app-delete", "{.status.phase}", "Bound", 1*time.Minute)
			deletePod("app-delete-binder")

			By("deleting the request")
			_, err := kubectl("delete", "storagerequest", "app-delete", "--wait=true")
			Expect(err).NotTo(HaveOccurred())

			By("observing the PVC be removed")
			expectPVCGone("app-delete", 2*time.Minute)
		})
	})
})
