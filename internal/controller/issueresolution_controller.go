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
	"encoding/json"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentv1alpha1 "github.com/hashicorp-academy/hal-k8s-operator/api/v1alpha1"
)

const (
	pendingApprovalRequeue = 30 * time.Second
	jobPollRequeue         = 10 * time.Second

	labelIssueResolution = "hal.dev/issueresolution"
	labelJobRole         = "hal.dev/job-role"
	jobRoleTriage        = "triage"

	defaultTriageImage      = "hal-k8s-operator:poc"
	defaultClaudeSecretName = "claude-api"
	defaultClaudeSecretKey  = "ANTHROPIC_API_KEY"
	defaultClaudeModel      = "claude-haiku-4-5-20251001"
)

// IssueResolutionReconciler reconciles a IssueResolution object.
// POC: creates a triage Job that calls Claude; result is in Job logs (+ termination-log).
type IssueResolutionReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// TriageImage is the container image for Job 1 (same image as operator in KinD POC).
	TriageImage string
	// ClaudeSecretName holds the Anthropic API key (key ANTHROPIC_API_KEY by default).
	ClaudeSecretName string
	// ClaudeSecretKey is the key inside the Secret.
	ClaudeSecretKey string
	// ClaudeModel is passed to the triage Job as ANTHROPIC_MODEL.
	ClaudeModel string
}

type triageJobResult struct {
	InScope    bool   `json:"inScope"`
	Suspicious bool   `json:"suspicious"`
	Summary    string `json:"summary"`
	Model      string `json:"model"`
}

// +kubebuilder:rbac:groups=agent.hal.dev,resources=issueresolutions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agent.hal.dev,resources=issueresolutions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agent.hal.dev,resources=issueresolutions/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

func (r *IssueResolutionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var ir agentv1alpha1.IssueResolution
	if err := r.Get(ctx, req.NamespacedName, &ir); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	orig := ir.DeepCopy()
	phase := ir.Status.Phase
	if phase == "" {
		phase = agentv1alpha1.PhaseTriage
		ir.Status.Phase = phase
	}

	var result ctrl.Result
	var err error

	switch phase {
	case agentv1alpha1.PhaseTriage:
		result, err = r.reconcileTriage(ctx, &ir)
	case agentv1alpha1.PhasePendingValidation:
		result, err = r.reconcilePendingValidation(ctx, &ir)
	case agentv1alpha1.PhaseReady, agentv1alpha1.PhaseExecuting,
		agentv1alpha1.PhasePROpen, agentv1alpha1.PhaseDone:
		// Job 2 / PR path not in this POC — hold.
		ir.Status.Message = "POC stops after triage; Job 2 not wired"
		result = ctrl.Result{}
	case agentv1alpha1.PhaseRejected, agentv1alpha1.PhaseFailed:
		result = ctrl.Result{}
	default:
		log.Info("unknown phase, resetting to Triage", "phase", phase)
		ir.Status.Phase = agentv1alpha1.PhaseTriage
		result = ctrl.Result{Requeue: true}
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	ir.Status.ObservedGeneration = ir.Generation
	if err := r.Status().Patch(ctx, &ir, client.MergeFrom(orig)); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("reconciled", "phase", ir.Status.Phase, "approved", ir.Spec.Approved)
	return result, nil
}

func (r *IssueResolutionReconciler) reconcileTriage(ctx context.Context, ir *agentv1alpha1.IssueResolution) (ctrl.Result, error) {
	jobName := triageJobName(ir)
	var job batchv1.Job
	err := r.Get(ctx, client.ObjectKey{Namespace: ir.Namespace, Name: jobName}, &job)
	if apierrors.IsNotFound(err) {
		job, err := r.buildTriageJob(ir)
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, job); err != nil {
			return ctrl.Result{}, err
		}
		ir.Status.Message = fmt.Sprintf("Triage Job %s created — watch logs with: kubectl logs job/%s -n %s", jobName, jobName, ir.Namespace)
		meta.SetStatusCondition(&ir.Status.Conditions, metav1.Condition{
			Type:               agentv1alpha1.ConditionTriaged,
			Status:             metav1.ConditionFalse,
			Reason:             "JobCreated",
			Message:            ir.Status.Message,
			ObservedGeneration: ir.Generation,
		})
		return ctrl.Result{RequeueAfter: jobPollRequeue}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	if job.Status.Succeeded > 0 {
		tr, termErr := r.readTriageResult(ctx, ir, &job)
		if termErr != nil {
			ir.Status.Message = fmt.Sprintf("Triage Job succeeded but could not read result: %v (see job logs)", termErr)
			ir.Status.Triage = agentv1alpha1.TriageStatus{
				Summary: ir.Status.Message,
				Model:   r.claudeModel(),
			}
		} else {
			ir.Status.Triage = agentv1alpha1.TriageStatus{
				InScope:    tr.InScope,
				Suspicious: tr.Suspicious,
				Summary:    tr.Summary,
				Model:      tr.Model,
			}
		}

		if ir.Status.Triage.Suspicious || !ir.Status.Triage.InScope {
			ir.Status.Phase = agentv1alpha1.PhaseRejected
			ir.Status.Message = "Rejected by triage — see Job logs for Claude analysis"
			meta.SetStatusCondition(&ir.Status.Conditions, metav1.Condition{
				Type:               agentv1alpha1.ConditionTriaged,
				Status:             metav1.ConditionTrue,
				Reason:             "Rejected",
				Message:            ir.Status.Triage.Summary,
				ObservedGeneration: ir.Generation,
			})
			return ctrl.Result{}, nil
		}

		ir.Status.Phase = agentv1alpha1.PhasePendingValidation
		ir.Status.Message = fmt.Sprintf("Triage OK. Claude summary in status + Job logs (kubectl logs job/%s -n %s). Waiting for spec.approved=true", jobName, ir.Namespace)
		meta.SetStatusCondition(&ir.Status.Conditions, metav1.Condition{
			Type:               agentv1alpha1.ConditionTriaged,
			Status:             metav1.ConditionTrue,
			Reason:             "InScope",
			Message:            ir.Status.Triage.Summary,
			ObservedGeneration: ir.Generation,
		})
		meta.SetStatusCondition(&ir.Status.Conditions, metav1.Condition{
			Type:               agentv1alpha1.ConditionAwaitingApproval,
			Status:             metav1.ConditionTrue,
			Reason:             "PendingAgentGo",
			Message:            "Set spec.approved=true to continue (Job 2 not in POC)",
			ObservedGeneration: ir.Generation,
		})
		return ctrl.Result{RequeueAfter: pendingApprovalRequeue}, nil
	}

	if job.Status.Failed > 0 {
		ir.Status.Phase = agentv1alpha1.PhaseFailed
		ir.Status.Message = fmt.Sprintf("Triage Job %s failed — kubectl logs job/%s -n %s", jobName, jobName, ir.Namespace)
		meta.SetStatusCondition(&ir.Status.Conditions, metav1.Condition{
			Type:               agentv1alpha1.ConditionFailed,
			Status:             metav1.ConditionTrue,
			Reason:             "TriageJobFailed",
			Message:            ir.Status.Message,
			ObservedGeneration: ir.Generation,
		})
		return ctrl.Result{}, nil
	}

	ir.Status.Message = fmt.Sprintf("Triage Job %s running…", jobName)
	return ctrl.Result{RequeueAfter: jobPollRequeue}, nil
}

func (r *IssueResolutionReconciler) reconcilePendingValidation(_ context.Context, ir *agentv1alpha1.IssueResolution) (ctrl.Result, error) {
	if !ir.Spec.Approved {
		ir.Status.Message = "Waiting for spec.approved=true (POC: kubectl patch …)"
		return ctrl.Result{RequeueAfter: pendingApprovalRequeue}, nil
	}
	ir.Status.Phase = agentv1alpha1.PhaseReady
	ir.Status.Message = "Approved — POC ends here (no Job 2 yet)"
	meta.SetStatusCondition(&ir.Status.Conditions, metav1.Condition{
		Type:               agentv1alpha1.ConditionAwaitingApproval,
		Status:             metav1.ConditionFalse,
		Reason:             "Approved",
		Message:            ir.Status.Message,
		ObservedGeneration: ir.Generation,
	})
	return ctrl.Result{}, nil
}

func (r *IssueResolutionReconciler) buildTriageJob(ir *agentv1alpha1.IssueResolution) (*batchv1.Job, error) {
	image := r.triageImage()
	secretName := r.claudeSecretName()
	secretKey := r.claudeSecretKey()
	model := r.claudeModel()
	jobName := triageJobName(ir)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: ir.Namespace,
			Labels: map[string]string{
				labelIssueResolution: ir.Name,
				labelJobRole:         jobRoleTriage,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(int32(1)),
			TTLSecondsAfterFinished: ptr.To(int32(3600)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						labelIssueResolution: ir.Name,
						labelJobRole:         jobRoleTriage,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "triage",
							Image:   image,
							Command: []string{"/triage"},
							Env: []corev1.EnvVar{
								{Name: "ISSUE_REPOSITORY", Value: ir.Spec.Repository},
								{Name: "ISSUE_NUMBER", Value: fmt.Sprintf("%d", ir.Spec.IssueNumber)},
								{Name: "ISSUE_AUTHOR", Value: ir.Spec.Author},
								{Name: "ISSUE_TITLE", Value: ir.Spec.Title},
								{Name: "ISSUE_BODY", Value: ir.Spec.Body},
								{Name: "ANTHROPIC_MODEL", Value: model},
								{
									Name: "ANTHROPIC_API_KEY",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
											Key:                  secretKey,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(ir, job, r.Scheme); err != nil {
		return nil, err
	}
	return job, nil
}

func (r *IssueResolutionReconciler) readTriageResult(ctx context.Context, ir *agentv1alpha1.IssueResolution, job *batchv1.Job) (triageJobResult, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(ir.Namespace),
		client.MatchingLabels{
			labelIssueResolution: ir.Name,
			labelJobRole:         jobRoleTriage,
		},
	); err != nil {
		return triageJobResult{}, err
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Terminated != nil && cs.State.Terminated.Message != "" {
				var tr triageJobResult
				if err := json.Unmarshal([]byte(cs.State.Terminated.Message), &tr); err != nil {
					return triageJobResult{}, fmt.Errorf("parse termination message: %w", err)
				}
				if tr.Model == "" {
					tr.Model = r.claudeModel()
				}
				return tr, nil
			}
		}
	}

	return triageJobResult{}, fmt.Errorf("no termination message on pods of job %s (check kubectl logs job/%s)", job.Name, job.Name)
}

func triageJobName(ir *agentv1alpha1.IssueResolution) string {
	return fmt.Sprintf("%s-triage", ir.Name)
}

func (r *IssueResolutionReconciler) triageImage() string {
	if r.TriageImage != "" {
		return r.TriageImage
	}
	return defaultTriageImage
}

func (r *IssueResolutionReconciler) claudeSecretName() string {
	if r.ClaudeSecretName != "" {
		return r.ClaudeSecretName
	}
	return defaultClaudeSecretName
}

func (r *IssueResolutionReconciler) claudeSecretKey() string {
	if r.ClaudeSecretKey != "" {
		return r.ClaudeSecretKey
	}
	return defaultClaudeSecretKey
}

func (r *IssueResolutionReconciler) claudeModel() string {
	if r.ClaudeModel != "" {
		return r.ClaudeModel
	}
	return defaultClaudeModel
}

// SetupWithManager sets up the controller with the Manager.
func (r *IssueResolutionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.IssueResolution{}).
		Owns(&batchv1.Job{}).
		Named("issueresolution").
		Complete(r)
}
