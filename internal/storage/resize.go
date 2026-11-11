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

package storage

import (
	"fmt"
	"strings"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
)

// ResizeState is what the PVC reports about volume expansion. It is read-only
// observation: this package never decides to resize a claim.
type ResizeState int

const (
	// ResizeNone means the PVC reports no expansion work.
	ResizeNone ResizeState = iota
	// ResizeInProgress means expansion is pending or running in the controller
	// or on the node, including a resize that Kubernetes is still retrying
	// after an error.
	ResizeInProgress
	// ResizeFailed means Kubernetes reports the expansion as terminally failed
	// (infeasible). It is not retried until the requested target changes.
	ResizeFailed
)

// Allocated-resource statuses that are terminal. The Infeasible values are
// the current Kubernetes API; the Failed values are the names used while the
// recovery feature was alpha (design section 6.4) and are still accepted.
var terminalResizeStatuses = map[corev1.ClaimResourceStatus]struct{}{
	corev1.PersistentVolumeClaimControllerResizeInfeasible: {},
	corev1.PersistentVolumeClaimNodeResizeInfeasible:       {},
	"ControllerResizeFailed":                               {},
	"NodeResizeFailed":                                     {},
}

var inProgressResizeStatuses = map[corev1.ClaimResourceStatus]struct{}{
	corev1.PersistentVolumeClaimControllerResizeInProgress: {},
	corev1.PersistentVolumeClaimNodeResizePending:          {},
	corev1.PersistentVolumeClaimNodeResizeInProgress:       {},
}

// PVC condition types that mean resize work remains. The error conditions are
// included because Kubernetes keeps retrying after them unless it also reports
// an infeasible allocated-resource status.
var inProgressResizeConditions = map[corev1.PersistentVolumeClaimConditionType]struct{}{
	corev1.PersistentVolumeClaimResizing:                {},
	corev1.PersistentVolumeClaimFileSystemResizePending: {},
	corev1.PersistentVolumeClaimControllerResizeError:   {},
	corev1.PersistentVolumeClaimNodeResizeError:         {},
}

// Bounds on the PVC text copied into status. Condition messages come from the
// resize controller and CSI driver and have no length limit of their own; the
// copy must stay far below the 32768-character condition message limit.
const (
	maxConditionDetail = 512
	maxResizeEvidence  = 2048
)

// ObserveResize reports the PVC's expansion state and a short description of
// the evidence, built from status.allocatedResourceStatuses["storage"] (when
// the PVC reports it) and the PVC resize conditions. A terminal status wins
// over any in-progress signal. The evidence is bounded in length.
func ObserveResize(pvc *corev1.PersistentVolumeClaim) (ResizeState, string) {
	allocated, present := pvc.Status.AllocatedResourceStatuses[corev1.ResourceStorage]
	details := resizeConditionDetails(pvc)

	if _, terminal := terminalResizeStatuses[allocated]; present && terminal {
		return ResizeFailed, describe(fmt.Sprintf("allocated resource status %s", allocated), details)
	}
	if _, running := inProgressResizeStatuses[allocated]; present && running {
		return ResizeInProgress, describe(fmt.Sprintf("allocated resource status %s", allocated), details)
	}
	if details != "" {
		return ResizeInProgress, Truncate(details, maxResizeEvidence)
	}
	return ResizeNone, ""
}

// resizeConditionDetails joins the true resize conditions of the PVC as
// "Type: message" pairs, in the order the PVC lists them.
func resizeConditionDetails(pvc *corev1.PersistentVolumeClaim) string {
	var parts []string
	for _, c := range pvc.Status.Conditions {
		if _, ok := inProgressResizeConditions[c.Type]; !ok || c.Status != corev1.ConditionTrue {
			continue
		}
		if c.Message == "" {
			parts = append(parts, string(c.Type))
			continue
		}
		// Trailing periods are dropped so callers can end their own sentence.
		message := Truncate(strings.TrimRight(c.Message, ". "), maxConditionDetail)
		parts = append(parts, fmt.Sprintf("%s: %s", c.Type, message))
	}
	return strings.Join(parts, "; ")
}

func describe(primary, details string) string {
	if details == "" {
		return primary
	}
	return Truncate(primary+"; "+details, maxResizeEvidence)
}

// Truncate shortens s to at most limit bytes without splitting a UTF-8
// character, marking a cut with a trailing ellipsis.
func Truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	const ellipsis = "…"
	cut := limit - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

// ResizeAction is what PlanResize decided about the claim's storage request.
type ResizeAction int

const (
	// ResizeNoop means the claim's request already matches, or the claim is
	// in a state (terminating, Lost) that is reported elsewhere.
	ResizeNoop ResizeAction = iota
	// ResizePatch means the claim's storage request must be patched to Target.
	ResizePatch
	// ResizeDeferred means the larger size cannot be applied yet: Kubernetes
	// accepts a request change only on a bound claim. The increase is applied
	// once the claim is bound.
	ResizeDeferred
	// ResizeRejected means the size change is not applied, and Reason/Message
	// explain why in a Degraded condition.
	ResizeRejected
)

// ResizePlan is PlanResize's decision. Target is set for ResizePatch; Reason
// and Message are set for ResizeRejected.
type ResizePlan struct {
	Action  ResizeAction
	Target  resource.Quantity
	Reason  string
	Message string
}

// NeedsStorageClass reports whether PlanResize needs the claim's StorageClass
// to decide: the claim is bound, not terminating, and its storage request
// differs from the requested size. Callers resolve the class only then, so a
// request in steady state costs no StorageClass read.
func NeedsStorageClass(sr *storagev1alpha1.StorageRequest, pvc *corev1.PersistentVolumeClaim) bool {
	if pvc.Status.Phase != corev1.ClaimBound || !pvc.DeletionTimestamp.IsZero() {
		return false
	}
	return sr.Spec.Storage.Size.Cmp(claimRequest(pvc)) != 0
}

// claimRequest returns the claim's storage request, zero when absent.
func claimRequest(pvc *corev1.PersistentVolumeClaim) resource.Quantity {
	return pvc.Spec.Resources.Requests[corev1.ResourceStorage]
}

// PlanResize decides whether the verified claim's storage request should be
// patched toward the requested size (design section 6.4). It never shrinks the
// actual volume: a decrease is applied only as recovery from a terminal
// expansion failure, to a target still above the actual capacity. class is the
// claim's resolved StorageClass, nil when it does not exist; it is consulted
// only when NeedsStorageClass is true. The caller must have validated sr.
func PlanResize(
	sr *storagev1alpha1.StorageRequest, pvc *corev1.PersistentVolumeClaim, class *storagev1.StorageClass,
) ResizePlan {
	requested := sr.Spec.Storage.Size
	current := claimRequest(pvc)
	cmp := requested.Cmp(current)
	if cmp == 0 || !pvc.DeletionTimestamp.IsZero() || pvc.Status.Phase == corev1.ClaimLost {
		return ResizePlan{Action: ResizeNoop}
	}
	reject := func(reason, format string, args ...any) ResizePlan {
		return ResizePlan{Action: ResizeRejected, Reason: reason, Message: fmt.Sprintf(format, args...) +
			fmt.Sprintf(". PersistentVolumeClaim %s was left requesting %s", pvc.Name, current.String())}
	}

	if pvc.Status.Phase != corev1.ClaimBound {
		if cmp > 0 {
			return ResizePlan{Action: ResizeDeferred}
		}
		return reject(storagev1alpha1.ReasonShrinkNotSupported,
			"The requested size %s is below the claim's request and the claim is not bound; "+
				"a request is never reduced before provisioning", requested.String())
	}

	if cmp > 0 {
		if reason, message := checkExpansion(pvc, class); reason != "" {
			return reject(reason, "%s", message)
		}
		return ResizePlan{Action: ResizePatch, Target: requested.DeepCopy()}
	}

	// A decrease on a bound claim: only the failed-expansion recovery rule.
	capacity, hasCapacity := pvc.Status.Capacity[corev1.ResourceStorage]
	switch state, evidence := ObserveResize(pvc); {
	case !hasCapacity:
		return reject(storagev1alpha1.ReasonShrinkNotSupported,
			"The requested size %s is below the claim's request and the claim reports no actual capacity; "+
				"the volume is never shrunk", requested.String())
	case requested.Cmp(capacity) <= 0:
		return reject(storagev1alpha1.ReasonShrinkNotSupported,
			"The requested size %s is not above the actual capacity %s; the volume is never shrunk",
			requested.String(), capacity.String())
	case state != ResizeFailed:
		detail := "no resize work"
		if evidence != "" {
			detail = evidence
		}
		return reject(storagev1alpha1.ReasonResizeRecoveryRejected,
			"The requested size %s lowers the pending resize target, which is allowed only after Kubernetes "+
				"reports a terminal resize failure; it reports %s", requested.String(), detail)
	}
	if reason, message := checkExpansion(pvc, class); reason != "" {
		return reject(reason, "%s", message)
	}
	return ResizePlan{Action: ResizePatch, Target: requested.DeepCopy()}
}

// checkExpansion verifies that the claim's StorageClass allows volume
// expansion. It returns the Degraded reason and message when it does not.
func checkExpansion(pvc *corev1.PersistentVolumeClaim, class *storagev1.StorageClass) (string, string) {
	name := claimStorageClass(pvc)
	switch {
	case name == "":
		return storagev1alpha1.ReasonStorageClassNotFound,
			"The claim has no StorageClass, so volume expansion cannot be verified"
	case class == nil:
		return storagev1alpha1.ReasonStorageClassNotFound,
			fmt.Sprintf("StorageClass %q of the claim does not exist, so volume expansion cannot be verified", name)
	case class.AllowVolumeExpansion == nil || !*class.AllowVolumeExpansion:
		return storagev1alpha1.ReasonExpansionNotSupported,
			fmt.Sprintf("StorageClass %q does not allow volume expansion (allowVolumeExpansion is not true)", name)
	}
	return "", ""
}
