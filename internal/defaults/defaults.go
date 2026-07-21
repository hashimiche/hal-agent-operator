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

// Package defaults holds shared POC constants so model IDs are not duplicated
// across cmd/main, cmd/triage, and the controller.
package defaults

// GeminiModel is the default Google AI Studio model for triage Jobs.
// Deliberately a PINNED id, not the rolling "gemini-flash-latest" alias: the
// alias caused intermittent model-unavailability errors, so we pin for
// stability and bump this manually as newer Flash-Lite versions ship.
// Keep in sync with charts/hal-k8s-operator/values.yaml (triage.model), which
// Helm passes to the operator via --gemini-model.
const GeminiModel = "gemini-3.1-flash-lite"

// GeminiSecretName is the default Secret name holding the API key.
const GeminiSecretName = "gemini-api"

// GeminiSecretKey is the default key inside that Secret.
const GeminiSecretKey = "GEMINI_API_KEY"

// TriageImage is the default container image for triage Jobs (KinD POC).
const TriageImage = "hal-k8s-operator:poc"
