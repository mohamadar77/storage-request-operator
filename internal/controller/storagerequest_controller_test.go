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
	)

	// newReconciler builds a reconciler whose cached client is c and whose
	// live reader is always the real API server.
	newReconciler := func(c client.Client) *StorageRequestReconciler {
		return &StorageRequestReconciler{Client: c, APIReader: k8sClient, Scheme: scheme.Scheme}
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

			Expect(result.RequeueAfter).To(BeZero())
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

			expectCondition(getRequest(), storagev1alpha1.ConditionReady, metav1.ConditionFalse, storagev1alpha1.ReasonProvisioning)
		})

		It("reports VolumeLost for a lost claim and does not replace it", func() {
			createRequest()
			pvc := provision()
			pvc.Status.Phase = corev1.ClaimLost
			Expect(k8sClient.Status().Update(ctx, pvc)).To(Succeed())

			reconcileOnce()

			expectCondition(getRequest(), storagev1alpha1.ConditionReady, metav1.ConditionFalse, storagev1alpha1.ReasonVolumeLost)
			Expect(getPVC().UID).To(Equal(pvc.UID))
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
				Expect(result).To(Equal(ctrl.Result{}))
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

		It("creates no PVC for a request deleted before its PVC existed", func() {
			sr := createRequest()
			reconcileOnce() // finalizer
			Expect(k8sClient.Delete(ctx, sr)).To(Succeed())

			reconcileOnce()

			expectNoPVC()
		})
	})
})
