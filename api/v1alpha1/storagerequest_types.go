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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// Identity markers, finalizer, and condition types used across the operator.
//
// These values form part of the operator's data-safety contract (design v1.1,
// sections 5 and 7) and must not be changed without a design review: the
// identity annotation and recorded PVC UID are the only authority for mutating
// or deleting a managed PVC.
const (
	// Finalizer is the StorageRequest finalizer. It is persisted before any PVC
	// is created and removed only after the selected deletion policy completes.
	Finalizer = "storage.example.io/finalizer"

	// LabelManagedBy is the standard Kubernetes managed-by label applied to the PVC.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// ManagedByValue is the value of LabelManagedBy for PVCs owned by this operator.
	ManagedByValue = "storage-request-operator"
	// LabelManaged marks a PVC as actively managed by a StorageRequest. It is
	// removed when a PVC is retained (detached) during finalization.
	LabelManaged = "storage.example.io/managed"
	// ManagedValue is the value of LabelManaged on an actively managed PVC.
	ManagedValue = "true"

	// AnnotationRequestUID records the owning StorageRequest UID on the PVC. A
	// name match alone never authorizes adoption, mutation, or deletion.
	AnnotationRequestUID = "storage.example.io/request-uid"
	// AnnotationOrphanedFrom records the StorageRequest UID a retained PVC was
	// detached from during Retain finalization.
	AnnotationOrphanedFrom = "storage.example.io/orphaned-from"
)

// Condition types reported on StorageRequest.status.conditions.
const (
	// ConditionReady is True only when the PVC is Bound, actual capacity is at
	// least the requested size, and no resize is pending or in progress.
	ConditionReady = "Ready"
	// ConditionDegraded is True for persistent, actionable problems such as an
	// ownership conflict, immutable-field change, or unsupported resize.
	ConditionDegraded = "Degraded"
)

// Condition reasons. Reasons are part of the API contract: tooling and alerts
// match on them, so existing values must not be renamed.
const (
	// ReasonProvisioning: the managed PVC exists but is not yet bound, or is
	// bound and reports no capacity yet.
	ReasonProvisioning = "Provisioning"
	// ReasonBound: the managed PVC is bound and reports at least the requested
	// capacity.
	ReasonBound = "Bound"
	// ReasonResizing: the managed PVC is bound but its actual capacity is below
	// the requested size, or Kubernetes reports resize work pending or in
	// progress.
	ReasonResizing = "Resizing"
	// ReasonResizeFailed: Kubernetes reports a terminal (infeasible) resize
	// failure for the managed PVC.
	ReasonResizeFailed = "ResizeFailed"
	// ReasonImmutableFieldChange: the request changed the StorageClass, access
	// modes, or volume mode after the PVC was created. The claim is not
	// replaced or changed.
	ReasonImmutableFieldChange = "ImmutableFieldChange"
	// ReasonShrinkNotSupported: the request asks for less than the claim
	// requests and the failed-resize recovery rule does not apply. The volume
	// is never shrunk.
	ReasonShrinkNotSupported = "ShrinkNotSupported"
	// ReasonResizeRecoveryRejected: the request lowers a pending resize target
	// above the actual capacity, but Kubernetes reports no terminal resize
	// failure for the claim.
	ReasonResizeRecoveryRejected = "ResizeRecoveryRejected"
	// ReasonExpansionNotSupported: the claim's StorageClass does not allow
	// volume expansion, so the larger size was not applied.
	ReasonExpansionNotSupported = "ExpansionNotSupported"
	// ReasonResizeRejected: the API server refused the storage request change
	// (for example a statically provisioned claim, or a recovery decrease on a
	// cluster that does not support failed-expansion recovery). The claim is
	// left unchanged instead of retrying the same rejected patch forever.
	ReasonResizeRejected = "ResizeRejected"
	// ReasonStorageClassNotFound: the claim's StorageClass could not be
	// resolved, so expansion could not be verified and was not applied.
	ReasonStorageClassNotFound = "StorageClassNotFound"
	// ReasonVolumeLost: the managed PVC reports phase Lost.
	ReasonVolumeLost = "VolumeLost"
	// ReasonPVCNameConflict: a PVC with the expected name exists but is not
	// managed by any StorageRequest. It is never adopted.
	ReasonPVCNameConflict = "PVCNameConflict"
	// ReasonOwnershipMismatch: the same-name PVC belongs to another request or
	// is not the PVC incarnation this request recorded.
	ReasonOwnershipMismatch = "OwnershipMismatch"
	// ReasonPVCMissing: the recorded PVC no longer exists. It is never
	// recreated automatically.
	ReasonPVCMissing = "PVCMissing"
	// ReasonPVCDeleting: the managed PVC has a deletion timestamp while its
	// request is still active.
	ReasonPVCDeleting = "PVCDeleting"
	// ReasonInvalidSpec: the spec failed the controller's defensive validation,
	// so no PVC was created or changed.
	ReasonInvalidSpec = "InvalidSpec"
	// ReasonAsExpected is the Degraded=False reason when no problem is detected.
	ReasonAsExpected = "AsExpected"
)

// Kubernetes Event reasons for transitions that have no condition reason of
// their own. A request entering a problem state is published as a Warning
// Event whose reason is the condition reason (for example PVCMissing).
const (
	// EventPVCRequested: the controller created the managed PVC.
	EventPVCRequested = "PVCRequested"
	// EventPVCBound: the request became Ready with a bound PVC.
	EventPVCBound = "PVCBound"
	// EventPVCResizing: the bound PVC's capacity is below the request, or
	// Kubernetes reports resize work pending or in progress.
	EventPVCResizing = "PVCResizing"
)

// OnDeletePolicy selects the PVC retention behavior when a StorageRequest is deleted.
// +kubebuilder:validation:Enum=Retain;Delete
type OnDeletePolicy string

const (
	// OnDeleteRetain leaves the managed PVC in place and detaches it from active
	// reconciliation. This is the default and is not a backup.
	OnDeleteRetain OnDeletePolicy = "Retain"
	// OnDeleteDelete deletes only the exact managed PVC. Backend cleanup is then
	// governed by the PersistentVolume reclaim policy, not by this operator.
	OnDeleteDelete OnDeletePolicy = "Delete"
)

// StorageSpec describes the requested persistent storage claim.
type StorageSpec struct {
	// size is the requested storage capacity as a positive Kubernetes quantity
	// (for example, 20Gi). The size may be increased. A decrease is rejected
	// except for the narrow terminal failed-resize recovery rule described in
	// the design, which never shrinks the actual volume.
	//
	// Validation note: the length and exponent guards must stay first in the CEL
	// rule. Quantity parsing and comparison are superlinear in the decimal
	// exponent, so an unguarded rule lets a short string such as 1e2147483647
	// exhaust API server CPU and memory. Integer values are accepted like core
	// PVC quantities.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="type(self) == int ? self > 0 : (self.size() <= 64 && !self.matches('[eE][+-]?[0-9]{3,}') && isQuantity(self) && quantity(self).compareTo(quantity('0')) > 0)",message="size must be a positive Kubernetes quantity"
	Size resource.Quantity `json:"size"`

	// storageClassName optionally selects the StorageClass. When omitted, the
	// PVC field is left unset and Kubernetes applies its defaulting behavior
	// (including retroactive default assignment). When supplied it must be
	// non-empty and must match the created PVC's class; no migration is
	// performed. Requesting an explicitly classless PVC ("") is not supported.
	// +optional
	// +kubebuilder:validation:MinLength=1
	StorageClassName *string `json:"storageClassName,omitempty"`

	// accessModes are the requested PVC access modes. They are immutable after
	// PVC creation. The installed CSI driver and backend must support the mode;
	// acceptance here does not guarantee the cluster can satisfy it.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Enum=ReadWriteOnce;ReadOnlyMany;ReadWriteMany;ReadWriteOncePod
	// +kubebuilder:default={ReadWriteOnce}
	// +listType=set
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`

	// volumeMode selects Filesystem or Block. It is immutable after PVC creation.
	// +optional
	// +kubebuilder:validation:Enum=Filesystem;Block
	// +kubebuilder:default=Filesystem
	VolumeMode *corev1.PersistentVolumeMode `json:"volumeMode,omitempty"`
}

// LifecycleSpec describes lifecycle behavior for the request.
type LifecycleSpec struct {
	// onDelete controls PVC retention when the StorageRequest is deleted. It may
	// be changed while the request is active; the value present at deletion time
	// is authoritative. Switching to Delete is destructive and should be gated
	// by namespace RBAC and GitOps review.
	// +optional
	// +kubebuilder:default=Retain
	OnDelete OnDeletePolicy `json:"onDelete,omitempty"`
}

// StorageRequestSpec defines the desired state of StorageRequest.
type StorageRequestSpec struct {
	// storage is the requested storage configuration.
	// +kubebuilder:validation:Required
	Storage StorageSpec `json:"storage"`

	// lifecycle configures request lifecycle behavior such as deletion policy.
	// +optional
	// +kubebuilder:default={}
	Lifecycle LifecycleSpec `json:"lifecycle,omitempty"`
}

// StorageRequestStatus defines the observed state of StorageRequest.
//
// The managed PVC remains authoritative for actual phase and capacity; this
// status is a user-friendly summary, never a second source of truth.
type StorageRequestStatus struct {
	// observedGeneration is the spec generation last processed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// pvcName is the managed PVC name (equal to the StorageRequest name).
	// +optional
	PVCName string `json:"pvcName,omitempty"`

	// pvcUID is the UID of the exact PVC incarnation verified as belonging to
	// this request. Once set it is retained for the request lifetime and is used
	// to detect same-name replacement; it is never used to adopt a PVC by name.
	// The CRD enforces this: once set, the value cannot be changed or cleared.
	// +optional
	PVCUID types.UID `json:"pvcUID,omitempty"`

	// pvcPhase is the observed PVC phase (Pending, Bound, or Lost) when a PVC
	// exists. It is omitted when no PVC exists; absence is expressed by a
	// condition, not a fabricated phase.
	// +optional
	PVCPhase corev1.PersistentVolumeClaimPhase `json:"pvcPhase,omitempty"`

	// volumeName is the bound PersistentVolume name, when assigned.
	// +optional
	VolumeName string `json:"volumeName,omitempty"`

	// capacity is the actual capacity reported by the PVC status, not the
	// requested target. It is set only when observed.
	// +optional
	Capacity *resource.Quantity `json:"capacity,omitempty"`

	// conditions represent the latest available observations of the request state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.pvcUID) || (has(self.status) && has(self.status.pvcUID) && self.status.pvcUID == oldSelf.status.pvcUID)",message="status.pvcUID is immutable once set"
// +kubebuilder:printcolumn:name="Requested",type=string,JSONPath=`.spec.storage.size`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.pvcPhase`
// +kubebuilder:printcolumn:name="Capacity",type=string,JSONPath=`.status.capacity`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// StorageRequest is the Schema for the storagerequests API. Each StorageRequest
// manages exactly one PersistentVolumeClaim with the same name in the same
// namespace. The PVC carries no owner reference; a finalizer implements the
// explicit Retain or Delete policy.
type StorageRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec   StorageRequestSpec   `json:"spec"`
	Status StorageRequestStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// StorageRequestList contains a list of StorageRequest.
type StorageRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StorageRequest `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(SchemeGroupVersion, &StorageRequest{}, &StorageRequestList{})
		metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
		return nil
	})
}
