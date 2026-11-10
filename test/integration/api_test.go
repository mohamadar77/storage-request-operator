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
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// rawRequest builds an untyped StorageRequest whose spec contains only
// spec.storage.size. The typed client always serializes "lifecycle":{}, which
// would hide a missing default on the lifecycle object itself, so tests that
// exercise defaulting or raw size values must use this instead.
func rawRequest(name string, size any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": storagev1alpha1.GroupVersion.String(),
		"kind":       "StorageRequest",
		"metadata":   map[string]any{"name": name, "namespace": "default"},
		"spec": map[string]any{
			"storage": map[string]any{"size": size},
		},
	}}
	return u
}

// TestServerSideDefaults verifies the API server applies the CRD defaults for
// access modes, volume mode, and the deletion policy when the client omits
// accessModes, volumeMode, and the whole lifecycle object, as a kubectl user
// would.
func TestServerSideDefaults(t *testing.T) {
	ctx := context.Background()
	raw := rawRequest("defaults-applied", "20Gi")
	if err := k8sClient.Create(ctx, raw); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, raw) })

	got := &storagev1alpha1.StorageRequest{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(raw), got); err != nil {
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

	// Assert on the stored object as well, so the check cannot be satisfied by
	// typed-client decoding.
	stored := &unstructured.Unstructured{}
	stored.SetGroupVersionKind(raw.GroupVersionKind())
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(raw), stored); err != nil {
		t.Fatalf("get unstructured: %v", err)
	}
	onDelete, found, err := unstructured.NestedString(stored.Object, "spec", "lifecycle", "onDelete")
	if err != nil || !found || onDelete != string(storagev1alpha1.OnDeleteRetain) {
		t.Errorf("stored spec.lifecycle.onDelete: got %q (found=%v, err=%v), want Retain", onDelete, found, err)
	}
}

// TestAcceptedSpecs verifies every supported enum value and the supported size
// forms are admitted, so a typo in a validation marker cannot go unnoticed.
func TestAcceptedSpecs(t *testing.T) {
	ctx := context.Background()

	sizes := []any{"20Gi", "1.5Gi", "1e3", "1E3", "7Ei", int64(20), int64(1073741824)}
	for i, size := range sizes {
		raw := rawRequest("accept-size-"+string(rune('a'+i)), size)
		if err := k8sClient.Create(ctx, raw); err != nil {
			t.Errorf("size %v: unexpected rejection: %v", size, err)
			continue
		}
		_ = k8sClient.Delete(ctx, raw)
	}

	i := 0
	create := func(mutate func(*storagev1alpha1.StorageRequest), what string) {
		sr := baseRequest("accept-enum-" + string(rune('a'+i)))
		i++
		mutate(sr)
		if err := k8sClient.Create(ctx, sr); err != nil {
			t.Errorf("%s: unexpected rejection: %v", what, err)
			return
		}
		_ = k8sClient.Delete(ctx, sr)
	}
	for _, mode := range []corev1.PersistentVolumeAccessMode{
		corev1.ReadWriteOnce, corev1.ReadOnlyMany, corev1.ReadWriteMany, corev1.ReadWriteOncePod,
	} {
		create(func(sr *storagev1alpha1.StorageRequest) {
			sr.Spec.Storage.AccessModes = []corev1.PersistentVolumeAccessMode{mode}
		}, "accessMode "+string(mode))
	}
	for _, vm := range []corev1.PersistentVolumeMode{corev1.PersistentVolumeFilesystem, corev1.PersistentVolumeBlock} {
		create(func(sr *storagev1alpha1.StorageRequest) { sr.Spec.Storage.VolumeMode = &vm }, "volumeMode "+string(vm))
	}
	for _, od := range []storagev1alpha1.OnDeletePolicy{storagev1alpha1.OnDeleteRetain, storagev1alpha1.OnDeleteDelete} {
		create(func(sr *storagev1alpha1.StorageRequest) { sr.Spec.Lifecycle.OnDelete = od }, "onDelete "+string(od))
	}
}

// TestSizeRejections verifies raw size values are rejected by the CEL rule,
// and that pathological exponents are rejected quickly. Quantity parsing and
// comparison are superlinear in the exponent; without the guards at the front
// of the rule these requests pin an API server core for minutes.
func TestSizeRejections(t *testing.T) {
	ctx := context.Background()
	const maxLatency = 5 * time.Second

	sizes := []any{
		"0", "-1Gi", int64(0), int64(-1), "not-a-quantity",
		"1e2147483647", "1e99999999999", "1e-99999999999", "1e-2147483647", "1e100", "1E+100",
		strings.Repeat("9", 65),
	}
	for i, size := range sizes {
		raw := rawRequest("reject-size-"+string(rune('a'+i)), size)
		start := time.Now()
		err := k8sClient.Create(ctx, raw)
		elapsed := time.Since(start)
		if err == nil {
			_ = k8sClient.Delete(ctx, raw)
			t.Errorf("size %v: expected admission error, got nil", size)
			continue
		}
		if !apierrors.IsInvalid(err) {
			t.Errorf("size %v: got %v, want an Invalid error", size, err)
		}
		if !strings.Contains(err.Error(), "size must be a positive Kubernetes quantity") {
			t.Errorf("size %v: error %q does not carry the rule message", size, err.Error())
		}
		if elapsed > maxLatency {
			t.Errorf("size %v: rejection took %s, want under %s", size, elapsed, maxLatency)
		}
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

	// A Status().Update persists status and ignores a spec change sent with it.
	afterSpecUpdate.Spec.Storage.Size = resource.MustParse("99Gi")
	afterSpecUpdate.Status.PVCName = "status-subresource"
	afterSpecUpdate.Status.PVCUID = "uid-1234"
	afterSpecUpdate.Status.ObservedGeneration = afterSpecUpdate.Generation
	capacity := resource.MustParse("20Gi")
	afterSpecUpdate.Status.Capacity = &capacity
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

// TestPVCUIDImmutable verifies status.pvcUID cannot be changed, cleared, or
// dropped with the whole status once recorded. The recorded UID is the only
// thing that stops the controller from recreating or adopting a same-name PVC.
func TestPVCUIDImmutable(t *testing.T) {
	ctx := context.Background()
	sr := baseRequest("pvcuid-immutable")
	if err := k8sClient.Create(ctx, sr); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, sr) })
	key := client.ObjectKeyFromObject(sr)

	fetch := func() *storagev1alpha1.StorageRequest {
		t.Helper()
		got := &storagev1alpha1.StorageRequest{}
		if err := k8sClient.Get(ctx, key, got); err != nil {
			t.Fatalf("get: %v", err)
		}
		return got
	}
	wantImmutable := func(what string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: expected rejection, got nil", what)
			return
		}
		if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "status.pvcUID is immutable once set") {
			t.Errorf("%s: got %v, want the pvcUID immutability error", what, err)
		}
	}

	// A status write before the UID is known, then the first UID write.
	obj := fetch()
	obj.Status.PVCName = sr.Name
	if err := k8sClient.Status().Update(ctx, obj); err != nil {
		t.Fatalf("status update without pvcUID: %v", err)
	}
	obj = fetch()
	obj.Status.PVCUID = "uid-a"
	if err := k8sClient.Status().Update(ctx, obj); err != nil {
		t.Fatalf("first pvcUID write: %v", err)
	}

	obj = fetch()
	obj.Status.PVCUID = "uid-b"
	wantImmutable("change", k8sClient.Status().Update(ctx, obj))

	obj = fetch()
	obj.Status.PVCUID = ""
	wantImmutable("clear", k8sClient.Status().Update(ctx, obj))

	raw := &unstructured.Unstructured{}
	raw.SetGroupVersionKind(storagev1alpha1.GroupVersion.WithKind("StorageRequest"))
	if err := k8sClient.Get(ctx, key, raw); err != nil {
		t.Fatalf("get unstructured: %v", err)
	}
	unstructured.RemoveNestedField(raw.Object, "status")
	wantImmutable("remove status", k8sClient.Status().Update(ctx, raw))

	// Other status fields and the spec stay writable.
	obj = fetch()
	obj.Status.PVCPhase = corev1.ClaimBound
	if err := k8sClient.Status().Update(ctx, obj); err != nil {
		t.Errorf("status update keeping pvcUID: %v", err)
	}
	obj = fetch()
	obj.Spec.Storage.Size = resource.MustParse("30Gi")
	if err := k8sClient.Update(ctx, obj); err != nil {
		t.Errorf("spec update after pvcUID set: %v", err)
	}
	if got := fetch().Status.PVCUID; got != "uid-a" {
		t.Errorf("pvcUID after updates: got %q, want uid-a", got)
	}
}
