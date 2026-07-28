/*
Copyright 2026 HAL.

Fix worker for Job 2: clone target repo, run go test, ask Gemini to rewrite
a single file, re-test, commit/push, and open a PR. Writes a compact JSON
result to /dev/termination-log for the controller.
*/

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gitHttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/google/go-github/v66/github"
	"golang.org/x/oauth2"

	"github.com/hashicorp-academy/hal-k8s-operator/internal/defaults"
	"github.com/hashicorp-academy/hal-k8s-operator/internal/gemini"
)

const (
	terminationLog = "/dev/termination-log"
	maxBodyRunes   = 12000
	maxTreeEntries = 400
	maxFileBytes   = 200_000
)

// fixJobResult is written to the termination-log (must match controller fixJobResult).
type fixJobResult struct {
	PRURL    string `json:"prURL"`
	PRNumber int32  `json:"prNumber"`
	Branch   string `json:"branch"`
	Attempt  int32  `json:"attempt"`
	Error    string `json:"error,omitempty"`
}

type locateResponse struct {
	Path string `json:"path"`
}

// fileEdit is one surgical replacement returned by Gemini (JSON).
type fileEdit struct {
	Old string `json:"old"`
	New string `json:"new"`
}

type editResponse struct {
	Edits []fileEdit `json:"edits"`
}

type fixConfig struct {
	Repository    string
	IssueNumber   string
	Title         string
	Body          string
	TriageSummary string
	APIKey        string
	Model         string
	GitHubToken   string
	BranchName    string
	BaseBranch    string
	Attempt       int32
	WorkDir       string
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fail(cfg, err)
	}
	if err := run(cfg); err != nil {
		fail(cfg, err)
	}
}

func fail(cfg fixConfig, err error) {
	fmt.Fprintf(os.Stderr, "fix failed: %v\n", err)
	result := fixJobResult{
		Branch:  cfg.BranchName,
		Attempt: cfg.Attempt,
		Error:   err.Error(),
	}
	_ = writeTermination(result)
	os.Exit(1)
}

func loadConfig() (fixConfig, error) {
	attempt, _ := strconv.ParseInt(envOr("FIX_ATTEMPT", "1"), 10, 32)
	cfg := fixConfig{
		Repository:    os.Getenv("ISSUE_REPOSITORY"),
		IssueNumber:   os.Getenv("ISSUE_NUMBER"),
		Title:         os.Getenv("ISSUE_TITLE"),
		Body:          gemini.TruncateRunes(os.Getenv("ISSUE_BODY"), maxBodyRunes),
		TriageSummary: os.Getenv("TRIAGE_SUMMARY"),
		APIKey:        os.Getenv("GEMINI_API_KEY"),
		Model:         envOr("GEMINI_MODEL", defaults.GeminiModel),
		GitHubToken:   os.Getenv("GITHUB_TOKEN"),
		BranchName:    os.Getenv("BRANCH_NAME"),
		BaseBranch:    os.Getenv("BASE_BRANCH"),
		Attempt:       int32(attempt),
		WorkDir:       envOr("WORKDIR", "/workspace"),
	}
	if cfg.Repository == "" {
		return cfg, fmt.Errorf("ISSUE_REPOSITORY is not set")
	}
	if cfg.IssueNumber == "" {
		return cfg, fmt.Errorf("ISSUE_NUMBER is not set")
	}
	if cfg.APIKey == "" {
		return cfg, fmt.Errorf("GEMINI_API_KEY is not set")
	}
	if cfg.GitHubToken == "" {
		return cfg, fmt.Errorf("GITHUB_TOKEN is not set")
	}
	if cfg.BranchName == "" {
		cfg.BranchName = defaultBranchName(cfg.IssueNumber, cfg.Attempt)
	}
	return cfg, nil
}

func run(cfg fixConfig) error {
	fmt.Println("=== HAL fix job (POC) ===")
	fmt.Printf("repository: %s\n", cfg.Repository)
	fmt.Printf("issue:      #%s\n", cfg.IssueNumber)
	fmt.Printf("branch:     %s\n", cfg.BranchName)
	fmt.Printf("attempt:    %d\n", cfg.Attempt)
	fmt.Printf("model:      %s\n", cfg.Model)

	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()

	repoDir := filepath.Join(cfg.WorkDir, "repo")
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return fmt.Errorf("mkdir workdir: %w", err)
	}
	for _, d := range []string{
		filepath.Join(cfg.WorkDir, ".cache"),
		filepath.Join(cfg.WorkDir, "gomod"),
		filepath.Join(cfg.WorkDir, "go"),
		filepath.Join(cfg.WorkDir, "tmp"),
	} {
		_ = os.MkdirAll(d, 0o755)
	}

	fmt.Println("--- cloning ---")
	repo, baseBranch, err := cloneRepo(ctx, cfg, repoDir)
	if err != nil {
		return err
	}
	fmt.Printf("default branch: %s\n", baseBranch)

	// Create the bugfix branch before editing so commit does not need a dirty checkout.
	if err := checkoutNewBranch(repo, cfg.BranchName); err != nil {
		return err
	}

	fmt.Println("--- baseline go test ---")
	baselineOut, baselineErr := runGoTest(ctx, repoDir)
	if baselineErr == nil {
		return fmt.Errorf("baseline go test passed; nothing to fix (unexpected for Job 2)")
	}
	fmt.Println(baselineOut)

	tree, err := listGoFiles(repoDir)
	if err != nil {
		return err
	}

	fmt.Println("--- locate file (Gemini) ---")
	targetPath, err := locateFile(ctx, cfg, tree, baselineOut)
	if err != nil {
		return err
	}
	fmt.Printf("target file: %s\n", targetPath)

	absPath := filepath.Join(repoDir, filepath.FromSlash(targetPath))
	original, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read target file %s: %w", targetPath, err)
	}
	if len(original) > maxFileBytes {
		return fmt.Errorf("target file %s too large (%d bytes)", targetPath, len(original))
	}

	fmt.Println("--- rewrite file (Gemini) ---")
	fixed, err := rewriteFile(ctx, cfg, targetPath, string(original), baselineOut)
	if err != nil {
		return err
	}
	if err := validateGoSource(targetPath, fixed); err != nil {
		return fmt.Errorf("rewritten %s is not valid Go: %w", targetPath, err)
	}
	if err := os.WriteFile(absPath, []byte(fixed), 0o644); err != nil {
		return fmt.Errorf("write fixed file: %w", err)
	}

	fmt.Println("--- verification go test ---")
	verifyOut, verifyErr := runGoTest(ctx, repoDir)
	if verifyErr != nil {
		fmt.Println(verifyOut)
		return fmt.Errorf("verification go test failed: %w", verifyErr)
	}
	fmt.Println("go test ./... passed")

	fmt.Println("--- commit + push ---")
	if err := commitAndPush(ctx, repo, cfg, targetPath); err != nil {
		return err
	}

	fmt.Println("--- open PR ---")
	prURL, prNumber, err := openPR(ctx, cfg, baseBranch)
	if err != nil {
		return err
	}

	result := fixJobResult{
		PRURL:    prURL,
		PRNumber: prNumber,
		Branch:   cfg.BranchName,
		Attempt:  cfg.Attempt,
	}
	pretty, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println("--- fix result ---")
	fmt.Println(string(pretty))
	if err := writeTermination(result); err != nil {
		fmt.Fprintf(os.Stderr, "warn: termination-log: %v\n", err)
	}
	fmt.Println("=== fix done ===")
	return nil
}

func cloneRepo(ctx context.Context, cfg fixConfig, repoDir string) (*git.Repository, string, error) {
	auth := &gitHttp.BasicAuth{Username: "x-access-token", Password: cfg.GitHubToken}
	url := fmt.Sprintf("https://github.com/%s.git", cfg.Repository)
	opts := &git.CloneOptions{
		URL:      url,
		Auth:     auth,
		Progress: os.Stdout,
	}
	if cfg.BaseBranch != "" {
		opts.ReferenceName = plumbing.NewBranchReferenceName(cfg.BaseBranch)
		opts.SingleBranch = true
	}
	repo, err := git.PlainCloneContext(ctx, repoDir, false, opts)
	if err != nil {
		return nil, "", fmt.Errorf("clone %s: %w", url, err)
	}
	if cfg.BaseBranch != "" {
		return repo, cfg.BaseBranch, nil
	}
	head, err := repo.Head()
	if err != nil {
		return nil, "", fmt.Errorf("resolve HEAD: %w", err)
	}
	base := head.Name().Short()
	return repo, base, nil
}

func runGoTest(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func listGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		if len(files) >= maxTreeEntries {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func locateFile(ctx context.Context, cfg fixConfig, tree []string, testOut string) (string, error) {
	system := strings.TrimSpace(`
You locate which single Go source file should be edited to fix a failing test.
Respond with ONLY a JSON object: {"path":"relative/path/to/file.go"}
Pick exactly one path from the provided file tree. Prefer production code over _test.go
unless the issue is clearly about the test itself.
`)
	user := fmt.Sprintf(
		"Repository: %s\nIssue #%s\nTitle: %s\nTriage summary: %s\n\nBody:\n%s\n\n"+
			"Failing test output:\n%s\n\nGo file tree:\n%s\n",
		cfg.Repository, cfg.IssueNumber, cfg.Title, cfg.TriageSummary, cfg.Body,
		gemini.TruncateRunes(testOut, 8000),
		strings.Join(tree, "\n"),
	)
	raw, err := gemini.Call(ctx, cfg.APIKey, cfg.Model, system, user, gemini.CallOptions{
		ResponseMIMEType: "application/json",
		MaxOutputTokens:  256,
	})
	if err != nil {
		return "", err
	}
	path, err := parseLocatePath(raw)
	if err != nil {
		return "", err
	}
	if !containsPath(tree, path) {
		return "", fmt.Errorf("model returned unknown path %q", path)
	}
	return path, nil
}

func rewriteFile(ctx context.Context, cfg fixConfig, path, content, testOut string) (string, error) {
	system := strings.TrimSpace(`
You fix a single Go source file with minimal surgical edits.
Respond with ONLY one or more edits in this exact plain-text format (no JSON, no markdown):

*** Begin Edit ***
*** Old ***
<exact unique substring copied from the file>
*** New ***
<replacement>
*** End Edit ***

Rules:
- Each Old block must appear EXACTLY ONCE in the current file.
- Prefer the smallest unique Old text (often one line).
- Do NOT rewrite the whole file. Do NOT invent unrelated refactors.
`)
	user := fmt.Sprintf(
		"Repository: %s\nIssue #%s\nTitle: %s\nTriage summary: %s\n\nBody:\n%s\n\n"+
			"Failing test output:\n%s\n\nFile path: %s\n\nCurrent file contents:\n%s\n",
		cfg.Repository, cfg.IssueNumber, cfg.Title, cfg.TriageSummary, cfg.Body,
		gemini.TruncateRunes(testOut, 8000),
		path, content,
	)
	raw, err := gemini.Call(ctx, cfg.APIKey, cfg.Model, system, user, gemini.CallOptions{
		ResponseMIMEType: "text/plain",
		MaxOutputTokens:  2048,
	})
	if err != nil {
		return "", err
	}
	edits, err := parseEdits(raw)
	if err != nil {
		return "", err
	}
	fixed, err := applyEdits(content, edits)
	if err != nil {
		return "", err
	}
	return fixed, nil
}

func parseEdits(raw string) ([]fileEdit, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "```json") {
		if edits, err := parseJSONEdits(trimmed); err == nil {
			return edits, nil
		}
	}
	edits, err := parseMarkerEdits(trimmed)
	if err != nil {
		return nil, err
	}
	return edits, nil
}

func parseJSONEdits(raw string) ([]fileEdit, error) {
	cleaned := extractJSONObject(raw)
	var resp editResponse
	if err := json.Unmarshal([]byte(cleaned), &resp); err != nil {
		return nil, fmt.Errorf("parse edit response: %w", err)
	}
	return validateEdits(resp.Edits)
}

func parseMarkerEdits(raw string) ([]fileEdit, error) {
	const (
		begin = "*** Begin Edit ***"
		oldM  = "*** Old ***"
		newM  = "*** New ***"
		endM  = "*** End Edit ***"
	)
	var edits []fileEdit
	rest := raw
	for {
		i := strings.Index(rest, begin)
		if i < 0 {
			break
		}
		rest = rest[i+len(begin):]
		end := strings.Index(rest, endM)
		if end < 0 {
			return nil, fmt.Errorf("edit block missing %q", endM)
		}
		block := rest[:end]
		rest = rest[end+len(endM):]

		oi := strings.Index(block, oldM)
		ni := strings.Index(block, newM)
		if oi < 0 || ni < 0 || ni < oi {
			return nil, fmt.Errorf("edit block missing Old/New markers")
		}
		oldPart := strings.TrimPrefix(block[oi+len(oldM):ni], "\n")
		oldPart = strings.TrimSuffix(oldPart, "\n")
		newPart := strings.TrimPrefix(block[ni+len(newM):], "\n")
		newPart = strings.TrimSuffix(newPart, "\n")
		edits = append(edits, fileEdit{Old: oldPart, New: newPart})
	}
	return validateEdits(edits)
}

func validateEdits(edits []fileEdit) ([]fileEdit, error) {
	if len(edits) == 0 {
		return nil, fmt.Errorf("model returned no edits")
	}
	for i, e := range edits {
		if e.Old == "" {
			return nil, fmt.Errorf("edit[%d]: empty old", i)
		}
		if e.Old == e.New {
			return nil, fmt.Errorf("edit[%d]: old and new are identical", i)
		}
	}
	return edits, nil
}

func applyEdits(content string, edits []fileEdit) (string, error) {
	out := content
	for i, e := range edits {
		n := strings.Count(out, e.Old)
		if n == 0 {
			return "", fmt.Errorf("edit[%d]: old string not found in file", i)
		}
		if n > 1 {
			return "", fmt.Errorf("edit[%d]: old string matches %d times (need exactly 1)", i, n)
		}
		out = strings.Replace(out, e.Old, e.New, 1)
	}
	if out == content {
		return "", fmt.Errorf("edits produced no change")
	}
	return out, nil
}

func validateGoSource(path, src string) error {
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, path, src, parser.AllErrors)
	return err
}

func extractJSONObject(raw string) string {
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)
	if !strings.HasPrefix(cleaned, "{") {
		start := strings.Index(cleaned, "{")
		end := strings.LastIndex(cleaned, "}")
		if start >= 0 && end > start {
			cleaned = cleaned[start : end+1]
		}
	}
	return cleaned
}

func checkoutNewBranch(repo *git.Repository, branch string) error {
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branch),
		Create: true,
	})
	if err != nil {
		return fmt.Errorf("checkout branch %s: %w", branch, err)
	}
	return nil
}

func commitAndPush(ctx context.Context, repo *git.Repository, cfg fixConfig, changedPath string) error {
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	if _, err := wt.Add(changedPath); err != nil {
		return fmt.Errorf("git add %s: %w", changedPath, err)
	}
	msg := fmt.Sprintf("fix: address issue #%s (attempt %d)", cfg.IssueNumber, cfg.Attempt)
	_, err = wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "hal-agent",
			Email: "hal-agent@users.noreply.github.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	auth := &gitHttp.BasicAuth{Username: "x-access-token", Password: cfg.GitHubToken}
	if err := repo.PushContext(ctx, &git.PushOptions{Auth: auth}); err != nil {
		return fmt.Errorf("push: %w", err)
	}
	return nil
}

func openPR(ctx context.Context, cfg fixConfig, baseBranch string) (string, int32, error) {
	owner, name, err := splitRepo(cfg.Repository)
	if err != nil {
		return "", 0, err
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cfg.GitHubToken})
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	title := cfg.Title
	if title == "" {
		title = fmt.Sprintf("fix: issue #%s", cfg.IssueNumber)
	}
	body := fmt.Sprintf("Fixes #%s\n\nAutomated fix by hal-agent (attempt %d).\n\nTriage: %s\n",
		cfg.IssueNumber, cfg.Attempt, cfg.TriageSummary)

	pr, _, err := client.PullRequests.Create(ctx, owner, name, &github.NewPullRequest{
		Title: github.String(title),
		Head:  github.String(cfg.BranchName),
		Base:  github.String(baseBranch),
		Body:  github.String(body),
	})
	if err != nil {
		return "", 0, fmt.Errorf("create PR: %w", err)
	}
	return pr.GetHTMLURL(), int32(pr.GetNumber()), nil
}

func writeTermination(result fixJobResult) error {
	compact := fixJobResult{
		PRURL:    result.PRURL,
		PRNumber: result.PRNumber,
		Branch:   result.Branch,
		Attempt:  result.Attempt,
		Error:    gemini.TruncateRunes(result.Error, 1500),
	}
	b, err := json.Marshal(compact)
	if err != nil {
		return err
	}
	return os.WriteFile(terminationLog, b, 0o600)
}

func parseLocatePath(raw string) (string, error) {
	cleaned := extractJSONObject(raw)
	var loc locateResponse
	if err := json.Unmarshal([]byte(cleaned), &loc); err != nil {
		return "", fmt.Errorf("parse locate response: %w", err)
	}
	path := filepath.ToSlash(strings.TrimSpace(loc.Path))
	path = strings.TrimPrefix(path, "./")
	if path == "" || strings.Contains(path, "..") {
		return "", fmt.Errorf("invalid path from model: %q", loc.Path)
	}
	return path, nil
}

func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	return s
}

func containsPath(tree []string, path string) bool {
	return slices.Contains(tree, path)
}

func splitRepo(repo string) (owner, name string, err error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid ISSUE_REPOSITORY %q (want owner/name)", repo)
	}
	return parts[0], parts[1], nil
}

func defaultBranchName(issueNumber string, attempt int32) string {
	return fmt.Sprintf("bugfix/issue-%s-attempt-%d", issueNumber, attempt)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
