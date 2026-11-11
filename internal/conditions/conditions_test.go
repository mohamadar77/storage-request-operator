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

package conditions

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
)

// requestedSize is the size most fixtures request.
const requestedSize = "20Gi"

// claim builds a PVC status fixture. capacity "" means no capacity reported.
func claim(phase corev1.PersistentVolumeClaimPhase, capacity string) *corev1.PersistentVolumeClaim {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "app-data"},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pv-1"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: phase},
	}
	if capacity != "" {
		pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(capacity)}
	}
	return pvc
}

func withAllocated(pvc *corev1.PersistentVolumeClaim, s corev1.ClaimResourceStatus) *corev1.PersistentVolumeClaim {
	pvc.Status.AllocatedResourceStatuses = map[corev1.ResourceName]corev1.ClaimResourceStatus{corev1.ResourceStorage: s}
	return pvc
}

func TestAssessClaim(t *testing.T) {
	request := func(size string) *storagev1alpha1.StorageRequest {
		return &storagev1alpha1.StorageRequest{Spec: storagev1alpha1.StorageRequestSpec{
			Storage: storagev1alpha1.StorageSpec{Size: resource.MustParse(size)},
		}}
	}
	terminating := claim(corev1.ClaimBound, requestedSize)
	terminating.DeletionTimestamp = new(metav1.Now())

	tests := []struct {
		name         string
		size         string
		pvc          *corev1.PersistentVolumeClaim
		ready        metav1.ConditionStatus
		readyReason  string
		degradedWhen string // expected Degraded reason; "" means not degraded
	}{
		{
			name: "unbound claim is provisioning", size: requestedSize, pvc: claim(corev1.ClaimPending, ""),
			ready: metav1.ConditionFalse, readyReason: storagev1alpha1.ReasonProvisioning,
		},
		{
			name: "a claim with no phase yet is provisioning", size: requestedSize, pvc: claim("", ""),
			ready: metav1.ConditionFalse, readyReason: storagev1alpha1.ReasonProvisioning,
		},
		{
			name: "bound without reported capacity is provisioning", size: requestedSize, pvc: claim(corev1.ClaimBound, ""),
			ready: metav1.ConditionFalse, readyReason: storagev1alpha1.ReasonProvisioning,
		},
		{
			name: "bound with the requested capacity is ready", size: requestedSize, pvc: claim(corev1.ClaimBound, requestedSize),
			ready: metav1.ConditionTrue, readyReason: storagev1alpha1.ReasonBound,
		},
		{
			name: "equivalent quantity formats compare equal", size: "1Gi", pvc: claim(corev1.ClaimBound, "1024Mi"),
			ready: metav1.ConditionTrue, readyReason: storagev1alpha1.ReasonBound,
		},
		{
			name: "numeric, not string, comparison", size: requestedSize, pvc: claim(corev1.ClaimBound, "100Gi"),
			ready: metav1.ConditionTrue, readyReason: storagev1alpha1.ReasonBound,
		},
		{
			name: "bound below the requested capacity is resizing", size: requestedSize, pvc: claim(corev1.ClaimBound, "10Gi"),
			ready: metav1.ConditionFalse, readyReason: storagev1alpha1.ReasonResizing,
		},
		{
			name: "resize in progress keeps Ready false even when capacity already satisfies the request",
			size: requestedSize,
			pvc: withAllocated(claim(corev1.ClaimBound, requestedSize),
				corev1.PersistentVolumeClaimNodeResizeInProgress),
			ready: metav1.ConditionFalse, readyReason: storagev1alpha1.ReasonResizing,
		},
		{
			name: "terminal failure below target is not ready and degraded",
			size: requestedSize,
			pvc: withAllocated(claim(corev1.ClaimBound, "10Gi"),
				corev1.PersistentVolumeClaimControllerResizeInfeasible),
			ready: metav1.ConditionFalse, readyReason: storagev1alpha1.ReasonResizeFailed,
			degradedWhen: storagev1alpha1.ReasonResizeFailed,
		},
		{
			name: "terminal failure with capacity still satisfying the request stays ready but degraded",
			size: "10Gi",
			pvc: withAllocated(claim(corev1.ClaimBound, "10Gi"),
				corev1.PersistentVolumeClaimControllerResizeInfeasible),
			ready: metav1.ConditionTrue, readyReason: storagev1alpha1.ReasonBound,
			degradedWhen: storagev1alpha1.ReasonResizeFailed,
		},
		{
			name: "lost claim is degraded", size: requestedSize, pvc: claim(corev1.ClaimLost, ""),
			ready: metav1.ConditionFalse, readyReason: storagev1alpha1.ReasonVolumeLost,
			degradedWhen: storagev1alpha1.ReasonVolumeLost,
		},
		{
			name: "terminating claim is degraded even when bound", size: requestedSize, pvc: terminating,
			ready: metav1.ConditionFalse, readyReason: storagev1alpha1.ReasonPVCDeleting,
			degradedWhen: storagev1alpha1.ReasonPVCDeleting,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := AssessClaim(request(tt.size), tt.pvc)
			if h.ReadyStatus != tt.ready || h.ReadyReason != tt.readyReason {
				t.Errorf("Ready = %s/%s, want %s/%s", h.ReadyStatus, h.ReadyReason, tt.ready, tt.readyReason)
			}
			if h.ReadyMessage == "" {
				t.Error("Ready message is empty")
			}
			if h.DegradedReason != tt.degradedWhen {
				t.Errorf("Degraded reason = %q, want %q", h.DegradedReason, tt.degradedWhen)
			}
			if h.Degraded() && h.DegradedMessage == "" {
				t.Error("Degraded message is empty")
			}
		})
	}
}

func TestProjectAndClearClaim(t *testing.T) {
	pvc := claim(corev1.ClaimBound, requestedSize)
	status := &storagev1alpha1.StorageRequestStatus{}

	ProjectClaim(status, pvc)
	if status.PVCPhase != corev1.ClaimBound || status.VolumeName != "pv-1" {
		t.Fatalf("projected phase/volume = %q/%q", status.PVCPhase, status.VolumeName)
	}
	if status.Capacity == nil || status.Capacity.Cmp(resource.MustParse(requestedSize)) != 0 {
		t.Fatalf("projected capacity = %v, want 20Gi", status.Capacity)
	}
	// The projection is a copy: changing the PVC afterwards must not leak in.
	pvc.Status.Capacity[corev1.ResourceStorage] = resource.MustParse("1Gi")
	if status.Capacity.Cmp(resource.MustParse(requestedSize)) != 0 {
		t.Fatalf("projected capacity aliases the PVC: %v", status.Capacity)
	}

	ProjectClaim(status, claim(corev1.ClaimPending, ""))
	if status.Capacity != nil || status.PVCPhase != corev1.ClaimPending {
		t.Fatalf("an unreported capacity must clear the previous value, got %v/%q", status.Capacity, status.PVCPhase)
	}

	ProjectClaim(status, pvc)
	ClearClaim(status)
	if status.PVCPhase != "" || status.VolumeName != "" || status.Capacity != nil {
		t.Fatalf("ClearClaim left %+v", status)
	}
}
