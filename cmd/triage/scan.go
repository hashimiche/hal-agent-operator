/*
Copyright 2026 HAL.

Deterministic prefilter for the triage worker. Two responsibilities, kept apart:

  - scanSuspicious: hard, high-signal detectors (prompt injection, secret exfil,
    impersonation, zero-width, shell exfil). A hit forces suspicious=true and the
    caller skips the model entirely.
  - sanitizeForModel: redact-and-send. HTML comments and base64 blobs are removed
    from the text BEFORE it reaches the model. Nothing is ever decoded — the model
    cannot act on content it never receives. Base64 detection is whitespace-aware
    (chunking a blob across spaces/newlines does not defeat it) and signal-based
    (needs +/= or a high uppercase ratio) so hex commit SHAs and version strings
    survive for the model to reason about.
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

	// A base64 "block" (whitespace stripped) must reach this many chars before it
	// is redacted. ~40 covers a typical encoded injection phrase (~44 chars) while
	// sparing short IDs and inline tokens.
	base64MinLen = 40
	// Minimum uppercase fraction (of non-space chars) for an all-alnum block to be
	// treated as base64 rather than prose. Random base64 is ~40% uppercase; prose
	// (incl. Title Case) sits well below this. Blocks with +/= skip the ratio test.
	base64UpperRatio = 0.25
)

var (
	// RE2 \x{...} hex syntax keeps the pattern free of literal invisible chars.
	reZeroWidth = regexp.MustCompile(`[\x{200B}\x{200C}\x{200D}\x{2060}\x{FEFF}]`)
	// curl/wget leaking env or secret-named vars.
	reShellExfil = regexp.MustCompile(`(?i)(curl|wget)\b[^;\n]{0,160}\b(env|\$\{?\w*(TOKEN|SECRET|KEY|PASSWORD|KUBECONFIG)\w*\}?)`)

	// Redact-and-send patterns: stripped from model input, never decoded.
	reHTMLComment   = regexp.MustCompile(`(?is)<!--.*?-->`)
	reRedactDataURI = regexp.MustCompile(`(?i)data:[^;\s]+;base64,[A-Za-z0-9+/=]+`)
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
	// Normalized copy (zero-width stripped, NFKC, lowercased) so that trivial
	// evasion (zero-width, fullwidth) still matches the substring and shell rules.
	norm := normalizeForMatch(text)

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

	// Zero-width must fire on the raw text (normalization would strip it).
	if reZeroWidth.MatchString(text) {
		add("zero_width", "zero-width / invisible Unicode characters")
	}
	// Shell exfil runs on raw AND normalized so fullwidth "ｃｕｒｌ" is caught too.
	if reShellExfil.MatchString(text) || reShellExfil.MatchString(norm) {
		add("shell_exfil", "curl/wget referencing env or secret-like variables")
	}

	for _, p := range suspiciousSubstrings {
		if strings.Contains(norm, p.sub) {
			add(p.rule, `matched "`+p.sub+`"`)
		}
	}

	return findings
}

// sanitizeForModel removes content the model must not act on before issue text
// is sent to Gemini: HTML comments (hidden in the GitHub UI) and base64 blobs
// (data: URIs, long runs, incl. runs chunked across whitespace). Nothing is
// decoded - payloads are removed, not inspected. Legit K8s/Vault base64
// (kubeconfig, Secret data, certs) is neutralized the same way and never shipped
// to the third-party API. Returns the cleaned text and the number of substitutions.
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
	s, m := redactBase64Blocks(s)
	return s, n + m
}

// isBase64Byte reports whether b is in the standard base64 alphabet. base64url
// (`-`/`_`) is intentionally excluded: `-`/`_` are common in prose (hyphenation,
// snake_case) and would cause false positives; standard base64 is what pasted
// kubeconfig / Secret data / encoded injections use.
func isBase64Byte(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		return true
	case b == '+', b == '/', b == '=':
		return true
	default:
		return false
	}
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

// redactBase64Blocks finds maximal spans of base64-alphabet characters
// (whitespace between chunks is tolerated so splitting a blob across lines does
// not evade detection) and redacts each span that looks like encoded data. A
// span qualifies when, ignoring whitespace, it is at least base64MinLen chars
// AND either contains +/= or has a high uppercase ratio. Prose (lowercase words,
// Title Case, ALL CAPS, hex SHAs, version strings) does not qualify and passes
// through untouched. All ASCII, so byte indexing is safe.
func redactBase64Blocks(s string) (string, int) {
	var b strings.Builder
	n := 0
	i := 0
	for i < len(s) {
		if !isBase64Byte(s[i]) {
			b.WriteByte(s[i])
			i++
			continue
		}
		// Extend a span over base64 chars, absorbing interior whitespace only when
		// it is followed by more base64 chars (trailing whitespace is left out).
		start, end := i, i
		j := i
		for j < len(s) {
			if isBase64Byte(s[j]) {
				j++
				end = j
				continue
			}
			if isSpaceByte(s[j]) {
				k := j
				for k < len(s) && isSpaceByte(s[k]) {
					k++
				}
				if k < len(s) && isBase64Byte(s[k]) {
					j = k
					continue
				}
			}
			break
		}
		span := s[start:end]
		if looksBase64Block(span) {
			b.WriteString(redactedBlobPlaceholder)
			n++
		} else {
			b.WriteString(span)
		}
		i = end
	}
	return b.String(), n
}

// looksBase64Block decides whether a base64-alphabet span (possibly containing
// interior whitespace) is encoded data rather than natural-language prose.
func looksBase64Block(span string) bool {
	var nonSpace, upper, lower, special int
	for i := 0; i < len(span); i++ {
		switch b := span[i]; {
		case b >= 'A' && b <= 'Z':
			upper++
			nonSpace++
		case b >= 'a' && b <= 'z':
			lower++
			nonSpace++
		case b >= '0' && b <= '9':
			nonSpace++
		case b == '+' || b == '/' || b == '=':
			special++
			nonSpace++
		}
	}
	if nonSpace < base64MinLen {
		return false
	}
	if special > 0 {
		return true
	}
	// No +/=: require a genuine mixed-case, high-uppercase signal so prose
	// (lowercase, Title Case, ALL CAPS, hex SHAs) is left alone.
	return upper > 0 && lower > 0 && float64(upper) >= base64UpperRatio*float64(nonSpace)
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
