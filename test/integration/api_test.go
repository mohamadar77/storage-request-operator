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

// Package integration contains envtest-backed tests that exercise the
// StorageRequest CRD against a real Kubernetes API server. These verify
// behavior a fake client cannot: CEL validation, server-side defaulting,
// structural-schema enforcement, and the status subresource.
package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
	scheme    = runtime.NewScheme()
)

func TestMain(m *testing.M) {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(storagev1alpha1.AddToScheme(scheme))

	// Allow `go test ./...` without KUBEBUILDER_ASSETS by falling back to the
	// binaries downloaded under bin/k8s via `make setup-envtest`. `make test`
	// sets KUBEBUILDER_ASSETS explicitly and takes precedence.
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if dir := firstEnvtestBinDir(); dir != "" {
			_ = os.Setenv("KUBEBUILDER_ASSETS", dir)
		}
	}

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		panic("failed to start envtest (run `make setup-envtest` to download binaries): " + err.Error())
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		_ = testEnv.Stop()
		panic("failed to create client: " + err.Error())
	}

	code := m.Run()
	_ = testEnv.Stop()
	os.Exit(code)
}

func firstEnvtestBinDir() string {
	base := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			abs, err := filepath.Abs(filepath.Join(base, e.Name()))
			if err == nil {
				return abs
			}
		}
	}
	return ""
}

func baseRequest(name string) *storagev1alpha1.StorageRequest {
	return &storagev1alpha1.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: storagev1alpha1.StorageRequestSpec{
			Storage: storagev1alpha1.StorageSpec{
				Size: resource.MustParse("20Gi"),
			},
		},
	}
}

// TestServerSideDefaults verifies the API server applies the CRD defaults for
// access modes, volume mode, and the deletion policy.
func TestServerSideDefaults(t *testing.T) {
	ctx := context.Background()
	sr := baseRequest("defaults-applied")
	if err := k8sClient.Create(ctx, sr); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, sr) })

	got := &storagev1alpha1.StorageRequest{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(sr), got); err != nil {
		t.Fatalf("get: %v", err)
	}

	if len(got.Spec.Storage.AccessModes) != 1 || got.Spec.Storage.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("accessModes default: got %v, want [ReadWriteOnce]", got.Spec.Storage.AccessModes)
	}
	if got.Spec.Storage.VolumeMode == nil || *got.Spec.Storage.VolumeMode != corev1.PersistentVolumeFilesystem {
		t.Errorf("volumeMode default: got %v, want Filesystem", got.Spec.Storage.VolumeMode)
	}
	if got.Spec.Lifecycle.OnDelete != storagev1alpha1.OnDeleteRetain {
		t.Errorf("onDelete default: got %q, want Retain", got.Spec.Lifecycle.OnDelete)
	}
}

// TestSchemaRejections verifies the CRD schema and CEL rules reject invalid
// specs at admission time, before any controller runs.
func TestSchemaRejections(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		mutate     func(*storagev1alpha1.StorageRequest)
		wantErrSub string
	}{
		{
			name:       "zero size rejected by CEL",
			mutate:     func(sr *storagev1alpha1.StorageRequest) { sr.Spec.Storage.Size = resource.MustParse("0") },
			wantErrSub: "positive",
		},
		{
			name:       "negative size rejected by CEL",
			mutate:     func(sr *storagev1alpha1.StorageRequest) { sr.Spec.Storage.Size = resource.MustParse("-1Gi") },
			wantErrSub: "positive",
		},
		{
			name: "empty storageClassName rejected by minLength",
			mutate: func(sr *storagev1alpha1.StorageRequest) {
				empty := ""
				sr.Spec.Storage.StorageClassName = &empty
			},
			wantErrSub: "storageClassName",
		},
		{
			name: "unsupported access mode rejected by enum",
			mutate: func(sr *storagev1alpha1.StorageRequest) {
				sr.Spec.Storage.AccessModes = []corev1.PersistentVolumeAccessMode{"ReadWriteSomehow"}
			},
			wantErrSub: "accessModes",
		},
		{
			name: "duplicate access modes rejected by set semantics",
			mutate: func(sr *storagev1alpha1.StorageRequest) {
				sr.Spec.Storage.AccessModes = []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce, corev1.ReadWriteOnce,
				}
			},
			wantErrSub: "accessModes",
		},
		{
			name: "unsupported volume mode rejected by enum",
			mutate: func(sr *storagev1alpha1.StorageRequest) {
				vm := corev1.PersistentVolumeMode("Raw")
				sr.Spec.Storage.VolumeMode = &vm
			},
			wantErrSub: "volumeMode",
		},
		{
			name: "unsupported onDelete rejected by enum",
			mutate: func(sr *storagev1alpha1.StorageRequest) {
				sr.Spec.Lifecycle.OnDelete = storagev1alpha1.OnDeletePolicy("Shred")
			},
			wantErrSub: "onDelete",
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sr := baseRequest("reject-" + string(rune('a'+i)))
			tc.mutate(sr)
			err := k8sClient.Create(ctx, sr)
			if err == nil {
				_ = k8sClient.Delete(ctx, sr)
				t.Fatalf("expected admission error, got nil")
			}
			if tc.wantErrSub != "" && !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrSub)
			}
		})
	}
}

// TestStatusSubresource verifies status is served as a subresource: a spec
// Update does not persist status, and a Status().Update does not alter spec.
func TestStatusSubresource(t *testing.T) {
	ctx := context.Background()
	sr := baseRequest("status-subresource")
	if err := k8sClient.Create(ctx, sr); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, sr) })

	// A plain Update that also sets status must not persist the status change.
	sr.Status.PVCName = "should-be-ignored-by-spec-update"
	if err := k8sClient.Update(ctx, sr); err != nil {
		t.Fatalf("spec update: %v", err)
	}
	afterSpecUpdate := &storagev1alpha1.StorageRequest{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(sr), afterSpecUpdate); err != nil {
		t.Fatalf("get: %v", err)
	}
	if afterSpecUpdate.Status.PVCName != "" {
		t.Errorf("spec Update persisted status: got pvcName %q, want empty", afterSpecUpdate.Status.PVCName)
	}

	// A Status().Update persists status and leaves spec untouched.
	afterSpecUpdate.Status.PVCName = "status-subresource"
	afterSpecUpdate.Status.PVCUID = "uid-1234"
	afterSpecUpdate.Status.ObservedGeneration = afterSpecUpdate.Generation
	cap := resource.MustParse("20Gi")
	afterSpecUpdate.Status.Capacity = &cap
	afterSpecUpdate.Status.Conditions = []metav1.Condition{{
		Type:               storagev1alpha1.ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             "Provisioning",
		Message:            "awaiting bind",
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: afterSpecUpdate.Generation,
	}}
	if err := k8sClient.Status().Update(ctx, afterSpecUpdate); err != nil {
		t.Fatalf("status update: %v", err)
	}

	final := &storagev1alpha1.StorageRequest{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(sr), final); err != nil {
		t.Fatalf("get final: %v", err)
	}
	if final.Status.PVCName != "status-subresource" {
		t.Errorf("status pvcName: got %q, want status-subresource", final.Status.PVCName)
	}
	if final.Status.PVCUID != "uid-1234" {
		t.Errorf("status pvcUID: got %q, want uid-1234", final.Status.PVCUID)
	}
	if len(final.Status.Conditions) != 1 || final.Status.Conditions[0].Type != storagev1alpha1.ConditionReady {
		t.Errorf("status conditions not persisted: got %v", final.Status.Conditions)
	}
	if final.Spec.Storage.Size.Cmp(resource.MustParse("20Gi")) != 0 {
		t.Errorf("status update changed spec size: got %s", final.Spec.Storage.Size.String())
	}
}
