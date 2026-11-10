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
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// supportedAccessModes is the set of PVC access modes accepted by the API. A
// value accepted here is not guaranteed to be supported by the installed CSI
// driver or storage backend.
var supportedAccessModes = map[corev1.PersistentVolumeAccessMode]struct{}{
	corev1.ReadWriteOnce:    {},
	corev1.ReadOnlyMany:     {},
	corev1.ReadWriteMany:    {},
	corev1.ReadWriteOncePod: {},
}

var supportedVolumeModes = map[corev1.PersistentVolumeMode]struct{}{
	corev1.PersistentVolumeFilesystem: {},
	corev1.PersistentVolumeBlock:      {},
}

var supportedOnDelete = map[OnDeletePolicy]struct{}{
	OnDeleteRetain: {},
	OnDeleteDelete: {},
}

// Magnitude bounds for spec.storage.size. Quantity.Cmp, rounding, and String
// are superlinear in the decimal exponent, so a quantity such as 1e2147483647
// must be rejected before any of them is called. The bounds are deliberately
// wider than anything the CRD rule admits (at most 64 characters with an
// exponent of at most two digits), so they never reject an admitted object.
// The check uses AsApproximateFloat64, which is constant-time.
const (
	minSizeMagnitude = 1e-165
	maxSizeMagnitude = 1e165
)

// ValidateStorageRequest runs defensive validation on a StorageRequest. The CRD
// schema and CEL rules are the primary guard; this repeats the checks in Go so
// the controller never creates or resizes a PVC from an invalid spec, including
// for objects admitted before a schema change. Defaults are assumed to have
// been applied by the API server, but an empty access-mode list is tolerated
// here and treated as the default rather than rejected.
func (sr *StorageRequest) ValidateStorageRequest() field.ErrorList {
	return validateStorageRequestSpec(&sr.Spec, field.NewPath("spec"))
}

func validateStorageRequestSpec(spec *StorageRequestSpec, fldPath *field.Path) field.ErrorList {
	allErrs := validateStorageSpec(&spec.Storage, fldPath.Child("storage"))
	return append(allErrs, validateLifecycleSpec(&spec.Lifecycle, fldPath.Child("lifecycle"))...)
}

func validateStorageSpec(storage *StorageSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	// size must be present, strictly positive, and of bounded magnitude. The
	// quantity is already parsed by the API machinery; Sign is constant-time.
	// Callers must not call Cmp on a size that has not passed this check.
	sizePath := fldPath.Child("size")
	if storage.Size.IsZero() {
		allErrs = append(allErrs, field.Required(sizePath, "a positive storage size is required"))
	} else if storage.Size.Sign() <= 0 {
		// Report the sign rather than Size.String(): formatting an extreme
		// quantity is as expensive as comparing it.
		allErrs = append(allErrs, field.Invalid(sizePath, "<non-positive quantity>", "storage size must be greater than zero"))
	} else if f := storage.Size.AsApproximateFloat64(); f < minSizeMagnitude || f > maxSizeMagnitude {
		allErrs = append(allErrs, field.Invalid(sizePath, "<out-of-range quantity>",
			"storage size is outside the supported range"))
	}

	// storageClassName, when supplied, must be non-empty. Omission (nil) means
	// "use Kubernetes defaulting" and is valid.
	if storage.StorageClassName != nil && *storage.StorageClassName == "" {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("storageClassName"), "",
			"storageClassName must be non-empty when set; omit the field to use the cluster default"))
	}

	// accessModes: tolerate empty (defaulting); reject unsupported and duplicates.
	amPath := fldPath.Child("accessModes")
	seen := make(map[corev1.PersistentVolumeAccessMode]struct{}, len(storage.AccessModes))
	for i, mode := range storage.AccessModes {
		if _, ok := supportedAccessModes[mode]; !ok {
			allErrs = append(allErrs, field.NotSupported(amPath.Index(i), mode, supportedAccessModeNames()))
			continue
		}
		if _, dup := seen[mode]; dup {
			allErrs = append(allErrs, field.Duplicate(amPath.Index(i), mode))
			continue
		}
		seen[mode] = struct{}{}
	}

	// volumeMode: tolerate nil (defaulting); reject unsupported values.
	if storage.VolumeMode != nil {
		if _, ok := supportedVolumeModes[*storage.VolumeMode]; !ok {
			allErrs = append(allErrs, field.NotSupported(fldPath.Child("volumeMode"), *storage.VolumeMode,
				[]string{string(corev1.PersistentVolumeFilesystem), string(corev1.PersistentVolumeBlock)}))
		}
	}

	return allErrs
}

func validateLifecycleSpec(lifecycle *LifecycleSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	// onDelete: tolerate empty (defaulting); reject unsupported values.
	if lifecycle.OnDelete != "" {
		if _, ok := supportedOnDelete[lifecycle.OnDelete]; !ok {
			allErrs = append(allErrs, field.NotSupported(fldPath.Child("onDelete"), lifecycle.OnDelete,
				[]string{string(OnDeleteRetain), string(OnDeleteDelete)}))
		}
	}
	return allErrs
}

func supportedAccessModeNames() []string {
	return []string{
		string(corev1.ReadWriteOnce),
		string(corev1.ReadOnlyMany),
		string(corev1.ReadWriteMany),
		string(corev1.ReadWriteOncePod),
	}
}
