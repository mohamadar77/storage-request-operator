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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
)

const (
	testName       = "app-data"
	testRequestUID = types.UID("11111111-1111-1111-1111-111111111111")
	testPVCUID     = types.UID("22222222-2222-2222-2222-222222222222")
	otherUID       = types.UID("33333333-3333-3333-3333-333333333333")
)

func testRequest(mutate func(*storagev1alpha1.StorageRequest)) *storagev1alpha1.StorageRequest {
	sr := &storagev1alpha1.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "team-a",
			Name:        testName,
			UID:         testRequestUID,
			Labels:      map[string]string{"user-label": "must-not-be-copied"},
			Annotations: map[string]string{"user-annotation": "must-not-be-copied"},
		},
		Spec: storagev1alpha1.StorageRequestSpec{
			Storage: storagev1alpha1.StorageSpec{
				Size:        resource.MustParse("20Gi"),
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				VolumeMode:  new(corev1.PersistentVolumeFilesystem),
			},
			Lifecycle: storagev1alpha1.LifecycleSpec{OnDelete: storagev1alpha1.OnDeleteRetain},
		},
	}
	if mutate != nil {
		mutate(sr)
	}
	return sr
}

func TestBuildPVC(t *testing.T) {
	sr := testRequest(func(sr *storagev1alpha1.StorageRequest) {
		sr.Spec.Storage.StorageClassName = new("fast-ssd")
		sr.Spec.Storage.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany, corev1.ReadOnlyMany}
		sr.Spec.Storage.VolumeMode = new(corev1.PersistentVolumeBlock)
	})
	pvc := BuildPVC(sr)

	if pvc.Namespace != "team-a" || pvc.Name != testName {
		t.Errorf("namespace/name: got %s/%s, want team-a/app-data", pvc.Namespace, pvc.Name)
	}
	wantLabels := map[string]string{
		"app.kubernetes.io/managed-by":   "storage-request-operator",
		"storage.base-domain.io/managed": "true",
	}
	if !equality.Semantic.DeepEqual(pvc.Labels, wantLabels) {
		t.Errorf("labels: got %v, want exactly %v", pvc.Labels, wantLabels)
	}
	wantAnnotations := map[string]string{"storage.base-domain.io/request-uid": string(testRequestUID)}
	if !equality.Semantic.DeepEqual(pvc.Annotations, wantAnnotations) {
		t.Errorf("annotations: got %v, want exactly %v", pvc.Annotations, wantAnnotations)
	}
	if len(pvc.OwnerReferences) != 0 {
		t.Errorf("owner references: got %v, want none", pvc.OwnerReferences)
	}
	if len(pvc.Finalizers) != 0 {
		t.Errorf("finalizers: got %v, want none", pvc.Finalizers)
	}
	if got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(resource.MustParse("20Gi")) != 0 {
		t.Errorf("storage request: got %s, want 20Gi", got.String())
	}
	if len(pvc.Spec.Resources.Requests) != 1 || len(pvc.Spec.Resources.Limits) != 0 {
		t.Errorf("resources: got %v, want only a storage request", pvc.Spec.Resources)
	}
	wantModes := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany, corev1.ReadOnlyMany}
	if !equality.Semantic.DeepEqual(pvc.Spec.AccessModes, wantModes) {
		t.Errorf("access modes: got %v, want %v", pvc.Spec.AccessModes, wantModes)
	}
	if pvc.Spec.VolumeMode == nil || *pvc.Spec.VolumeMode != corev1.PersistentVolumeBlock {
		t.Errorf("volume mode: got %v, want Block", pvc.Spec.VolumeMode)
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "fast-ssd" {
		t.Errorf("storage class: got %v, want fast-ssd", pvc.Spec.StorageClassName)
	}

	// Everything else in the spec must stay unset: the operator owns only the
	// fields above.
	rest := pvc.Spec.DeepCopy()
	rest.AccessModes, rest.VolumeMode, rest.StorageClassName = nil, nil, nil
	rest.Resources = corev1.VolumeResourceRequirements{}
	if !equality.Semantic.DeepEqual(*rest, corev1.PersistentVolumeClaimSpec{}) {
		t.Errorf("unexpected extra spec fields set: %+v", *rest)
	}
}

func TestBuildPVCOmitsStorageClassWhenUnset(t *testing.T) {
	pvc := BuildPVC(testRequest(nil))
	if pvc.Spec.StorageClassName != nil {
		t.Errorf("storage class: got %q, want nil so Kubernetes applies its default", *pvc.Spec.StorageClassName)
	}
}

func TestBuildPVCAppliesDefaultsWhenFieldsAreEmpty(t *testing.T) {
	pvc := BuildPVC(testRequest(func(sr *storagev1alpha1.StorageRequest) {
		sr.Spec.Storage.AccessModes = nil
		sr.Spec.Storage.VolumeMode = nil
	}))
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("access modes: got %v, want [ReadWriteOnce]", pvc.Spec.AccessModes)
	}
	if pvc.Spec.VolumeMode == nil || *pvc.Spec.VolumeMode != corev1.PersistentVolumeFilesystem {
		t.Errorf("volume mode: got %v, want Filesystem", pvc.Spec.VolumeMode)
	}
}

func TestBuildPVCIsDeterministicAndDoesNotAliasTheRequest(t *testing.T) {
	sr := testRequest(func(sr *storagev1alpha1.StorageRequest) {
		sr.Spec.Storage.StorageClassName = new("fast-ssd")
	})
	first, second := BuildPVC(sr), BuildPVC(sr)
	if !equality.Semantic.DeepEqual(first, second) {
		t.Fatalf("BuildPVC is not deterministic:\n%+v\n%+v", first, second)
	}

	// Mutating the result must not reach back into the request.
	first.Spec.AccessModes[0] = corev1.ReadWriteMany
	*first.Spec.StorageClassName = "changed"
	*first.Spec.VolumeMode = corev1.PersistentVolumeBlock
	if sr.Spec.Storage.AccessModes[0] != corev1.ReadWriteOnce ||
		*sr.Spec.Storage.StorageClassName != "fast-ssd" ||
		*sr.Spec.Storage.VolumeMode != corev1.PersistentVolumeFilesystem {
		t.Errorf("BuildPVC result aliases the request spec: %+v", sr.Spec.Storage)
	}
}
