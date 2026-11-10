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

// fallbackRequeue is the bounded requeue used for every state of an active
// request, Ready included: a PVC can be bound, deleted, replaced, or lost
// without producing an event this controller watches. PVC watches arrive in a
// later milestone; this requeue then remains as the fallback for missed events.
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
//   - a recorded PVC is reported missing only when the API server, not only
//     the cache, says it is gone;
//   - status writes carry the resourceVersion they were computed from, so a
//     decision made on a stale object is rejected instead of persisted;
//   - an existing PVC is never adopted, changed, or deleted unless the request
//     UID annotation and the recorded PVC UID both match;
//   - a recorded PVC that disappears is reported, never recreated.
//
// The controller does not yet change or delete any PVC it did not just create.
type StorageRequestReconciler struct {
	client.Client
	// APIReader reads directly from the API server. It is used wherever a PVC
	// appears to be absent, because acting on a lagging cache there could
	// create a second PVC or report an existing one as missing.
	APIReader client.Reader
	Scheme    *runtime.Scheme
}

// +kubebuilder:rbac:groups=storage.example.io,resources=storagerequests,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=storage.example.io,resources=storagerequests/status,verbs=get;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create

// Reconcile moves one StorageRequest toward its desired state. It is
// idempotent and never waits for provisioning.
func (r *StorageRequestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	sr := &storagev1alpha1.StorageRequest{}
	if err := r.Get(ctx, req.NamespacedName, sr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := logf.FromContext(ctx).WithValues("requestUID", sr.UID, "generation", sr.Generation)
	ctx = logf.IntoContext(ctx, log)

	if !sr.DeletionTimestamp.IsZero() {
		// Finalization (Retain/Delete) is implemented in a later milestone.
		// Until then the finalizer is deliberately left in place: removing it
		// here would skip the deletion policy the user selected.
		if controllerutil.ContainsFinalizer(sr, storagev1alpha1.Finalizer) {
			log.Info("Skipped StorageRequest finalization because it is not implemented yet; finalizer kept")
		}
		return ctrl.Result{}, nil
	}

	// The finalizer must be persisted before any PVC exists. Persist it and
	// return; the resulting update event triggers the next reconcile. Any
	// event predicate added to this controller must therefore let metadata
	// updates of the StorageRequest through.
	if !controllerutil.ContainsFinalizer(sr, storagev1alpha1.Finalizer) {
		base := sr.DeepCopy()
		controllerutil.AddFinalizer(sr, storagev1alpha1.Finalizer)
		// The optimistic lock makes the patch fail on a concurrent change
		// instead of overwriting finalizers added by other controllers.
		patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
		if err := r.Patch(ctx, sr, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer to StorageRequest: %w", err)
		}
		log.Info("Added finalizer to StorageRequest")
		return ctrl.Result{}, nil
	}

	if invalid, err := r.rejectInvalidSpec(ctx, sr); invalid {
		return ctrl.Result{}, err
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

// rejectInvalidSpec reports InvalidSpec when the spec fails defensive
// validation and tells the caller to stop. A PVC is never built from such a
// spec, even if it was admitted (for example, before a schema change). No
// requeue is needed: only a spec change, which produces an event, can fix it.
func (r *StorageRequestReconciler) rejectInvalidSpec(
	ctx context.Context, sr *storagev1alpha1.StorageRequest,
) (bool, error) {
	errs := sr.ValidateStorageRequest()
	if len(errs) == 0 {
		return false, nil
	}
	detail := errs.ToAggregate().Error()
	logf.FromContext(ctx).Info("StorageRequest spec failed validation", "errors", detail)
	err := r.patchStatus(ctx, sr, func(status *storagev1alpha1.StorageRequestStatus) {
		setProblem(status, sr.Generation, storagev1alpha1.ReasonInvalidSpec,
			"Spec failed validation, so no PersistentVolumeClaim was created or changed: "+detail)
	})
	if err != nil {
		return true, fmt.Errorf("reporting invalid spec in status: %w", err)
	}
	return true, nil
}

// reconcileAbsentPVC handles the case where the cache shows no PVC with the
// request's name. The cache can lag behind the API server and behind this
// controller's own writes, so nothing here is decided from the cache: the
// request is re-read from the API server, and a recorded PVC is reported
// missing only after the API server confirms it is gone.
func (r *StorageRequestReconciler) reconcileAbsentPVC(
	ctx context.Context, cached *storagev1alpha1.StorageRequest,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	key := client.ObjectKeyFromObject(cached)

	sr := &storagev1alpha1.StorageRequest{}
	if err := r.APIReader.Get(ctx, key, sr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if sr.UID != cached.UID || !sr.DeletionTimestamp.IsZero() ||
		!controllerutil.ContainsFinalizer(sr, storagev1alpha1.Finalizer) {
		log.Info("Skipped PersistentVolumeClaim handling because the cached StorageRequest was stale")
		return ctrl.Result{RequeueAfter: staleCacheRequeue}, nil
	}

	if sr.Status.PVCUID != "" {
		// A PVC was recorded. It is never recreated; the only question is
		// whether it is really gone or the cache has not seen it yet.
		pvc := &corev1.PersistentVolumeClaim{}
		err := r.APIReader.Get(ctx, key, pvc)
		switch {
		case apierrors.IsNotFound(err):
			return r.reportMissing(ctx, sr)
		case err != nil:
			return ctrl.Result{}, fmt.Errorf("confirming PersistentVolumeClaim absence: %w", err)
		}
		return r.reconcileExistingPVC(ctx, sr, pvc)
	}

	// The PVC is built from the object just read, so validate that object too:
	// the cached copy that passed validation may be an older generation.
	if invalid, err := r.rejectInvalidSpec(ctx, sr); invalid {
		return ctrl.Result{}, err
	}

	pvc := storage.BuildPVC(sr)
	err := r.Create(ctx, pvc)
	switch {
	case apierrors.IsAlreadyExists(err):
		// AlreadyExists is never permission to adopt. Read the object that is
		// actually there and run the normal identity checks on it.
		existing := &corev1.PersistentVolumeClaim{}
		if err := r.APIReader.Get(ctx, key, existing); err != nil {
			return ctrl.Result{}, fmt.Errorf("reading PersistentVolumeClaim after AlreadyExists: %w", err)
		}
		return r.reconcileExistingPVC(ctx, sr, existing)
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("creating PersistentVolumeClaim: %w", err)
	}
	log.Info("Created PersistentVolumeClaim", "pvcUID", pvc.UID)

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
	log := logf.FromContext(ctx).WithValues("pvcUID", pvc.UID)

	identity, message := storage.CheckIdentity(sr, pvc)
	if identity == storage.IdentityNameConflict || identity == storage.IdentityOwnershipMismatch {
		reason := storagev1alpha1.ReasonOwnershipMismatch
		if identity == storage.IdentityNameConflict {
			reason = storagev1alpha1.ReasonPVCNameConflict
		}
		log.Info("PersistentVolumeClaim failed the identity check and was left unchanged", "reason", reason)
		err := r.patchStatus(ctx, sr, func(status *storagev1alpha1.StorageRequestStatus) {
			setProblem(status, sr.Generation, reason, message)
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("reporting PersistentVolumeClaim identity failure in status: %w", err)
		}
		return ctrl.Result{RequeueAfter: fallbackRequeue}, nil
	}

	// Identity holds. Record the PVC UID (a no-op once recorded) together with
	// the observed state, in a single status write. If sr came from a cache
	// that does not yet show a recorded UID, the write is rejected as a
	// conflict rather than recording a different PVC.
	terminating := !pvc.DeletionTimestamp.IsZero()
	err := r.patchStatus(ctx, sr, func(status *storagev1alpha1.StorageRequestStatus) {
		status.PVCName = pvc.Name
		status.PVCUID = pvc.UID
		switch {
		case terminating:
			setProblem(status, sr.Generation, storagev1alpha1.ReasonPVCDeleting, fmt.Sprintf(
				"PersistentVolumeClaim %s is being deleted while this request is active; "+
					"no replacement will be created", pvc.Name))
		case pvc.Status.Phase == corev1.ClaimLost:
			setProblem(status, sr.Generation, storagev1alpha1.ReasonVolumeLost, fmt.Sprintf(
				"PersistentVolumeClaim %s lost its PersistentVolume. It is not recreated automatically; "+
					"inspect the PersistentVolume and backend", pvc.Name))
		default:
			setHealthy(status, sr, pvc)
		}
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("recording PersistentVolumeClaim state in status: %w", err)
	}
	if identity == storage.IdentityUnrecorded {
		log.Info("Recorded PersistentVolumeClaim UID")
	}
	if terminating {
		log.Info("Observed PersistentVolumeClaim deletion while its StorageRequest was active")
	}
	return ctrl.Result{RequeueAfter: fallbackRequeue}, nil
}

// reportMissing reports that the recorded PVC no longer exists. Callers must
// have confirmed the absence against the API server. The claim is never
// recreated: a replacement would be empty and would mask a storage incident.
func (r *StorageRequestReconciler) reportMissing(
	ctx context.Context, sr *storagev1alpha1.StorageRequest,
) (ctrl.Result, error) {
	logf.FromContext(ctx).Info("Recorded PersistentVolumeClaim was missing and was not recreated",
		"pvcUID", sr.Status.PVCUID)
	message := fmt.Sprintf(
		"PersistentVolumeClaim %s (UID %s) no longer exists. It is not recreated automatically; "+
			"inspect the PersistentVolume and backend before creating a new StorageRequest",
		sr.Name, sr.Status.PVCUID)
	err := r.patchStatus(ctx, sr, func(status *storagev1alpha1.StorageRequestStatus) {
		setProblem(status, sr.Generation, storagev1alpha1.ReasonPVCMissing, message)
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reporting missing PersistentVolumeClaim in status: %w", err)
	}
	// Keep checking: a same-name PVC created later must be reported as an
	// ownership mismatch, and nothing else would trigger that reconcile.
	return ctrl.Result{RequeueAfter: fallbackRequeue}, nil
}

// patchStatus applies mutate to the status and writes it only when something
// changed. The write goes through the status subresource and cannot alter
// spec. It carries the resourceVersion of sr, so a status computed from a stale
// object fails with a conflict and is retried instead of overwriting newer
// state.
func (r *StorageRequestReconciler) patchStatus(
	ctx context.Context, sr *storagev1alpha1.StorageRequest, mutate func(*storagev1alpha1.StorageRequestStatus),
) error {
	base := sr.DeepCopy()
	mutate(&sr.Status)
	sr.Status.ObservedGeneration = sr.Generation
	if equality.Semantic.DeepEqual(base.Status, sr.Status) {
		return nil
	}
	return r.Status().Patch(ctx, sr, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
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

// setHealthy reports a verified claim with no detected problem: Degraded=False,
// and Ready=True only when the claim is bound with at least the requested
// capacity. Resize awareness is added with the resize milestone; until then the
// controller never changes the PVC size.
func setHealthy(
	status *storagev1alpha1.StorageRequestStatus, sr *storagev1alpha1.StorageRequest, pvc *corev1.PersistentVolumeClaim,
) {
	ready := metav1.Condition{
		Type:               storagev1alpha1.ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             storagev1alpha1.ReasonProvisioning,
		Message:            fmt.Sprintf("PersistentVolumeClaim %s is not bound yet.", pvc.Name),
		ObservedGeneration: sr.Generation,
	}
	if pvc.Status.Phase == corev1.ClaimBound {
		capacity, reported := pvc.Status.Capacity[corev1.ResourceStorage]
		switch {
		case !reported:
			ready.Message = fmt.Sprintf("PersistentVolumeClaim %s is bound but reports no capacity yet.", pvc.Name)
		case capacity.Cmp(sr.Spec.Storage.Size) >= 0:
			ready.Status = metav1.ConditionTrue
			ready.Reason = storagev1alpha1.ReasonBound
			ready.Message = fmt.Sprintf("PersistentVolumeClaim %s is bound and has the requested capacity.", pvc.Name)
		default:
			ready.Message = fmt.Sprintf(
				"PersistentVolumeClaim %s is bound with capacity %s, which is below the requested %s.",
				pvc.Name, capacity.String(), sr.Spec.Storage.Size.String())
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
