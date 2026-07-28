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

package main

import (
	"strings"
	"testing"
)

func TestFormatTriageComment(t *testing.T) {
	t.Parallel()

	got := formatTriageComment(triageResult{
		InScope:    true,
		Suspicious: false,
		Summary:    "Safe docs typo fix.",
		Model:      "gemini-flash-latest",
	})
	for _, want := range []string{
		"## Hal Operator triage result",
		"| **In scope** | `true` |",
		"| **Suspicious** | `false` |",
		"| **Model** | `gemini-flash-latest` |",
		"### Analysis",
		"Safe docs typo fix.",
		"agent go",
		"LLM-generated analysis",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("comment missing %q\n%s", want, got)
		}
	}
}

func TestTriageLabelPlan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		result     triageResult
		wantAdd    []string
		wantRemove []string
	}{
		{
			name:   "pending validation",
			result: triageResult{InScope: true, Suspicious: false},
			wantAdd: []string{
				labelTriageExecuted,
				labelSuspiciousFalse,
				labelInScopeTrue,
				labelPendingValidation,
			},
			wantRemove: []string{
				labelSuspiciousTrue,
				labelInScopeFalse,
				labelRejected,
			},
		},
		{
			name:   "rejected suspicious",
			result: triageResult{InScope: false, Suspicious: true},
			wantAdd: []string{
				labelTriageExecuted,
				labelSuspiciousTrue,
				labelInScopeFalse,
				labelRejected,
			},
			wantRemove: []string{
				labelSuspiciousFalse,
				labelInScopeTrue,
				labelPendingValidation,
			},
		},
		{
			name:   "rejected out of scope",
			result: triageResult{InScope: false, Suspicious: false},
			wantAdd: []string{
				labelTriageExecuted,
				labelSuspiciousFalse,
				labelInScopeFalse,
				labelRejected,
			},
			wantRemove: []string{
				labelSuspiciousTrue,
				labelInScopeTrue,
				labelPendingValidation,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			add, remove := triageLabelPlan(tt.result)
			assertSameSet(t, "add", add, tt.wantAdd)
			assertSameSet(t, "remove", remove, tt.wantRemove)
		})
	}
}

func assertSameSet(t *testing.T, kind string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v want %v", kind, got, want)
	}
	set := make(map[string]struct{}, len(got))
	for _, g := range got {
		set[g] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			t.Fatalf("%s: missing %q in %v", kind, w, got)
		}
	}
}
