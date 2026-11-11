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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
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

const (
	requestName = "app-data"
	resourcePVC = "persistentvolumeclaims"
)

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
	// drainEvents returns and clears the Events recorded so far, each as
	// "<type> <reason> <note>".
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
	// invalidSpec wraps a reader so every StorageRequest it returns carries an
	// empty StorageClass, a spec the Go validator rejects and a looser CRD
	// schema could have admitted.
	invalidSpec := func(inner client.WithWatch) client.WithWatch {
		return interceptor.NewClient(inner, interceptor.Funcs{
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
						return apierrors.NewNotFound(schema.GroupResource{Resource: resourcePVC}, k.Name)
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
						return apierrors.NewNotFound(schema.GroupResource{Resource: resourcePVC}, k.Name)
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
			drainEvents()

			_, err := reconcileWith(cachedAs(snapshot))

			Expect(apierrors.IsConflict(err)).To(BeTrue(), "the stale status write must be rejected as a conflict, got: %v", err)
			Expect(drainEvents()).To(BeEmpty(), "a write rejected as a conflict emits no Event")
			Expect(getRequest().Status.PVCUID).To(Equal(original.UID), "the replacement must never be recorded")
			Expect(getPVC().ResourceVersion).To(Equal(replacement.ResourceVersion))

			// With a current cache the replacement is reported, not adopted.
			reconcileOnce()
			expectProblem(storagev1alpha1.ReasonOwnershipMismatch)
		})

		It("validates the live request before assessing a recorded PVC the cache does not show", func() {
			createRequest()
			provision()
			pvc := getPVC()
			pvc.Status.Phase = corev1.ClaimBound
			pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")}
			Expect(k8sClient.Status().Update(ctx, pvc)).To(Succeed())
			drainEvents()
			r := &StorageRequestReconciler{
				Client: withoutPVCs(k8sClient), APIReader: invalidSpec(k8sClient),
				Scheme: scheme.Scheme, Recorder: recorder,
			}

			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})

			Expect(err).NotTo(HaveOccurred())
			expectProblem(storagev1alpha1.ReasonInvalidSpec)
			Expect(drainEvents()).To(ConsistOf(HavePrefix("Warning InvalidSpec ")), "no PVCBound for an invalid spec")
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

		It("emits PVCBound again only after Ready really went false", func() {
			createRequest()
			provision()
			bind("20Gi")
			reconcileOnce()
			Expect(drainEvents()).To(ContainElement(HavePrefix("Normal PVCBound ")))

			By("a terminal resize failure while capacity still satisfies the request")
			setPVCStatus(func(status *corev1.PersistentVolumeClaimStatus) {
				status.AllocatedResourceStatuses = map[corev1.ResourceName]corev1.ClaimResourceStatus{
					corev1.ResourceStorage: corev1.PersistentVolumeClaimControllerResizeInfeasible,
				}
			})
			reconcileOnce()
			got := getRequest()
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionTrue, storagev1alpha1.ReasonBound)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionTrue, storagev1alpha1.ReasonResizeFailed)
			Expect(drainEvents()).To(ConsistOf(HavePrefix("Warning ResizeFailed ")))

			By("the failure clearing: Ready never changed, so no PVCBound")
			setPVCStatus(func(status *corev1.PersistentVolumeClaimStatus) {
				status.AllocatedResourceStatuses = nil
			})
			reconcileOnce()
			expectCondition(getRequest(), storagev1alpha1.ConditionDegraded, metav1.ConditionFalse, storagev1alpha1.ReasonAsExpected)
			Expect(drainEvents()).To(BeEmpty())
		})

		It("emits PVCBound when Ready becomes true while Degraded is still reported", func() {
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
			drainEvents()

			// Lowering the request to the actual capacity satisfies Ready but
			// is itself rejected (ShrinkNotSupported), so Degraded stays True.
			sr := getRequest()
			sr.Spec.Storage.Size = resource.MustParse("10Gi")
			Expect(k8sClient.Update(ctx, sr)).To(Succeed())
			reconcileOnce()

			got := getRequest()
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionTrue, storagev1alpha1.ReasonBound)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionTrue, storagev1alpha1.ReasonShrinkNotSupported)
			Expect(drainEvents()).To(ConsistOf(HavePrefix("Normal PVCBound "), HavePrefix("Warning ShrinkNotSupported ")))
		})

		It("truncates a long resize message so the Warning Event is never dropped", func() {
			createRequest()
			provision()
			bind("10Gi")
			setPVCStatus(func(status *corev1.PersistentVolumeClaimStatus) {
				status.AllocatedResourceStatuses = map[corev1.ResourceName]corev1.ClaimResourceStatus{
					corev1.ResourceStorage: corev1.PersistentVolumeClaimControllerResizeInfeasible,
				}
				// Each message is capped on its own; four of them still exceed
				// the 1024-byte Event note limit together.
				long := strings.Repeat("driver error detail ", 500) // 10000 bytes
				for _, t := range []corev1.PersistentVolumeClaimConditionType{
					corev1.PersistentVolumeClaimControllerResizeError, corev1.PersistentVolumeClaimNodeResizeError,
					corev1.PersistentVolumeClaimResizing, corev1.PersistentVolumeClaimFileSystemResizePending,
				} {
					status.Conditions = append(status.Conditions, corev1.PersistentVolumeClaimCondition{
						Type: t, Status: corev1.ConditionTrue, Message: long,
					})
				}
			})
			drainEvents()

			reconcileOnce()

			got := expectProblem(storagev1alpha1.ReasonResizeFailed)
			degraded := meta.FindStatusCondition(got.Status.Conditions, storagev1alpha1.ConditionDegraded)
			Expect(len(degraded.Message)).To(BeNumerically("<", 4096))
			Expect(degraded.Message).To(ContainSubstring("driver error detail"))
			events := drainEvents()
			Expect(events).To(ConsistOf(HavePrefix("Warning ResizeFailed ")))
			note := strings.TrimPrefix(events[0], "Warning ResizeFailed ")
			Expect(len(note)).To(BeNumerically("<=", maxEventNote))
			Expect(note).To(HaveSuffix("…"))
		})

		It("names the last observed PersistentVolume when the PVC goes missing", func() {
			createRequest()
			provision()
			bind("20Gi")
			reconcileOnce()
			removePVC()

			reconcileOnce()

			degraded := meta.FindStatusCondition(getRequest().Status.Conditions, storagev1alpha1.ConditionDegraded)
			Expect(degraded.Message).To(ContainSubstring("PersistentVolume pv-" + namespace))
		})

		It("clears the projected claim while the spec is invalid", func() {
			createRequest()
			provision()
			bind("20Gi")
			reconcileOnce()
			Expect(getRequest().Status.PVCPhase).To(Equal(corev1.ClaimBound))

			_, err := reconcileWith(invalidSpec(k8sClient))

			Expect(err).NotTo(HaveOccurred())
			got := expectProblem(storagev1alpha1.ReasonInvalidSpec)
			Expect(got.Status.PVCPhase).To(BeEmpty())
			Expect(got.Status.VolumeName).To(BeEmpty())
			Expect(got.Status.Capacity).To(BeNil())
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

	Context("safe updates and resize", func() {
		// createClass creates a cluster-scoped StorageClass for this test and
		// removes it afterwards. allow is its allowVolumeExpansion.
		createClass := func(allow *bool) string {
			GinkgoHelper()
			sc := &storagev1.StorageClass{
				ObjectMeta:           metav1.ObjectMeta{Name: namespace + "-class"},
				Provisioner:          "example.com/test",
				AllowVolumeExpansion: allow,
			}
			Expect(k8sClient.Create(ctx, sc)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, sc))).To(Succeed()) })
			return sc.Name
		}
		createRequestWith := func(mutate func(*storagev1alpha1.StorageSpec)) *storagev1alpha1.StorageRequest {
			GinkgoHelper()
			sr := &storagev1alpha1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: requestName},
				Spec: storagev1alpha1.StorageRequestSpec{
					Storage: storagev1alpha1.StorageSpec{Size: resource.MustParse("20Gi")},
				},
			}
			mutate(&sr.Spec.Storage)
			Expect(k8sClient.Create(ctx, sr)).To(Succeed())
			return sr
		}
		withClass := func(class string) func(*storagev1alpha1.StorageSpec) {
			return func(spec *storagev1alpha1.StorageSpec) { spec.StorageClassName = &class }
		}
		updateSpec := func(mutate func(*storagev1alpha1.StorageSpec)) {
			GinkgoHelper()
			sr := getRequest()
			mutate(&sr.Spec.Storage)
			Expect(k8sClient.Update(ctx, sr)).To(Succeed())
		}
		setSize := func(size string) {
			GinkgoHelper()
			updateSpec(func(spec *storagev1alpha1.StorageSpec) { spec.Size = resource.MustParse(size) })
		}
		expectClaimRequest := func(size string) {
			GinkgoHelper()
			got := getPVC().Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(got.Cmp(resource.MustParse(size))).To(Equal(0), "claim requests %s, want %s", got.String(), size)
		}
		// boundWithClass provisions a bound 20Gi claim in a StorageClass whose
		// allowVolumeExpansion is allow, reconciled to Ready, with no pending
		// Events.
		boundWithClass := func(allow *bool) {
			GinkgoHelper()
			createRequestWith(withClass(createClass(allow)))
			provision()
			bind("20Gi")
			reconcileOnce()
			expectCondition(getRequest(), storagev1alpha1.ConditionReady, metav1.ConditionTrue, storagev1alpha1.ReasonBound)
			drainEvents()
		}
		// failedExpansion provisions a claim requesting 20Gi that is bound
		// with an actual capacity of 10Gi and the given resize status.
		failedExpansion := func(allow *bool, resize corev1.ClaimResourceStatus) {
			GinkgoHelper()
			createRequestWith(withClass(createClass(allow)))
			provision()
			bind("10Gi")
			setPVCStatus(func(status *corev1.PersistentVolumeClaimStatus) {
				status.AllocatedResourceStatuses = map[corev1.ResourceName]corev1.ClaimResourceStatus{
					corev1.ResourceStorage: resize,
				}
			})
			reconcileOnce()
			drainEvents()
		}

		It("expands a bound claim whose StorageClass allows expansion, exactly once", func() {
			boundWithClass(new(true))

			setSize("30Gi")
			reconcileOnce()

			expectClaimRequest("30Gi")
			got := getRequest()
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionFalse, storagev1alpha1.ReasonResizing)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionFalse, storagev1alpha1.ReasonAsExpected)
			Expect(drainEvents()).To(ConsistOf(HavePrefix("Normal PVCResizing ")))

			before := getPVC().ResourceVersion
			reconcileOnce()
			Expect(getPVC().ResourceVersion).To(Equal(before), "the same target is not patched twice")
			Expect(drainEvents()).To(BeEmpty())

			By("the claim converging")
			setPVCStatus(func(status *corev1.PersistentVolumeClaimStatus) {
				status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("30Gi")}
			})
			reconcileOnce()
			expectCondition(getRequest(), storagev1alpha1.ConditionReady, metav1.ConditionTrue, storagev1alpha1.ReasonBound)
		})

		It("reports ExpansionNotSupported and leaves the claim when the StorageClass disallows expansion", func() {
			boundWithClass(nil)

			setSize("30Gi")
			reconcileOnce()

			expectClaimRequest("20Gi")
			expectProblem(storagev1alpha1.ReasonExpansionNotSupported)
			Expect(drainEvents()).To(ConsistOf(HavePrefix("Warning ExpansionNotSupported ")))
		})

		It("reports StorageClassNotFound when the claim's StorageClass does not exist", func() {
			createRequestWith(withClass("does-not-exist-" + namespace))
			provision()
			bind("20Gi")

			setSize("30Gi")
			reconcileOnce()

			expectClaimRequest("20Gi")
			expectProblem(storagev1alpha1.ReasonStorageClassNotFound)
		})

		It("reports StorageClassNotFound for a bound claim without a StorageClass", func() {
			createRequest()
			provision()
			bind("20Gi")

			setSize("30Gi")
			reconcileOnce()

			expectClaimRequest("20Gi")
			expectProblem(storagev1alpha1.ReasonStorageClassNotFound)
		})

		It("defers an increase while the claim is unbound and applies it once bound", func() {
			createRequestWith(withClass(createClass(new(true))))
			provision()
			drainEvents()

			setSize("30Gi")
			reconcileOnce()

			expectClaimRequest("20Gi")
			got := getRequest()
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionFalse, storagev1alpha1.ReasonProvisioning)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionFalse, storagev1alpha1.ReasonAsExpected)
			Expect(drainEvents()).To(BeEmpty())

			bind("20Gi")
			reconcileOnce()

			expectClaimRequest("30Gi")
			expectCondition(getRequest(), storagev1alpha1.ConditionReady, metav1.ConditionFalse, storagev1alpha1.ReasonResizing)
		})

		It("rejects a decrease while the claim is unbound", func() {
			createRequestWith(withClass(createClass(new(true))))
			provision()

			setSize("10Gi")
			reconcileOnce()

			expectClaimRequest("20Gi")
			got := getRequest()
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionFalse, storagev1alpha1.ReasonProvisioning)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionTrue, storagev1alpha1.ReasonShrinkNotSupported)
		})

		It("rejects an ordinary shrink of a bound claim and keeps it Ready", func() {
			boundWithClass(new(true))
			before := getPVC().ResourceVersion

			setSize("10Gi")
			reconcileOnce()

			Expect(getPVC().ResourceVersion).To(Equal(before))
			got := getRequest()
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionTrue, storagev1alpha1.ReasonBound)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionTrue, storagev1alpha1.ReasonShrinkNotSupported)
			Expect(drainEvents()).To(ConsistOf(HavePrefix("Warning ShrinkNotSupported ")))

			By("restoring the size")
			setSize("20Gi")
			reconcileOnce()
			expectCondition(getRequest(), storagev1alpha1.ConditionDegraded, metav1.ConditionFalse, storagev1alpha1.ReasonAsExpected)
		})

		It("lowers a terminally failed expansion target to a value above the actual capacity", func() {
			failedExpansion(new(true), corev1.PersistentVolumeClaimControllerResizeInfeasible)
			expectProblem(storagev1alpha1.ReasonResizeFailed)

			setSize("15Gi")
			reconcileOnce()

			expectClaimRequest("15Gi")
			// Kubernetes has not reacted yet, so the failure is still reported.
			expectProblem(storagev1alpha1.ReasonResizeFailed)
			Expect(drainEvents()).To(BeEmpty())

			By("Kubernetes completing the lower target")
			setPVCStatus(func(status *corev1.PersistentVolumeClaimStatus) {
				status.AllocatedResourceStatuses = nil
				status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("15Gi")}
			})
			reconcileOnce()
			got := getRequest()
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionTrue, storagev1alpha1.ReasonBound)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionFalse, storagev1alpha1.ReasonAsExpected)
		})

		It("rejects a recovery target that is not above the actual capacity", func() {
			failedExpansion(new(true), corev1.PersistentVolumeClaimControllerResizeInfeasible)

			setSize("10Gi")
			reconcileOnce()

			expectClaimRequest("20Gi")
			got := getRequest()
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionTrue, storagev1alpha1.ReasonBound)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionTrue, storagev1alpha1.ReasonShrinkNotSupported)
		})

		It("rejects lowering the target while no terminal failure is reported", func() {
			failedExpansion(new(true), corev1.PersistentVolumeClaimNodeResizeInProgress)

			setSize("15Gi")
			reconcileOnce()

			expectClaimRequest("20Gi")
			got := getRequest()
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionFalse, storagev1alpha1.ReasonResizing)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionTrue, storagev1alpha1.ReasonResizeRecoveryRejected)
		})

		It("rejects recovery when the StorageClass disallows expansion", func() {
			failedExpansion(nil, corev1.PersistentVolumeClaimControllerResizeInfeasible)

			setSize("15Gi")
			reconcileOnce()

			expectClaimRequest("20Gi")
			// Ready keeps the claim assessment (ResizeFailed, capacity below
			// the request); Degraded reports why the recovery was refused.
			got := getRequest()
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionFalse, storagev1alpha1.ReasonResizeFailed)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionTrue, storagev1alpha1.ReasonExpansionNotSupported)
		})

		It("reports ImmutableFieldChange for changed access modes and applies no size change", func() {
			boundWithClass(new(true))
			before := getPVC().ResourceVersion

			updateSpec(func(spec *storagev1alpha1.StorageSpec) {
				spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
				spec.Size = resource.MustParse("30Gi")
			})
			reconcileOnce()

			Expect(getPVC().ResourceVersion).To(Equal(before), "the claim must not be changed")
			expectProblem(storagev1alpha1.ReasonImmutableFieldChange)
			Expect(drainEvents()).To(ConsistOf(HavePrefix("Warning ImmutableFieldChange ")))

			By("reverting the access modes: the size increase is applied")
			updateSpec(func(spec *storagev1alpha1.StorageSpec) {
				spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
			})
			reconcileOnce()
			expectClaimRequest("30Gi")
			expectCondition(getRequest(), storagev1alpha1.ConditionDegraded, metav1.ConditionFalse, storagev1alpha1.ReasonAsExpected)
		})

		It("reports ImmutableFieldChange for a changed volume mode", func() {
			boundWithClass(new(true))

			updateSpec(func(spec *storagev1alpha1.StorageSpec) {
				spec.VolumeMode = new(corev1.PersistentVolumeBlock)
			})
			reconcileOnce()

			got := getRequest()
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionTrue, storagev1alpha1.ReasonBound)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionTrue, storagev1alpha1.ReasonImmutableFieldChange)
			Expect(getPVC().Spec.VolumeMode).To(HaveValue(Equal(corev1.PersistentVolumeFilesystem)))
		})

		It("reports ImmutableFieldChange for a changed explicit StorageClass", func() {
			boundWithClass(new(true))

			updateSpec(func(spec *storagev1alpha1.StorageSpec) { spec.StorageClassName = new("other-class") })
			reconcileOnce()

			degraded := meta.FindStatusCondition(getRequest().Status.Conditions, storagev1alpha1.ConditionDegraded)
			Expect(degraded.Reason).To(Equal(storagev1alpha1.ReasonImmutableFieldChange))
			Expect(degraded.Message).To(ContainSubstring(`storageClassName "other-class"`))
			Expect(degraded.Message).To(ContainSubstring(namespace + "-class"))
		})

		It("accepts the claim's StorageClass when the request omits it", func() {
			sr := createRequest() // no class
			// A claim created with a class, as Kubernetes retroactive
			// defaulting would leave it, carrying this request's UID.
			class := "assigned-default"
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace, Name: requestName,
					Annotations: map[string]string{storagev1alpha1.AnnotationRequestUID: string(sr.UID)},
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: &class,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())

			reconcileOnce() // finalizer
			reconcileOnce() // records the UID
			reconcileOnce() // verified: compares immutable fields

			expectCondition(getRequest(), storagev1alpha1.ConditionDegraded, metav1.ConditionFalse, storagev1alpha1.ReasonAsExpected)
		})

		It("changes nothing in the reconcile that records an unrecorded claim", func() {
			class := createClass(new(true))
			sr := createRequestWith(withClass(class))
			// A 1Gi claim created before a crash, bound meanwhile, that the
			// request has not recorded yet.
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace, Name: requestName,
					Annotations: map[string]string{storagev1alpha1.AnnotationRequestUID: string(sr.UID)},
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: &class,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())
			bind("1Gi")
			reconcileOnce() // finalizer

			reconcileOnce()

			Expect(getRequest().Status.PVCUID).To(Equal(pvc.UID))
			expectClaimRequest("1Gi")

			reconcileOnce()

			expectClaimRequest("20Gi")
		})

		It("returns the error and leaves status when the claim patch fails", func() {
			boundWithClass(new(true))
			before := getRequest()
			setSize("30Gi")
			failPatch := interceptor.NewClient(k8sClient, interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch,
					opts ...client.PatchOption) error {
					if isPVC(obj) {
						return errInjected
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
			})

			_, err := reconcileWith(failPatch)

			Expect(err).To(MatchError(errInjected))
			expectClaimRequest("20Gi")
			Expect(getRequest().Status.Conditions).To(Equal(before.Status.Conditions))
			Expect(drainEvents()).To(BeEmpty())
		})

		It("reports ResizeRejected instead of looping when the API server refuses the resize patch", func() {
			boundWithClass(new(true))
			setSize("30Gi")
			// The API server can refuse the request change itself, for example
			// for a statically provisioned claim. That is a persistent state,
			// not a transient error, so it must surface as a condition.
			rejectPatch := interceptor.NewClient(k8sClient, interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch,
					opts ...client.PatchOption) error {
					if isPVC(obj) {
						return apierrors.NewForbidden(
							schema.GroupResource{Resource: resourcePVC}, obj.GetName(),
							errors.New("only dynamically provisioned pvc can be resized"))
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
			})

			_, err := reconcileWith(rejectPatch)

			Expect(err).NotTo(HaveOccurred(), "a refused patch must not loop in backoff")
			expectClaimRequest("20Gi")
			got := getRequest()
			expectCondition(got, storagev1alpha1.ConditionReady, metav1.ConditionFalse, storagev1alpha1.ReasonResizeRejected)
			expectCondition(got, storagev1alpha1.ConditionDegraded, metav1.ConditionTrue, storagev1alpha1.ReasonResizeRejected)
			degraded := meta.FindStatusCondition(got.Status.Conditions, storagev1alpha1.ConditionDegraded)
			Expect(degraded.Message).To(ContainSubstring("only dynamically provisioned pvc can be resized"))
			Expect(drainEvents()).To(ContainElement(HavePrefix("Warning ResizeRejected ")))
		})
	})

	Context("finalization", func() {
		deleteRequest := func() {
			GinkgoHelper()
			Expect(k8sClient.Delete(ctx, getRequest())).To(Succeed())
		}
		expectRequestGone := func() {
			GinkgoHelper()
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key, &storagev1alpha1.StorageRequest{}))).To(BeTrue(),
				"the StorageRequest should be gone once the finalizer is removed")
		}
		createDeleteRequest := func() *storagev1alpha1.StorageRequest {
			GinkgoHelper()
			sr := &storagev1alpha1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: requestName},
				Spec: storagev1alpha1.StorageRequestSpec{
					Storage:   storagev1alpha1.StorageSpec{Size: resource.MustParse("20Gi")},
					Lifecycle: storagev1alpha1.LifecycleSpec{OnDelete: storagev1alpha1.OnDeleteDelete},
				},
			}
			Expect(k8sClient.Create(ctx, sr)).To(Succeed())
			return sr
		}
		// clearPVCProtection removes the pvc-protection finalizer the API server
		// adds, standing in for the protection controller that does not run in
		// envtest, so a terminating PVC can actually disappear.
		clearPVCProtection := func() {
			GinkgoHelper()
			pvc := &corev1.PersistentVolumeClaim{}
			if apierrors.IsNotFound(k8sClient.Get(ctx, key, pvc)) {
				return
			}
			base := pvc.DeepCopy()
			pvc.Finalizers = nil
			Expect(k8sClient.Patch(ctx, pvc, client.MergeFrom(base))).To(Succeed())
		}

		Context("Retain", func() {
			It("detaches the PVC and removes the finalizer, leaving the claim in place", func() {
				sr := createRequest() // Retain is the default
				pvc := provision()
				deleteRequest()

				result := reconcileOnce()

				Expect(result).To(Equal(ctrl.Result{}))
				expectRequestGone()
				retained := getPVC()
				Expect(retained.UID).To(Equal(pvc.UID))
				Expect(retained.DeletionTimestamp.IsZero()).To(BeTrue(), "the claim must not be deleted")
				Expect(retained.Annotations).To(HaveKeyWithValue(storagev1alpha1.AnnotationOrphanedFrom, string(sr.UID)))
				Expect(retained.Labels).NotTo(HaveKey(storagev1alpha1.LabelManaged))
				Expect(drainEvents()).To(ContainElement(HavePrefix("Normal StorageRetained ")))
			})

			It("completes when the PVC is already gone", func() {
				createRequest()
				provision()
				removePVC()
				deleteRequest()

				reconcileOnce()

				expectRequestGone()
			})

			It("leaves an unowned same-name PVC untouched and still completes", func() {
				createRequest()
				reconcileOnce() // finalizer only; no PVC recorded
				foreign := createForeignPVC("")
				deleteRequest()

				reconcileOnce()

				expectRequestGone()
				after := getPVC()
				Expect(after.ResourceVersion).To(Equal(foreign.ResourceVersion), "the unowned claim must not be changed")
				Expect(after.Annotations).NotTo(HaveKey(storagev1alpha1.AnnotationOrphanedFrom))
				Expect(drainEvents()).To(ContainElement(HavePrefix("Warning OwnershipMismatch ")))
			})

			It("does not claim retention when the PVC is already terminating", func() {
				createRequest()
				pvc := provision()
				Expect(k8sClient.Delete(ctx, pvc)).To(Succeed()) // pvc-protection keeps it terminating
				Expect(getPVC().DeletionTimestamp.IsZero()).To(BeFalse())
				deleteRequest()

				reconcileOnce()

				expectRequestGone()
				terminating := getPVC()
				Expect(terminating.Annotations).NotTo(HaveKey(storagev1alpha1.AnnotationOrphanedFrom),
					"a terminating claim is not detached or marked retained")
				Expect(drainEvents()).To(ContainElement(HavePrefix("Warning PVCDeleting ")))
			})
		})

		Context("Delete", func() {
			It("deletes the owned PVC with a UID precondition, then removes the finalizer", func() {
				createDeleteRequest()
				provision()
				deleteRequest()

				// The first reconcile requests deletion; pvc-protection keeps the
				// claim terminating until the protection finalizer is cleared.
				Expect(reconcileOnce().RequeueAfter).To(Equal(staleCacheRequeue))
				Expect(getPVC().DeletionTimestamp.IsZero()).To(BeFalse())
				Expect(getRequest().DeletionTimestamp.IsZero()).To(BeFalse(), "the request waits for the claim")
				Expect(drainEvents()).To(ContainElement(HavePrefix("Normal PVCDeletionRequested ")))

				clearPVCProtection()
				reconcileOnce()

				expectRequestGone()
				Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.PersistentVolumeClaim{}))).To(BeTrue())
			})

			It("waits while the PVC is still terminating and keeps the finalizer", func() {
				createDeleteRequest()
				provision()
				deleteRequest()
				reconcileOnce() // requests deletion

				Expect(reconcileOnce().RequeueAfter).To(Equal(fallbackRequeue), "still terminating")

				got := getRequest()
				Expect(got.DeletionTimestamp.IsZero()).To(BeFalse())
				Expect(controllerutil.ContainsFinalizer(got, storagev1alpha1.Finalizer)).To(BeTrue())
			})

			It("keeps the request finalizer when ownership cannot be proven", func() {
				createDeleteRequest()
				reconcileOnce() // finalizer only; nothing recorded
				foreign := createForeignPVC("")
				deleteRequest()

				Expect(reconcileOnce().RequeueAfter).To(Equal(fallbackRequeue))

				got := getRequest()
				Expect(got.DeletionTimestamp.IsZero()).To(BeFalse())
				Expect(controllerutil.ContainsFinalizer(got, storagev1alpha1.Finalizer)).To(BeTrue(),
					"an unprovable claim blocks deletion for an administrator to resolve")
				Expect(getPVC().ResourceVersion).To(Equal(foreign.ResourceVersion))
				Expect(drainEvents()).To(ContainElement(HavePrefix("Warning OwnershipMismatch ")))
			})

			It("never deletes a same-name replacement with a different UID", func() {
				sr := createDeleteRequest()
				original := provision()
				removePVC()
				replacement := createForeignPVC(string(sr.UID)) // request-UID copied, new PVC UID
				Expect(replacement.UID).NotTo(Equal(original.UID))
				deleteRequest()

				Expect(reconcileOnce().RequeueAfter).To(Equal(fallbackRequeue))

				got := getPVC()
				Expect(got.UID).To(Equal(replacement.UID))
				Expect(got.DeletionTimestamp.IsZero()).To(BeTrue(), "the replacement must not be deleted")
				Expect(controllerutil.ContainsFinalizer(getRequest(), storagev1alpha1.Finalizer)).To(BeTrue())
			})
		})
	})
})
