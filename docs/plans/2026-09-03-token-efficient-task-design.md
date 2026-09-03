# Token-efficient development task design

Status: approved by the user on 2026-09-03.

## Goal

Finish YuanCI v1 through manually triggered, context-bounded Codex tasks. Each
task must deliver one independently testable change, one commit, and a concise
handoff. A new Codex conversation starts the next task so repository state,
rather than chat history, is the source of truth.

## Unit of work

The normal unit is 20–45 minutes, one capability, three to eight hand-written
files, focused tests, and one commit. Generated bindings and migration fixtures
do not count toward the file guideline. If safe completion requires an unrelated
capability or substantially exceeds the guideline, the agent stops and proposes
a new task ID instead of silently expanding scope.

Every task has one primary model class:

- `T`: GPT-5.6 Terra, medium reasoning; default implementation choice.
- `S`: GPT-5.6 Sol, medium reasoning; security, concurrency, transactions,
  migrations, state machines, and final review.
- `L`: GPT-5.6 Luna, low or medium reasoning; documentation and bounded
  mechanical UI work.

## Execution contract

At the start of a task, read only the named task entry, its direct dependencies,
the latest five commits, repository status, and directly relevant source files.
Exclude generated web output, dependency trees, caches, coverage output, and
vendor directories from searches.

Use tests in this order:

1. add or update the smallest failing focused test;
2. implement only the named capability;
3. run the affected package or component tests;
4. run one broader verification only when the task entry marks a phase gate;
5. run `git diff --check`, update the development log when material, commit, and
   push only after tests pass.

Do not stream full successful build output into the conversation. Preserve full
logs outside model context and report the failing excerpt or final summary only.
Do not repeatedly rebuild deployment images inside ordinary implementation
tasks.

## Ordering and safety

Dependencies in the atomic task plan are mandatory. GitHub-only CI Alpha is the
first usable target, followed by complete CI, secrets, remaining SCM providers,
console administration, protected CD, and release qualification. Production
claims remain forbidden until every release gate is complete.

External credentials, real Git provider sandboxes, cross-host machines, the
72-hour soak, and third-party review are operator-owned gates. An agent prepares
automation and checklists but never invents credentials or marks those gates
complete without evidence.

## Handoff format

Each task ends with: task ID, outcome, commit SHA, focused tests, phase-gate
tests if any, remote CI URL/result, remaining manual check, and exact next task
ID. The working tree must be clean unless an explicit blocker is reported.

