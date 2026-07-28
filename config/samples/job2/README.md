# Job 2 KinD samples (fixture fork)

Pre-approved CRs for the Job 2 POC: issue snapshot + `approved: true` +
`baseBranch: fixture/bugN`. After Job 1 succeeds, the controller advances
`PendingValidation` → `Ready` → fix Job **without** a manual patch.

(Production keeps `create-cr` at `approved: false`; a separate `"agent go"`
Action patches approval. To demo that gate, use
[`../agent_v1alpha1_issueresolution.yaml`](../agent_v1alpha1_issueresolution.yaml)
and patch as in [`POC.md`](../../../POC.md) Step 6.)

| File | Issue | `baseBranch` | Expected Job 2 |
|---|---|---|---|
| `issue-5.yaml` | #5 | `fixture/bug1` | mono-file (should succeed) |
| `issue-6.yaml` | #6 | `fixture/bug2` | mono-file |
| `issue-7.yaml` | #7 | `fixture/bug3` | mono-file |
| `issue-8.yaml` | #8 | `fixture/bug4` | **multi-file** (POC limit — expect fail/retry) |

## POC flow

1. Rebuild + reload images, Helm upgrade / rollout (operator **and** fix image).
2. `kubectl apply -f config/samples/job2/issue-5.yaml`
3. Wait Job 1 → GitHub triage comment + labels → briefly `PendingValidation` → `Ready` → `Executing`.
4. Job 2 → PR `bugfix/**` → base `fixture/bugN`.

```bash
kubectl apply -f config/samples/job2/issue-5.yaml
kubectl -n hal-agent get issueresolutions -w
```
