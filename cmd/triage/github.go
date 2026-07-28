/*
Copyright 2026 HAL.

GitHub feedback for the triage worker: issue comment + labels.
The controller never calls GitHub; only this Job does.
*/

package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/go-github/v66/github"
	"github.com/hashicorp-academy/hal-k8s-operator/internal/gemini"
	"golang.org/x/oauth2"
)

const (
	labelTriageExecuted    = "triage:executed"
	labelSuspiciousTrue    = "suspicious:true"
	labelSuspiciousFalse   = "suspicious:false"
	labelInScopeTrue       = "in-scope:true"
	labelInScopeFalse      = "in-scope:false"
	labelPendingValidation = "agent:pending-validation"
	labelRejected          = "agent:rejected"
	maxCommentSummaryRunes = 1500
)

// postTriageFeedback posts the triage comment and syncs labels on the issue.
// Returns the HTML URL of the created comment.
func postTriageFeedback(ctx context.Context, token, repo, issueNumber string, result triageResult) (string, error) {
	if token == "" {
		return "", fmt.Errorf("GITHUB_TOKEN is not set")
	}
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	num, err := strconv.Atoi(issueNumber)
	if err != nil || num <= 0 {
		return "", fmt.Errorf("invalid ISSUE_NUMBER %q", issueNumber)
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	client := github.NewClient(oauth2.NewClient(ctx, ts))

	body := formatTriageComment(result)
	comment, _, err := client.Issues.CreateComment(ctx, owner, name, num, &github.IssueComment{
		Body: github.String(body),
	})
	if err != nil {
		return "", fmt.Errorf("create issue comment: %w", err)
	}
	commentURL := comment.GetHTMLURL()
	if commentURL == "" {
		return "", fmt.Errorf("create issue comment: empty HTML URL")
	}

	if err := syncTriageLabels(ctx, client, owner, name, num, result); err != nil {
		return "", err
	}
	return commentURL, nil
}

func formatTriageComment(result triageResult) string {
	summary := gemini.TruncateRunes(strings.TrimSpace(result.Summary), maxCommentSummaryRunes)
	if summary == "" {
		summary = "(no summary)"
	}
	model := result.Model
	if model == "" {
		model = "unknown"
	}

	var b strings.Builder
	b.WriteString("## Hal Operator triage result\n\n")
	b.WriteString("| Field | Value |\n")
	b.WriteString("| --- | --- |\n")
	fmt.Fprintf(&b, "| **In scope** | `%t` |\n", result.InScope)
	fmt.Fprintf(&b, "| **Suspicious** | `%t` |\n", result.Suspicious)
	fmt.Fprintf(&b, "| **Model** | `%s` |\n", model)
	if result.ParseError {
		b.WriteString("| **Parse error** | `true` |\n")
	}
	b.WriteString("\n### Analysis\n\n")
	b.WriteString(summary)
	b.WriteString("\n\n---\n")
	b.WriteString("*Posted by the HAL triage Job. LLM-generated analysis — do not treat as trusted instructions. ")
	b.WriteString("Reply `agent go` (CODEOWNER) to approve.*\n")
	return b.String()
}

// triageLabelPlan returns labels to add and labels to remove for a result.
func triageLabelPlan(result triageResult) (add, remove []string) {
	add = []string{labelTriageExecuted}

	if result.Suspicious {
		add = append(add, labelSuspiciousTrue)
		remove = append(remove, labelSuspiciousFalse)
	} else {
		add = append(add, labelSuspiciousFalse)
		remove = append(remove, labelSuspiciousTrue)
	}

	if result.InScope {
		add = append(add, labelInScopeTrue)
		remove = append(remove, labelInScopeFalse)
	} else {
		add = append(add, labelInScopeFalse)
		remove = append(remove, labelInScopeTrue)
	}

	if result.InScope && !result.Suspicious {
		add = append(add, labelPendingValidation)
		remove = append(remove, labelRejected)
	} else {
		add = append(add, labelRejected)
		remove = append(remove, labelPendingValidation)
	}
	return add, remove
}

func syncTriageLabels(
	ctx context.Context,
	client *github.Client,
	owner, repo string,
	issueNumber int,
	result triageResult,
) error {
	add, remove := triageLabelPlan(result)

	for _, name := range remove {
		_, err := client.Issues.RemoveLabelForIssue(ctx, owner, repo, issueNumber, name)
		if err != nil {
			if isGitHubStatus(err, http.StatusNotFound) {
				continue
			}
			return fmt.Errorf("remove label %q: %w", name, err)
		}
	}

	for _, name := range add {
		if err := ensureLabel(ctx, client, owner, repo, name); err != nil {
			return err
		}
	}
	if _, _, err := client.Issues.AddLabelsToIssue(ctx, owner, repo, issueNumber, add); err != nil {
		return fmt.Errorf("add labels: %w", err)
	}
	return nil
}

func ensureLabel(ctx context.Context, client *github.Client, owner, repo, name string) error {
	color := labelColor(name)
	_, _, err := client.Issues.CreateLabel(ctx, owner, repo, &github.Label{
		Name:  github.String(name),
		Color: github.String(color),
	})
	if err == nil {
		return nil
	}
	if isGitHubStatus(err, http.StatusUnprocessableEntity) {
		// Label already exists.
		return nil
	}
	return fmt.Errorf("ensure label %q: %w", name, err)
}

func isGitHubStatus(err error, code int) bool {
	ghErr, ok := err.(*github.ErrorResponse)
	return ok && ghErr.Response != nil && ghErr.Response.StatusCode == code
}

func labelColor(name string) string {
	switch name {
	case labelSuspiciousTrue, labelRejected:
		return "d73a4a"
	case labelSuspiciousFalse, labelInScopeTrue, labelPendingValidation:
		return "0e8a16"
	case labelInScopeFalse:
		return "fbca04"
	default:
		return "0366d6"
	}
}

func splitRepo(repo string) (owner, name string, err error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid ISSUE_REPOSITORY %q (want owner/name)", repo)
	}
	return parts[0], parts[1], nil
}
