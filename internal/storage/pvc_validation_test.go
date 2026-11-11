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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
)

func TestCheckImmutableFields(t *testing.T) {
	rwo := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	rwx := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
	both := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce, corev1.ReadOnlyMany}
	bothReversed := []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany, corev1.ReadWriteOnce}
	block := corev1.PersistentVolumeBlock
	fs := corev1.PersistentVolumeFilesystem

	request := func(class *string, modes []corev1.PersistentVolumeAccessMode, mode *corev1.PersistentVolumeMode) *storagev1alpha1.StorageRequest {
		return &storagev1alpha1.StorageRequest{Spec: storagev1alpha1.StorageRequestSpec{
			Storage: storagev1alpha1.StorageSpec{StorageClassName: class, AccessModes: modes, VolumeMode: mode},
		}}
	}
	claim := func(class *string, modes []corev1.PersistentVolumeAccessMode, mode *corev1.PersistentVolumeMode) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: testName},
			Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: class, AccessModes: modes, VolumeMode: mode},
		}
	}

	tests := []struct {
		name string
		sr   *storagev1alpha1.StorageRequest
		pvc  *corev1.PersistentVolumeClaim
		want []string // substrings the message must contain; empty means no difference
	}{
		{name: "identical explicit settings", sr: request(new("fast"), rwo, &fs), pvc: claim(new("fast"), rwo, &fs)},
		{name: "request defaults match claim defaults", sr: request(nil, nil, nil), pvc: claim(nil, rwo, nil)},
		{name: "omitted class accepts any claim class", sr: request(nil, rwo, &fs), pvc: claim(new("assigned"), rwo, &fs)},
		{name: "access mode order is irrelevant", sr: request(nil, both, nil), pvc: claim(nil, bothReversed, nil)},
		{name: "explicit class differs", sr: request(new("fast"), rwo, nil), pvc: claim(new("slow"), rwo, nil), want: []string{`storageClassName "fast"`, `"slow"`}},
		{name: "explicit class on a classless claim", sr: request(new("fast"), rwo, nil), pvc: claim(nil, rwo, nil), want: []string{`storageClassName "fast"`, "no StorageClass"}},
		{name: "access modes differ", sr: request(nil, rwx, nil), pvc: claim(nil, rwo, nil), want: []string{"accessModes [ReadWriteMany]", "[ReadWriteOnce]"}},
		{name: "access mode added", sr: request(nil, both, nil), pvc: claim(nil, rwo, nil), want: []string{"accessModes"}},
		{name: "volume mode differs", sr: request(nil, rwo, &block), pvc: claim(nil, rwo, nil), want: []string{"volumeMode Block", "Filesystem"}},
		{name: "every difference is listed", sr: request(new("fast"), rwx, &block), pvc: claim(new("slow"), rwo, &fs), want: []string{"storageClassName", "accessModes", "volumeMode", "neither changed nor replaced"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckImmutableFields(tt.sr, tt.pvc)
			if len(tt.want) == 0 {
				if got != "" {
					t.Fatalf("CheckImmutableFields() = %q, want no difference", got)
				}
				return
			}
			if got == "" {
				t.Fatalf("CheckImmutableFields() reported no difference, want %v", tt.want)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("message %q does not contain %q", got, w)
				}
			}
		})
	}
}
