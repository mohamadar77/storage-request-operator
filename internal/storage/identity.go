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

	corev1 "k8s.io/api/core/v1"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
)

// Identity is the outcome of checking whether an existing PVC is the exact
// claim managed by a StorageRequest (design section 5.3).
type Identity int

const (
	// IdentityVerified means the request UID annotation and the recorded PVC
	// UID both match. Only this result authorizes mutating or deleting the PVC.
	IdentityVerified Identity = iota
	// IdentityUnrecorded means the request UID annotation matches and no PVC
	// UID has been recorded yet. This is the state right after creation and
	// after a crash between PVC creation and the status write; the caller must
	// record the PVC UID before doing anything else.
	IdentityUnrecorded
	// IdentityNameConflict means a PVC with the expected name exists but
	// carries no request UID annotation, and this request has never recorded a
	// PVC. It is an unrelated claim and must not be adopted or touched.
	IdentityNameConflict
	// IdentityOwnershipMismatch means the PVC belongs to another request, or is
	// not the PVC incarnation this request recorded. It must not be adopted or
	// touched, even if the request UID annotation was copied onto it.
	IdentityOwnershipMismatch
)

// String returns a short name for logs and test failures.
func (i Identity) String() string {
	switch i {
	case IdentityVerified:
		return "Verified"
	case IdentityUnrecorded:
		return "Unrecorded"
	case IdentityNameConflict:
		return "NameConflict"
	case IdentityOwnershipMismatch:
		return "OwnershipMismatch"
	default:
		return fmt.Sprintf("Identity(%d)", int(i))
	}
}

// CheckIdentity decides whether pvc is the claim managed by sr. A name match
// is never sufficient. The checks, in order:
//
//  1. If the request has recorded a PVC UID and it differs from this PVC's UID,
//     the claim was replaced: OwnershipMismatch, regardless of annotations.
//  2. If the PVC has no request UID annotation: NameConflict when nothing was
//     recorded, OwnershipMismatch when the recorded claim lost its marker.
//  3. If the annotation names another request UID: OwnershipMismatch.
//  4. Otherwise the annotation matches: Unrecorded when no PVC UID is recorded
//     yet, Verified when the recorded UID matches.
//
// The returned message explains a conflict or mismatch in user-facing terms and
// is empty for the two matching results.
func CheckIdentity(sr *storagev1alpha1.StorageRequest, pvc *corev1.PersistentVolumeClaim) (Identity, string) {
	recorded := sr.Status.PVCUID
	owner := pvc.Annotations[storagev1alpha1.AnnotationRequestUID]

	if recorded != "" && recorded != pvc.UID {
		return IdentityOwnershipMismatch, fmt.Sprintf(
			"PersistentVolumeClaim %s has UID %s, but this request recorded UID %s; "+
				"the recorded claim was replaced and the existing claim was left unchanged",
			pvc.Name, pvc.UID, recorded)
	}
	if owner == "" {
		if recorded != "" {
			return IdentityOwnershipMismatch, fmt.Sprintf(
				"PersistentVolumeClaim %s no longer carries the %s annotation; it was left unchanged",
				pvc.Name, storagev1alpha1.AnnotationRequestUID)
		}
		return IdentityNameConflict, fmt.Sprintf(
			"PersistentVolumeClaim %s already exists and is not managed by this request; "+
				"it was not adopted or changed",
			pvc.Name)
	}
	if owner != string(sr.UID) {
		return IdentityOwnershipMismatch, fmt.Sprintf(
			"PersistentVolumeClaim %s belongs to StorageRequest UID %s, not to this request; "+
				"it was not adopted or changed",
			pvc.Name, owner)
	}
	if recorded == "" {
		return IdentityUnrecorded, ""
	}
	return IdentityVerified, ""
}
