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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
)

func TestCheckIdentity(t *testing.T) {
	tests := []struct {
		name        string
		recordedUID types.UID
		// annotation is the request-uid annotation on the PVC; nil means absent.
		annotation *string
		pvcUID     types.UID
		want       Identity
	}{
		{
			name:       "annotation matches and nothing recorded: unrecorded (fresh create or crash recovery)",
			annotation: new(string(testRequestUID)),
			pvcUID:     testPVCUID,
			want:       IdentityUnrecorded,
		},
		{
			name:        "annotation and recorded UID both match: verified",
			recordedUID: testPVCUID,
			annotation:  new(string(testRequestUID)),
			pvcUID:      testPVCUID,
			want:        IdentityVerified,
		},
		{
			name:   "no annotation and nothing recorded: name conflict",
			pvcUID: testPVCUID,
			want:   IdentityNameConflict,
		},
		{
			name:       "empty annotation and nothing recorded: name conflict",
			annotation: new(""),
			pvcUID:     testPVCUID,
			want:       IdentityNameConflict,
		},
		{
			name:       "annotation names another request: ownership mismatch",
			annotation: new(string(otherUID)),
			pvcUID:     testPVCUID,
			want:       IdentityOwnershipMismatch,
		},
		{
			name:        "annotation names another request, recorded UID matches: ownership mismatch",
			recordedUID: testPVCUID,
			annotation:  new(string(otherUID)),
			pvcUID:      testPVCUID,
			want:        IdentityOwnershipMismatch,
		},
		{
			name:        "same-name replacement with copied annotation: ownership mismatch",
			recordedUID: testPVCUID,
			annotation:  new(string(testRequestUID)),
			pvcUID:      otherUID,
			want:        IdentityOwnershipMismatch,
		},
		{
			name:        "same-name replacement without annotation: ownership mismatch",
			recordedUID: testPVCUID,
			pvcUID:      otherUID,
			want:        IdentityOwnershipMismatch,
		},
		{
			name:        "recorded claim lost its annotation: ownership mismatch",
			recordedUID: testPVCUID,
			pvcUID:      testPVCUID,
			want:        IdentityOwnershipMismatch,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sr := testRequest(func(sr *storagev1alpha1.StorageRequest) {
				sr.Status.PVCUID = tc.recordedUID
			})
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: sr.Namespace,
					Name:      sr.Name,
					UID:       tc.pvcUID,
					// The managed labels alone must never establish identity.
					Labels: map[string]string{
						storagev1alpha1.LabelManagedBy: storagev1alpha1.ManagedByValue,
						storagev1alpha1.LabelManaged:   storagev1alpha1.ManagedValue,
					},
				},
			}
			if tc.annotation != nil {
				pvc.Annotations = map[string]string{storagev1alpha1.AnnotationRequestUID: *tc.annotation}
			}

			got, message := CheckIdentity(sr, pvc)
			if got != tc.want {
				t.Fatalf("identity: got %s, want %s", got, tc.want)
			}
			matched := got == IdentityVerified || got == IdentityUnrecorded
			if matched && message != "" {
				t.Errorf("message for a matching identity: got %q, want empty", message)
			}
			if !matched && message == "" {
				t.Errorf("message for %s: got empty, want an explanation", got)
			}
		})
	}
}
