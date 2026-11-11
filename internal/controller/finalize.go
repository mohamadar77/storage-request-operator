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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
	"github.com/mohamadar77/storage-request-operator/internal/storage"
)

// finalize carries out the StorageRequest's deletion policy and removes the
// finalizer only once that policy is complete (design section 7). It is the
// most data-sensitive path in the operator, so:
//
//   - the PVC is read live (APIReader), never from the cache: a deletion
//     decision must not act on a stale or lagging view of the claim;
//   - the claim is touched only when both available identity checks pass (the
//     request UID annotation and, once recorded, the PVC UID);
//   - under Delete, the claim is never deleted when ownership cannot be proven;
//     the finalizer is kept so an administrator resolves the conflict;
//   - the finalizer is removed only after the selected policy has completed.
//
// The onDelete value is frozen once the request is being deleted, so the cached
// StorageRequest is authoritative for the policy (design section 7.4).
func (r *StorageRequestReconciler) finalize(
	ctx context.Context, sr *storagev1alpha1.StorageRequest,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	key := client.ObjectKeyFromObject(sr)

	pvc := &corev1.PersistentVolumeClaim{}
	err := r.APIReader.Get(ctx, key, pvc)
	switch {
	case apierrors.IsNotFound(err):
		// No claim to act on: finalization is complete under either policy.
		log.Info("No PersistentVolumeClaim to finalize; completing finalization")
		return r.removeFinalizer(ctx, sr)
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("reading PersistentVolumeClaim during finalization: %w", err)
	}

	identity, mismatch := storage.CheckIdentity(sr, pvc)
	// Verified and Unrecorded are the two proven-ownership results: the request
	// UID annotation matches, and the recorded PVC UID either matches or has not
	// been recorded yet (the crash-recovery case). Any other result means the
	// claim cannot be proven to belong to this request.
	proven := identity == storage.IdentityVerified || identity == storage.IdentityUnrecorded

	if sr.Spec.Lifecycle.OnDelete == storagev1alpha1.OnDeleteDelete {
		return r.finalizeDelete(ctx, sr, pvc, proven, mismatch)
	}
	return r.finalizeRetain(ctx, sr, pvc, proven, mismatch)
}

// finalizeRetain implements onDelete=Retain (design section 7.1): detach the
// owned claim and leave it in place. Retain never deletes a claim, so even an
// unprovable or already-terminating claim lets finalization complete; it just
// cannot be detached or honestly reported as retained.
func (r *StorageRequestReconciler) finalizeRetain(
	ctx context.Context, sr *storagev1alpha1.StorageRequest, pvc *corev1.PersistentVolumeClaim,
	proven bool, mismatch string,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !proven {
		// The same-name claim cannot be proven to belong to this request, so it
		// is never modified. Retain would change nothing anyway, so finalization
		// completes rather than blocking the request's deletion.
		log.Info("Retain finalization left an unowned PersistentVolumeClaim untouched", "detail", mismatch)
		r.finalizeEvent(sr, corev1.EventTypeWarning, storagev1alpha1.ReasonOwnershipMismatch, mismatch)
		return r.removeFinalizer(ctx, sr)
	}
	if !pvc.DeletionTimestamp.IsZero() {
		// Another actor already started deleting the claim; Retain cannot cancel
		// that and must not claim the claim was retained.
		message := fmt.Sprintf(
			"PersistentVolumeClaim %s was already being deleted by another actor; "+
				"it was not retained by this request", pvc.Name)
		log.Info("Retain finalization found the PersistentVolumeClaim already terminating; not retained")
		r.finalizeEvent(sr, corev1.EventTypeWarning, storagev1alpha1.ReasonPVCDeleting, message)
		return r.removeFinalizer(ctx, sr)
	}

	// Detach the claim: record which request it was orphaned from and drop the
	// active managed label so it is no longer reconciled as a request's claim.
	// The claim and its data remain in place.
	base := pvc.DeepCopy()
	if pvc.Annotations == nil {
		pvc.Annotations = map[string]string{}
	}
	pvc.Annotations[storagev1alpha1.AnnotationOrphanedFrom] = string(sr.UID)
	delete(pvc.Labels, storagev1alpha1.LabelManaged)
	if err := r.Patch(ctx, pvc, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return ctrl.Result{}, fmt.Errorf("marking PersistentVolumeClaim as retained: %w", err)
	}
	log.Info("Retained and detached PersistentVolumeClaim")
	r.finalizeEvent(sr, corev1.EventTypeNormal, storagev1alpha1.EventStorageRetained, fmt.Sprintf(
		"Retained PersistentVolumeClaim %s and detached it from this request; it is no longer managed", pvc.Name))
	return r.removeFinalizer(ctx, sr)
}

// finalizeDelete implements onDelete=Delete (design section 7.2): delete only
// the exact owned claim, with a UID precondition, and keep the finalizer until
// the claim is actually gone. Ownership that cannot be proven is a safety block,
// never a reason to delete.
func (r *StorageRequestReconciler) finalizeDelete(
	ctx context.Context, sr *storagev1alpha1.StorageRequest, pvc *corev1.PersistentVolumeClaim,
	proven bool, mismatch string,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !proven {
		// Never delete a claim this request cannot prove it owns. Keep the
		// finalizer so the request stays visible and an administrator resolves
		// the conflict; favor data safety over automatic deletion.
		log.Info("Delete finalization blocked: PersistentVolumeClaim ownership could not be proven", "detail", mismatch)
		r.finalizeEvent(sr, corev1.EventTypeWarning, storagev1alpha1.ReasonOwnershipMismatch, mismatch)
		return ctrl.Result{RequeueAfter: fallbackRequeue}, nil
	}
	if !pvc.DeletionTimestamp.IsZero() {
		// Deletion is already under way (requested here earlier, or delayed by
		// PVC protection while a workload still uses it). Wait for it to finish;
		// never bypass or remove Kubernetes protection finalizers.
		log.Info("Waiting for the PersistentVolumeClaim to finish deleting before completing finalization")
		return ctrl.Result{RequeueAfter: fallbackRequeue}, nil
	}

	// Delete exactly this claim incarnation. The UID precondition means a
	// same-name replacement created in the meantime is never deleted.
	uid := pvc.UID
	err := r.Delete(ctx, pvc, client.Preconditions{UID: &uid})
	switch {
	case apierrors.IsNotFound(err):
		// Gone between the read and the delete: finalization is complete.
		return r.removeFinalizer(ctx, sr)
	case apierrors.IsConflict(err):
		// The UID precondition failed: the claim was replaced under the same
		// name. Re-read on the next reconcile and re-check identity.
		log.Info("PersistentVolumeClaim changed under the delete precondition; re-reading")
		return ctrl.Result{RequeueAfter: staleCacheRequeue}, nil
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("deleting PersistentVolumeClaim: %w", err)
	}
	log.Info("Requested PersistentVolumeClaim deletion", "pvcUID", uid)
	r.finalizeEvent(sr, corev1.EventTypeNormal, storagev1alpha1.EventPVCDeletionRequested, fmt.Sprintf(
		"Requested deletion of PersistentVolumeClaim %s", pvc.Name))
	// Keep the finalizer until a later reconcile confirms the claim is gone.
	return ctrl.Result{RequeueAfter: staleCacheRequeue}, nil
}

// removeFinalizer drops the operator finalizer so Kubernetes can delete the
// StorageRequest. It removes only this operator's finalizer and uses an
// optimistic lock, so a concurrent change is retried rather than overwritten.
func (r *StorageRequestReconciler) removeFinalizer(
	ctx context.Context, sr *storagev1alpha1.StorageRequest,
) (ctrl.Result, error) {
	base := sr.DeepCopy()
	controllerutil.RemoveFinalizer(sr, storagev1alpha1.Finalizer)
	if err := r.Patch(ctx, sr, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer from StorageRequest: %w", err)
	}
	logf.FromContext(ctx).Info("Removed finalizer; StorageRequest finalization complete")
	return ctrl.Result{}, nil
}

// finalizeEvent publishes one finalization Event on the request, truncating the
// note to the API limit so a long message never causes the Event to be dropped.
func (r *StorageRequestReconciler) finalizeEvent(sr *storagev1alpha1.StorageRequest, eventType, reason, note string) {
	r.Recorder.Eventf(sr, nil, eventType, reason, eventActionFinalize, "%s", storage.Truncate(note, maxEventNote))
}
