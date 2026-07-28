# hal-k8s-operator

Kubernetes operator for the HAL issue-resolver agent. Reconciles `IssueResolution`
CRs through a HITL state machine (triage → human `"agent go"` → fix Job → PR).

**LLM / design context:** [`LLM_CONTEXT.md`](LLM_CONTEXT.md) · [`docs/operator-architecture.md`](docs/operator-architecture.md)

## Description

The controller watches `IssueResolution` custom resources and advances
`status.phase`. It never talks to GitHub or Vault — those are the GitHub Action
and the Jobs.

**Current POC (KinD):** apply a CR by hand → controller creates a **triage Job**
that calls **Gemini** → analysis in Job logs + `status.triage`.  
Step-by-step runbook: [`POC.md`](POC.md). Command entry point: `task` ([`Taskfile.yml`](Taskfile.yml)).
Plan of record (resume here): [`LLM_PLAN.md`](LLM_PLAN.md) (triage POC history: [`GROK_PLAN.md`](GROK_PLAN.md)). Helm chart: `charts/hal-k8s-operator`.

## Getting Started

### Prerequisites
- go version v1.24.6+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/hal-k8s-operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/hal-k8s-operator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/hal-k8s-operator:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/hal-k8s-operator/<tag or branch>/dist/install.yaml
```

### Helm chart (OCI on GHCR)

Published on every `v*` git tag by [`.github/workflows/publish.yml`](.github/workflows/publish.yml):

| Artifact | Reference |
|---|---|
| Operator image | `ghcr.io/hashimiche/hal-k8s-operator:<tag>` |
| Fix Job image | `ghcr.io/hashimiche/hal-k8s-operator-fix:<tag>` |
| Helm chart | `oci://ghcr.io/hashimiche/charts/hal-k8s-operator` |

Chart `version` is the tag without the leading `v` (e.g. tag `v0.2.0` → chart `--version 0.2.0`).
`appVersion` and image tags match the full git tag (e.g. `v0.2.0`).

**Install from GHCR** (replace `0.2.0` / `v0.2.0` with your release):

```bash
export GEMINI_API_KEY='...'   # never commit
export GITHUB_TOKEN='ghp_...' # never commit

helm install hal-agent oci://ghcr.io/hashimiche/charts/hal-k8s-operator \
  --version 0.2.0 \
  --namespace hal-agent --create-namespace \
  -f charts/hal-k8s-operator/values-ghcr.yaml \
  --set gemini.apiKey="$GEMINI_API_KEY" \
  --set github.token="$GITHUB_TOKEN"
```

KinD POC with local images still uses [`values.yaml`](charts/hal-k8s-operator/values.yaml) — see [`POC.md`](POC.md).

**Validate a release (after you push a `v*` tag):**

1. Confirm the [Publish workflow](https://github.com/hashimiche/hal-k8s-operator/actions/workflows/publish.yml) succeeded.
2. Confirm images exist: `docker pull ghcr.io/hashimiche/hal-k8s-operator:v0.2.0` (and `-fix`).
3. On a cluster with pull access to GHCR (public packages, or `imagePullSecrets`):
   ```bash
   helm pull oci://ghcr.io/hashimiche/charts/hal-k8s-operator --version 0.2.0
   tar -xzf hal-k8s-operator-0.2.0.tgz
   helm install hal-agent ./hal-k8s-operator \
     -f hal-k8s-operator/values-ghcr.yaml \
     --namespace hal-agent --create-namespace \
     --set gemini.apiKey="$GEMINI_API_KEY" \
     --set github.token="$GITHUB_TOKEN"
   ```
4. For KinD smoke: create cluster, `helm install` as above (no `kind load` needed if GHCR is reachable).
   Run the CR steps in [`POC.md`](POC.md) from step 4 onward.

If GHCR packages are private, add a pull secret to the namespace or make the packages public under org settings.

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

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

