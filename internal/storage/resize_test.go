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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// boundClaim builds a bound 1Gi PVC fixture.
func boundClaim() *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "resize-fixture"},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase:    corev1.ClaimBound,
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
		},
	}
}

func withAllocated(pvc *corev1.PersistentVolumeClaim, s corev1.ClaimResourceStatus) *corev1.PersistentVolumeClaim {
	pvc.Status.AllocatedResourceStatuses = map[corev1.ResourceName]corev1.ClaimResourceStatus{corev1.ResourceStorage: s}
	return pvc
}

func withCondition(
	pvc *corev1.PersistentVolumeClaim, t corev1.PersistentVolumeClaimConditionType, s corev1.ConditionStatus, msg string,
) *corev1.PersistentVolumeClaim {
	pvc.Status.Conditions = append(pvc.Status.Conditions,
		corev1.PersistentVolumeClaimCondition{Type: t, Status: s, Message: msg})
	return pvc
}

func TestObserveResize(t *testing.T) {
	tests := []struct {
		name         string
		pvc          *corev1.PersistentVolumeClaim
		want         ResizeState
		wantEvidence string
	}{
		{name: "no resize signals", pvc: boundClaim(), want: ResizeNone},
		{
			name: "controller resize in progress",
			pvc:  withAllocated(boundClaim(), corev1.PersistentVolumeClaimControllerResizeInProgress),
			want: ResizeInProgress, wantEvidence: "ControllerResizeInProgress",
		},
		{
			name: "node resize pending",
			pvc:  withAllocated(boundClaim(), corev1.PersistentVolumeClaimNodeResizePending),
			want: ResizeInProgress, wantEvidence: "NodeResizePending",
		},
		{
			name: "node resize in progress",
			pvc:  withAllocated(boundClaim(), corev1.PersistentVolumeClaimNodeResizeInProgress),
			want: ResizeInProgress, wantEvidence: "NodeResizeInProgress",
		},
		{
			name: "controller resize infeasible is terminal",
			pvc:  withAllocated(boundClaim(), corev1.PersistentVolumeClaimControllerResizeInfeasible),
			want: ResizeFailed, wantEvidence: "ControllerResizeInfeasible",
		},
		{
			name: "node resize infeasible is terminal",
			pvc:  withAllocated(boundClaim(), corev1.PersistentVolumeClaimNodeResizeInfeasible),
			want: ResizeFailed, wantEvidence: "NodeResizeInfeasible",
		},
		{
			name: "legacy alpha failure name is terminal",
			pvc:  withAllocated(boundClaim(), "ControllerResizeFailed"),
			want: ResizeFailed, wantEvidence: "ControllerResizeFailed",
		},
		{
			name: "terminal status wins over a Resizing condition, and the condition message is surfaced",
			pvc: withCondition(
				withAllocated(boundClaim(), corev1.PersistentVolumeClaimControllerResizeInfeasible),
				corev1.PersistentVolumeClaimControllerResizeError, corev1.ConditionTrue, "quota exceeded"),
			want: ResizeFailed, wantEvidence: "ControllerResizeError: quota exceeded",
		},
		{
			name: "Resizing condition without allocated status",
			pvc:  withCondition(boundClaim(), corev1.PersistentVolumeClaimResizing, corev1.ConditionTrue, ""),
			want: ResizeInProgress, wantEvidence: "Resizing",
		},
		{
			name: "file system resize pending",
			pvc: withCondition(boundClaim(),
				corev1.PersistentVolumeClaimFileSystemResizePending, corev1.ConditionTrue, "waiting for pod"),
			want: ResizeInProgress, wantEvidence: "FileSystemResizePending: waiting for pod",
		},
		{
			name: "a retried controller error is still in progress",
			pvc: withCondition(boundClaim(),
				corev1.PersistentVolumeClaimControllerResizeError, corev1.ConditionTrue, "timeout"),
			want: ResizeInProgress, wantEvidence: "ControllerResizeError: timeout",
		},
		{
			name: "a false resize condition is ignored",
			pvc:  withCondition(boundClaim(), corev1.PersistentVolumeClaimResizing, corev1.ConditionFalse, ""),
			want: ResizeNone,
		},
		{
			name: "unrelated conditions are ignored",
			pvc: withCondition(boundClaim(),
				corev1.PersistentVolumeClaimVolumeModifyingVolume, corev1.ConditionTrue, ""),
			want: ResizeNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, evidence := ObserveResize(tt.pvc)
			if got != tt.want {
				t.Fatalf("ObserveResize() = %v, want %v", got, tt.want)
			}
			if !strings.Contains(evidence, tt.wantEvidence) {
				t.Errorf("evidence %q does not contain %q", evidence, tt.wantEvidence)
			}
			if tt.want == ResizeNone && evidence != "" {
				t.Errorf("evidence = %q, want empty", evidence)
			}
		})
	}
}
