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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func ptr[T any](v T) *T { return &v }

func newRequest(mutate func(*StorageRequest)) *StorageRequest {
	sr := &StorageRequest{
		Spec: StorageRequestSpec{
			Storage: StorageSpec{
				Size:        resource.MustParse("20Gi"),
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				VolumeMode:  ptr(corev1.PersistentVolumeFilesystem),
			},
			Lifecycle: LifecycleSpec{OnDelete: OnDeleteRetain},
		},
	}
	if mutate != nil {
		mutate(sr)
	}
	return sr
}

func TestValidateStorageRequest(t *testing.T) {
	tests := []struct {
		name string
		sr   *StorageRequest
		// wantErrs maps the expected error field path to its error type.
		wantErrs map[string]field.ErrorType
	}{
		{
			name:     "valid full spec",
			sr:       newRequest(nil),
			wantErrs: map[string]field.ErrorType{},
		},
		{
			name: "valid minimal spec relies on server defaulting",
			sr: newRequest(func(sr *StorageRequest) {
				sr.Spec.Storage.AccessModes = nil
				sr.Spec.Storage.VolumeMode = nil
				sr.Spec.Lifecycle.OnDelete = ""
			}),
			wantErrs: map[string]field.ErrorType{},
		},
		{
			name: "valid with storageClassName set",
			sr: newRequest(func(sr *StorageRequest) {
				sr.Spec.Storage.StorageClassName = ptr("fast-ssd")
			}),
			wantErrs: map[string]field.ErrorType{},
		},
		{
			name: "zero size is required error",
			sr: newRequest(func(sr *StorageRequest) {
				sr.Spec.Storage.Size = resource.Quantity{}
			}),
			wantErrs: map[string]field.ErrorType{"spec.storage.size": field.ErrorTypeRequired},
		},
		{
			name: "negative size is invalid",
			sr: newRequest(func(sr *StorageRequest) {
				sr.Spec.Storage.Size = resource.MustParse("-1Gi")
			}),
			wantErrs: map[string]field.ErrorType{"spec.storage.size": field.ErrorTypeInvalid},
		},
		{
			name: "empty storageClassName is invalid",
			sr: newRequest(func(sr *StorageRequest) {
				sr.Spec.Storage.StorageClassName = ptr("")
			}),
			wantErrs: map[string]field.ErrorType{"spec.storage.storageClassName": field.ErrorTypeInvalid},
		},
		{
			name: "unsupported access mode",
			sr: newRequest(func(sr *StorageRequest) {
				sr.Spec.Storage.AccessModes = []corev1.PersistentVolumeAccessMode{"ReadWriteSomehow"}
			}),
			wantErrs: map[string]field.ErrorType{"spec.storage.accessModes[0]": field.ErrorTypeNotSupported},
		},
		{
			name: "duplicate access mode",
			sr: newRequest(func(sr *StorageRequest) {
				sr.Spec.Storage.AccessModes = []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce, corev1.ReadWriteOnce,
				}
			}),
			wantErrs: map[string]field.ErrorType{"spec.storage.accessModes[1]": field.ErrorTypeDuplicate},
		},
		{
			name: "unsupported volume mode",
			sr: newRequest(func(sr *StorageRequest) {
				sr.Spec.Storage.VolumeMode = ptr(corev1.PersistentVolumeMode("Raw"))
			}),
			wantErrs: map[string]field.ErrorType{"spec.storage.volumeMode": field.ErrorTypeNotSupported},
		},
		{
			name: "unsupported onDelete",
			sr: newRequest(func(sr *StorageRequest) {
				sr.Spec.Lifecycle.OnDelete = OnDeletePolicy("Shred")
			}),
			wantErrs: map[string]field.ErrorType{"spec.lifecycle.onDelete": field.ErrorTypeNotSupported},
		},
		{
			name: "multiple errors are aggregated",
			sr: newRequest(func(sr *StorageRequest) {
				sr.Spec.Storage.Size = resource.Quantity{}
				sr.Spec.Storage.StorageClassName = ptr("")
				sr.Spec.Lifecycle.OnDelete = OnDeletePolicy("Shred")
			}),
			wantErrs: map[string]field.ErrorType{
				"spec.storage.size":             field.ErrorTypeRequired,
				"spec.storage.storageClassName": field.ErrorTypeInvalid,
				"spec.lifecycle.onDelete":       field.ErrorTypeNotSupported,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := tc.sr.ValidateStorageRequest()
			got := make(map[string]field.ErrorType, len(errs))
			for _, e := range errs {
				got[e.Field] = e.Type
			}
			if len(got) != len(tc.wantErrs) {
				t.Fatalf("got %d errors %v, want %d %v", len(got), got, len(tc.wantErrs), tc.wantErrs)
			}
			for fldPath, wantType := range tc.wantErrs {
				gotType, ok := got[fldPath]
				if !ok {
					t.Errorf("missing expected error at %q (got %v)", fldPath, got)
					continue
				}
				if gotType != wantType {
					t.Errorf("error at %q: got type %q, want %q", fldPath, gotType, wantType)
				}
			}
		})
	}
}
