# Inference flat API contract

The Model version response supplies `engine_image`, `engine_type`, and the
complete startup `command` as UI defaults. The create request is authoritative:
the final values submitted by the caller are forwarded and persisted as the
Inference configuration snapshot. Changing a Model version later does not
rewrite an existing Inference service.

The public command is one string, for example:

```text
exec vllm serve /models --served-model-name qwen2.5-1.5b-instruct
```

The image must contain `/bin/sh`. The Kubernetes rendering layer wraps the
string exactly once as:

```yaml
command: ["/bin/sh", "-c"]
args:
  - exec vllm serve /models --served-model-name qwen2.5-1.5b-instruct
```

Governance and Inference never parse the string, append arguments, inject an
engine flag, or expose Kubernetes `command[]`/`args[]` arrays. The caller owns
the complete shell command and should use `exec` so the engine receives
termination signals.

`runtime_mode` defaults to the KServe Deployment path. Use
`RUNTIME_MODE_LEADER_WORKER_SET` only for distributed serving; its
`worker_replicas` value applies to every worker set group. For GPU requests,
the public selection contains only `cluster_id`, `pool_id`, `profile_id`, and
`profile_version`. Deployment GPU Pod demand is `replicas`; LeaderWorkerSet
demand is `replicas * (worker_replicas + 1)`. The backend derives container
names and device counts. Omitting `gpu` means no Accelerator admission path is
called.

Get and List return Governance's flat `InferenceView`. It contains the
identity, desired state, generation, model version, served model name, phase,
health, observation time, final engine and resource values, runtime mode,
worker count, and the four GPU selection fields. Internal `resource`,
`runtime`, `engine`, `ModelArtifact`, accelerator attachments, and shell arrays
are not part of this public response.
