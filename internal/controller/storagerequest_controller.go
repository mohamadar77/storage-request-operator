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
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
	"github.com/mohamadar77/storage-request-operator/internal/conditions"
	"github.com/mohamadar77/storage-request-operator/internal/storage"
)

// fallbackRequeue is the bounded requeue used for every state of an active
// request, Ready included. Watch events on managed PVCs are the primary
// progress signal; this requeue covers what the watch deliberately does not
// see (an unmarked same-name PVC appearing or disappearing) and any missed
// event.
const fallbackRequeue = 30 * time.Second

// staleCacheRequeue is the short retry used when the cached StorageRequest
// disagrees with the API server, so the next attempt reads a fresher cache.
const staleCacheRequeue = time.Second

// Actions recorded with the controller's events.k8s.io/v1 Events.
const (
	eventActionReconcile = "Reconcile"
	eventActionCreate    = "CreatePersistentVolumeClaim"
)

// maxEventNote is the events.k8s.io/v1 limit on an Event's note. The API
// server rejects a longer note and the recorder does not retry, so a note is
// always truncated to fit.
const maxEventNote = 1024

// StorageRequestReconciler reconciles a StorageRequest object.
//
// Safety contract (design v1.1, sections 5, 6.2, and 6.5):
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
//   - a recorded PVC that disappears is reported, never recreated;
//   - status reports a PVC's phase, volume, and capacity only after its
//     identity is verified, and Events are emitted only for state transitions;
//   - the only field ever changed on a PVC is its storage request, only when
//     both identity checks pass, and never below the actual capacity; a
//     changed StorageClass, access mode, or volume mode is reported, and the
//     claim is never replaced.
//
// The controller does not yet delete any PVC.
type StorageRequestReconciler struct {
	client.Client
	// APIReader reads directly from the API server. It is used wherever a PVC
	// appears to be absent, because acting on a lagging cache there could
	// create a second PVC or report an existing one as missing.
	APIReader client.Reader
	Scheme    *runtime.Scheme
	// Recorder publishes events.k8s.io/v1 Events. SetupWithManager provides
	// the manager's recorder when this is nil.
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=storage.example.io,resources=storagerequests,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=storage.example.io,resources=storagerequests/status,verbs=get;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

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
		// The PVC is not read while the spec is invalid, so its last observed
		// phase, volume, and capacity would go stale; do not report them.
		conditions.ClearClaim(status)
		conditions.SetProblem(status, sr.Generation, storagev1alpha1.ReasonInvalidSpec,
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

	// Everything below uses the object just read, so validate it too: the
	// cached copy that passed validation may be an older generation, and an
	// unvalidated size must never be compared or used to build a PVC.
	if invalid, err := r.rejectInvalidSpec(ctx, sr); invalid {
		return ctrl.Result{}, err
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
	r.Recorder.Eventf(sr, pvc, corev1.EventTypeNormal, storagev1alpha1.EventPVCRequested, eventActionCreate,
		"Created PersistentVolumeClaim %s", pvc.Name)

	// The new PVC carries this request's UID and no PVC UID is recorded yet,
	// so the shared path below records its UID in the very next call. If that
	// write fails the error is returned and the next reconcile recovers by
	// reading the still-existing PVC.
	return r.reconcileExistingPVC(ctx, sr, pvc)
}

// reconcileExistingPVC verifies the identity of an existing same-name PVC and
// reports the result. It records the PVC UID the first time the claim is
// verified. Only a claim that passes both identity checks is ever changed, and
// then only its storage request (see reconcileClaimSpec).
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
			// The same-name PVC is not this request's claim: never report its
			// phase, volume, or capacity as if it were.
			conditions.ClearClaim(status)
			conditions.SetProblem(status, sr.Generation, reason, message)
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
	health := conditions.AssessClaim(sr, pvc)
	if identity == storage.IdentityVerified {
		// An unrecorded claim is only recorded in this reconcile; the status
		// write below triggers the next one, which may then change the claim.
		var err error
		if pvc, health, err = r.reconcileClaimSpec(ctx, sr, pvc, health); err != nil {
			return ctrl.Result{}, err
		}
	}
	err := r.patchStatus(ctx, sr, func(status *storagev1alpha1.StorageRequestStatus) {
		status.PVCName = pvc.Name
		status.PVCUID = pvc.UID
		conditions.ProjectClaim(status, pvc)
		conditions.SetHealth(status, sr.Generation, health)
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

// reconcileClaimSpec compares the request's spec with the verified claim and
// applies the one supported change, a storage request update (design sections
// 6.3 and 6.4). It returns the claim as it is after any patch and the health
// to report. A changed immutable field or a rejected size change is reported
// as a problem on the returned health; nothing is patched then.
func (r *StorageRequestReconciler) reconcileClaimSpec(
	ctx context.Context, sr *storagev1alpha1.StorageRequest, pvc *corev1.PersistentVolumeClaim,
	health conditions.Health,
) (*corev1.PersistentVolumeClaim, conditions.Health, error) {
	log := logf.FromContext(ctx)
	if !pvc.DeletionTimestamp.IsZero() || pvc.Status.Phase == corev1.ClaimLost {
		// Reported by the claim assessment; a terminating or lost claim is
		// never changed.
		return pvc, health, nil
	}

	if message := storage.CheckImmutableFields(sr, pvc); message != "" {
		log.Info("StorageRequest changed an immutable field; PersistentVolumeClaim left unchanged")
		return pvc, health.WithProblem(storagev1alpha1.ReasonImmutableFieldChange, message), nil
	}

	var class *storagev1.StorageClass
	if storage.NeedsStorageClass(sr, pvc) && pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName != "" {
		// Read live rather than through the cache: a StorageClass is read only
		// around a resize, which does not justify a cluster-wide informer.
		name := *pvc.Spec.StorageClassName
		class = &storagev1.StorageClass{}
		if err := r.APIReader.Get(ctx, client.ObjectKey{Name: name}, class); err != nil {
			if !apierrors.IsNotFound(err) {
				return pvc, health, fmt.Errorf("reading StorageClass %s: %w", name, err)
			}
			class = nil
		}
	}

	plan := storage.PlanResize(sr, pvc, class)
	switch plan.Action {
	case storage.ResizeRejected:
		log.Info("Rejected StorageRequest size change; PersistentVolumeClaim left unchanged", "reason", plan.Reason)
		return pvc, health.WithProblem(plan.Reason, plan.Message), nil
	case storage.ResizeDeferred:
		log.Info("Deferred PersistentVolumeClaim size increase until the claim is bound",
			"requested", sr.Spec.Storage.Size.String())
		return pvc, health, nil
	case storage.ResizePatch:
		// The cached object is never mutated; the patch is computed between a
		// copy and the desired state, with the claim's resourceVersion as the
		// optimistic lock so a concurrent change rejects it.
		base := pvc.DeepCopy()
		desired := pvc.DeepCopy()
		if desired.Spec.Resources.Requests == nil {
			desired.Spec.Resources.Requests = corev1.ResourceList{}
		}
		desired.Spec.Resources.Requests[corev1.ResourceStorage] = plan.Target
		patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
		if err := r.Patch(ctx, desired, patch); err != nil {
			return pvc, health, fmt.Errorf("patching PersistentVolumeClaim storage request: %w", err)
		}
		log.Info("Patched PersistentVolumeClaim storage request",
			"from", base.Spec.Resources.Requests.Storage().String(), "to", plan.Target.String())
		return desired, conditions.AssessClaim(sr, desired), nil
	}
	return pvc, health, nil
}

// reportMissing reports that the recorded PVC no longer exists. Callers must
// have confirmed the absence against the API server. The claim is never
// recreated: a replacement would be empty and would mask a storage incident.
func (r *StorageRequestReconciler) reportMissing(
	ctx context.Context, sr *storagev1alpha1.StorageRequest,
) (ctrl.Result, error) {
	logf.FromContext(ctx).Info("Recorded PersistentVolumeClaim was missing and was not recreated",
		"pvcUID", sr.Status.PVCUID)
	// Name the last observed PersistentVolume before status forgets it: it is
	// what the administrator has to inspect.
	volume := "the PersistentVolume"
	if sr.Status.VolumeName != "" {
		volume = "PersistentVolume " + sr.Status.VolumeName
	}
	message := fmt.Sprintf(
		"PersistentVolumeClaim %s (UID %s) no longer exists. It is not recreated automatically; "+
			"inspect %s and the backend before creating a new StorageRequest",
		sr.Name, sr.Status.PVCUID, volume)
	err := r.patchStatus(ctx, sr, func(status *storagev1alpha1.StorageRequestStatus) {
		conditions.ClearClaim(status)
		conditions.SetProblem(status, sr.Generation, storagev1alpha1.ReasonPVCMissing, message)
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
//
// After a successful write it emits the Events for the condition transitions
// it persisted (see recordTransitions). Tying an Event to the write that
// persisted the new state emits it once per transition: a later reconcile reads
// the persisted state and finds nothing new, and a write rejected as a conflict
// emits nothing.
func (r *StorageRequestReconciler) patchStatus(
	ctx context.Context, sr *storagev1alpha1.StorageRequest, mutate func(*storagev1alpha1.StorageRequestStatus),
) error {
	base := sr.DeepCopy()
	mutate(&sr.Status)
	sr.Status.ObservedGeneration = sr.Generation
	if equality.Semantic.DeepEqual(base.Status, sr.Status) {
		return nil
	}
	if err := r.Status().Patch(ctx, sr, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return err
	}
	r.recordTransitions(sr, &base.Status, &sr.Status)
	return nil
}

// recordTransitions emits one Event per condition transition between two
// persisted statuses. Ready and Degraded are judged independently:
//
//   - Ready becoming True emits PVCBound;
//   - Ready's reason becoming Resizing emits PVCResizing;
//   - Degraded becoming True, or changing reason while True, emits a Warning
//     whose reason is the Degraded reason.
//
// Provisioning has no Event of its own: PVCRequested already covers it.
func (r *StorageRequestReconciler) recordTransitions(
	sr *storagev1alpha1.StorageRequest, before, after *storagev1alpha1.StorageRequestStatus,
) {
	oldReady := meta.FindStatusCondition(before.Conditions, storagev1alpha1.ConditionReady)
	newReady := meta.FindStatusCondition(after.Conditions, storagev1alpha1.ConditionReady)
	if newReady != nil {
		wasTrue := oldReady != nil && oldReady.Status == metav1.ConditionTrue
		switch {
		case newReady.Status == metav1.ConditionTrue && !wasTrue:
			r.event(sr, corev1.EventTypeNormal, storagev1alpha1.EventPVCBound, newReady.Message)
		case newReady.Status == metav1.ConditionFalse && newReady.Reason == storagev1alpha1.ReasonResizing &&
			(oldReady == nil || oldReady.Status != metav1.ConditionFalse || oldReady.Reason != newReady.Reason):
			r.event(sr, corev1.EventTypeNormal, storagev1alpha1.EventPVCResizing, newReady.Message)
		}
	}

	oldDegraded := meta.FindStatusCondition(before.Conditions, storagev1alpha1.ConditionDegraded)
	newDegraded := meta.FindStatusCondition(after.Conditions, storagev1alpha1.ConditionDegraded)
	if newDegraded != nil && newDegraded.Status == metav1.ConditionTrue &&
		(oldDegraded == nil || oldDegraded.Status != metav1.ConditionTrue || oldDegraded.Reason != newDegraded.Reason) {
		r.event(sr, corev1.EventTypeWarning, newDegraded.Reason, newDegraded.Message)
	}
}

// event publishes one Event on the request, truncating the note to the API
// limit so a long message can never cause the Event to be dropped.
func (r *StorageRequestReconciler) event(sr *storagev1alpha1.StorageRequest, eventType, reason, note string) {
	r.Recorder.Eventf(sr, nil, eventType, reason, eventActionReconcile, "%s", storage.Truncate(note, maxEventNote))
}

// isMarkedPVC reports whether a PVC carries either operator identity marker.
// The marker only selects which events to look at; Reconcile still verifies
// the request UID annotation and the recorded PVC UID before acting.
func isMarkedPVC(obj client.Object) bool {
	if obj == nil {
		return false
	}
	if obj.GetLabels()[storagev1alpha1.LabelManaged] == storagev1alpha1.ManagedValue {
		return true
	}
	_, annotated := obj.GetAnnotations()[storagev1alpha1.AnnotationRequestUID]
	return annotated
}

// managedPVCPredicate passes create, update, delete, and generic events for
// PVCs that carry an identity marker. An update passes when either the old or
// the new object is marked, so removing the label or annotation, and setting a
// deletion timestamp, still reach the request (design section 6.5).
func managedPVCPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isMarkedPVC(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return isMarkedPVC(e.ObjectOld) || isMarkedPVC(e.ObjectNew) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isMarkedPVC(e.Object) },
		GenericFunc: func(e event.GenericEvent) bool { return isMarkedPVC(e.Object) },
	}
}

// requestForPVC maps a PVC to the StorageRequest with the same namespace and
// name. It deliberately does not trust the annotation to pick the request: the
// mapping is by name, and identity is decided inside Reconcile.
func requestForPVC(_ context.Context, obj client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Namespace: obj.GetNamespace(), Name: obj.GetName(),
	}}}
}

// SetupWithManager sets up the controller with the Manager. StorageRequest
// events are not filtered, so finalizer and deletion-timestamp updates always
// reconcile. PVCs are not owner-referenced, so they are watched explicitly.
func (r *StorageRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorder("storagerequest-controller")
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.StorageRequest{}).
		Watches(&corev1.PersistentVolumeClaim{},
			handler.EnqueueRequestsFromMapFunc(requestForPVC),
			builder.WithPredicates(managedPVCPredicate())).
		Named("storagerequest").
		Complete(r)
}
