# Job commands shorthand

The user approved supporting both Job-level `commands` and explicit `steps`.
The former suits a single script; the latter supports distinct execution images,
timeouts, working directories and log segments. Each step is a separate container
sharing the Job workspace, not a shared shell session.

Add only the shorthand to the YAML Job model. Compilation produces the existing
PlanJob shape: one implicit step named `commands`, using the Job's image and
environment. Existing explicit steps retain their names and behavior. No Runner,
database, queue, resource-default, permission or UI changes are needed.

Reject both non-null execution forms together, including empty lists. A null
value is equivalent to omission. Reject missing/empty commands and missing images
using the existing validation principles. Preserve strict unknown-field and
trigger-policy validation. Job commands are normalized before canonical hashing;
equivalent explicit `steps: [{name: commands, commands: ...}]` yields the same
configuration digest. Resources remain optional with existing defaults.

Keep existing live YuanPlan YAML valid. Update the Server after tests/reviews so
both forms are available; do not create another production application release
just to demonstrate equivalent syntax.
