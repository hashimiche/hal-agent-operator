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
	"github.com/hashicorp-academy/hal-k8s-operator/internal/defaults"
)

const (
	pendingApprovalRequeue = 30 * time.Second
	jobPollRequeue         = 10 * time.Second

	labelIssueResolution  = "hal.dev/issueresolution"
	labelJobRole          = "hal.dev/job-role"
	jobRoleTriage         = "triage"
	jobRoleFix            = "fix"
	defaultMaxFixAttempts = int32(2) // fallback if spec.MaxFixAttempts == nil
	fixWorkDir            = "/workspace"

	// labelJobControllerUID is the label the Job controller sets on Pods it owns
	// (batch.kubernetes.io/controller-uid). Filtering on it scopes readTriageResult
	// to pods of the current Job, not a stale/recreated Job that shared the same name.
	labelJobControllerUID = "batch.kubernetes.io/controller-uid"
)

// IssueResolutionReconciler reconciles a IssueResolution object.
// POC: creates triage/fix Jobs that call Gemini; results are in Job logs (+ termination-log).
type IssueResolutionReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// TriageImage is the container image for Job 1 (same image as operator in KinD POC).
	TriageImage string
	// FixImage is the container image for Job 2 (needs Go toolchain; separate from distroless).
	FixImage string
	// GeminiSecretName holds the Gemini API key.
	GeminiSecretName string
	// GeminiSecretKey is the key inside the Secret.
	GeminiSecretKey string
	// GeminiModel is passed to triage/fix Jobs as GEMINI_MODEL.
	GeminiModel string
	// GitHubTriageSecretName holds the GitHub token Secret for Job 1 (comment/labels).
	GitHubTriageSecretName string
	// GitHubFixSecretName holds the GitHub token Secret for Job 2 (clone/push/PR).
	GitHubFixSecretName string
	// GitHubSecretKey is the key inside the GitHub Secrets.
	GitHubSecretKey string
	// JobTriageServiceAccount is the SA name for triage Job pods.
	JobTriageServiceAccount string
	// JobFixServiceAccount is the SA name for fix Job pods.
	JobFixServiceAccount string
	// JobRuntimeClassName is applied to triage/fix Job pod templates when non-empty.
	JobRuntimeClassName string
}

type triageJobResult struct {
	InScope    bool   `json:"inScope"`
	Suspicious bool   `json:"suspicious"`
	Summary    string `json:"summary"`
	Model      string `json:"model"`
	ParseError bool   `json:"parseError,omitempty"`
	CommentURL string `json:"commentURL,omitempty"`
}

// fixJobResult mirrors what /fix writes to the termination-log.
type fixJobResult struct {
	PRURL    string `json:"prURL"`
	PRNumber int32  `json:"prNumber"`
	Branch   string `json:"branch"`
	Attempt  int32  `json:"attempt"`
	Error    string `json:"error,omitempty"`
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
	case agentv1alpha1.PhaseReady, agentv1alpha1.PhaseExecuting, agentv1alpha1.PhasePROpen:
		result, err = r.reconcileFix(ctx, &ir)
	case agentv1alpha1.PhaseDone:
		result = ctrl.Result{} // terminal, no-op
	case agentv1alpha1.PhaseRejected, agentv1alpha1.PhaseFailed:
		result = ctrl.Result{}
	default:
		log.Info("unknown phase, resetting to Triage", "phase", phase)
		ir.Status.Phase = agentv1alpha1.PhaseTriage
		result = ctrl.Result{RequeueAfter: time.Second}
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
		if termErr != nil || tr.ParseError {
			var msg string
			if termErr != nil {
				msg = fmt.Sprintf("Triage Job succeeded but could not read result: %v (see job logs)", termErr)
			} else {
				msg = "Triage Job succeeded but model output was not valid JSON (see job logs)"
			}
			ir.Status.Phase = agentv1alpha1.PhaseFailed
			ir.Status.Message = msg
			ir.Status.Triage = agentv1alpha1.TriageStatus{
				Summary:    msg,
				Model:      firstNonEmpty(tr.Model, r.geminiModel()),
				ParseError: true,
			}
			meta.SetStatusCondition(&ir.Status.Conditions, metav1.Condition{
				Type:               agentv1alpha1.ConditionFailed,
				Status:             metav1.ConditionTrue,
				Reason:             "TriageResultUnreadable",
				Message:            msg,
				ObservedGeneration: ir.Generation,
			})
			return ctrl.Result{}, nil
		}

		ir.Status.Triage = agentv1alpha1.TriageStatus{
			InScope:    tr.InScope,
			Suspicious: tr.Suspicious,
			Summary:    tr.Summary,
			Model:      tr.Model,
		}
		ir.Status.Plan = agentv1alpha1.PlanStatus{
			CommentURL: tr.CommentURL,
			Summary:    tr.Summary,
		}

		if ir.Status.Triage.Suspicious || !ir.Status.Triage.InScope {
			ir.Status.Phase = agentv1alpha1.PhaseRejected
			ir.Status.Message = "Rejected by triage — see GitHub comment / Job logs"
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
		if tr.CommentURL != "" {
			ir.Status.Message = fmt.Sprintf("Triage OK. Plan comment: %s — waiting for spec.approved=true", tr.CommentURL)
		} else {
			ir.Status.Message = fmt.Sprintf("Triage OK. Gemini summary in status + Job logs (kubectl logs job/%s -n %s). Waiting for spec.approved=true", jobName, ir.Namespace)
		}
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
			Message:            "Set spec.approved=true to continue",
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

// reconcilePendingValidation advances PendingValidation → Ready when approved.
// error is part of the shared reconcile helper signature (always nil here).
func (r *IssueResolutionReconciler) reconcilePendingValidation(
	_ context.Context,
	ir *agentv1alpha1.IssueResolution,
) (ctrl.Result, error) { //nolint:unparam // error kept for reconcile helper consistency
	if !ir.Spec.Approved {
		ir.Status.Message = "Waiting for spec.approved=true (POC: kubectl patch …)"
		return ctrl.Result{RequeueAfter: pendingApprovalRequeue}, nil
	}
	ir.Status.Phase = agentv1alpha1.PhaseReady
	ir.Status.Message = "Approved — ready for fix Job"
	meta.SetStatusCondition(&ir.Status.Conditions, metav1.Condition{
		Type:               agentv1alpha1.ConditionAwaitingApproval,
		Status:             metav1.ConditionFalse,
		Reason:             "Approved",
		Message:            ir.Status.Message,
		ObservedGeneration: ir.Generation,
	})
	// Requeue so reconcileFix runs deterministically on the next pass (cluster).
	return ctrl.Result{RequeueAfter: time.Second}, nil
}

func (r *IssueResolutionReconciler) reconcileFix(ctx context.Context, ir *agentv1alpha1.IssueResolution) (ctrl.Result, error) {
	// Terminal: PR already open — strong idempotence, never relaunch.
	if ir.Status.Phase == agentv1alpha1.PhasePROpen {
		return ctrl.Result{}, nil
	}

	attempt := ir.Status.Execution.Attempt
	if attempt == 0 {
		attempt = 1
	}
	maxAttempts := defaultMaxFixAttempts
	if ir.Spec.MaxFixAttempts != nil {
		maxAttempts = *ir.Spec.MaxFixAttempts
	}
	jobName := fixJobName(ir, attempt)
	branch := fixBranchName(ir, attempt)

	var job batchv1.Job
	err := r.Get(ctx, client.ObjectKey{Namespace: ir.Namespace, Name: jobName}, &job)

	if apierrors.IsNotFound(err) {
		// Only create from Ready (guard against recreating after TTL cleanup of a finished Job).
		if ir.Status.Phase != agentv1alpha1.PhaseReady && ir.Status.Execution.JobName != "" {
			return ctrl.Result{}, nil
		}
		newJob, buildErr := r.buildFixJob(ir, attempt, branch)
		if buildErr != nil {
			return ctrl.Result{}, buildErr
		}
		if createErr := r.Create(ctx, newJob); createErr != nil {
			return ctrl.Result{}, createErr
		}

		ir.Status.Phase = agentv1alpha1.PhaseExecuting
		ir.Status.Execution.Attempt = attempt
		ir.Status.Execution.JobName = jobName
		ir.Status.Execution.Branch = branch
		ir.Status.Message = fmt.Sprintf("Fix Job %s created (attempt %d/%d)", jobName, attempt, maxAttempts)
		meta.SetStatusCondition(&ir.Status.Conditions, metav1.Condition{
			Type:               agentv1alpha1.ConditionExecuting,
			Status:             metav1.ConditionTrue,
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
		fr, termErr := r.readFixResult(ctx, ir, &job)
		if termErr != nil || fr.PRURL == "" {
			return r.handleFixFailure(ir, attempt, maxAttempts,
				fmt.Sprintf("Fix Job succeeded but result unreadable: %v", termErr))
		}
		ir.Status.Phase = agentv1alpha1.PhasePROpen
		ir.Status.Execution = agentv1alpha1.ExecutionStatus{
			Attempt:  attempt,
			JobName:  jobName,
			Branch:   fr.Branch,
			PRURL:    fr.PRURL,
			PRNumber: fr.PRNumber,
		}
		ir.Status.Message = fmt.Sprintf("PR opened: %s", fr.PRURL)
		meta.SetStatusCondition(&ir.Status.Conditions, metav1.Condition{
			Type:               agentv1alpha1.ConditionPROpen,
			Status:             metav1.ConditionTrue,
			Reason:             "PROpened",
			Message:            fr.PRURL,
			ObservedGeneration: ir.Generation,
		})
		return ctrl.Result{}, nil
	}

	if job.Status.Failed > 0 {
		return r.handleFixFailure(ir, attempt, maxAttempts,
			fmt.Sprintf("Fix Job %s failed", jobName))
	}

	ir.Status.Phase = agentv1alpha1.PhaseExecuting
	ir.Status.Message = fmt.Sprintf("Fix Job %s running… (attempt %d/%d)", jobName, attempt, maxAttempts)
	return ctrl.Result{RequeueAfter: jobPollRequeue}, nil
}

func (r *IssueResolutionReconciler) handleFixFailure(ir *agentv1alpha1.IssueResolution, attempt, maxAttempts int32, msg string) (ctrl.Result, error) {
	if attempt < maxAttempts {
		ir.Status.Execution.Attempt = attempt + 1
		ir.Status.Phase = agentv1alpha1.PhaseReady
		ir.Status.Message = fmt.Sprintf("%s — retrying (attempt %d/%d)", msg, attempt+1, maxAttempts)
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	ir.Status.Phase = agentv1alpha1.PhaseFailed
	ir.Status.Message = fmt.Sprintf("%s — max attempts (%d) exhausted", msg, maxAttempts)
	meta.SetStatusCondition(&ir.Status.Conditions, metav1.Condition{
		Type:               agentv1alpha1.ConditionFailed,
		Status:             metav1.ConditionTrue,
		Reason:             "FixAttemptsExhausted",
		Message:            ir.Status.Message,
		ObservedGeneration: ir.Generation,
	})
	return ctrl.Result{}, nil
}

func (r *IssueResolutionReconciler) buildFixJob(ir *agentv1alpha1.IssueResolution, attempt int32, branch string) (*batchv1.Job, error) {
	image := r.fixImage()
	geminiSecret := r.geminiSecretName()
	geminiKey := r.geminiSecretKey()
	githubSecret := r.githubFixSecretName()
	githubKey := r.githubSecretKey()
	model := r.geminiModel()
	jobName := fixJobName(ir, attempt)

	env := []corev1.EnvVar{
		{Name: "ISSUE_REPOSITORY", Value: ir.Spec.Repository},
		{Name: "ISSUE_NUMBER", Value: fmt.Sprintf("%d", ir.Spec.IssueNumber)},
		{Name: "ISSUE_TITLE", Value: ir.Spec.Title},
		{Name: "ISSUE_BODY", Value: ir.Spec.Body},
		{Name: "TRIAGE_SUMMARY", Value: ir.Status.Triage.Summary},
		{Name: "GEMINI_MODEL", Value: model},
		{
			Name: "GEMINI_API_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: geminiSecret},
					Key:                  geminiKey,
				},
			},
		},
		{
			Name: defaults.GitHubSecretKey,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: githubSecret},
					Key:                  githubKey,
				},
			},
		},
		{Name: "BRANCH_NAME", Value: branch},
		{Name: "FIX_ATTEMPT", Value: fmt.Sprintf("%d", attempt)},
		{Name: "WORKDIR", Value: fixWorkDir},
		{Name: "HOME", Value: fixWorkDir},
		{Name: "GOCACHE", Value: fixWorkDir + "/.cache"},
		{Name: "GOMODCACHE", Value: fixWorkDir + "/gomod"},
		{Name: "GOPATH", Value: fixWorkDir + "/go"},
		{Name: "TMPDIR", Value: fixWorkDir + "/tmp"},
	}
	if ir.Spec.BaseBranch != "" {
		env = append(env, corev1.EnvVar{Name: "BASE_BRANCH", Value: ir.Spec.BaseBranch})
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: ir.Namespace,
			Labels: map[string]string{
				labelIssueResolution: ir.Name,
				labelJobRole:         jobRoleFix,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(int32(0)),
			ActiveDeadlineSeconds:   ptr.To(int64(600)),
			TTLSecondsAfterFinished: ptr.To(int32(3600)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						labelIssueResolution: ir.Name,
						labelJobRole:         jobRoleFix,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					ServiceAccountName:           r.jobFixServiceAccount(),
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptr.To(true),
						RunAsUser:    ptr.To(int64(65532)),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{
						{
							Name:    jobRoleFix,
							Image:   image,
							Command: []string{"/" + jobRoleFix},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr.To(false),
								ReadOnlyRootFilesystem:   ptr.To(true),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},
							Env: env,
							VolumeMounts: []corev1.VolumeMount{
								{Name: "workspace", MountPath: fixWorkDir},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "workspace",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}

	r.applyJobPodRuntimeClass(&job.Spec.Template.Spec)

	if err := controllerutil.SetControllerReference(ir, job, r.Scheme); err != nil {
		return nil, err
	}
	return job, nil
}

func (r *IssueResolutionReconciler) readFixResult(ctx context.Context, ir *agentv1alpha1.IssueResolution, job *batchv1.Job) (fixJobResult, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(ir.Namespace),
		client.MatchingLabels{
			labelIssueResolution:  ir.Name,
			labelJobRole:          jobRoleFix,
			labelJobControllerUID: string(job.UID),
		},
	); err != nil {
		return fixJobResult{}, err
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Terminated == nil {
				continue
			}
			// T1: only trust successful containers (failed attempts also write termination-log).
			if cs.State.Terminated.ExitCode != 0 {
				continue
			}
			if cs.State.Terminated.Message == "" {
				continue
			}
			var fr fixJobResult
			if err := json.Unmarshal([]byte(cs.State.Terminated.Message), &fr); err != nil {
				return fixJobResult{}, fmt.Errorf("parse termination message: %w", err)
			}
			return fr, nil
		}
	}

	return fixJobResult{}, fmt.Errorf("no successful termination message on pods of job %s (check kubectl logs job/%s)", job.Name, job.Name)
}

func fixJobName(ir *agentv1alpha1.IssueResolution, attempt int32) string {
	return fmt.Sprintf("%s-fix-%d", ir.Name, attempt)
}

func fixBranchName(ir *agentv1alpha1.IssueResolution, attempt int32) string {
	return fmt.Sprintf("bugfix/issue-%d-attempt-%d", ir.Spec.IssueNumber, attempt)
}

func (r *IssueResolutionReconciler) buildTriageJob(ir *agentv1alpha1.IssueResolution) (*batchv1.Job, error) {
	image := r.triageImage()
	secretName := r.geminiSecretName()
	secretKey := r.geminiSecretKey()
	githubSecret := r.githubTriageSecretName()
	githubKey := r.githubSecretKey()
	model := r.geminiModel()
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
					RestartPolicy:                corev1.RestartPolicyNever,
					ServiceAccountName:           r.jobTriageServiceAccount(),
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptr.To(true),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{
						{
							Name:    jobRoleTriage,
							Image:   image,
							Command: []string{"/" + jobRoleTriage},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr.To(false),
								ReadOnlyRootFilesystem:   ptr.To(true),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},
							Env: []corev1.EnvVar{
								{Name: "ISSUE_REPOSITORY", Value: ir.Spec.Repository},
								{Name: "ISSUE_NUMBER", Value: fmt.Sprintf("%d", ir.Spec.IssueNumber)},
								{Name: "ISSUE_AUTHOR", Value: ir.Spec.Author},
								{Name: "ISSUE_TITLE", Value: ir.Spec.Title},
								{Name: "ISSUE_BODY", Value: ir.Spec.Body},
								{Name: "GEMINI_MODEL", Value: model},
								{
									Name: "GEMINI_API_KEY",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
											Key:                  secretKey,
										},
									},
								},
								{
									Name: defaults.GitHubSecretKey,
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: githubSecret},
											Key:                  githubKey,
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

	r.applyJobPodRuntimeClass(&job.Spec.Template.Spec)

	if err := controllerutil.SetControllerReference(ir, job, r.Scheme); err != nil {
		return nil, err
	}
	return job, nil
}

func (r *IssueResolutionReconciler) applyJobPodRuntimeClass(spec *corev1.PodSpec) {
	if r.JobRuntimeClassName != "" {
		spec.RuntimeClassName = ptr.To(r.JobRuntimeClassName)
	}
}

func (r *IssueResolutionReconciler) readTriageResult(ctx context.Context, ir *agentv1alpha1.IssueResolution, job *batchv1.Job) (triageJobResult, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(ir.Namespace),
		client.MatchingLabels{
			labelIssueResolution:  ir.Name,
			labelJobRole:          jobRoleTriage,
			labelJobControllerUID: string(job.UID),
		},
	); err != nil {
		return triageJobResult{}, err
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Terminated == nil {
				continue
			}
			// T1: only trust successful containers (failed attempts also write termination-log).
			if cs.State.Terminated.ExitCode != 0 {
				continue
			}
			if cs.State.Terminated.Message == "" {
				continue
			}
			var tr triageJobResult
			if err := json.Unmarshal([]byte(cs.State.Terminated.Message), &tr); err != nil {
				return triageJobResult{}, fmt.Errorf("parse termination message: %w", err)
			}
			if tr.Model == "" {
				tr.Model = r.geminiModel()
			}
			return tr, nil
		}
	}

	return triageJobResult{}, fmt.Errorf("no successful termination message on pods of job %s (check kubectl logs job/%s)", job.Name, job.Name)
}

func triageJobName(ir *agentv1alpha1.IssueResolution) string {
	return fmt.Sprintf("%s-triage", ir.Name)
}

func (r *IssueResolutionReconciler) triageImage() string {
	if r.TriageImage != "" {
		return r.TriageImage
	}
	return defaults.TriageImage
}

func (r *IssueResolutionReconciler) geminiSecretName() string {
	if r.GeminiSecretName != "" {
		return r.GeminiSecretName
	}
	return defaults.GeminiSecretName
}

func (r *IssueResolutionReconciler) geminiSecretKey() string {
	if r.GeminiSecretKey != "" {
		return r.GeminiSecretKey
	}
	return defaults.GeminiSecretKey
}

func (r *IssueResolutionReconciler) geminiModel() string {
	if r.GeminiModel != "" {
		return r.GeminiModel
	}
	return defaults.GeminiModel
}

func (r *IssueResolutionReconciler) fixImage() string {
	if r.FixImage != "" {
		return r.FixImage
	}
	return defaults.FixImage
}

func (r *IssueResolutionReconciler) githubTriageSecretName() string {
	if r.GitHubTriageSecretName != "" {
		return r.GitHubTriageSecretName
	}
	return defaults.GitHubTriageSecretName
}

func (r *IssueResolutionReconciler) githubFixSecretName() string {
	if r.GitHubFixSecretName != "" {
		return r.GitHubFixSecretName
	}
	return defaults.GitHubFixSecretName
}

func (r *IssueResolutionReconciler) githubSecretKey() string {
	if r.GitHubSecretKey != "" {
		return r.GitHubSecretKey
	}
	return defaults.GitHubSecretKey
}

func (r *IssueResolutionReconciler) jobTriageServiceAccount() string {
	if r.JobTriageServiceAccount != "" {
		return r.JobTriageServiceAccount
	}
	return defaults.JobTriageServiceAccount
}

func (r *IssueResolutionReconciler) jobFixServiceAccount() string {
	if r.JobFixServiceAccount != "" {
		return r.JobFixServiceAccount
	}
	return defaults.JobFixServiceAccount
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// SetupWithManager sets up the controller with the Manager.
func (r *IssueResolutionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.IssueResolution{}).
		Owns(&batchv1.Job{}).
		Named("issueresolution").
		Complete(r)
}
