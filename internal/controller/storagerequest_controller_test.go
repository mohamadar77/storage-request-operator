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
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
)

// These tests drive Reconcile directly against a real API server (envtest).
// No manager, cache, or storage provisioner runs, so each call is one
// deterministic reconcile and PVCs stay Pending unless a test sets their status.
// Client interceptors simulate a stale cache and failed writes.

const requestName = "app-data"

var errInjected = errors.New("injected failure")

var _ = Describe("StorageRequest controller: PVC creation and identity", func() {
	var (
		namespace string
		key       types.NamespacedName
		recorder  *events.FakeRecorder
	)

	// newReconciler builds a reconciler whose cached client is c and whose
	// live reader is always the real API server.
	newReconciler := func(c client.Client) *StorageRequestReconciler {
		return &StorageRequestReconciler{Client: c, APIReader: k8sClient, Scheme: scheme.Scheme, Recorder: recorder}
	}
	reconcileWith := func(c client.Client) (ctrl.Result, error) {
		return newReconciler(c).Reconcile(ctx, ctrl.Request{NamespacedName: key})
	}
	reconcileOnce := func() ctrl.Result {
		GinkgoHelper()
		result, err := reconcileWith(k8sClient)
		Expect(err).NotTo(HaveOccurred())
		return result
	}

	createRequest := func() *storagev1alpha1.StorageRequest {
		GinkgoHelper()
		sr := &storagev1alpha1.StorageRequest{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:   namespace,
				Name:        requestName,
				Labels:      map[string]string{"user-label": "x"},
				Annotations: map[string]string{"user-annotation": "x"},
			},
			Spec: storagev1alpha1.StorageRequestSpec{
				Storage: storagev1alpha1.StorageSpec{Size: resource.MustParse("20Gi")},
			},
		}
		Expect(k8sClient.Create(ctx, sr)).To(Succeed())
		return sr
	}
	getRequest := func() *storagev1alpha1.StorageRequest {
		GinkgoHelper()
		sr := &storagev1alpha1.StorageRequest{}
		Expect(k8sClient.Get(ctx, key, sr)).To(Succeed())
		return sr
	}
	getPVC := func() *corev1.PersistentVolumeClaim {
		GinkgoHelper()
		pvc := &corev1.PersistentVolumeClaim{}
		Expect(k8sClient.Get(ctx, key, pvc)).To(Succeed())
		return pvc
	}
	expectNoPVC := func() {
		GinkgoHelper()
		list := &corev1.PersistentVolumeClaimList{}
		Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(Succeed())
		Expect(list.Items).To(BeEmpty(), "no PersistentVolumeClaim may exist in the namespace")
	}
	// createForeignPVC creates a same-name PVC that the controller did not
	// create, optionally carrying a request UID annotation.
	createForeignPVC := func(requestUID string) *corev1.PersistentVolumeClaim {
		GinkgoHelper()
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: requestName},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		}
		if requestUID != "" {
			pvc.Annotations = map[string]string{storagev1alpha1.AnnotationRequestUID: requestUID}
		}
		Expect(k8sClient.Create(ctx, pvc)).To(Succeed())
		return pvc
	}
	// removePVC deletes the PVC completely, clearing the pvc-protection
	// finalizer the API server adds (no controller removes it in envtest).
	removePVC := func() {
		GinkgoHelper()
		pvc := getPVC()
		Expect(k8sClient.Delete(ctx, pvc)).To(Succeed())
		current := &corev1.PersistentVolumeClaim{}
		err := k8sClient.Get(ctx, key, current)
		if apierrors.IsNotFound(err) {
			return
		}
		Expect(err).NotTo(HaveOccurred())
		base := current.DeepCopy()
		current.Finalizers = nil
		Expect(k8sClient.Patch(ctx, current, client.MergeFrom(base))).To(Succeed())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key, current))).To(BeTrue())
	}
	// provision runs the two reconciles that add the finalizer and create the
	// PVC, and returns the created PVC.
	provision := func() *corev1.PersistentVolumeClaim {
		GinkgoHelper()
		reconcileOnce()
		reconcileOnce()
		return getPVC()
	}
	expectCondition := func(sr *storagev1alpha1.StorageRequest, conditionType string, status metav1.ConditionStatus, reason string) {
		GinkgoHelper()
		condition := meta.FindStatusCondition(sr.Status.Conditions, conditionType)
		Expect(condition).NotTo(BeNil(), "condition %s must be set", conditionType)
		Expect(condition.Status).To(Equal(status), "condition %s status", conditionType)
		Expect(condition.Reason).To(Equal(reason), "condition %s reason", conditionType)
		Expect(condition.ObservedGeneration).To(Equal(sr.Generation))
	}
	expectProblem := func(reason string) *storagev1alpha1.StorageRequest {
		GinkgoHelper()
		sr := getRequest()
		expectCondition(sr, storagev1alpha1.ConditionReady, metav1.ConditionFalse, reason)
		expectCondition(sr, storagev1alpha1.ConditionDegraded, metav1.ConditionTrue, reason)
		return sr
	}
	isRequest := func(obj client.Object) bool {
		_, ok := obj.(*storagev1alpha1.StorageRequest)
		return ok
	}
	isPVC := func(obj client.Object) bool {
		_, ok := obj.(*corev1.PersistentVolumeClaim)
		return ok
	}

	BeforeEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "m3-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		namespace = ns.Name
		key = types.NamespacedName{Namespace: namespace, Name: requestName}
		// Large enough that no test blocks on an undrained channel.
		recorder = events.NewFakeRecorder(1000)
	})

	It("does nothing for a StorageRequest that does not exist", func() {
		result := reconcileOnce()
		Expect(result).To(Equal(ctrl.Result{}))
		expectNoPVC()
	})

	Context("creation", func() {
		It("persists the finalizer first and creates no PVC in that reconcile", func() {
			createRequest()

			reconcileOnce()

			Expect(controllerutil.ContainsFinalizer(getRequest(), storagev1alpha1.Finalizer)).To(BeTrue())
			expectNoPVC()
		})

		It("creates no PVC when the finalizer cannot be persisted", func() {
			createRequest()
			failing := interceptor.NewClient(k8sClient, interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if isRequest(obj) {
						return errInjected
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
			})

			for range 3 {
				_, err := reconcileWith(failing)
				Expect(err).To(MatchError(errInjected))
			}

			Expect(getRequest().Finalizers).To(BeEmpty())
			expectNoPVC()
		})

		It("keeps finalizers owned by other controllers", func() {
			sr := createRequest()
			base := sr.DeepCopy()
			sr.Finalizers = []string{"other.example.io/keep"}
			Expect(k8sClient.Patch(ctx, sr, client.MergeFrom(base))).To(Succeed())

			reconcileOnce()

			Expect(getRequest().Finalizers).To(ConsistOf("other.example.io/keep", storagev1alpha1.Finalizer))
		})

		It("creates exactly one PVC with identity markers and records its UID", func() {
			sr := createRequest()

			pvc := provision()

			Expect(pvc.Labels).To(Equal(map[string]string{
				"app.kubernetes.io/managed-by": "storage-request-operator",
				"storage.example.io/managed":   "true",
			}))
			Expect(pvc.Annotations).To(HaveKeyWithValue("storage.example.io/request-uid", string(sr.UID)))
			Expect(pvc.Annotations).NotTo(HaveKey("user-annotation"))
			Expect(pvc.OwnerReferences).To(BeEmpty(), "the PVC must not have an owner reference")
			Expect(pvc.Spec.AccessModes).To(Equal([]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}))
			Expect(*pvc.Spec.VolumeMode).To(Equal(corev1.PersistentVolumeFilesystem))
			Expect(pvc.Spec.StorageClassName).To(BeNil(), "an omitted StorageClass must stay omitted")
			requested := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(requested.Cmp(resource.MustParse("20Gi"))).To(Equal(0))

			got := getRequest()
			Expect(got.Status.PVCUID).To(Equal(pvc.UID))
			Expect(got.Status.PVCName).To(Equal(requestName))
			Expect(got.Status.ObservedGeneration).To(Equal(got.Generation))
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionFalse, storagev1alpha1.ReasonProvisioning)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionFalse, storagev1alpha1.ReasonAsExpected)
		})

		It("passes an explicit StorageClass, access modes, and volume mode to the PVC", func() {
			sr := &storagev1alpha1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: requestName},
				Spec: storagev1alpha1.StorageRequestSpec{
					Storage: storagev1alpha1.StorageSpec{
						Size:             resource.MustParse("5Gi"),
						StorageClassName: new("fast-ssd"),
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
						VolumeMode:       new(corev1.PersistentVolumeBlock),
					},
				},
			}
			Expect(k8sClient.Create(ctx, sr)).To(Succeed())

			pvc := provision()

			Expect(*pvc.Spec.StorageClassName).To(Equal("fast-ssd"))
			Expect(pvc.Spec.AccessModes).To(Equal([]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}))
			Expect(*pvc.Spec.VolumeMode).To(Equal(corev1.PersistentVolumeBlock))
		})

		It("is idempotent: further reconciles change neither the PVC nor the request", func() {
			createRequest()
			pvc := provision()
			sr := getRequest()

			for range 3 {
				reconcileOnce()
			}

			Expect(getPVC().ResourceVersion).To(Equal(pvc.ResourceVersion))
			Expect(getRequest().ResourceVersion).To(Equal(sr.ResourceVersion))
		})

		It("reports Ready once the PVC is bound with the requested capacity", func() {
			createRequest()
			pvc := provision()
			pvc.Status.Phase = corev1.ClaimBound
			pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")}
			Expect(k8sClient.Status().Update(ctx, pvc)).To(Succeed())

			result := reconcileOnce()

			// Even a Ready request is re-checked: nothing else would notice
			// its PVC being deleted, replaced, or lost.
			Expect(result.RequeueAfter).To(Equal(fallbackRequeue))
			got := getRequest()
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionTrue, storagev1alpha1.ReasonBound)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionFalse, storagev1alpha1.ReasonAsExpected)
		})

		It("stays not Ready when the bound capacity is below the requested size", func() {
			createRequest()
			pvc := provision()
			pvc.Status.Phase = corev1.ClaimBound
			pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}
			Expect(k8sClient.Status().Update(ctx, pvc)).To(Succeed())

			reconcileOnce()

			expectCondition(getRequest(), storagev1alpha1.ConditionReady, metav1.ConditionFalse, storagev1alpha1.ReasonResizing)
		})

		It("reports VolumeLost for a lost claim and does not replace it", func() {
			createRequest()
			pvc := provision()
			pvc.Status.Phase = corev1.ClaimLost
			Expect(k8sClient.Status().Update(ctx, pvc)).To(Succeed())

			reconcileOnce()

			expectProblem(storagev1alpha1.ReasonVolumeLost)
			Expect(getPVC().UID).To(Equal(pvc.UID))
		})

		It("reports Ready when the bound capacity exceeds the requested size", func() {
			createRequest()
			pvc := provision()
			pvc.Status.Phase = corev1.ClaimBound
			// 100Gi > 20Gi numerically, but "100Gi" < "20Gi" as strings.
			pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("100Gi")}
			Expect(k8sClient.Status().Update(ctx, pvc)).To(Succeed())

			reconcileOnce()

			expectCondition(getRequest(), storagev1alpha1.ConditionReady, metav1.ConditionTrue, storagev1alpha1.ReasonBound)
		})

		It("requeues a provisioning request as a fallback for missed PVC events", func() {
			createRequest()
			reconcileOnce() // finalizer

			Expect(reconcileOnce().RequeueAfter).To(Equal(fallbackRequeue), "the reconcile that creates the PVC")
			Expect(reconcileOnce().RequeueAfter).To(Equal(fallbackRequeue), "later reconciles")
		})

		It("sets observedGeneration to the generation it processed", func() {
			createRequest()
			provision()
			sr := getRequest()
			base := sr.DeepCopy()
			sr.Spec.Lifecycle.OnDelete = storagev1alpha1.OnDeleteDelete
			Expect(k8sClient.Patch(ctx, sr, client.MergeFrom(base))).To(Succeed())

			reconcileOnce()

			got := getRequest()
			Expect(got.Generation).To(Equal(int64(2)))
			Expect(got.Status.ObservedGeneration).To(Equal(int64(2)))
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionFalse, storagev1alpha1.ReasonAsExpected)
		})

		It("creates no PVC from a spec that fails defensive validation", func() {
			createRequest()
			reconcileOnce() // finalizer
			invalid := interceptor.NewClient(k8sClient, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if err := c.Get(ctx, k, obj, opts...); err != nil {
						return err
					}
					if sr, ok := obj.(*storagev1alpha1.StorageRequest); ok {
						sr.Spec.Storage.Size = resource.MustParse("-1Gi")
					}
					return nil
				},
			})

			_, err := reconcileWith(invalid)

			Expect(err).NotTo(HaveOccurred())
			expectNoPVC()
			expectProblem(storagev1alpha1.ReasonInvalidSpec)

			// Once the spec is valid again the PVC is created and the problem clears.
			reconcileOnce()

			got := getRequest()
			Expect(got.Status.PVCUID).To(Equal(getPVC().UID))
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionFalse, storagev1alpha1.ReasonAsExpected)
		})

		It("validates the object the PVC is built from, not only the cached copy", func() {
			createRequest()
			reconcileOnce() // finalizer
			// The API server returns a spec the Go validator rejects (an empty
			// StorageClass, as a looser CRD schema could admit) while the
			// cached copy is still valid.
			liveInvalid := interceptor.NewClient(k8sClient, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if err := c.Get(ctx, k, obj, opts...); err != nil {
						return err
					}
					if sr, ok := obj.(*storagev1alpha1.StorageRequest); ok {
						sr.Spec.Storage.StorageClassName = new("")
					}
					return nil
				},
			})
			r := &StorageRequestReconciler{Client: k8sClient, APIReader: liveInvalid, Scheme: scheme.Scheme, Recorder: recorder}

			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})

			Expect(err).NotTo(HaveOccurred())
			expectNoPVC()
			expectProblem(storagev1alpha1.ReasonInvalidSpec)
		})
	})

	Context("crash recovery and races", func() {
		It("recovers when the status write after PVC creation fails", func() {
			createRequest()
			reconcileOnce() // finalizer
			failStatus := interceptor.NewClient(k8sClient, interceptor.Funcs{
				SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
					return errInjected
				},
			})

			_, err := reconcileWith(failStatus)

			Expect(err).To(MatchError(errInjected), "the failed UID write must be returned so the reconcile is retried")
			pvc := getPVC()
			Expect(getRequest().Status.PVCUID).To(BeEmpty())

			// The next reconcile (as after a restart) finds the PVC it created
			// and records its UID instead of creating another one.
			reconcileOnce()

			Expect(getRequest().Status.PVCUID).To(Equal(pvc.UID))
			list := &corev1.PersistentVolumeClaimList{}
			Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(Succeed())
			Expect(list.Items).To(HaveLen(1))
			Expect(getPVC().ResourceVersion).To(Equal(pvc.ResourceVersion))
		})

		It("records the UID of an unrecorded PVC that carries this request's UID", func() {
			sr := createRequest()
			pvc := createForeignPVC(string(sr.UID))
			reconcileOnce() // finalizer

			reconcileOnce()

			Expect(getRequest().Status.PVCUID).To(Equal(pvc.UID))
			Expect(getPVC().ResourceVersion).To(Equal(pvc.ResourceVersion), "recovery must not modify the PVC")
		})

		// notFoundPVCs simulates a cache that has not yet seen an existing PVC.
		notFoundPVCs := func() client.Client {
			return interceptor.NewClient(k8sClient, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if isPVC(obj) {
						return apierrors.NewNotFound(schema.GroupResource{Resource: "persistentvolumeclaims"}, k.Name)
					}
					return c.Get(ctx, k, obj, opts...)
				},
			})
		}

		It("does not adopt an unrelated PVC when create returns AlreadyExists", func() {
			createRequest()
			foreign := createForeignPVC("")
			reconcileOnce() // finalizer

			_, err := reconcileWith(notFoundPVCs())

			Expect(err).NotTo(HaveOccurred())
			sr := expectProblem(storagev1alpha1.ReasonPVCNameConflict)
			Expect(sr.Status.PVCUID).To(BeEmpty())
			Expect(getPVC().ResourceVersion).To(Equal(foreign.ResourceVersion))
		})

		It("verifies and records its own PVC when create returns AlreadyExists", func() {
			sr := createRequest()
			own := createForeignPVC(string(sr.UID))
			reconcileOnce() // finalizer

			_, err := reconcileWith(notFoundPVCs())

			Expect(err).NotTo(HaveOccurred())
			Expect(getRequest().Status.PVCUID).To(Equal(own.UID))
			Expect(getPVC().ResourceVersion).To(Equal(own.ResourceVersion))
		})

		It("does not recreate a lost PVC when the cached request does not yet show the recorded UID", func() {
			createRequest()
			pvc := provision()
			removePVC()
			// The cache still serves the request as it was before the UID was recorded.
			stale := interceptor.NewClient(k8sClient, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if err := c.Get(ctx, k, obj, opts...); err != nil {
						return err
					}
					if sr, ok := obj.(*storagev1alpha1.StorageRequest); ok {
						sr.Status = storagev1alpha1.StorageRequestStatus{}
					}
					return nil
				},
			})

			_, err := reconcileWith(stale)

			Expect(err).NotTo(HaveOccurred())
			expectNoPVC()
			sr := expectProblem(storagev1alpha1.ReasonPVCMissing)
			Expect(sr.Status.PVCUID).To(Equal(pvc.UID))
		})
	})

	Context("stale cache", func() {
		// cachedAs simulates a cache that still serves an old snapshot of the
		// StorageRequest. Writes and all other reads go to the API server.
		cachedAs := func(snapshot *storagev1alpha1.StorageRequest) client.WithWatch {
			return interceptor.NewClient(k8sClient, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if sr, ok := obj.(*storagev1alpha1.StorageRequest); ok {
						snapshot.DeepCopyInto(sr)
						return nil
					}
					return c.Get(ctx, k, obj, opts...)
				},
			})
		}
		// withoutPVCs additionally hides every PVC from the cache.
		withoutPVCs := func(inner client.WithWatch) client.Client {
			return interceptor.NewClient(inner, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if isPVC(obj) {
						return apierrors.NewNotFound(schema.GroupResource{Resource: "persistentvolumeclaims"}, k.Name)
					}
					return c.Get(ctx, k, obj, opts...)
				},
			})
		}

		It("does not report PVCMissing when only the cache has not seen the recorded PVC", func() {
			createRequest()
			pvc := provision()
			before := getRequest()

			_, err := reconcileWith(withoutPVCs(k8sClient))

			Expect(err).NotTo(HaveOccurred())
			got := getRequest()
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionFalse, storagev1alpha1.ReasonProvisioning)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionFalse, storagev1alpha1.ReasonAsExpected)
			Expect(got.ResourceVersion).To(Equal(before.ResourceVersion), "nothing changed, so nothing may be written")
			Expect(getPVC().UID).To(Equal(pvc.UID))
		})

		It("does not report PVCMissing when both the request and the PVC cache are stale", func() {
			createRequest()
			reconcileOnce()          // finalizer
			snapshot := getRequest() // before the PVC UID was recorded
			reconcileOnce()
			pvc := getPVC()

			_, err := reconcileWith(withoutPVCs(cachedAs(snapshot)))

			Expect(err).NotTo(HaveOccurred())
			got := getRequest()
			Expect(got.Status.PVCUID).To(Equal(pvc.UID))
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionFalse, storagev1alpha1.ReasonAsExpected)
			list := &corev1.PersistentVolumeClaimList{}
			Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(Succeed())
			Expect(list.Items).To(HaveLen(1))
		})

		It("never records a replacement PVC from a cached request that predates the recorded UID", func() {
			sr := createRequest()
			reconcileOnce()          // finalizer
			snapshot := getRequest() // before the PVC UID was recorded
			reconcileOnce()
			original := getPVC()
			removePVC()
			replacement := createForeignPVC(string(sr.UID)) // annotation copied

			_, err := reconcileWith(cachedAs(snapshot))

			Expect(apierrors.IsConflict(err)).To(BeTrue(), "the stale status write must be rejected as a conflict, got: %v", err)
			Expect(getRequest().Status.PVCUID).To(Equal(original.UID), "the replacement must never be recorded")
			Expect(getPVC().ResourceVersion).To(Equal(replacement.ResourceVersion))

			// With a current cache the replacement is reported, not adopted.
			reconcileOnce()
			expectProblem(storagev1alpha1.ReasonOwnershipMismatch)
		})

		It("keeps another controller's finalizer the cache has not seen yet", func() {
			sr := createRequest()
			snapshot := sr.DeepCopy()
			base := sr.DeepCopy()
			sr.Finalizers = []string{"other.example.io/keep"}
			Expect(k8sClient.Patch(ctx, sr, client.MergeFrom(base))).To(Succeed())

			_, err := reconcileWith(cachedAs(snapshot))

			Expect(apierrors.IsConflict(err)).To(BeTrue(), "the stale finalizer write must be rejected as a conflict, got: %v", err)
			Expect(getRequest().Finalizers).To(ConsistOf("other.example.io/keep"))
		})

		It("creates no PVC for a request deleted after the cache snapshot", func() {
			sr := createRequest()
			reconcileOnce() // finalizer
			snapshot := getRequest()
			Expect(k8sClient.Delete(ctx, sr)).To(Succeed())

			result, err := reconcileWith(cachedAs(snapshot))

			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(staleCacheRequeue))
			expectNoPVC()
		})

		It("creates no PVC for a request that was recreated with a new UID after the cache snapshot", func() {
			old := createRequest()
			reconcileOnce() // finalizer
			snapshot := getRequest()
			Expect(k8sClient.Delete(ctx, old)).To(Succeed())
			current := getRequest()
			base := current.DeepCopy()
			current.Finalizers = nil
			Expect(k8sClient.Patch(ctx, current, client.MergeFrom(base))).To(Succeed())
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key, &storagev1alpha1.StorageRequest{}))).To(BeTrue())
			recreated := createRequest()
			Expect(recreated.UID).NotTo(Equal(old.UID))

			result, err := reconcileWith(cachedAs(snapshot))

			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(staleCacheRequeue))
			expectNoPVC()
		})
	})

	Context("collisions", func() {
		It("reports PVCNameConflict for a same-name PVC without a request UID and leaves it unchanged", func() {
			createRequest()
			foreign := createForeignPVC("")

			reconcileOnce()
			result := reconcileOnce()

			Expect(result.RequeueAfter).To(Equal(fallbackRequeue))
			sr := expectProblem(storagev1alpha1.ReasonPVCNameConflict)
			Expect(sr.Status.PVCUID).To(BeEmpty(), "a conflicting PVC must never be recorded")
			after := getPVC()
			Expect(after.ResourceVersion).To(Equal(foreign.ResourceVersion))
			Expect(after.Labels).To(BeEmpty())
			Expect(after.Annotations).To(BeEmpty())
		})

		It("reports OwnershipMismatch for a PVC owned by another request UID and leaves it unchanged", func() {
			createRequest()
			foreign := createForeignPVC("00000000-0000-0000-0000-000000000000")

			reconcileOnce()
			reconcileOnce()

			sr := expectProblem(storagev1alpha1.ReasonOwnershipMismatch)
			Expect(sr.Status.PVCUID).To(BeEmpty())
			Expect(getPVC().ResourceVersion).To(Equal(foreign.ResourceVersion))
		})

		It("creates its PVC once a conflicting PVC is gone, and clears the conflict", func() {
			createRequest()
			createForeignPVC("")
			reconcileOnce()
			reconcileOnce()
			expectProblem(storagev1alpha1.ReasonPVCNameConflict)
			removePVC()

			reconcileOnce()

			sr := getRequest()
			Expect(sr.Status.PVCUID).To(Equal(getPVC().UID))
			expectCondition(sr, storagev1alpha1.ConditionDegraded, metav1.ConditionFalse, storagev1alpha1.ReasonAsExpected)
		})
	})

	Context("after the PVC UID is recorded", func() {
		It("reports PVCMissing and never recreates the PVC", func() {
			createRequest()
			pvc := provision()
			removePVC()

			for range 3 {
				result := reconcileOnce()
				Expect(result.RequeueAfter).To(Equal(fallbackRequeue))
			}

			expectNoPVC()
			sr := expectProblem(storagev1alpha1.ReasonPVCMissing)
			Expect(sr.Status.PVCUID).To(Equal(pvc.UID), "the recorded UID must be kept")
		})

		It("reports OwnershipMismatch for a same-name replacement, even with the annotation copied", func() {
			sr := createRequest()
			original := provision()
			removePVC()
			replacement := createForeignPVC(string(sr.UID))
			Expect(replacement.UID).NotTo(Equal(original.UID))

			reconcileOnce()

			got := expectProblem(storagev1alpha1.ReasonOwnershipMismatch)
			Expect(got.Status.PVCUID).To(Equal(original.UID), "the recorded UID must not move to the replacement")
			Expect(getPVC().ResourceVersion).To(Equal(replacement.ResourceVersion))
		})

		It("reports OwnershipMismatch when the recorded PVC loses its request UID annotation", func() {
			createRequest()
			pvc := provision()
			base := pvc.DeepCopy()
			pvc.Annotations = nil
			Expect(k8sClient.Patch(ctx, pvc, client.MergeFrom(base))).To(Succeed())

			reconcileOnce()

			expectProblem(storagev1alpha1.ReasonOwnershipMismatch)
			Expect(getPVC().ResourceVersion).To(Equal(pvc.ResourceVersion))
		})

		It("reports PVCDeleting for a terminating PVC and creates no replacement", func() {
			createRequest()
			pvc := provision()
			Expect(pvc.Finalizers).NotTo(BeEmpty(), "the API server is expected to add pvc-protection")
			Expect(k8sClient.Delete(ctx, pvc)).To(Succeed())
			terminating := getPVC()
			Expect(terminating.DeletionTimestamp.IsZero()).To(BeFalse())

			reconcileOnce()

			sr := expectProblem(storagev1alpha1.ReasonPVCDeleting)
			Expect(sr.Status.PVCUID).To(Equal(pvc.UID))
			after := getPVC()
			Expect(after.UID).To(Equal(pvc.UID))
			Expect(after.ResourceVersion).To(Equal(terminating.ResourceVersion), "a terminating PVC must not be modified")
		})

		It("records the UID of an unrecorded PVC that is already terminating, then reports PVCMissing", func() {
			sr := createRequest()
			pvc := createForeignPVC(string(sr.UID))
			Expect(k8sClient.Delete(ctx, pvc)).To(Succeed())
			reconcileOnce() // finalizer

			reconcileOnce()

			got := expectProblem(storagev1alpha1.ReasonPVCDeleting)
			Expect(got.Status.PVCUID).To(Equal(pvc.UID))

			removePVC()
			reconcileOnce()

			expectProblem(storagev1alpha1.ReasonPVCMissing)
			expectNoPVC()
		})
	})

	Context("status projection, resize state, and events", func() {
		// setPVCStatus applies mutate to the PVC status through the status
		// subresource, standing in for the Kubernetes storage controllers.
		setPVCStatus := func(mutate func(*corev1.PersistentVolumeClaimStatus)) *corev1.PersistentVolumeClaim {
			GinkgoHelper()
			pvc := getPVC()
			mutate(&pvc.Status)
			Expect(k8sClient.Status().Update(ctx, pvc)).To(Succeed())
			return pvc
		}
		bind := func(capacity string) *corev1.PersistentVolumeClaim {
			GinkgoHelper()
			pvc := getPVC()
			pvc.Spec.VolumeName = "pv-" + namespace
			Expect(k8sClient.Update(ctx, pvc)).To(Succeed())
			return setPVCStatus(func(status *corev1.PersistentVolumeClaimStatus) {
				status.Phase = corev1.ClaimBound
				status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(capacity)}
			})
		}
		drainEvents := func() []string {
			var got []string
			for {
				select {
				case e := <-recorder.Events:
					got = append(got, e)
				default:
					return got
				}
			}
		}

		It("projects the bound claim's phase, volume, and actual capacity", func() {
			createRequest()
			provision()
			Expect(getRequest().Status.PVCPhase).To(Equal(corev1.ClaimPending))

			bind("25Gi")
			reconcileOnce()

			got := getRequest()
			Expect(got.Status.PVCPhase).To(Equal(corev1.ClaimBound))
			Expect(got.Status.VolumeName).To(Equal("pv-" + namespace))
			Expect(got.Status.Capacity).NotTo(BeNil())
			Expect(got.Status.Capacity.Cmp(resource.MustParse("25Gi"))).To(Equal(0),
				"capacity is the actual capacity, not the requested 20Gi")
		})

		It("clears the projected claim when the recorded PVC goes missing", func() {
			createRequest()
			provision()
			bind("20Gi")
			reconcileOnce()
			removePVC()

			reconcileOnce()

			got := expectProblem(storagev1alpha1.ReasonPVCMissing)
			Expect(got.Status.PVCPhase).To(BeEmpty())
			Expect(got.Status.VolumeName).To(BeEmpty())
			Expect(got.Status.Capacity).To(BeNil())
		})

		It("never projects a same-name PVC that fails the identity check", func() {
			createRequest()
			createForeignPVC("")
			pvc := getPVC()
			pvc.Status.Phase = corev1.ClaimBound
			pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}
			Expect(k8sClient.Status().Update(ctx, pvc)).To(Succeed())

			reconcileOnce() // finalizer
			reconcileOnce()

			got := expectProblem(storagev1alpha1.ReasonPVCNameConflict)
			Expect(got.Status.PVCPhase).To(BeEmpty())
			Expect(got.Status.Capacity).To(BeNil())
		})

		It("keeps Ready false while Kubernetes reports a resize in progress", func() {
			createRequest()
			provision()
			bind("20Gi")
			setPVCStatus(func(status *corev1.PersistentVolumeClaimStatus) {
				status.Conditions = []corev1.PersistentVolumeClaimCondition{{
					Type: corev1.PersistentVolumeClaimFileSystemResizePending, Status: corev1.ConditionTrue,
				}}
			})

			reconcileOnce()

			got := getRequest()
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionFalse, storagev1alpha1.ReasonResizing)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionFalse, storagev1alpha1.ReasonAsExpected)
		})

		It("reports ResizeFailed for a terminal resize failure", func() {
			createRequest()
			provision()
			bind("10Gi")
			setPVCStatus(func(status *corev1.PersistentVolumeClaimStatus) {
				status.AllocatedResourceStatuses = map[corev1.ResourceName]corev1.ClaimResourceStatus{
					corev1.ResourceStorage: corev1.PersistentVolumeClaimControllerResizeInfeasible,
				}
			})

			reconcileOnce()

			expectProblem(storagev1alpha1.ReasonResizeFailed)
		})

		It("preserves the Ready transition time when only the reason changes", func() {
			createRequest()
			provision()
			before := meta.FindStatusCondition(getRequest().Status.Conditions, storagev1alpha1.ConditionReady)
			Expect(before.Reason).To(Equal(storagev1alpha1.ReasonProvisioning))

			bind("10Gi") // below the requested 20Gi: still Ready=False
			reconcileOnce()

			after := meta.FindStatusCondition(getRequest().Status.Conditions, storagev1alpha1.ConditionReady)
			Expect(after.Reason).To(Equal(storagev1alpha1.ReasonResizing))
			Expect(after.LastTransitionTime).To(Equal(before.LastTransitionTime))
		})

		It("emits one Event per transition and none for an unchanged state", func() {
			createRequest()
			provision()
			Expect(drainEvents()).To(ConsistOf(HavePrefix("Normal PVCRequested ")),
				"creating the PVC; Provisioning itself has no Event")

			reconcileOnce()
			Expect(drainEvents()).To(BeEmpty(), "nothing changed")

			bind("20Gi")
			reconcileOnce()
			reconcileOnce()
			Expect(drainEvents()).To(ConsistOf(HavePrefix("Normal PVCBound ")))

			removePVC()
			reconcileOnce()
			reconcileOnce()
			Expect(drainEvents()).To(ConsistOf(HavePrefix("Warning PVCMissing ")))
		})

		It("emits a Warning for an ownership conflict once, not on every reconcile", func() {
			createRequest()
			createForeignPVC("")
			reconcileOnce() // finalizer

			reconcileOnce()
			reconcileOnce()

			Expect(drainEvents()).To(ConsistOf(HavePrefix("Warning PVCNameConflict ")))
		})

		It("emits no Event when the status write is rejected", func() {
			createRequest()
			provision()
			drainEvents()
			bind("20Gi")
			failStatus := interceptor.NewClient(k8sClient, interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
					patch client.Patch, opts ...client.SubResourcePatchOption) error {
					return errInjected
				},
			})

			_, err := reconcileWith(failStatus)

			Expect(err).To(MatchError(errInjected))
			Expect(drainEvents()).To(BeEmpty())
		})
	})

	Context("deletion", func() {
		It("keeps the finalizer and the PVC, because finalization is not implemented yet", func() {
			sr := createRequest()
			pvc := provision()
			Expect(k8sClient.Delete(ctx, sr)).To(Succeed())

			reconcileOnce()

			got := getRequest()
			Expect(got.DeletionTimestamp.IsZero()).To(BeFalse())
			Expect(controllerutil.ContainsFinalizer(got, storagev1alpha1.Finalizer)).To(BeTrue())
			Expect(getPVC().ResourceVersion).To(Equal(pvc.ResourceVersion))
		})

		It("creates no PVC for a request deleted before its PVC existed, and does not requeue or write", func() {
			sr := createRequest()
			reconcileOnce() // finalizer
			Expect(k8sClient.Delete(ctx, sr)).To(Succeed())
			before := getRequest()

			result := reconcileOnce()

			Expect(result).To(Equal(ctrl.Result{}))
			Expect(getRequest().ResourceVersion).To(Equal(before.ResourceVersion))
			expectNoPVC()
		})
	})
})
