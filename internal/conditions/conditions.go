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

// Package conditions derives the StorageRequest status from an observed PVC:
// the Ready and Degraded conditions and the projected claim fields. It holds
// no client and makes no API calls, so every status decision is unit tested.
package conditions

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
	"github.com/mohamadar77/storage-request-operator/internal/storage"
)

// Health is the Ready and Degraded assessment of a PVC whose identity has
// already been verified. Degraded is reported only for persistent, actionable
// problems; when DegradedReason is empty the claim has none.
type Health struct {
	ReadyStatus  metav1.ConditionStatus
	ReadyReason  string
	ReadyMessage string

	DegradedReason  string
	DegradedMessage string
}

// Degraded reports whether the assessment found a persistent problem.
func (h Health) Degraded() bool { return h.DegradedReason != "" }

// AssessClaim derives Ready and Degraded from the verified PVC (design
// sections 4.4 and 8). Ready is True only when the claim is Bound, reports at
// least the requested capacity, and Kubernetes reports no resize pending or in
// progress. The caller must have validated sr, so its size is safe to compare.
func AssessClaim(sr *storagev1alpha1.StorageRequest, pvc *corev1.PersistentVolumeClaim) Health {
	notReady := func(reason, message string) Health {
		return Health{ReadyStatus: metav1.ConditionFalse, ReadyReason: reason, ReadyMessage: message}
	}
	problem := func(reason, message string) Health {
		return Health{
			ReadyStatus: metav1.ConditionFalse, ReadyReason: reason, ReadyMessage: message,
			DegradedReason: reason, DegradedMessage: message,
		}
	}

	if !pvc.DeletionTimestamp.IsZero() {
		return problem(storagev1alpha1.ReasonPVCDeleting, fmt.Sprintf(
			"PersistentVolumeClaim %s is being deleted while this request is active; "+
				"no replacement will be created", pvc.Name))
	}
	switch pvc.Status.Phase {
	case corev1.ClaimLost:
		return problem(storagev1alpha1.ReasonVolumeLost, fmt.Sprintf(
			"PersistentVolumeClaim %s lost its PersistentVolume. It is not recreated automatically; "+
				"inspect the PersistentVolume and backend", pvc.Name))
	case corev1.ClaimBound:
	default:
		return notReady(storagev1alpha1.ReasonProvisioning,
			fmt.Sprintf("PersistentVolumeClaim %s is not bound yet.", pvc.Name))
	}

	capacity, reported := pvc.Status.Capacity[corev1.ResourceStorage]
	if !reported {
		return notReady(storagev1alpha1.ReasonProvisioning,
			fmt.Sprintf("PersistentVolumeClaim %s is bound but reports no capacity yet.", pvc.Name))
	}
	requested := sr.Spec.Storage.Size
	satisfied := capacity.Cmp(requested) >= 0
	resize, evidence := storage.ObserveResize(pvc)

	var h Health
	switch {
	case resize == storage.ResizeInProgress:
		h = notReady(storagev1alpha1.ReasonResizing, fmt.Sprintf(
			"PersistentVolumeClaim %s is resizing (capacity %s, requested %s): %s.",
			pvc.Name, capacity.String(), requested.String(), evidence))
	case !satisfied:
		h = notReady(storagev1alpha1.ReasonResizing, fmt.Sprintf(
			"PersistentVolumeClaim %s is bound with capacity %s, which is below the requested %s.",
			pvc.Name, capacity.String(), requested.String()))
	default:
		h = Health{
			ReadyStatus: metav1.ConditionTrue,
			ReadyReason: storagev1alpha1.ReasonBound,
			ReadyMessage: fmt.Sprintf("PersistentVolumeClaim %s is bound and has the requested capacity.",
				pvc.Name),
		}
	}

	if resize == storage.ResizeFailed {
		message := fmt.Sprintf(
			"Kubernetes reports a terminal resize failure for PersistentVolumeClaim %s (capacity %s): %s. "+
				"The same target is not retried",
			pvc.Name, capacity.String(), evidence)
		h.DegradedReason = storagev1alpha1.ReasonResizeFailed
		h.DegradedMessage = message
		if !satisfied {
			h.ReadyReason = storagev1alpha1.ReasonResizeFailed
			h.ReadyMessage = message
		}
	}
	return h
}

// ProjectClaim copies the observed PVC phase, bound volume, and actual
// capacity into status. The PVC stays authoritative; status only summarizes.
func ProjectClaim(status *storagev1alpha1.StorageRequestStatus, pvc *corev1.PersistentVolumeClaim) {
	status.PVCPhase = pvc.Status.Phase
	status.VolumeName = pvc.Spec.VolumeName
	status.Capacity = nil
	if capacity, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		status.Capacity = new(capacity.DeepCopy())
	}
}

// ClearClaim removes the observed PVC fields from status. It is used when the
// recorded PVC is gone or the same-name PVC is not this request's claim: a
// foreign or absent claim's phase and capacity must never be reported.
func ClearClaim(status *storagev1alpha1.StorageRequestStatus) {
	status.PVCPhase = ""
	status.VolumeName = ""
	status.Capacity = nil
}

// SetProblem reports a persistent, actionable problem: Ready=False and
// Degraded=True with the same reason. Transition times are preserved when a
// condition's status does not change.
func SetProblem(status *storagev1alpha1.StorageRequestStatus, generation int64, reason, message string) {
	SetHealth(status, generation, Health{
		ReadyStatus: metav1.ConditionFalse, ReadyReason: reason, ReadyMessage: message,
		DegradedReason: reason, DegradedMessage: message,
	})
}

// SetHealth writes the Ready and Degraded conditions of an assessment.
// Transition times are preserved when a condition's status does not change.
func SetHealth(status *storagev1alpha1.StorageRequestStatus, generation int64, health Health) {
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               storagev1alpha1.ConditionReady,
		Status:             health.ReadyStatus,
		Reason:             health.ReadyReason,
		Message:            health.ReadyMessage,
		ObservedGeneration: generation,
	})
	degraded := metav1.Condition{
		Type:               storagev1alpha1.ConditionDegraded,
		Status:             metav1.ConditionFalse,
		Reason:             storagev1alpha1.ReasonAsExpected,
		Message:            "No storage request errors detected.",
		ObservedGeneration: generation,
	}
	if health.Degraded() {
		degraded.Status = metav1.ConditionTrue
		degraded.Reason = health.DegradedReason
		degraded.Message = health.DegradedMessage
	}
	meta.SetStatusCondition(&status.Conditions, degraded)
}
