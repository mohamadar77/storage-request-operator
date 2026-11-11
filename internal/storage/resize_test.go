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
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
)

const (
	size30Gi = "30Gi"
	size15Gi = "15Gi"
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
			name: "terminal status wins over a retried-error condition, and the condition message is surfaced",
			pvc: withCondition(
				withAllocated(boundClaim(), corev1.PersistentVolumeClaimControllerResizeInfeasible),
				corev1.PersistentVolumeClaimControllerResizeError, corev1.ConditionTrue, "quota exceeded"),
			want: ResizeFailed, wantEvidence: "ControllerResizeError: quota exceeded",
		},
		{
			name: "legacy alpha node failure name is terminal",
			pvc:  withAllocated(boundClaim(), "NodeResizeFailed"),
			want: ResizeFailed, wantEvidence: "NodeResizeFailed",
		},
		{
			name: "an unknown allocated status is ignored",
			pvc:  withAllocated(boundClaim(), "SomeFutureStatus"),
			want: ResizeNone,
		},
		{
			name: "terminal status wins over a Resizing condition",
			pvc: withCondition(
				withAllocated(boundClaim(), corev1.PersistentVolumeClaimNodeResizeInfeasible),
				corev1.PersistentVolumeClaimResizing, corev1.ConditionTrue, ""),
			want: ResizeFailed, wantEvidence: "NodeResizeInfeasible; Resizing",
		},
		{
			name: "a trailing period in a condition message is dropped",
			pvc: withCondition(boundClaim(),
				corev1.PersistentVolumeClaimControllerResizeError, corev1.ConditionTrue, "disk busy."),
			want: ResizeInProgress, wantEvidence: "ControllerResizeError: disk busy",
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

func TestObserveResizeBoundsEvidence(t *testing.T) {
	pvc := boundClaim()
	long := strings.Repeat("x", 10000)
	for _, ct := range []corev1.PersistentVolumeClaimConditionType{
		corev1.PersistentVolumeClaimControllerResizeError, corev1.PersistentVolumeClaimNodeResizeError,
		corev1.PersistentVolumeClaimResizing, corev1.PersistentVolumeClaimFileSystemResizePending,
	} {
		pvc = withCondition(pvc, ct, corev1.ConditionTrue, long)
	}
	for _, allocated := range []corev1.ClaimResourceStatus{"", corev1.PersistentVolumeClaimControllerResizeInfeasible} {
		if allocated != "" {
			pvc = withAllocated(pvc, allocated)
		}
		_, evidence := ObserveResize(pvc)
		if len(evidence) > maxResizeEvidence {
			t.Errorf("allocated %q: evidence is %d bytes, want at most %d", allocated, len(evidence), maxResizeEvidence)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{name: "short string unchanged", in: "abc", limit: 3, want: "abc"},
		{name: "ascii cut with ellipsis", in: "abcdefgh", limit: 6, want: "abc…"},
		// "é" is two bytes; a cut inside it must back off to the rune start.
		{name: "cut on a character boundary", in: "aéééé", limit: 6, want: "aé…"},
		{name: "never splits a UTF-8 character", in: "aéééé", limit: 5, want: "a…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.in, tt.limit)
			if got != tt.want {
				t.Fatalf("Truncate(%q, %d) = %q, want %q", tt.in, tt.limit, got, tt.want)
			}
			if len(got) > tt.limit || !utf8.ValidString(got) {
				t.Fatalf("Truncate(%q, %d) = %q: %d bytes, valid UTF-8 %v", tt.in, tt.limit, got, len(got), utf8.ValidString(got))
			}
		})
	}
}

func TestPlanResize(t *testing.T) {
	request := func(size string) *storagev1alpha1.StorageRequest {
		return &storagev1alpha1.StorageRequest{Spec: storagev1alpha1.StorageRequestSpec{
			Storage: storagev1alpha1.StorageSpec{Size: resource.MustParse(size)},
		}}
	}
	// claim builds a PVC requesting 20Gi in StorageClass class ("" for none),
	// with the given phase and actual capacity ("" for none).
	claim := func(class string, phase corev1.PersistentVolumeClaimPhase, capacity string) *corev1.PersistentVolumeClaim {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "app-data"},
			Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")},
			}},
			Status: corev1.PersistentVolumeClaimStatus{Phase: phase},
		}
		if class != "" {
			pvc.Spec.StorageClassName = &class
		}
		if capacity != "" {
			pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(capacity)}
		}
		return pvc
	}
	terminating := claim("fast", corev1.ClaimBound, "20Gi")
	terminating.DeletionTimestamp = new(metav1.Now())
	class := func(allow *bool) *storagev1.StorageClass {
		return &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "fast"}, AllowVolumeExpansion: allow}
	}
	allow, disallow := class(new(true)), class(nil)
	failed := func(c *corev1.PersistentVolumeClaim) *corev1.PersistentVolumeClaim {
		return withAllocated(c, corev1.PersistentVolumeClaimControllerResizeInfeasible)
	}
	inProgress := func(c *corev1.PersistentVolumeClaim) *corev1.PersistentVolumeClaim {
		return withAllocated(c, corev1.PersistentVolumeClaimNodeResizeInProgress)
	}

	tests := []struct {
		name       string
		size       string
		pvc        *corev1.PersistentVolumeClaim
		class      *storagev1.StorageClass
		needsClass bool
		want       ResizeAction
		target     string
		reason     string
	}{
		{name: "equal request is a no-op", size: "20Gi", pvc: claim("fast", corev1.ClaimBound, "20Gi"), class: allow, want: ResizeNoop},
		{name: "equivalent quantity formats are equal", size: "20480Mi", pvc: claim("fast", corev1.ClaimBound, "20Gi"), class: allow, want: ResizeNoop},
		{name: "terminating claim is never changed", size: size30Gi, pvc: terminating, class: allow, want: ResizeNoop},
		{name: "lost claim is never changed", size: size30Gi, pvc: claim("fast", corev1.ClaimLost, ""), class: allow, want: ResizeNoop},
		{name: "unbound increase is deferred", size: size30Gi, pvc: claim("fast", corev1.ClaimPending, ""), want: ResizeDeferred},
		{name: "unbound decrease is a shrink", size: "10Gi", pvc: claim("fast", corev1.ClaimPending, ""), want: ResizeRejected, reason: storagev1alpha1.ReasonShrinkNotSupported},
		{name: "bound increase with expansion allowed is patched", size: size30Gi, pvc: claim("fast", corev1.ClaimBound, "20Gi"), class: allow, needsClass: true, want: ResizePatch, target: size30Gi},
		{name: "bound increase without a class name", size: size30Gi, pvc: claim("", corev1.ClaimBound, "20Gi"), needsClass: true, want: ResizeRejected, reason: storagev1alpha1.ReasonStorageClassNotFound},
		{name: "bound increase with a missing class", size: size30Gi, pvc: claim("fast", corev1.ClaimBound, "20Gi"), class: nil, needsClass: true, want: ResizeRejected, reason: storagev1alpha1.ReasonStorageClassNotFound},
		{name: "bound increase with expansion disallowed", size: size30Gi, pvc: claim("fast", corev1.ClaimBound, "20Gi"), class: disallow, needsClass: true, want: ResizeRejected, reason: storagev1alpha1.ReasonExpansionNotSupported},
		{name: "decrease without actual capacity", size: size15Gi, pvc: failed(claim("fast", corev1.ClaimBound, "")), class: allow, needsClass: true, want: ResizeRejected, reason: storagev1alpha1.ReasonShrinkNotSupported},
		{name: "decrease to the actual capacity", size: "10Gi", pvc: failed(claim("fast", corev1.ClaimBound, "10Gi")), class: allow, needsClass: true, want: ResizeRejected, reason: storagev1alpha1.ReasonShrinkNotSupported},
		{name: "decrease below the actual capacity", size: "5Gi", pvc: failed(claim("fast", corev1.ClaimBound, "10Gi")), class: allow, needsClass: true, want: ResizeRejected, reason: storagev1alpha1.ReasonShrinkNotSupported},
		{name: "decrease above capacity without a terminal failure", size: size15Gi, pvc: claim("fast", corev1.ClaimBound, "10Gi"), class: allow, needsClass: true, want: ResizeRejected, reason: storagev1alpha1.ReasonResizeRecoveryRejected},
		{name: "decrease above capacity while a resize is in progress", size: size15Gi, pvc: inProgress(claim("fast", corev1.ClaimBound, "10Gi")), class: allow, needsClass: true, want: ResizeRejected, reason: storagev1alpha1.ReasonResizeRecoveryRejected},
		{name: "recovery to a target above capacity after a terminal failure", size: size15Gi, pvc: failed(claim("fast", corev1.ClaimBound, "10Gi")), class: allow, needsClass: true, want: ResizePatch, target: size15Gi},
		{name: "recovery with the legacy failure name", size: size15Gi, pvc: withAllocated(claim("fast", corev1.ClaimBound, "10Gi"), "NodeResizeFailed"), class: allow, needsClass: true, want: ResizePatch, target: size15Gi},
		{name: "recovery still needs expansion allowed", size: size15Gi, pvc: failed(claim("fast", corev1.ClaimBound, "10Gi")), class: disallow, needsClass: true, want: ResizeRejected, reason: storagev1alpha1.ReasonExpansionNotSupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sr := request(tt.size)
			if got := NeedsStorageClass(sr, tt.pvc); got != tt.needsClass {
				t.Errorf("NeedsStorageClass() = %v, want %v", got, tt.needsClass)
			}
			plan := PlanResize(sr, tt.pvc, tt.class)
			if plan.Action != tt.want {
				t.Fatalf("PlanResize().Action = %v, want %v (reason %q, message %q)", plan.Action, tt.want, plan.Reason, plan.Message)
			}
			switch tt.want {
			case ResizePatch:
				if plan.Target.Cmp(resource.MustParse(tt.target)) != 0 {
					t.Errorf("target = %s, want %s", plan.Target.String(), tt.target)
				}
			case ResizeRejected:
				if plan.Reason != tt.reason {
					t.Errorf("reason = %q, want %q", plan.Reason, tt.reason)
				}
				if !strings.Contains(plan.Message, "was left requesting 20Gi") {
					t.Errorf("message %q does not say what the claim was left at", plan.Message)
				}
			default:
				if plan.Reason != "" || plan.Message != "" {
					t.Errorf("unexpected reason/message %q/%q", plan.Reason, plan.Message)
				}
			}
		})
	}
}
