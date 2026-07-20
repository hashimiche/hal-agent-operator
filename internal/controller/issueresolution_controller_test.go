/*
Copyright 2026 HAL.

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
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/hashicorp-academy/hal-k8s-operator/api/v1alpha1"
)

var _ = Describe("IssueResolution Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName      = "issue-1234"
			resourceNamespace = "default"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: resourceNamespace,
		}

		BeforeEach(func() {
			By("creating the custom resource for the Kind IssueResolution")
			issueresolution := &agentv1alpha1.IssueResolution{}
			err := k8sClient.Get(ctx, typeNamespacedName, issueresolution)
			if err != nil && errors.IsNotFound(err) {
				resource := &agentv1alpha1.IssueResolution{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: resourceNamespace,
					},
					Spec: agentv1alpha1.IssueResolutionSpec{
						Repository:  "owner/hal",
						IssueNumber: 1234,
						Author:      "alice",
						Title:       "docs: typo in vault oidc skill",
						Body:        "Please fix the wording.",
						Approved:    false,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &agentv1alpha1.IssueResolution{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			job := &batchv1.Job{}
			jobKey := types.NamespacedName{Name: resourceName + "-triage", Namespace: resourceNamespace}
			if err := k8sClient.Get(ctx, jobKey, job); err == nil {
				_ = k8sClient.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
			}
		})

		It("should create a triage Job", func() {
			reconciler := &IssueResolutionReconciler{
				Client:           k8sClient,
				Scheme:           k8sClient.Scheme(),
				TriageImage:      "hal-k8s-operator:poc",
				ClaudeSecretName: "claude-api",
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			job := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName + "-triage",
				Namespace: resourceNamespace,
			}, job)).To(Succeed())
			Expect(job.Spec.Template.Spec.Containers[0].Command).To(Equal([]string{"/triage"}))

			updated := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(agentv1alpha1.PhaseTriage))
		})

		It("should move to PendingValidation when triage Job succeeds with in-scope result", func() {
			reconciler := &IssueResolutionReconciler{
				Client:      k8sClient,
				Scheme:      k8sClient.Scheme(),
				TriageImage: "hal-k8s-operator:poc",
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			job := &batchv1.Job{}
			jobKey := types.NamespacedName{Name: resourceName + "-triage", Namespace: resourceNamespace}
			Expect(k8sClient.Get(ctx, jobKey, job)).To(Succeed())

			By("simulating a succeeded Job + pod termination message")
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-triage-pod",
					Namespace: resourceNamespace,
					Labels: map[string]string{
						labelIssueResolution: resourceName,
						labelJobRole:         jobRoleTriage,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "triage", Image: "hal-k8s-operator:poc"}},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name: "triage",
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 0,
						Message:  `{"inScope":true,"suspicious":false,"summary":"doc fix ok","model":"test"}`,
					},
				},
			}}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(agentv1alpha1.PhasePendingValidation))
			Expect(updated.Status.Triage.InScope).To(BeTrue())
			Expect(updated.Status.Triage.Summary).To(Equal("doc fix ok"))

			Expect(k8sClient.Delete(ctx, pod)).To(Succeed())
		})
	})
})
