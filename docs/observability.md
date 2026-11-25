# Observability

The control plane exposes `GET /healthz`, `GET /readyz`, and `GET /version`. Liveness checks the HTTP process and readiness checks its database. The Docker build accepts `KUBEREEL_VERSION` and `KUBEREEL_REVISION` build arguments. The build pipeline passes the Git revision and uses the tag for release builds.

Prometheus can scrape `GET /metrics` with a KubeReel API token. The endpoint also accepts an authenticated browser session. Store the token in a file readable by Prometheus.

```yaml
scrape_configs:
  - job_name: kubereel
    metrics_path: /metrics
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/secrets/kubereel/token
    static_configs:
      - targets: ["kubereel-kubereel.kubereel.svc:8080"]
```

| Metric | Meaning |
| --- | --- |
| `kubereel_active_executions` | Active workflows and VideoRuns, refreshed from the controller's state |
| `kubereel_active_jobs` | Nonterminal Jobs belonging to the Helm release |
| `kubereel_executions_completed_total` | Successful executions |
| `kubereel_executions_failed_total` | Failed executions |
| `kubereel_execution_duration_seconds` | Finished execution duration by outcome |
| `kubereel_step_duration_seconds` | Finished VideoRun step attempt duration by outcome |
| `kubereel_step_retries_total` | Additional step attempts reserved |
| `kubereel_artifacts_generated_total` | Published artifacts |
| `kubereel_provider_request_duration_seconds` | Gemini and fal HTTP latency through receipt of headers |
| `kubereel_provider_errors_total` | Provider HTTP attempts with transport errors or non-2xx responses |

Counters and histograms report observations since the control plane process started. The active Job and VideoRun gauges use the existing informer caches and recover after startup synchronization. Worker provider metrics appear after the control plane observes the finished step. A worker killed before it writes its summary, or a Pod removed before collection, can leave those samples unavailable. Provider summaries contain aggregate counts and durations. See [Kubernetes termination messages](https://kubernetes.io/docs/tasks/debug/debug-application/determine-reason-pod-failure/) for the underlying transport.

Application logs use JSON. Control plane execution events include `workflow_id` and `run_id`. Step and Job events add `step_id` and `job_id` when available. The Job submission event includes `work_id`, which links it to the worker's task logs. These identifiers stay in logs. Metric labels use only the finite provider and outcome sets.

F29 covers metrics, logs, and system probes. Distributed trace export is outside this instrumentation.
