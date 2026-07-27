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
	"testing"
)

func TestParseResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		raw         string
		wantSummary string
		wantInScope bool
		wantErr     bool
	}{
		{
			name:        "bare json",
			raw:         `{"inScope":true,"suspicious":false,"summary":"ok docs"}`,
			wantSummary: "ok docs",
			wantInScope: true,
		},
		{
			name: "json in fences",
			raw: "```json\n" +
				`{"inScope":false,"suspicious":true,"summary":"injection"}` +
				"\n```",
			wantSummary: "injection",
			wantInScope: false,
		},
		{
			name:        "json buried in prose",
			raw:         "Here is my answer:\n{\"inScope\":true,\"suspicious\":false,\"summary\":\"buried\"}\nThanks.",
			wantSummary: "buried",
			wantInScope: true,
		},
		{
			name:    "invalid json",
			raw:     "not json at all",
			wantErr: true,
		},
		{
			name:        "empty summary fallback",
			raw:         `{"inScope":true,"suspicious":false,"summary":""}`,
			wantSummary: "No summary returned by model",
			wantInScope: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseResult(tt.raw, "test-model")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Summary != tt.wantSummary {
				t.Fatalf("summary=%q want %q", got.Summary, tt.wantSummary)
			}
			if got.InScope != tt.wantInScope {
				t.Fatalf("inScope=%v want %v", got.InScope, tt.wantInScope)
			}
			if got.Model != "test-model" {
				t.Fatalf("model=%q", got.Model)
			}
		})
	}
}
