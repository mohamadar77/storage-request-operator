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
