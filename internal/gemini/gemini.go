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

// Package gemini provides a thin Gemini API helper shared by triage and fix workers.
package gemini

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// CallOptions configures a single GenerateContent request.
type CallOptions struct {
	// ResponseMIMEType defaults to "application/json" when empty.
	ResponseMIMEType string
	// MaxOutputTokens defaults to 1024 when zero.
	MaxOutputTokens int32
}

// Call sends a system+user prompt to Gemini and returns the raw text response.
func Call(ctx context.Context, apiKey, model, system, user string, opts CallOptions) (string, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return "", fmt.Errorf("create gemini client: %w", err)
	}

	mime := opts.ResponseMIMEType
	if mime == "" {
		mime = "application/json"
	}
	maxTokens := opts.MaxOutputTokens
	if maxTokens == 0 {
		maxTokens = 1024
	}

	temp := float32(0)
	cfg := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: system}},
		},
		Temperature:      &temp,
		MaxOutputTokens:  maxTokens,
		ResponseMIMEType: mime,
	}

	resp, err := client.Models.GenerateContent(ctx, model, genai.Text(user), cfg)
	if err != nil {
		return "", fmt.Errorf("gemini generate: %w", err)
	}

	text := resp.Text()
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("empty model content")
	}
	return text, nil
}

// TruncateRunes truncates s to at most max runes, appending "…" when cut.
func TruncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
