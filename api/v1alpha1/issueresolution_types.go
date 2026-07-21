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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Phase is the IssueResolution state-machine value stored in status.phase.
// +kubebuilder:validation:Enum=Triage;PendingValidation;Ready;Executing;PROpen;Done;Rejected;Failed
type Phase string

const (
	PhaseTriage            Phase = "Triage"
	PhasePendingValidation Phase = "PendingValidation"
	PhaseReady             Phase = "Ready"
	PhaseExecuting         Phase = "Executing"
	PhasePROpen            Phase = "PROpen"
	PhaseDone              Phase = "Done"
	PhaseRejected          Phase = "Rejected"
	PhaseFailed            Phase = "Failed"
)

// Condition types for IssueResolution status.conditions.
const (
	ConditionTriaged          = "Triaged"
	ConditionAwaitingApproval = "AwaitingApproval"
	ConditionReady            = "Ready"
	ConditionExecuting        = "Executing"
	ConditionPROpen           = "PROpen"
	ConditionFailed           = "Failed"
)

// IssueResolutionSpec defines the desired state of IssueResolution.
// Written by the GitHub Action (issue snapshot + human approval gate).
// Must never contain secrets.
type IssueResolutionSpec struct {
	// repository is the GitHub repo in "owner/name" form.
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=200
	Repository string `json:"repository"`

	// issueNumber is the GitHub issue number. CR name should be issue-<number>.
	// +kubebuilder:validation:Minimum=1
	IssueNumber int `json:"issueNumber"`

	// issueURL is the canonical GitHub issue URL.
	// +optional
	IssueURL string `json:"issueURL,omitempty"`

	// author is the GitHub login of the issue author.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Author string `json:"author"`

	// authorAssociation is GitHub's association (OWNER, MEMBER, CONTRIBUTOR, NONE, …).
	// +optional
	AuthorAssociation string `json:"authorAssociation,omitempty"`

	// title is the issue title snapshot at webhook time.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=500
	Title string `json:"title"`

	// body is a truncated snapshot of the issue body (prefer ≤16KiB).
	// Jobs may re-fetch the full body from GitHub when needed.
	// +optional
	// +kubebuilder:validation:MaxLength=16384
	Body string `json:"body,omitempty"`

	// labels are GitHub issue labels at webhook time.
	// +optional
	Labels []string `json:"labels,omitempty"`

	// createdAt is the GitHub issue creation timestamp.
	// +optional
	CreatedAt *metav1.Time `json:"createdAt,omitempty"`

	// approved is the human gate. Set true by the GitHub Action after a
	// CODEOWNER posts the comment "agent go".
	// +optional
	Approved bool `json:"approved,omitempty"`

	// approvedBy is the GitHub login that approved via "agent go".
	// +optional
	ApprovedBy string `json:"approvedBy,omitempty"`

	// approvedAt is when the human gate was cleared.
	// +optional
	ApprovedAt *metav1.Time `json:"approvedAt,omitempty"`

	// maxFixAttempts caps Job 2 retries (cost / runaway protection).
	// +optional
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	MaxFixAttempts *int32 `json:"maxFixAttempts,omitempty"`
}

// TriageStatus is the result of Job 1 (text-only analysis).
type TriageStatus struct {
	// inScope is true when the issue is eligible for autonomous fix.
	// +optional
	InScope bool `json:"inScope,omitempty"`

	// suspicious is true when prompt-injection / hostile intent was detected.
	// +optional
	Suspicious bool `json:"suspicious,omitempty"`

	// summary is a short human-readable triage rationale.
	// +optional
	// +kubebuilder:validation:MaxLength=2000
	Summary string `json:"summary,omitempty"`

	// model identifies the model used for triage (no secrets).
	// +optional
	Model string `json:"model,omitempty"`

	// parseError is true when the Job succeeded but the model output was not usable JSON.
	// Distinct from Rejected (business out-of-scope).
	// +optional
	ParseError bool `json:"parseError,omitempty"`
}

// PlanStatus points at the plan the human reviews before approval.
type PlanStatus struct {
	// commentURL is the GitHub issue comment containing the plan.
	// +optional
	CommentURL string `json:"commentURL,omitempty"`

	// summary is a short plan synopsis mirrored into the CR for kubectl/UX.
	// +optional
	// +kubebuilder:validation:MaxLength=2000
	Summary string `json:"summary,omitempty"`
}

// ExecutionStatus tracks Job 2 (fix) and the resulting PR.
type ExecutionStatus struct {
	// attempt is the current fix attempt (1-based).
	// +optional
	Attempt int32 `json:"attempt,omitempty"`

	// jobName is the Kubernetes Job name for the current/last fix run.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// branch is the git branch pushed for the PR.
	// +optional
	Branch string `json:"branch,omitempty"`

	// prURL is the opened pull request URL.
	// +optional
	PRURL string `json:"prURL,omitempty"`

	// prNumber is the GitHub PR number.
	// +optional
	PRNumber int `json:"prNumber,omitempty"`
}

// IssueResolutionStatus defines the observed state of IssueResolution.
// Written only by the operator from Job results. Must never contain secrets.
type IssueResolutionStatus struct {
	// phase is the state-machine position.
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// observedGeneration is the .metadata.generation last processed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// triage holds Job 1 results.
	// +optional
	Triage TriageStatus `json:"triage,omitempty"`

	// plan holds the plan comment / summary awaiting human approval.
	// +optional
	Plan PlanStatus `json:"plan,omitempty"`

	// execution holds Job 2 / PR progress.
	// +optional
	Execution ExecutionStatus `json:"execution,omitempty"`

	// message is a short operator-facing status line (failures, wait reasons).
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`

	// conditions represent the current state of the IssueResolution resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Approved",type=boolean,JSONPath=`.spec.approved`
// +kubebuilder:printcolumn:name="Issue",type=integer,JSONPath=`.spec.issueNumber`
// +kubebuilder:printcolumn:name="Author",type=string,JSONPath=`.spec.author`
// +kubebuilder:printcolumn:name="PR",type=string,JSONPath=`.status.execution.prURL`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// IssueResolution is the Schema for the issueresolutions API.
// One CR per GitHub issue (metadata.name = issue-<number>).
type IssueResolution struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of IssueResolution
	// +required
	Spec IssueResolutionSpec `json:"spec"`

	// status defines the observed state of IssueResolution
	// +optional
	Status IssueResolutionStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// IssueResolutionList contains a list of IssueResolution
type IssueResolutionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []IssueResolution `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &IssueResolution{}, &IssueResolutionList{})
		return nil
	})
}
