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

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"

	storagev1alpha1 "github.com/mohamadar77/storage-request-operator/api/v1alpha1"
)

var _ = Describe("PVC watch predicate and mapping", func() {
	pvc := func(labels, annotations map[string]string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
			Namespace: "demo", Name: requestName, Labels: labels, Annotations: annotations,
		}}
	}
	managedLabel := map[string]string{storagev1alpha1.LabelManaged: storagev1alpha1.ManagedValue}
	requestAnnotation := map[string]string{storagev1alpha1.AnnotationRequestUID: "uid-1"}
	unmarked := pvc(map[string]string{"app": "x"}, nil)

	p := managedPVCPredicate()

	DescribeTable("create, delete, and generic events pass only for marked PVCs",
		func(obj *corev1.PersistentVolumeClaim, want bool) {
			Expect(p.Create(event.CreateEvent{Object: obj})).To(Equal(want))
			Expect(p.Delete(event.DeleteEvent{Object: obj})).To(Equal(want))
			Expect(p.Generic(event.GenericEvent{Object: obj})).To(Equal(want))
		},
		Entry("managed label", pvc(managedLabel, nil), true),
		Entry("request UID annotation only (label removed)", pvc(nil, requestAnnotation), true),
		Entry("both markers", pvc(managedLabel, requestAnnotation), true),
		Entry("managed label with another value", pvc(map[string]string{storagev1alpha1.LabelManaged: "false"}, nil), false),
		Entry("no markers", unmarked, false),
	)

	It("passes an update when either the old or the new PVC is marked", func() {
		marked := pvc(managedLabel, requestAnnotation)
		Expect(p.Update(event.UpdateEvent{ObjectOld: marked, ObjectNew: marked})).To(BeTrue())
		Expect(p.Update(event.UpdateEvent{ObjectOld: marked, ObjectNew: unmarked})).To(BeTrue(),
			"removing both markers must still reconcile the request")
		Expect(p.Update(event.UpdateEvent{ObjectOld: unmarked, ObjectNew: marked})).To(BeTrue())
		Expect(p.Update(event.UpdateEvent{ObjectOld: unmarked, ObjectNew: unmarked})).To(BeFalse())
	})

	It("passes a deletion-timestamp update of a marked PVC", func() {
		old := pvc(managedLabel, requestAnnotation)
		terminating := old.DeepCopy()
		terminating.DeletionTimestamp = new(metav1.Now())
		Expect(p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: terminating})).To(BeTrue())
	})

	It("maps a PVC to the same-namespace, same-name request, whatever its annotation says", func() {
		obj := pvc(nil, map[string]string{storagev1alpha1.AnnotationRequestUID: "another-request"})
		requests := requestForPVC(ctx, obj)
		Expect(requests).To(HaveLen(1))
		Expect(requests[0].NamespacedName).To(Equal(types.NamespacedName{Namespace: "demo", Name: requestName}))
	})
})
