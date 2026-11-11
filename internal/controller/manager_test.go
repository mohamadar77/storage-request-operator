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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	// The fallback requeue is 30 seconds, so once the request has settled a
	// reaction within a few seconds can only come from the PVC watch.
	const watchTimeout = 5 * time.Second

	// waitSettled waits until the request stops changing, so that no reconcile
	// triggered by the controller's own writes is still pending.
	waitSettled := func(key types.NamespacedName) {
		GinkgoHelper()
		var last string
		Eventually(func(g Gomega) {
			got := &storagev1alpha1.StorageRequest{}
			g.Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
			previous := last
			last = got.ResourceVersion
			g.Expect(last).To(Equal(previous))
		}, 10*time.Second, time.Second).Should(Succeed())
	}
	removePVC := func(key types.NamespacedName) {
		GinkgoHelper()
		current := &corev1.PersistentVolumeClaim{}
		Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
		Expect(k8sClient.Delete(ctx, current)).To(Succeed())
		Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
		base := current.DeepCopy()
		current.Finalizers = nil // no pvc-protection controller runs in envtest
		Expect(k8sClient.Patch(ctx, current, client.MergeFrom(base))).To(Succeed())
	}

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

		waitSettled(key)

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

		waitSettled(key)

		By("deleting the PVC")
		removePVC(key)
		Eventually(func(g Gomega) {
			got := &storagev1alpha1.StorageRequest{}
			g.Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
			degraded := meta.FindStatusCondition(got.Status.Conditions, storagev1alpha1.ConditionDegraded)
			g.Expect(degraded).NotTo(BeNil())
			g.Expect(degraded.Reason).To(Equal(storagev1alpha1.ReasonPVCMissing))
		}, watchTimeout, 100*time.Millisecond).Should(Succeed())

		By("reading the published Events: exactly one per transition")
		// A repeated Event would appear as a second object or as a series on
		// the first one, so both are checked.
		expectEvents := func(g Gomega) {
			list := &eventsv1.EventList{}
			g.Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(Succeed())
			reasons := map[string]string{}
			for _, e := range list.Items {
				g.Expect(e.Regarding.UID).To(Equal(sr.UID))
				g.Expect(reasons).NotTo(HaveKey(e.Reason), "duplicate %s Event", e.Reason)
				g.Expect(e.Series).To(BeNil(), "repeated %s Event", e.Reason)
				reasons[e.Reason] = e.Type
			}
			g.Expect(reasons).To(HaveKeyWithValue(storagev1alpha1.EventPVCRequested, corev1.EventTypeNormal))
			g.Expect(reasons).To(HaveKeyWithValue(storagev1alpha1.EventPVCBound, corev1.EventTypeNormal))
			g.Expect(reasons).To(HaveKeyWithValue(storagev1alpha1.ReasonPVCMissing, corev1.EventTypeWarning))
			// The PVC is terminating briefly before it is gone; the controller
			// may or may not observe that state, so PVCDeleting is optional.
			delete(reasons, storagev1alpha1.ReasonPVCDeleting)
			g.Expect(reasons).To(HaveLen(3), "unexpected Events: %v", reasons)
		}
		Eventually(expectEvents, 10*time.Second, 200*time.Millisecond).Should(Succeed())
		Consistently(expectEvents, 2*time.Second, 200*time.Millisecond).Should(Succeed())
	})

	It("runs Delete finalization end to end: the request and its PVC are removed", func() {
		namespace := startManager()
		key := types.NamespacedName{Namespace: namespace, Name: requestName}
		sr := &storagev1alpha1.StorageRequest{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: requestName},
			Spec: storagev1alpha1.StorageRequestSpec{
				Storage:   storagev1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
				Lifecycle: storagev1alpha1.LifecycleSpec{OnDelete: storagev1alpha1.OnDeleteDelete},
			},
		}
		Expect(k8sClient.Create(ctx, sr)).To(Succeed())

		pvc := &corev1.PersistentVolumeClaim{}
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, key, pvc)).To(Succeed())
			got := &storagev1alpha1.StorageRequest{}
			g.Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
			g.Expect(got.Status.PVCUID).To(Equal(pvc.UID))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
		waitSettled(key)

		By("deleting the request")
		Expect(k8sClient.Delete(ctx, sr)).To(Succeed())

		By("the controller requesting PVC deletion and waiting")
		Eventually(func(g Gomega) {
			current := &corev1.PersistentVolumeClaim{}
			g.Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
			g.Expect(current.DeletionTimestamp.IsZero()).To(BeFalse())
		}, watchTimeout, 100*time.Millisecond).Should(Succeed())
		// The request is still present, held by its finalizer, while the PVC
		// lingers under pvc-protection.
		held := &storagev1alpha1.StorageRequest{}
		Expect(k8sClient.Get(ctx, key, held)).To(Succeed())
		Expect(held.DeletionTimestamp.IsZero()).To(BeFalse())

		By("clearing pvc-protection so the claim can disappear")
		current := &corev1.PersistentVolumeClaim{}
		Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
		base := current.DeepCopy()
		current.Finalizers = nil
		Expect(k8sClient.Patch(ctx, current, client.MergeFrom(base))).To(Succeed())

		By("the request and PVC both becoming NotFound")
		Eventually(func(g Gomega) {
			g.Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.PersistentVolumeClaim{}))).To(BeTrue())
			g.Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key, &storagev1alpha1.StorageRequest{}))).To(BeTrue())
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("does not reconcile for events of an unmarked same-name PVC", func() {
		namespace := startManager()
		key := types.NamespacedName{Namespace: namespace, Name: requestName}
		unmarked := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: requestName},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		}
		Expect(k8sClient.Create(ctx, unmarked)).To(Succeed())
		Expect(k8sClient.Create(ctx, newRequest(namespace))).To(Succeed())
		Eventually(func(g Gomega) {
			got := &storagev1alpha1.StorageRequest{}
			g.Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
			degraded := meta.FindStatusCondition(got.Status.Conditions, storagev1alpha1.ConditionDegraded)
			g.Expect(degraded).NotTo(BeNil())
			g.Expect(degraded.Reason).To(Equal(storagev1alpha1.ReasonPVCNameConflict))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
		waitSettled(key)

		// Removing the conflicting PVC would let a reconcile create the
		// request's own PVC. The predicate filters the unmarked PVC's events,
		// so that happens only on the 30-second fallback requeue (design 6.5).
		removePVC(key)

		Consistently(func(g Gomega) {
			err := k8sClient.Get(ctx, key, &corev1.PersistentVolumeClaim{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "no reconcile may run for an unmarked PVC event")
		}, watchTimeout, 200*time.Millisecond).Should(Succeed())
	})
})
