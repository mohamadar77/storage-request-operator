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

	corev1 "k8s.io/api/core/v1"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
)

// CheckImmutableFields compares the request's StorageClass, access modes, and
// volume mode with the verified PVC (design section 6.3). These fields cannot
// change after the claim exists, and the claim is never replaced to make a
// change appear satisfied. The returned message names every differing field
// and is empty when the spec matches the claim.
//
// An omitted spec.storage.storageClassName is never a difference: the PVC's
// observed class is authoritative then, including a default that Kubernetes
// assigned after creation.
func CheckImmutableFields(sr *storagev1alpha1.StorageRequest, pvc *corev1.PersistentVolumeClaim) string {
	var diffs []string

	if sr.Spec.Storage.StorageClassName != nil {
		want := *sr.Spec.Storage.StorageClassName
		have := claimStorageClass(pvc)
		if want != have {
			diffs = append(diffs, fmt.Sprintf("storageClassName %q (the claim has %s)", want, describeClass(have)))
		}
	}
	if want, have := EffectiveAccessModes(sr), pvc.Spec.AccessModes; !sameAccessModes(want, have) {
		diffs = append(diffs, fmt.Sprintf("accessModes %v (the claim has %v)", want, have))
	}
	if want, have := EffectiveVolumeMode(sr), claimVolumeMode(pvc); want != have {
		diffs = append(diffs, fmt.Sprintf("volumeMode %s (the claim has %s)", want, have))
	}

	if len(diffs) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"spec.storage asks for %s, which cannot change after PersistentVolumeClaim %s was created. "+
			"The claim was neither changed nor replaced; to use different settings, create a new "+
			"StorageRequest with a different name",
		strings.Join(diffs, "; "), pvc.Name)
}

// claimStorageClass returns the PVC's StorageClass name, "" when unset.
func claimStorageClass(pvc *corev1.PersistentVolumeClaim) string {
	if pvc.Spec.StorageClassName == nil {
		return ""
	}
	return *pvc.Spec.StorageClassName
}

func describeClass(name string) string {
	if name == "" {
		return "no StorageClass"
	}
	return fmt.Sprintf("%q", name)
}

// claimVolumeMode returns the PVC's volume mode, applying the Kubernetes
// default when unset.
func claimVolumeMode(pvc *corev1.PersistentVolumeClaim) corev1.PersistentVolumeMode {
	if pvc.Spec.VolumeMode == nil {
		return corev1.PersistentVolumeFilesystem
	}
	return *pvc.Spec.VolumeMode
}

// sameAccessModes compares two access-mode lists as sets.
func sameAccessModes(a, b []corev1.PersistentVolumeAccessMode) bool {
	set := func(modes []corev1.PersistentVolumeAccessMode) map[corev1.PersistentVolumeAccessMode]struct{} {
		s := make(map[corev1.PersistentVolumeAccessMode]struct{}, len(modes))
		for _, m := range modes {
			s[m] = struct{}{}
		}
		return s
	}
	sa, sb := set(a), set(b)
	if len(sa) != len(sb) {
		return false
	}
	for m := range sa {
		if _, ok := sb[m]; !ok {
			return false
		}
	}
	return true
}
