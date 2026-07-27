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
	"k8s.io/apimachinery/pkg/api/meta"
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
			triageImagePOC    = "hal-k8s-operator:poc"
			fixImagePOC       = "hal-k8s-operator-fix:poc"
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

			// Clean up fix Jobs (attempt 1 and 2 cover the retry case).
			for _, suffix := range []string{"-fix-1", "-fix-2"} {
				fixJob := &batchv1.Job{}
				fixKey := types.NamespacedName{Name: resourceName + suffix, Namespace: resourceNamespace}
				if err := k8sClient.Get(ctx, fixKey, fixJob); err == nil {
					_ = k8sClient.Delete(ctx, fixJob, client.PropagationPolicy(metav1.DeletePropagationBackground))
				}
			}

			var pods corev1.PodList
			_ = k8sClient.List(ctx, &pods, client.InNamespace(resourceNamespace), client.MatchingLabels{
				labelIssueResolution: resourceName,
			})
			for i := range pods.Items {
				_ = k8sClient.Delete(ctx, &pods.Items[i])
			}
		})

		newReconciler := func() *IssueResolutionReconciler {
			return &IssueResolutionReconciler{
				Client:           k8sClient,
				Scheme:           k8sClient.Scheme(),
				TriageImage:      triageImagePOC,
				FixImage:         fixImagePOC,
				GeminiSecretName: "gemini-api",
				GitHubSecretName: "github-pat",
			}
		}

		createSucceededJobWithMessage := func(message string) {
			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			job := &batchv1.Job{}
			jobKey := types.NamespacedName{Name: resourceName + "-triage", Namespace: resourceNamespace}
			Expect(k8sClient.Get(ctx, jobKey, job)).To(Succeed())
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-triage-ok",
					Namespace: resourceNamespace,
					Labels: map[string]string{
						labelIssueResolution:  resourceName,
						labelJobRole:          jobRoleTriage,
						labelJobControllerUID: string(job.UID),
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: jobRoleTriage, Image: triageImagePOC}},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name: jobRoleTriage,
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 0,
						Message:  message,
					},
				},
			}}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		}

		It("should create a triage Job", func() {
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			job := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName + "-triage",
				Namespace: resourceNamespace,
			}, job)).To(Succeed())
			Expect(job.Spec.Template.Spec.Containers[0].Command).To(Equal([]string{"/triage"}))
			Expect(job.Spec.Template.Spec.Containers[0].Env).To(ContainElement(HaveField("Name", "GEMINI_API_KEY")))

			updated := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(agentv1alpha1.PhaseTriage))
		})

		It("should move to PendingValidation when triage Job succeeds with in-scope result", func() {
			createSucceededJobWithMessage(`{"inScope":true,"suspicious":false,"summary":"doc fix ok","model":"test"}`)

			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(agentv1alpha1.PhasePendingValidation))
			Expect(updated.Status.Triage.InScope).To(BeTrue())
			Expect(updated.Status.Triage.Summary).To(Equal("doc fix ok"))
		})

		It("should move to Rejected when triage says out of scope", func() {
			createSucceededJobWithMessage(`{"inScope":false,"suspicious":false,"summary":"needs Multipass","model":"test"}`)

			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(agentv1alpha1.PhaseRejected))
			cond := meta.FindStatusCondition(updated.Status.Conditions, agentv1alpha1.ConditionTriaged)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal("Rejected"))
		})

		It("should move to Failed when Job fails", func() {
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			job := &batchv1.Job{}
			jobKey := types.NamespacedName{Name: resourceName + "-triage", Namespace: resourceNamespace}
			Expect(k8sClient.Get(ctx, jobKey, job)).To(Succeed())
			job.Status.Failed = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			_, err = newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(agentv1alpha1.PhaseFailed))
			cond := meta.FindStatusCondition(updated.Status.Conditions, agentv1alpha1.ConditionFailed)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})

		It("should move to Failed when termination message has parseError", func() {
			createSucceededJobWithMessage(`{"inScope":false,"suspicious":false,"summary":"bad json","model":"test","parseError":true}`)

			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(agentv1alpha1.PhaseFailed))
			Expect(updated.Status.Triage.ParseError).To(BeTrue())
			cond := meta.FindStatusCondition(updated.Status.Conditions, agentv1alpha1.ConditionFailed)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal("TriageResultUnreadable"))
		})

		It("should prefer the successful pod when a failed attempt also has a termination message", func() {
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			job := &batchv1.Job{}
			jobKey := types.NamespacedName{Name: resourceName + "-triage", Namespace: resourceNamespace}
			Expect(k8sClient.Get(ctx, jobKey, job)).To(Succeed())
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			failedPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-triage-fail",
					Namespace: resourceNamespace,
					Labels: map[string]string{
						labelIssueResolution:  resourceName,
						labelJobRole:          jobRoleTriage,
						labelJobControllerUID: string(job.UID),
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: jobRoleTriage, Image: triageImagePOC}},
				},
			}
			Expect(k8sClient.Create(ctx, failedPod)).To(Succeed())
			failedPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name: jobRoleTriage,
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 1,
						Message:  `{"inScope":false,"suspicious":false,"summary":"from failed pod","model":"test"}`,
					},
				},
			}}
			Expect(k8sClient.Status().Update(ctx, failedPod)).To(Succeed())

			okPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-triage-ok2",
					Namespace: resourceNamespace,
					Labels: map[string]string{
						labelIssueResolution:  resourceName,
						labelJobRole:          jobRoleTriage,
						labelJobControllerUID: string(job.UID),
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: jobRoleTriage, Image: triageImagePOC}},
				},
			}
			Expect(k8sClient.Create(ctx, okPod)).To(Succeed())
			okPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name: jobRoleTriage,
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 0,
						Message:  `{"inScope":true,"suspicious":false,"summary":"from success pod","model":"test"}`,
					},
				},
			}}
			Expect(k8sClient.Status().Update(ctx, okPod)).To(Succeed())

			_, err = newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(agentv1alpha1.PhasePendingValidation))
			Expect(updated.Status.Triage.Summary).To(Equal("from success pod"))
		})

		It("should move to Ready when approved while PendingValidation", func() {
			createSucceededJobWithMessage(`{"inScope":true,"suspicious":false,"summary":"ok","model":"test"}`)
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			current := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, current)).To(Succeed())
			Expect(current.Status.Phase).To(Equal(agentv1alpha1.PhasePendingValidation))
			current.Spec.Approved = true
			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			_, err = newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(agentv1alpha1.PhaseReady))
		})

		bringToReady := func() {
			createSucceededJobWithMessage(`{"inScope":true,"suspicious":false,"summary":"ok","model":"test"}`)
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			current := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, current)).To(Succeed())
			current.Spec.Approved = true
			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			_, err = newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(agentv1alpha1.PhaseReady))
		}

		createSucceededFixJob := func(message string) {
			job := &batchv1.Job{}
			jobKey := types.NamespacedName{Name: resourceName + "-fix-1", Namespace: resourceNamespace}
			Expect(k8sClient.Get(ctx, jobKey, job)).To(Succeed())
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-fix-ok",
					Namespace: resourceNamespace,
					Labels: map[string]string{
						labelIssueResolution:  resourceName,
						labelJobRole:          jobRoleFix,
						labelJobControllerUID: string(job.UID),
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: jobRoleFix, Image: fixImagePOC}},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name: jobRoleFix,
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 0,
						Message:  message,
					},
				},
			}}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		}

		It("should create a fix Job with OwnerRef when Ready", func() {
			bringToReady()

			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			job := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName + "-fix-1",
				Namespace: resourceNamespace,
			}, job)).To(Succeed())
			Expect(job.Spec.Template.Spec.Containers[0].Command).To(Equal([]string{"/fix"}))
			Expect(job.Spec.Template.Spec.Containers[0].Env).To(ContainElement(HaveField("Name", "GITHUB_TOKEN")))
			Expect(job.Spec.Template.Spec.Containers[0].Env).To(ContainElement(HaveField("Name", "GEMINI_API_KEY")))

			ir := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, ir)).To(Succeed())
			Expect(job.OwnerReferences).To(ContainElement(SatisfyAll(
				HaveField("UID", ir.UID),
				HaveField("Controller", HaveValue(BeTrue())),
			)))

			Expect(ir.Status.Phase).To(Equal(agentv1alpha1.PhaseExecuting))
			Expect(ir.Status.Execution.JobName).To(Equal(resourceName + "-fix-1"))
			Expect(ir.Status.Execution.Attempt).To(Equal(int32(1)))
		})

		It("should move to PROpen when fix Job succeeds", func() {
			bringToReady()
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			createSucceededFixJob(`{"prURL":"https://github.com/owner/hal/pull/7","prNumber":7,"branch":"bugfix/issue-1234-attempt-1","attempt":1}`)

			_, err = newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(agentv1alpha1.PhasePROpen))
			Expect(updated.Status.Execution.PRURL).To(Equal("https://github.com/owner/hal/pull/7"))
			Expect(updated.Status.Execution.PRNumber).To(Equal(int32(7)))
			cond := meta.FindStatusCondition(updated.Status.Conditions, agentv1alpha1.ConditionPROpen)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})

		It("should move to Failed when fix Job fails beyond maxFixAttempts", func() {
			bringToReady()

			current := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, current)).To(Succeed())
			maxAttempts := int32(1)
			current.Spec.MaxFixAttempts = &maxAttempts
			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			job := &batchv1.Job{}
			jobKey := types.NamespacedName{Name: resourceName + "-fix-1", Namespace: resourceNamespace}
			Expect(k8sClient.Get(ctx, jobKey, job)).To(Succeed())
			job.Status.Failed = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			_, err = newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(agentv1alpha1.PhaseFailed))
			cond := meta.FindStatusCondition(updated.Status.Conditions, agentv1alpha1.ConditionFailed)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal("FixAttemptsExhausted"))
		})

		It("should retry with a new fix Job when attempt < maxFixAttempts", func() {
			bringToReady()

			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			job := &batchv1.Job{}
			jobKey := types.NamespacedName{Name: resourceName + "-fix-1", Namespace: resourceNamespace}
			Expect(k8sClient.Get(ctx, jobKey, job)).To(Succeed())
			job.Status.Failed = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			_, err = newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.IssueResolution{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(agentv1alpha1.PhaseReady))
			Expect(updated.Status.Execution.Attempt).To(Equal(int32(2)))

			_, err = newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			job2 := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName + "-fix-2",
				Namespace: resourceNamespace,
			}, job2)).To(Succeed())

			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(agentv1alpha1.PhaseExecuting))
			Expect(updated.Status.Execution.JobName).To(Equal(resourceName + "-fix-2"))
		})
	})
})
