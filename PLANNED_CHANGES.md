## Plan: ACP Supervisor Orchestration

This document is the implementation spec for a coding agent. Each step must be completed in order. Function names are authoritative identifiers — locate them by name, not by line number, since file splits and refactors change line positions.

Replace the current free-running demo agents with a supervisor-controlled ACP execution model. The supervisor reads project instructions and workflow definition, generates an execution plan via an ACP session with GitHub Copilot in planning-only mode, then delegates repository branch setup to a dedicated worker agent before running remaining workers serially through ACP over stdio. Worker output is persisted and streamed into the UI. Project start/stop controls only the supervisor/project lifecycle. Restart resumes by re-running the last worker agent with bounded saved context (last 50 activities, summary capped at 4000 chars) rather than letting the supervisor or UI act on files directly. Any ACP failure causes a fail-stop: the supervisor persists a checkpoint and halts; the user restarts manually.

**Steps**

### Phase 0: Centralize styles and split model.go

1. Run `go get github.com/lrstanley/bubbletint@latest` to add the bubbletint theme library. Create `internal/ui/styles.go` as the single source of truth for all lipgloss colors, styles, and theme constants. Use `tint.TintBirdsOfParadise` as the initial color palette — derive all named color constants (`FocusedColor`, `HeaderFg`, `SelectedBg`, `ErrorColor`, etc.) from the tint's exported colors (e.g. `BrightPurple()`, `Green()`, `Red()`, `Fg()`, `Bg()`). Export style variables built from these colors (e.g. `HeaderStyle`, `CellStyle`, `SelectedFocusedStyle`, `SelectedBlurredStyle`, `EditorBorderStyle`, `ButtonActiveStyle`, `ButtonInactiveStyle`, `ErrorStyle`) so that every package imports styles from one place. To switch the app's theme later, change the single `tint.TintBirdsOfParadise` reference to another bubbletint tint. Move the hardcoded style construction from `newTableStyles()` in [cmd/app/main.go](cmd/app/main.go) into this file. Update [cmd/app/main.go](cmd/app/main.go) and [internal/logic/model.go](internal/logic/model.go) to use the exported styles instead of constructing their own. All existing tests must pass unchanged after this extraction.

2. Split [internal/logic/model.go](internal/logic/model.go) into four files before any behavioral changes. The file assignment is:

| File | Contents |
|------|----------|
| `model.go` | `Model` struct, `NewModel()`, `Init()`, `Update()`, `View()`, `tableViews()`, `updateTables()`, `ResizeTables()`, `SetFocus()`, `handleKeyPress()`, selection helpers (`selectedProjectIndex`, `selectedProject`, `isCreateProjectSelected`, `selectedAgentIndex`, `selectedAgent`, `selectedProjectSummary`, `selectedProjectWorkplaceSummary`), refresh helpers (`refreshProjectTable`, `refreshSelectedProjectTables`), persistence wrappers (`persistProjectState`, `persistProjectAgentStates`, `persistActivity`), `Shutdown()`, `parseAgentState`, resize helpers, type definitions (`viewMode`, `ActivityEntry`, `Project`, `Keymap`, `ActivitySource`, `activityMsg`) |
| `editor.go` | `projectEditor` struct, `newProjectEditor()`, `openSelectedProjectEditor()`, `closeSelectedProjectEditor()`, `setProjectEditorFocus()`, `projectEditorView()`, `projectEditorOKButtonView()`, `projectEditorOKButtonBounds()`, `updateProjectEditor()`, `saveSelectedProjectEditor()`, `resizeProjectEditor()`, `refreshProjectAndSelection()` |
| `lifecycle.go` | `startSelectedProject()`, `stopSelectedProject()`, `completeSelectedProject()`, `startSelectedAgent()`, `stopSelectedAgent()`, `completeSelectedAgent()`, `cycleSelectedProjectState()`, `cycleSelectedAgentState()` |
| `activity.go` | `recordActivity()`, `BuildActivitySources()`, `waitForActivity()` |

Move tests into matching `_test.go` files. Shared test helpers (`mustUpdateModel`, `newTestRepository`) go into `helpers_test.go`. All existing tests must pass unchanged after the split — this is a pure file reorganization with zero behavioral changes.

### Phase 1: Stabilize boundaries and threading

3. Remove or isolate the sample/demo assumptions in [cmd/app/main.go](cmd/app/main.go), especially `sampleProjects`, `seedSampleProjects`, and `restoreAgentState`, so runtime state comes from persisted project data and future supervisor/runtime factories instead of hardcoded worker instances.

4. Thread `context.Context` through the Model and worker layer. Pass a cancellable context from `main` into `Model`, propagate it to every supervisor and worker goroutine, and use it as the single shutdown/cancellation signal instead of bare `chan struct{}` in [internal/workers/agent.go](internal/workers/agent.go).

5. Split the current overloaded project model into persisted configuration versus runtime execution state. The target structs are:
   - `ProjectConfig` — persisted fields: ProjectID, Name, Workplace, Instructions, SelectedAgentTypes (ordered `[]string`). Lives in `internal/logic/`.
   - `ProjectRuntime` — transient fields: supervisor state, current worker index, live ACP session handle, per-run `EventBus` instance. Lives in `internal/logic/`.
   - `ProjectState` — composite struct holding both `ProjectConfig` and `*ProjectRuntime` (nil when project is stopped). `Model` holds a slice of `ProjectState`.
   - `data.Project` in the repository package remains the persistence DTO and maps to/from `ProjectConfig`.

   This step blocks the rest because the UI, repository, and supervisor all need the same canonical model.

### Phase 2: Persistence and discovery

6. Extend persistence in [internal/data/repository.go](internal/data/repository.go) to support workflow definitions, allowed agent types, supervisor progress, and resumable runs. Keep the existing projects/agents/activities tables as the base, but add:
   - Project workflow as an ordered agent list: a normalized `project_workflow_steps` table with columns `(project_id, step_order, agent_type)`. For the first implementation there is no separate "workflow" entity — the ordered agent list *is* the workflow, stored per project. Users reorder and select agent types in the editor; there is no standalone workflow editor.
   - Supervisor runs: a `supervisor_runs` table with columns `(id, project_id, status, current_step_index, transcript_summary TEXT, started_at, updated_at)`. The `transcript_summary` column stores bounded resume context (last 50 activities, capped at 4000 chars).
   - Durable activity/event records using the existing `activities` table.

   Prefer normalized tables rather than hiding everything inside the current `instructions` field.

7. Add startup discovery of available worker agent definitions. Create a new package `internal/discovery/` exporting:
   - `AgentDefinition` struct: `Name string`, `InstructionsPath string`, `LaunchConfig LaunchConfig`.
   - `LaunchConfig` struct: `Binary string`, `Args []string`, `Env map[string]string`.
   - `LoadAgentDefinitions(rootDir string) ([]AgentDefinition, error)` — scans `<rootDir>/agents/` directory.

   Each `./agents/<name>/` folder must contain an `instructions.md` file (the system prompt for that worker). An optional `agent.json` in the folder overrides launch configuration. A global `./agents/copilot.json` provides the default launch config used when no per-folder `agent.json` exists. The `LaunchConfig` fields are `binary` (resolved via PATH), `args` (string list), and `env` (optional environment map). The default is `binary: "copilot"`, `args: ["--acp"]`, since Copilot is the ACP agent being launched. Per-folder `agent.json` uses the same shape and overrides all fields.

   Discovery rejects projects that reference agent folders not present on disk. Import `internal/discovery/` from `cmd/app/main.go` and `internal/supervisor/`.

   **Initial `./agents/` directory contents.** Create the following agent folders during implementation. Each folder contains an `instructions.md` (the system prompt for that worker). The global `copilot.json` sits at `./agents/copilot.json` with the default launch config `{ "binary": "copilot", "args": ["--acp"] }`.

   | Folder | Role | `instructions.md` focus |
   |--------|------|------------------------|
   | `branch-setup/` | Repository preparation | Create or check out the project branch (`<project-slug>/<run-id>`), ensure clean working tree, pull latest from main. Already referenced in step 11 — always auto-prepended by the supervisor. |
   | `planner/` | Task decomposition | Break down the project instructions into small, concrete, independently-completable tasks. Output a numbered task list with clear acceptance criteria. Does not write code — only produces the plan consumed by subsequent workers. |
   | `frontend-developer/` | UI / frontend implementation | Implement frontend features: components, styling, layouts, client-side logic, accessibility. Follow project conventions and existing patterns. |
   | `backend-developer/` | Server / API / data implementation | Implement backend features: APIs, database queries, business logic, data models, server configuration. Follow project conventions and existing patterns. |
   | `tester/` | Test authorship and validation | Write unit, integration, and end-to-end tests for implemented features. Run the existing test suite, report failures, and fix regressions introduced by earlier workers. |
   | `code-reviewer/` | Quality and security review | Review recent changes for correctness, style, potential bugs, security issues, and adherence to project conventions. Output actionable findings; optionally apply safe fixes. |
   | `docs-writer/` | Documentation | Write or update documentation: README, API docs, inline comments, architecture decision records, changelog entries. |
   | `devops/` | CI / CD and infrastructure | Create or update build scripts, CI pipelines, Dockerfiles, deployment configs, and environment setup. Ensure the project can be built and deployed reproducibly. |

   The `planner/` agent is distinct from the supervisor's own planning step (step 10). The supervisor's plan is a high-level execution schedule (which workers run in what order); the `planner/` worker produces fine-grained implementation tasks that downstream workers consume. Projects are free to omit `planner/` from their workflow if the instructions are already granular enough.

   Agent folders are committed to the repository so every checkout has the same set. The user selects and orders them per project via the editor multiselect (step 14). `branch-setup/` is always auto-prepended regardless of selection.

### Phase 3A: ACP client wrapper

8. Run `go get github.com/coder/acp-go-sdk@latest` to add the ACP SDK dependency. Pin the resolved version in `go.mod`.

9. Build an ACP client wrapper in a new package `internal/acp/`. The wrapper should launch worker processes (GitHub Copilot) over stdio, create a client connection with `NewClientSideConnection`, call `Initialize`, `NewSession`, and `Prompt`, capture `SessionUpdate` streaming events, map `Cancel` to project stop/shutdown, and persist streamed text/tool status into the activity log. Bounded transcript context for resume is injected as a preamble in the `Prompt` call's user message using this format:
   ```
   --- Resume context (last N activities) ---
   <activity lines>
   --- End resume context ---
   <original prompt>
   ```
   Start with fresh sessions; treat ACP unstable resume support as optional enhancement rather than the primary recovery path.

### Phase 3B: Supervisor and sequencing

10. Create a new package `internal/supervisor/` for the supervisor runtime. The supervisor is the only component allowed to start worker agents. It imports `internal/acp/` for ACP sessions and `internal/discovery/` for agent definitions. Reuse the existing event fan-out pattern from `BuildActivitySources` and `recordActivity` (locate by function name in `internal/logic/`), but replace direct table actions such as `startSelectedProject`, `stopSelectedProject`, `startSelectedAgent`, and `stopSelectedAgent` with supervisor commands. The supervisor runs in planning mode: it opens an ACP session with GitHub Copilot using a planning-only system prompt (instructing the agent to produce a structured execution plan and never perform file/terminal actions), reads project instructions and workflow definition, and generates or updates the execution plan. `Model` in `internal/logic/` owns a supervisor instance per running project.

11. Treat branch preparation as the first supervised worker step after planning. Branch-setup is a standard `./agents/branch-setup/` folder with its own `instructions.md` containing git checkout/create instructions. It appears in discovery and in the editor's multiselect list but is automatically prepended by the supervisor regardless of user selection. The supervisor always runs: plan → branch-setup → user-selected agents in order. The worker creates or checks out a branch named `<project-slug>/<run-id>` so resume/restart can find the expected branch deterministically.

12. Define the supervisor sequencing policy and failure strategy. Default: a project stores an ordered list of selected worker agent types via `project_workflow_steps`; the supervisor always runs planning first, then the branch-setup worker, then the remaining runnable workers from that ordered list; when a worker finishes, the supervisor converts its final output into the next worker's prompt context and starts only the next worker. **Failure strategy:** fail-stop with checkpoint — any ACP error, worker crash, or unexpected disconnect causes the supervisor to persist a checkpoint into `supervisor_runs` (current step index, last worker transcript bounded to last 50 activities / 4000 char summary) and halt. The user restarts the project manually, and the supervisor resumes from the checkpointed step.

### Phase 4: UI refactoring

13. Refactor the UI in `internal/logic/model.go` to reflect supervisor ownership. Remove direct per-agent start/stop controls from the main table actions; project start/stop remains. Update the project table to show project plus supervisor status, update the agent table to show configured worker agents and their latest state/output summary, and keep the activity table as the detailed stream. Preserve the existing custom table component in [internal/ui/table/table.go](internal/ui/table/table.go) because its per-cell selected styling is already correct for multi-column rows. All color and style references must use `internal/ui/styles.go` — do not hardcode lipgloss styles inline.

14. Expand the project editor from three inputs plus OK into a richer configuration screen. Keep the current name, workplace, and instructions fields, but add: a cancel button alongside save, and a multiselect list of worker agent types derived from discovered `./agents/` folders. The multiselect controls which worker agent types the supervisor is allowed to use for that project, and their drag/selection order defines the execution order stored in `project_workflow_steps`. Save validates that all selected agent types exist on disk.

15. Rework editor focus and tests around the new controls. The current focus cycle in `setProjectEditorFocus`, OK-only button rendering in `projectEditorOKButtonView`, and click handling in `updateProjectEditor` will become a small editor state machine that supports list navigation, checkbox toggling, explicit Save and Cancel actions, and restoring existing selections when editing a project.

### Phase 5: Worker and event refactoring

16. Replace the current demo worker implementation in [internal/workers/agent.go](internal/workers/agent.go) with a thinner abstraction: worker run state and supervisor-facing events. Keep the state enum concept (`Stopped`, `Running`, `Completed`) and the event publishing pattern. Remove the synthetic ticker loop, the `loop()` goroutine, and the idea that workers self-run independently of the supervisor. The actual ACP execution lives in `internal/acp/`; this package provides only the state model and event types consumed by the supervisor and UI.

17. Strengthen activity/event modeling. Reuse [internal/events/eventbus.go](internal/events/eventbus.go). The existing `Unsubscribe` method is retained but not relied upon for cleanup — per-run `EventBus` instances are the primary cleanup mechanism; old instances and their channels are garbage-collected when the supervisor run completes. Instantiate a new `EventBus` per supervisor run. Move from plain string `Event.Payload` toward typed execution events so the UI and persistence can distinguish supervisor plan updates, worker message chunks, tool calls, completion, cancellation, and failure.

### Phase 6: Tests

18. Update and expand tests. Start from the existing tests in:
   - `internal/logic/` (split across `model_test.go`, `editor_test.go`, `lifecycle_test.go`, `activity_test.go`, `helpers_test.go` after Phase 0)
   - [internal/data/repository_test.go](internal/data/repository_test.go)
   - [internal/workers/agent_test.go](internal/workers/agent_test.go)
   - [internal/ui/table/table_test.go](internal/ui/table/table_test.go)

   Add coverage for: `./agents/` discovery and `copilot.json` loading, `project_workflow_steps` persistence round-trip, supervisor-only orchestration, fail-stop checkpoint persist and resume, bounded transcript injection format, cancel vs save in the project editor, multiselect selections round-trip through persistence, style constants importable from `internal/ui/styles.go`, and streamed ACP activity handling via fake ACP client.

**New packages introduced**

| Package | Purpose |
|---------|---------|
| `internal/ui/styles.go` | Centralized lipgloss colors and styles derived from `github.com/lrstanley/bubbletint` (`tint.TintBirdsOfParadise`). Single file to edit for visual changes or theme swaps. |
| `internal/discovery/` | `./agents/` folder scanner. Exports `AgentDefinition`, `LaunchConfig`, `LoadAgentDefinitions()`. |
| `internal/acp/` | ACP client wrapper around `github.com/coder/acp-go-sdk`. Handles stdio launch, session lifecycle, streaming, cancellation, transcript injection. |
| `internal/supervisor/` | Supervisor runtime. Imports `internal/acp/` and `internal/discovery/`. Owns planning session, worker sequencing, fail-stop checkpointing. |

**Relevant files**
- [cmd/app/main.go](cmd/app/main.go) — replace demo seeding/runtime restoration with repository-backed project loading, discovery, and supervisor construction. Import styles from `internal/ui/styles.go`.
- `internal/ui/styles.go` **(new)** — all lipgloss color constants derived from bubbletint `tint.TintBirdsOfParadise`, table styles (focused/blurred header, cell, selected), editor styles (border, button active/inactive, error). Swap the tint to change the entire app's palette.
- [internal/logic/model.go](internal/logic/model.go) — split into `model.go`, `editor.go`, `lifecycle.go`, `activity.go` in Phase 0; then refactor around supervisor ownership. Holds `ProjectConfig`, `ProjectRuntime`, `ProjectState`.
- `internal/logic/helpers_test.go` **(new after Phase 0)** — shared test helpers `mustUpdateModel` and `newTestRepository`.
- [internal/data/repository.go](internal/data/repository.go) — migration and persistence API expansion: `project_workflow_steps`, `supervisor_runs` tables, bounded transcript storage.
- [internal/data/repository_test.go](internal/data/repository_test.go) — migration and persistence tests for the new normalized tables and resume semantics.
- `internal/discovery/` **(new)** — `./agents/` folder scanner, `copilot.json` loader.
- `internal/acp/` **(new)** — ACP client wrapper (stdio launch, session lifecycle, streaming, transcript injection).
- `internal/supervisor/` **(new)** — supervisor runtime (planning session, worker sequencing, fail-stop checkpoint/resume).
- [internal/workers/agent.go](internal/workers/agent.go) — slim down to state enum + event types; remove ticker loop.
- [internal/workers/agent_test.go](internal/workers/agent_test.go) — adapt tests to supervisor-facing state changes.
- [internal/events/eventbus.go](internal/events/eventbus.go) — retain existing API including `Unsubscribe`; instantiate per supervisor run for cleanup.
- [internal/ui/table/table.go](internal/ui/table/table.go) — preserve the reusable table widget.
- [internal/ui/table/table_test.go](internal/ui/table/table_test.go) — protect rendering behavior.

**Verification**
1. Phase 0 gate: all existing tests pass unchanged after style extraction and model.go split, with zero behavioral changes.
2. Add repository migration tests proving old databases migrate cleanly and new tables (`project_workflow_steps`, `supervisor_runs`) persist workflow definitions, selected worker agent types, supervisor checkpoint state, and bounded activity history.
3. Add model tests proving: project start launches only the supervisor path; direct agent start/stop is no longer available from the UI; stop cancels the current worker via context cancellation; restart resumes from the saved checkpoint by re-running the last worker with bounded saved context.
4. Add project editor tests proving: cancel discards edits, save persists edits, multiselect selections round-trip through `project_workflow_steps`, and missing `./agents/` folders are rejected with visible errors.
5. Add worker/supervisor tests with a fake ACP client proving streamed `SessionUpdate` events become activity rows, planning always happens before work execution, branch-setup runs as the first worker, worker completion triggers the next worker, cancellation terminates the in-flight run cleanly, and ACP failure triggers fail-stop with persisted checkpoint in `supervisor_runs`.
6. Run the full Go test suite and at least one manual end-to-end scenario: create project, select worker agents (order defines workflow), start project, observe the supervisor create a plan via Copilot in planning mode, observe the branch-setup worker create or check out the project branch in the workplace repo, observe streamed activity, stop mid-run, restart project, and confirm the supervisor replays the last worker from persisted context and continues serial orchestration.

**Decisions**
- Included scope: centralized styles (`internal/ui/styles.go`), model.go split (Phase 0), `context.Context` threading, supervisor-planned orchestration via ACP session with GitHub Copilot in planning-only mode, ACP client integration (`internal/acp/`), workflow as ordered agent list per project (`project_workflow_steps` table), project editor cancel plus multiselect, durable fail-stop resume with bounded transcript, branch preparation as a delegated `./agents/branch-setup/` worker, per-run `EventBus` instances, and streaming worker activity in the existing TUI.
- Excluded from the first implementation: parallel worker execution, arbitrary ad hoc agent start/stop from the UI, standalone workflow entities or workflow editor (ordered agent list is the workflow), and making the supervisor itself perform code/file or git changes.
- The existing `Unsubscribe` method on `EventBus` is retained but not relied upon for cleanup. Per-run `EventBus` instances are the primary cleanup mechanism; old instances and their channels are garbage-collected when the supervisor run completes.
- Recommended worker folder contract: each `./agents/<name>/` folder provides `instructions.md` (system prompt) plus an optional `agent.json` (launch config override). A global `./agents/copilot.json` supplies the default launch config: `binary: "copilot"`, `args: ["--acp"]`.
- Recommended resume semantics: restart creates a fresh ACP session for the interrupted worker. Bounded transcript (last 50 activities, summary capped at 4000 chars) is prepended to the `Prompt` user message as a `--- Resume context ---` block. Unstable ACP session-resume APIs can be added later.
- Recommended sequencing rule: plan → `branch-setup` (auto-prepended) → user-selected agents in `project_workflow_steps` order. Explicit handoff of one worker's output into the next worker's prompt.
- Recommended failure strategy: fail-stop with checkpoint — any ACP failure persists current step index and bounded transcript into `supervisor_runs`, then halts. User restarts manually.

**Further Considerations**
1. Workflow authoring starts simple: the ordered agent list in `project_workflow_steps` *is* the workflow. Richer branching or gating rules come only after the serial supervisor loop is stable.
2. The `branch-setup` worker uses a deterministic branch name: `<project-slug>/<run-id>`, so resume/restart can find the expected branch without guessing.
3. The project editor likely needs a dedicated checkbox-list component instead of overloading [internal/ui/table/table.go](internal/ui/table/table.go); otherwise the table widget starts absorbing editor-specific behavior.
4. ACP permissions and terminal/file capabilities should default to the minimum required per worker type, because the supervisor model is explicitly intended to orchestrate safely rather than grant broad ambient access.
5. The supervisor's planning ACP session uses a system prompt that explicitly instructs Copilot to output a structured plan (step names, worker assignments, expected outcomes) and forbids file/terminal actions. This prompt lives in the codebase (e.g. embedded string in `internal/supervisor/`), not in `./agents/`, since the supervisor is not a user-configurable worker.
6. `internal/ui/styles.go` is the single place to change colors and lipgloss styling. Colors are derived from `github.com/lrstanley/bubbletint` using `tint.TintBirdsOfParadise` as the initial theme. To change the app's palette, swap the tint to any other bubbletint tint (e.g. `tint.TintDracula`, `tint.TintGruvboxDark`). No other file should construct `lipgloss.NewStyle()` with hardcoded color values — all visual tuning happens by editing the exported constants and style variables in that one file.