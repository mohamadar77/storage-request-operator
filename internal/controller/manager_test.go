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

package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
)

// This test runs the controller the way the manager binary does: through a
// real manager, informer cache, and watch. It covers the wiring the direct
// Reconcile tests cannot: SetupWithManager, the cached client, and the
// finalizer update re-triggering the reconcile that creates the PVC, the PVC
// watch, and Events published to the API server.
var _ = Describe("StorageRequest controller running in a manager", func() {
	// startManager runs the controller in a manager whose cache is limited to a
	// fresh namespace, and returns that namespace.
	startManager := func() string {
		GinkgoHelper()
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "mgr-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		// The cache is limited to this namespace so the manager does not
		// reconcile objects that belong to the direct Reconcile tests.
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:  scheme.Scheme,
			Metrics: metricsserver.Options{BindAddress: "0"},
			Cache:   cache.Options{DefaultNamespaces: map[string]cache.Config{ns.Name: {}}},
			// Controller names are registered process-wide; skip the check so
			// more than one spec can start a manager with this controller.
			Controller: config.Controller{SkipNameValidation: new(true)},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect((&StorageRequestReconciler{
			Client:    mgr.GetClient(),
			APIReader: mgr.GetAPIReader(),
			Scheme:    mgr.GetScheme(),
		}).SetupWithManager(mgr)).To(Succeed())

		mgrCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- mgr.Start(mgrCtx) }()
		DeferCleanup(func() {
			stop()
			Eventually(done, 10*time.Second).Should(Receive(BeNil()))
		})
		return ns.Name
	}
	newRequest := func(namespace string) *storagev1alpha1.StorageRequest {
		return &storagev1alpha1.StorageRequest{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: requestName},
			Spec: storagev1alpha1.StorageRequestSpec{
				Storage: storagev1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
			},
		}
	}

	It("adds the finalizer, creates the PVC, and records its UID without duplicates", func() {
		namespace := startManager()
		key := types.NamespacedName{Namespace: namespace, Name: requestName}
		sr := newRequest(namespace)
		Expect(k8sClient.Create(ctx, sr)).To(Succeed())

		pvc := &corev1.PersistentVolumeClaim{}
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, key, pvc)).To(Succeed())
			got := &storagev1alpha1.StorageRequest{}
			g.Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
			g.Expect(got.Finalizers).To(ContainElement(storagev1alpha1.Finalizer))
			g.Expect(got.Status.PVCUID).To(Equal(pvc.UID))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
		Expect(pvc.Annotations).To(HaveKeyWithValue(storagev1alpha1.AnnotationRequestUID, string(sr.UID)))

		// The controller settles: the same single PVC, unchanged.
		Consistently(func(g Gomega) {
			list := &corev1.PersistentVolumeClaimList{}
			g.Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(Succeed())
			g.Expect(list.Items).To(HaveLen(1))
			g.Expect(list.Items[0].UID).To(Equal(pvc.UID))
			g.Expect(list.Items[0].ResourceVersion).To(Equal(pvc.ResourceVersion))
		}, 2*time.Second, 200*time.Millisecond).Should(Succeed())
	})

	// The fallback requeue is 30 seconds, so a reaction within a few seconds
	// can only come from the PVC watch.
	const watchTimeout = 5 * time.Second

	It("reacts to PVC binding and deletion through the PVC watch, and publishes Events", func() {
		Expect(watchTimeout).To(BeNumerically("<", fallbackRequeue/2))
		namespace := startManager()
		key := types.NamespacedName{Namespace: namespace, Name: requestName}
		sr := newRequest(namespace)
		Expect(k8sClient.Create(ctx, sr)).To(Succeed())

		pvc := &corev1.PersistentVolumeClaim{}
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, key, pvc)).To(Succeed())
			got := &storagev1alpha1.StorageRequest{}
			g.Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
			g.Expect(got.Status.PVCUID).To(Equal(pvc.UID))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		By("binding the PVC")
		pvc.Status.Phase = corev1.ClaimBound
		pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}
		Expect(k8sClient.Status().Update(ctx, pvc)).To(Succeed())
		Eventually(func(g Gomega) {
			got := &storagev1alpha1.StorageRequest{}
			g.Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
			g.Expect(got.Status.PVCPhase).To(Equal(corev1.ClaimBound))
			ready := meta.FindStatusCondition(got.Status.Conditions, storagev1alpha1.ConditionReady)
			g.Expect(ready).NotTo(BeNil())
			g.Expect(ready.Status).To(Equal(metav1.ConditionTrue))
		}, watchTimeout, 100*time.Millisecond).Should(Succeed())

		By("deleting the PVC")
		Expect(k8sClient.Delete(ctx, pvc)).To(Succeed())
		current := &corev1.PersistentVolumeClaim{}
		Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
		base := current.DeepCopy()
		current.Finalizers = nil // no pvc-protection controller runs in envtest
		Expect(k8sClient.Patch(ctx, current, client.MergeFrom(base))).To(Succeed())
		Eventually(func(g Gomega) {
			got := &storagev1alpha1.StorageRequest{}
			g.Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
			degraded := meta.FindStatusCondition(got.Status.Conditions, storagev1alpha1.ConditionDegraded)
			g.Expect(degraded).NotTo(BeNil())
			g.Expect(degraded.Reason).To(Equal(storagev1alpha1.ReasonPVCMissing))
		}, watchTimeout, 100*time.Millisecond).Should(Succeed())

		By("reading the published Events")
		Eventually(func(g Gomega) {
			list := &eventsv1.EventList{}
			g.Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(Succeed())
			reasons := map[string]string{}
			for _, e := range list.Items {
				g.Expect(e.Regarding.UID).To(Equal(sr.UID))
				reasons[e.Reason] = e.Type
			}
			g.Expect(reasons).To(HaveKeyWithValue(storagev1alpha1.EventPVCRequested, corev1.EventTypeNormal))
			g.Expect(reasons).To(HaveKeyWithValue(storagev1alpha1.EventPVCBound, corev1.EventTypeNormal))
			g.Expect(reasons).To(HaveKeyWithValue(storagev1alpha1.ReasonPVCMissing, corev1.EventTypeWarning))
		}, 10*time.Second, 200*time.Millisecond).Should(Succeed())
	})
})
