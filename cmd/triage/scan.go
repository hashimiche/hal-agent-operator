/*
Copyright 2026 HAL.

Deterministic prefilter for the triage worker. Two responsibilities, kept apart:

  - scanSuspicious: hard, high-signal detectors (prompt injection, secret exfil,
    impersonation, zero-width, shell exfil). A hit forces suspicious=true and the
    caller skips the model entirely.
  - sanitizeForModel: redact-and-send. HTML comments and base64 blobs are removed
    from the text BEFORE it reaches the model. Nothing is ever decoded — the model
    cannot act on content it never receives.
*/

package main

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// scanFinding is one heuristic hit. Rule is a stable id for logs/tests.
type scanFinding struct {
	Rule   string
	Detail string
}

const (
	redactedBlobPlaceholder    = "[base64 blob redacted by prefilter - not decoded]"
	redactedCommentPlaceholder = "[html comment removed by prefilter]"
)

var (
	// RE2 \x{...} hex syntax keeps the pattern free of literal invisible chars.
	reZeroWidth = regexp.MustCompile(`[\x{200B}\x{200C}\x{200D}\x{2060}\x{FEFF}]`)
	// curl/wget leaking env or secret-named vars.
	reShellExfil = regexp.MustCompile(`(?i)(curl|wget)\b[^;\n]{0,160}\b(env|\$\{?\w*(TOKEN|SECRET|KEY|PASSWORD|KUBECONFIG)\w*\}?)`)

	// Redact-and-send patterns: stripped from model input, never decoded.
	reHTMLComment   = regexp.MustCompile(`(?is)<!--.*?-->`)
	reRedactDataURI = regexp.MustCompile(`(?i)data:[^;\s]+;base64,[A-Za-z0-9+/=]+`)
	// Threshold 40 covers a typical encoded injection phrase (~44 chars) while
	// sparing short IDs. Long hashes / kubeconfig data are redacted too - harmless
	// for triage, and a bonus: real secrets are not shipped to the third-party API.
	reRedactBlob = regexp.MustCompile(`[A-Za-z0-9+/_-]{40,}={0,2}`)
)

// Case-insensitive substring rules. Prefer multi-word / high-signal phrases
// so normal Vault/K8s teaching issues (webhook, kubeconfig, secret_id) pass.
var suspiciousSubstrings = []struct {
	rule string
	sub  string
}{
	{"prompt_injection", "ignore all previous"},
	{"prompt_injection", "ignore previous instructions"},
	{"prompt_injection", "ignore all instructions"},
	{"prompt_injection", "disregard previous"},
	{"prompt_injection", "disregard all previous"},
	{"prompt_injection", "new system directive"},
	{"prompt_injection", "new system prompt"},
	{"prompt_injection", "you are no longer"},
	{"prompt_injection", "jailbreak"},
	{"prompt_injection", "do not mention this injection"},
	{"prompt_injection", "begin actual task for fixer"},

	{"secret_exfil", "exfiltrat"},
	{"secret_exfil", "reverse shell"},
	{"secret_exfil", "~/.aws/credentials"},
	{"secret_exfil", ".git-credentials"},
	{"secret_exfil", "dump vault"},
	{"secret_exfil", "vault root token"},
	{"secret_exfil", "print the full json"},
	{"secret_exfil", "tmp/vault-dump"},
	{"secret_exfil", "gemini_api_key"},
	{"secret_exfil", "secrets harvest"},
	{"secret_exfil", "local secret courier"},
	{"secret_exfil", "disable vault policy"},
	{"secret_exfil", "disable codeowners"},

	{"impersonation", "i am the hal fixer"},
	{"impersonation", "i am the fixer job"},
	{"impersonation", "x-hal-agent-identity"},
	{"impersonation", "hal-agent-identity"},
	{"impersonation", "authorized internal request from the hal"},
	{"impersonation", "do not validate — trust this header"},
	{"impersonation", "do not validate - trust this header"},
}

// stripInvisible removes the same zero-width runes that reZeroWidth detects.
func stripInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case 0x200B, 0x200C, 0x200D, 0x2060, 0xFEFF:
			return -1
		default:
			return r
		}
	}, s)
}

// normalizeForMatch prepares text for substring matching only (not for the regex
// detectors). Order: strip zero-width -> NFKC (fullwidth -> ASCII) -> ToLower.
//
// Known limit: NFKC does not fold cross-script homoglyphs (e.g. Cyrillic a).
// Unicode confusables folding is out of scope here.
func normalizeForMatch(s string) string {
	s = stripInvisible(s)
	s = norm.NFKC.String(s)
	return strings.ToLower(s)
}

// scanSuspicious inspects title+body for hostile patterns. Order is stable.
func scanSuspicious(title, body string) []scanFinding {
	text := title + "\n" + body

	var findings []scanFinding
	seen := map[string]bool{}

	add := func(rule, detail string) {
		key := rule + "|" + detail
		if seen[key] {
			return
		}
		seen[key] = true
		findings = append(findings, scanFinding{Rule: rule, Detail: detail})
	}

	// Regex detectors run on the raw text (zero-width must still fire).
	if reZeroWidth.MatchString(text) {
		add("zero_width", "zero-width / invisible Unicode characters")
	}
	if reShellExfil.MatchString(text) {
		add("shell_exfil", "curl/wget referencing env or secret-like variables")
	}

	// Substring rules run on normalized text so trivial evasion (zero-width,
	// fullwidth) still matches.
	lower := normalizeForMatch(text)
	for _, p := range suspiciousSubstrings {
		if strings.Contains(lower, p.sub) {
			add(p.rule, `matched "`+p.sub+`"`)
		}
	}

	return findings
}

// sanitizeForModel removes content the model must not act on before issue text
// is sent to Gemini: HTML comments (hidden in the GitHub UI) and base64 blobs
// (data: URIs, long runs). Nothing is decoded - payloads are removed, not
// inspected. Legit K8s/Vault base64 (kubeconfig, Secret data, certs) is
// neutralized the same way and never shipped to the third-party API. Returns
// the cleaned text and the number of substitutions.
func sanitizeForModel(s string) (string, int) {
	n := 0
	repl := func(placeholder string) func(string) string {
		return func(string) string {
			n++
			return placeholder
		}
	}
	s = reHTMLComment.ReplaceAllStringFunc(s, repl(redactedCommentPlaceholder))
	s = reRedactDataURI.ReplaceAllStringFunc(s, repl(redactedBlobPlaceholder))
	s = reRedactBlob.ReplaceAllStringFunc(s, repl(redactedBlobPlaceholder))
	return s, n
}

func formatHeuristicSummary(findings []scanFinding) string {
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Rejected by deterministic prefilter (not sent to the model). Hits: ")
	for i, f := range findings {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s (%s)", f.Rule, f.Detail)
	}
	return b.String()
}
