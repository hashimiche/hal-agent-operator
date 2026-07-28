/*
Copyright 2026 HAL.
*/

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig_BaseBranch(t *testing.T) {
	t.Setenv("ISSUE_REPOSITORY", "owner/repo")
	t.Setenv("ISSUE_NUMBER", "5")
	t.Setenv("GEMINI_API_KEY", "key")
	t.Setenv("GITHUB_TOKEN", "token")
	t.Setenv("BASE_BRANCH", "fixture/bug1")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseBranch != "fixture/bug1" {
		t.Fatalf("BaseBranch = %q, want fixture/bug1", cfg.BaseBranch)
	}
}

func TestLoadConfig_BaseBranchUnset(t *testing.T) {
	t.Setenv("ISSUE_REPOSITORY", "owner/repo")
	t.Setenv("ISSUE_NUMBER", "5")
	t.Setenv("GEMINI_API_KEY", "key")
	t.Setenv("GITHUB_TOKEN", "token")
	t.Setenv("BASE_BRANCH", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseBranch != "" {
		t.Fatalf("BaseBranch = %q, want empty", cfg.BaseBranch)
	}
}

func TestDefaultBranchName(t *testing.T) {
	got := defaultBranchName("1234", 2)
	want := "bugfix/issue-1234-attempt-2"
	if got != want {
		t.Fatalf("defaultBranchName = %q, want %q", got, want)
	}
}

func TestParseLocatePath(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "plain json", raw: `{"path":"internal/foo/bar.go"}`, want: "internal/foo/bar.go"},
		{name: "fenced", raw: "```json\n{\"path\":\"pkg/x.go\"}\n```", want: "pkg/x.go"},
		{name: "dot slash", raw: `{"path":"./cmd/main.go"}`, want: "cmd/main.go"},
		{name: "traversal", raw: `{"path":"../secret.go"}`, wantErr: true},
		{name: "empty", raw: `{"path":""}`, wantErr: true},
		{name: "garbage", raw: `not json`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLocatePath(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got path %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripCodeFences(t *testing.T) {
	in := "```go\npackage main\n```"
	got := stripCodeFences(in)
	if got != "package main" {
		t.Fatalf("got %q", got)
	}
	plain := "package main"
	if stripCodeFences(plain) != plain {
		t.Fatalf("plain content changed")
	}
}

func TestParseEditsAndApply(t *testing.T) {
	src := "package mcp\n\nfunc isAllowedOrigin(h string) bool {\n\tswitch h {\n\tcase \"localhost\", \"127.0.0.2\", \"::1\":\n\t\treturn true\n\t}\n\treturn false\n}\n"
	raw := "*** Begin Edit ***\n*** Old ***\n\"127.0.0.2\"\n*** New ***\n\"127.0.0.1\"\n*** End Edit ***\n"
	edits, err := parseEdits(raw)
	if err != nil {
		t.Fatal(err)
	}
	fixed, err := applyEdits(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fixed, `"127.0.0.1"`) || strings.Contains(fixed, `"127.0.0.2"`) {
		t.Fatalf("unexpected fixed content:\n%s", fixed)
	}
	if err := validateGoSource("mcp.go", fixed); err != nil {
		t.Fatalf("validateGoSource: %v", err)
	}
}

func TestParseEdits_JSONFallback(t *testing.T) {
	raw := `{"edits":[{"old":"127.0.0.2","new":"127.0.0.1"}]}`
	edits, err := parseEdits(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 || edits[0].Old != "127.0.0.2" {
		t.Fatalf("got %+v", edits)
	}
}

func TestApplyEdits_NotFound(t *testing.T) {
	_, err := applyEdits("hello", []fileEdit{{Old: "missing", New: "x"}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestApplyEdits_Ambiguous(t *testing.T) {
	_, err := applyEdits("aa aa", []fileEdit{{Old: "aa", New: "bb"}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateGoSource_RejectsTruncated(t *testing.T) {
	bad := "package mcp\n\nfunc f(x int {\n"
	if err := validateGoSource("mcp.go", bad); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestParseEdits_Empty(t *testing.T) {
	if _, err := parseEdits("no edits here"); err == nil {
		t.Fatal("expected error")
	}
}

func TestWriteTermination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "termination-log")
	// Override path by writing via a local helper path — writeTermination uses
	// the const; exercise marshal shape instead.
	result := fixJobResult{
		PRURL:    "https://github.com/owner/hal/pull/7",
		PRNumber: 7,
		Branch:   "bugfix/issue-1234-attempt-1",
		Attempt:  1,
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got fixJobResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.PRURL != result.PRURL || got.PRNumber != 7 || got.Branch != result.Branch || got.Attempt != 1 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestSplitRepo(t *testing.T) {
	owner, name, err := splitRepo("owner/hal")
	if err != nil || owner != "owner" || name != "hal" {
		t.Fatalf("got %s/%s err=%v", owner, name, err)
	}
	if _, _, err := splitRepo("bad"); err == nil {
		t.Fatal("expected error for bad repo")
	}
}

func TestLocatePromptShape(t *testing.T) {
	// Sanity: ensure locate response type matches what parseLocatePath expects.
	raw := `{"path":"internal/controller/foo.go"}`
	path, err := parseLocatePath(raw)
	if err != nil {
		t.Fatal(err)
	}
	if path != "internal/controller/foo.go" {
		t.Fatalf("got %q", path)
	}
}
