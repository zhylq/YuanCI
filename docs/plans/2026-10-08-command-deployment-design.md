# Event-driven command deployment

The user approved branch-filtered push automation, no mandatory application verification or services, serial execution per deployment environment, a stop action, and no repeated deployment of a commit. This replaces the YuanPlan-specific external publisher as the eventual delivery path; existing production must remain healthy during migration.

Use the existing authenticated Webhook inbox, immutable source checkout, transactional Run graph, Runner commands executor, leases and cancellation API. Implement YAML trigger matching at orchestration. Validate delayed push commits by their immutable SHA in their repository instead of requiring them to remain branch HEAD. Merge pushes follow the same path.

An optional pipeline `deployment.environment` identifies a deployment run. Persist a unique repository/environment/commit registration, reuse it across deliveries, disallow reruns and retries, and retain the record after failure/cancellation. A run-level FIFO environment gate serializes deployment jobs across Runners; ordinary CI remains unaffected. Cancellation and Runner loss must retain the gate until actual execution cleanup is confirmed, avoiding overlap with the next deployment. Lost cleanup confirmation requires operator intervention rather than automatic re-execution.

Commands, optional services, build, backup, migration and health checks remain user-authored. Deployment access is an administrator-controlled Runner policy and cannot be granted by arbitrary YAML. Standard jobs retain their existing isolation. No ACR dependency, database or test stage is injected into user jobs.

The stop button uses existing authenticated cancellation and becomes deployment-aware. Stopping terminates execution; external rollback is a user-script responsibility. A durable at-most-once dispatch contract prevents automatic replay, while arbitrary remote side effects cannot be made transactional by the CI server.

Verification must cover branch mismatch, main/direct/merge/delayed pushes, repeated delivery with distinct IDs, concurrent claims, cancellation cleanup, lost Runner, rerun rejection, standard-job isolation, and actual user-command execution without a database service.
