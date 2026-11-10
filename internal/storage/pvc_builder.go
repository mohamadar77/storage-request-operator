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

// Package storage holds the pure PersistentVolumeClaim logic of the operator:
// building the desired claim and verifying that an existing claim is the exact
// object a StorageRequest manages. Nothing in this package talks to the API
// server, so every safety decision here can be unit tested in isolation.
package storage

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
)

// EffectiveAccessModes returns the access modes to request on the PVC. The API
// server defaults an omitted list to ReadWriteOnce; the same default is applied
// here so a request read before defaulting never yields a PVC without modes.
func EffectiveAccessModes(sr *storagev1alpha1.StorageRequest) []corev1.PersistentVolumeAccessMode {
	if len(sr.Spec.Storage.AccessModes) == 0 {
		return []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}
	return append([]corev1.PersistentVolumeAccessMode(nil), sr.Spec.Storage.AccessModes...)
}

// EffectiveVolumeMode returns the volume mode to request on the PVC, applying
// the Filesystem default when the field is unset.
func EffectiveVolumeMode(sr *storagev1alpha1.StorageRequest) corev1.PersistentVolumeMode {
	if sr.Spec.Storage.VolumeMode == nil {
		return corev1.PersistentVolumeFilesystem
	}
	return *sr.Spec.Storage.VolumeMode
}

// BuildPVC returns the PersistentVolumeClaim a StorageRequest asks for. The
// result is deterministic and contains only operator-owned fields:
//
//   - the same namespace and name as the request;
//   - the identity labels and the request UID annotation;
//   - size, access modes, volume mode, and (only when set) the StorageClass.
//
// It deliberately sets no owner reference: garbage collection must never delete
// a data-bearing claim, and the request finalizer implements the deletion
// policy instead. No user labels or annotations are copied from the request.
// When spec.storage.storageClassName is omitted the PVC field stays nil so
// Kubernetes applies its own defaulting.
func BuildPVC(sr *storagev1alpha1.StorageRequest) *corev1.PersistentVolumeClaim {
	volumeMode := EffectiveVolumeMode(sr)

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: sr.Namespace,
			Name:      sr.Name,
			Labels: map[string]string{
				storagev1alpha1.LabelManagedBy: storagev1alpha1.ManagedByValue,
				storagev1alpha1.LabelManaged:   storagev1alpha1.ManagedValue,
			},
			Annotations: map[string]string{
				storagev1alpha1.AnnotationRequestUID: string(sr.UID),
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: EffectiveAccessModes(sr),
			VolumeMode:  &volumeMode,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: sr.Spec.Storage.Size.DeepCopy(),
				},
			},
		},
	}
	if sr.Spec.Storage.StorageClassName != nil {
		pvc.Spec.StorageClassName = new(*sr.Spec.Storage.StorageClassName)
	}
	return pvc
}
