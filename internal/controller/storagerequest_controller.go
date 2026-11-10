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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
	"github.com/mohamadar77/storage-request-operator/internal/storage"
)

// fallbackRequeue is the bounded requeue used for states that can change
// without producing an event this controller watches. PVC watches arrive in a
// later milestone; until then this is also how binding is noticed.
const fallbackRequeue = 30 * time.Second

// staleCacheRequeue is the short retry used when the cached StorageRequest
// disagrees with the API server, so the next attempt reads a fresher cache.
const staleCacheRequeue = time.Second

// StorageRequestReconciler reconciles a StorageRequest object.
//
// Safety contract for this milestone (design v1.1, sections 5 and 6.2):
//
//   - the finalizer is persisted before any PVC is created;
//   - a PVC is created only when no PVC UID was ever recorded, and that is
//     confirmed against the API server, not only the cache;
//   - the created PVC's UID is persisted to status immediately;
//   - an existing PVC is never adopted, changed, or deleted unless the request
//     UID annotation and the recorded PVC UID both match;
//   - a recorded PVC that disappears is reported, never recreated.
//
// The controller does not yet change or delete any PVC it did not just create.
type StorageRequestReconciler struct {
	client.Client
	// APIReader reads directly from the API server. It is used where acting on
	// a stale cache could create a second PVC: before creating one, and when
	// resolving an AlreadyExists response.
	APIReader client.Reader
	Scheme    *runtime.Scheme
}

// +kubebuilder:rbac:groups=storage.example.io,resources=storagerequests,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=storage.example.io,resources=storagerequests/status,verbs=get;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create

// Reconcile moves one StorageRequest toward its desired state. It is
// idempotent and never waits for provisioning.
func (r *StorageRequestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	sr := &storagev1alpha1.StorageRequest{}
	if err := r.Get(ctx, req.NamespacedName, sr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !sr.DeletionTimestamp.IsZero() {
		// Finalization (Retain/Delete) is implemented in a later milestone.
		// Until then the finalizer is deliberately left in place: removing it
		// here would skip the deletion policy the user selected.
		if controllerutil.ContainsFinalizer(sr, storagev1alpha1.Finalizer) {
			log.Info("Skipped StorageRequest finalization because it is not implemented yet; finalizer kept",
				"requestUID", sr.UID)
		}
		return ctrl.Result{}, nil
	}

	// The finalizer must be persisted before any PVC exists. Persist it and
	// return; the resulting update event triggers the next reconcile.
	if !controllerutil.ContainsFinalizer(sr, storagev1alpha1.Finalizer) {
		base := sr.DeepCopy()
		controllerutil.AddFinalizer(sr, storagev1alpha1.Finalizer)
		// The optimistic lock makes the patch fail on a concurrent change
		// instead of overwriting finalizers added by other controllers.
		patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
		if err := r.Patch(ctx, sr, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer to StorageRequest: %w", err)
		}
		log.Info("Added finalizer to StorageRequest", "requestUID", sr.UID)
		return ctrl.Result{}, nil
	}

	// Never build a PVC from a spec that fails defensive validation, even if
	// it was admitted (for example, before a schema change).
	if errs := sr.ValidateStorageRequest(); len(errs) > 0 {
		message := "Spec failed validation, so no PersistentVolumeClaim was created or changed: " +
			errs.ToAggregate().Error()
		log.Info("StorageRequest spec failed validation", "requestUID", sr.UID, "errors", errs.ToAggregate().Error())
		return ctrl.Result{}, r.patchStatus(ctx, sr, func(status *storagev1alpha1.StorageRequestStatus) {
			setProblem(status, sr.Generation, storagev1alpha1.ReasonInvalidSpec, message)
		})
	}

	pvc := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, req.NamespacedName, pvc)
	switch {
	case apierrors.IsNotFound(err):
		return r.reconcileAbsentPVC(ctx, sr)
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("reading PersistentVolumeClaim: %w", err)
	}
	return r.reconcileExistingPVC(ctx, sr, pvc)
}

// reconcileAbsentPVC handles the case where no PVC with the request's name is
// visible. It creates the PVC only for a request that has never recorded one.
func (r *StorageRequestReconciler) reconcileAbsentPVC(
	ctx context.Context, cached *storagev1alpha1.StorageRequest,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if cached.Status.PVCUID != "" {
		return r.reportMissing(ctx, cached)
	}

	// The cache can lag behind this controller's own writes. Creating a PVC
	// from a cached request that does not yet show a recorded PVC UID would
	// recreate a claim that was recorded and then lost, so confirm the
	// decision against the API server first.
	sr := &storagev1alpha1.StorageRequest{}
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(cached), sr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if sr.UID != cached.UID || !sr.DeletionTimestamp.IsZero() ||
		!controllerutil.ContainsFinalizer(sr, storagev1alpha1.Finalizer) {
		log.Info("Skipped PersistentVolumeClaim creation because the cached StorageRequest was stale",
			"requestUID", cached.UID)
		return ctrl.Result{RequeueAfter: staleCacheRequeue}, nil
	}
	if sr.Status.PVCUID != "" {
		return r.reportMissing(ctx, sr)
	}

	pvc := storage.BuildPVC(sr)
	err := r.Create(ctx, pvc)
	switch {
	case apierrors.IsAlreadyExists(err):
		// AlreadyExists is never permission to adopt. Read the object that is
		// actually there and run the normal identity checks on it.
		existing := &corev1.PersistentVolumeClaim{}
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(sr), existing); err != nil {
			return ctrl.Result{}, fmt.Errorf("reading PersistentVolumeClaim after AlreadyExists: %w", err)
		}
		return r.reconcileExistingPVC(ctx, sr, existing)
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("creating PersistentVolumeClaim: %w", err)
	}
	log.Info("Created PersistentVolumeClaim", "requestUID", sr.UID, "pvcUID", pvc.UID)

	// The new PVC carries this request's UID and no PVC UID is recorded yet,
	// so the shared path below records its UID in the very next call. If that
	// write fails the error is returned and the next reconcile recovers by
	// reading the still-existing PVC.
	return r.reconcileExistingPVC(ctx, sr, pvc)
}

// reconcileExistingPVC verifies the identity of an existing same-name PVC and
// reports the result. It records the PVC UID the first time the claim is
// verified, and it never modifies the PVC.
func (r *StorageRequestReconciler) reconcileExistingPVC(
	ctx context.Context, sr *storagev1alpha1.StorageRequest, pvc *corev1.PersistentVolumeClaim,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	identity, message := storage.CheckIdentity(sr, pvc)
	switch identity {
	case storage.IdentityNameConflict, storage.IdentityOwnershipMismatch:
		reason := storagev1alpha1.ReasonOwnershipMismatch
		if identity == storage.IdentityNameConflict {
			reason = storagev1alpha1.ReasonPVCNameConflict
		}
		log.Info("PersistentVolumeClaim failed the identity check and was left unchanged",
			"requestUID", sr.UID, "pvcUID", pvc.UID, "reason", reason)
		err := r.patchStatus(ctx, sr, func(status *storagev1alpha1.StorageRequestStatus) {
			setProblem(status, sr.Generation, reason, message)
		})
		return ctrl.Result{RequeueAfter: fallbackRequeue}, err
	case storage.IdentityUnrecorded:
		log.Info("Recording PersistentVolumeClaim UID", "requestUID", sr.UID, "pvcUID", pvc.UID)
	case storage.IdentityVerified:
	}

	// Identity holds. Record the PVC UID (a no-op once recorded) together with
	// the observed state, in a single status write.
	terminating := !pvc.DeletionTimestamp.IsZero()
	err := r.patchStatus(ctx, sr, func(status *storagev1alpha1.StorageRequestStatus) {
		status.PVCName = pvc.Name
		status.PVCUID = pvc.UID
		if terminating {
			setProblem(status, sr.Generation, storagev1alpha1.ReasonPVCDeleting, fmt.Sprintf(
				"PersistentVolumeClaim %s is being deleted while this request is active; "+
					"no replacement will be created", pvc.Name))
			return
		}
		setHealthy(status, sr, pvc)
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("recording PersistentVolumeClaim state in status: %w", err)
	}
	if terminating {
		log.Info("PersistentVolumeClaim is being deleted while its StorageRequest is active",
			"requestUID", sr.UID, "pvcUID", pvc.UID)
	}
	if meta.IsStatusConditionTrue(sr.Status.Conditions, storagev1alpha1.ConditionReady) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: fallbackRequeue}, nil
}

// reportMissing reports that the recorded PVC no longer exists. The claim is
// never recreated: a replacement would be empty and would mask a storage
// incident.
func (r *StorageRequestReconciler) reportMissing(
	ctx context.Context, sr *storagev1alpha1.StorageRequest,
) (ctrl.Result, error) {
	logf.FromContext(ctx).Info("Recorded PersistentVolumeClaim is missing and was not recreated",
		"requestUID", sr.UID, "pvcUID", sr.Status.PVCUID)
	message := fmt.Sprintf(
		"PersistentVolumeClaim %s (UID %s) no longer exists. It is not recreated automatically; "+
			"inspect the PersistentVolume and backend before creating a new StorageRequest",
		sr.Name, sr.Status.PVCUID)
	err := r.patchStatus(ctx, sr, func(status *storagev1alpha1.StorageRequestStatus) {
		setProblem(status, sr.Generation, storagev1alpha1.ReasonPVCMissing, message)
	})
	return ctrl.Result{}, err
}

// patchStatus applies mutate to the status and writes it only when something
// changed. The write goes through the status subresource and cannot alter spec.
func (r *StorageRequestReconciler) patchStatus(
	ctx context.Context, sr *storagev1alpha1.StorageRequest, mutate func(*storagev1alpha1.StorageRequestStatus),
) error {
	base := sr.DeepCopy()
	mutate(&sr.Status)
	sr.Status.ObservedGeneration = sr.Generation
	if equality.Semantic.DeepEqual(base.Status, sr.Status) {
		return nil
	}
	return r.Status().Patch(ctx, sr, client.MergeFrom(base))
}

// setProblem reports a persistent, actionable problem: Ready=False and
// Degraded=True with the same reason. Transition times are preserved when the
// condition status does not change.
func setProblem(status *storagev1alpha1.StorageRequestStatus, generation int64, reason, message string) {
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               storagev1alpha1.ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               storagev1alpha1.ConditionDegraded,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}

// setHealthy reports a verified, non-terminating PVC: Degraded=False, and Ready
// according to the claim's phase and reported capacity. Resize awareness is
// added with the resize milestone; until then the controller never changes the
// PVC size, so Ready=True still requires capacity at least the requested size.
func setHealthy(
	status *storagev1alpha1.StorageRequestStatus, sr *storagev1alpha1.StorageRequest, pvc *corev1.PersistentVolumeClaim,
) {
	ready := metav1.Condition{
		Type:               storagev1alpha1.ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             storagev1alpha1.ReasonProvisioning,
		Message:            fmt.Sprintf("PersistentVolumeClaim %s is not yet bound with the requested capacity.", pvc.Name),
		ObservedGeneration: sr.Generation,
	}
	switch pvc.Status.Phase {
	case corev1.ClaimLost:
		ready.Reason = storagev1alpha1.ReasonVolumeLost
		ready.Message = fmt.Sprintf(
			"PersistentVolumeClaim %s lost its PersistentVolume. It is not recreated automatically.", pvc.Name)
	case corev1.ClaimBound:
		if capacity, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok && capacity.Cmp(sr.Spec.Storage.Size) >= 0 {
			ready.Status = metav1.ConditionTrue
			ready.Reason = storagev1alpha1.ReasonBound
			ready.Message = fmt.Sprintf("PersistentVolumeClaim %s is bound and has the requested capacity.", pvc.Name)
		}
	}
	meta.SetStatusCondition(&status.Conditions, ready)
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               storagev1alpha1.ConditionDegraded,
		Status:             metav1.ConditionFalse,
		Reason:             storagev1alpha1.ReasonAsExpected,
		Message:            "No storage request errors detected.",
		ObservedGeneration: sr.Generation,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *StorageRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.StorageRequest{}).
		Named("storagerequest").
		Complete(r)
}
