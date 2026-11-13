# Phase 20 plan: Findings from daily use

- **Status:** In progress. Written 2026-11-13 with the recommended option for every choice, as Ozan asked
  ("run it like the earlier phases ... with the recommended options, no check-ins, one summary at the end").
- **Roadmap:** [docs/plan.md](../plan.md), Phase 20. Progress: [PROGRESS.md](../../PROGRESS.md).
- **Source:** Ozan's runs of Bouncibo (Goals 03 and 04) and the clinic chatbot.

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

Sandboxed workers are not held up by Claude Code prompts nobody watches. The Tech Lead can look at its
team's work, renders included. A change request on a plan withdraws that version. A dropped network
heals by itself. The office shows the board, and draws windows only on outside walls. Caps say at save
time what they will do.

## What exists and what it means

Measured with Claude Code 2.1.323 in Spike F's sandboxed worker (`spikes/sandbox-claude`, haiku):

- **Dangerous rm prompts.** With `--dangerously-skip-permissions`, a Bash command whose rm target
  Claude Code cannot check (e.g. `rm -rf "$(ls -d dist)"`) still shows "Dangerous rm operation on
  statically-unresolvable target ... Do you want to proceed?" with "Claude Code will automatically
  block this request in 1:59". It fires a **`PermissionRequest` hook** first (`tool_name: Bash`,
  `tool_input.command`, `cwd`, `permission_mode: bypassPermissions`). A hook reply
  `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}` runs
  the command with no prompt; the same reply sent **40 s later** (the dialog on screen meanwhile) is
  honoured too; `"deny"` with a `message` refuses it and the model reads the message. With no reply the
  dialog blocks the command after 2 minutes and the model works around it. Literal paths, `$(pwd)` and
  `D=x; rm -rf "$D/out"` do not prompt at all.
- **Hooks today:** every hook goes through `agent-office hook`, which gives up after 8 s; the daemon's
  socket handler has 5 s. `PermissionRequest` only sets the state to `waiting_permission`.
- **The Tech Lead's workspace** is a read-only clone (ADR 0029). Sub-task and QA branches exist in the
  main repository (imports, ADR 0014), not in the lead's clone, so it cannot list or open their files.
- **Request changes** (Phase 19) stores the request on the version and leaves it `proposed`: the
  Approvals badge, the inbox's "Approve plan" and the Architect's "needs you" stay until the new version.
- **Network loss.** With the upstream cut (a CONNECT proxy in front of the API proxy) **while the answer
  streams**, the turn ends at once, with no retry, through a **`StopFailure` hook** (`error:
  server_error`, `last_assistant_message: "API Error: Connection lost mid-response. The response above
  may be incomplete."`): Ozan's case. Cut before the stream starts, Claude Code retries about 10 times
  over 2 min 15 s (a 60 s cut heals by itself), then ends the same way with "API Error: 502 status code
  (no body) ..." (the API proxy's 502). Either way the agent then waits at its prompt. Claude Code's own texts for a stream cut are
  "Connection lost mid-response", "Part of the response never arrived", "The response stopped arriving",
  "Server error mid-response" and "... before a response was produced" (from the 2.1.323 binary).
  agent-office ignores `StopFailure`'s fields today.
- **The office bar** shows pose counts; the **whiteboard** shows one sticky note per sub-task of the lead's
  goal. The board has five columns over active goals (`boardTasks`) plus QA round cards.
- **Windows** are placed along every room's back wall (default plan: planning, Tech Lead, your office,
  review room, QA lab; custom layouts: every room), around what hangs there and around doorways that end
  at that wall's floor. Rooms of the second band and the workroom have rooms behind their back wall.
- **Caps** (ADR 0061): the project's settings show "Effective: K" for the saved value only, and nothing
  at save time.

## Decisions (recommended options)

| # | Question | Decision |
|---|---|---|
| 1 | Which prompts | Claude Code agents **in the sandbox** (Podman), `PermissionRequest` for `Bash`. In bypass mode the only Bash prompts left are Claude Code's own rm checks. Host-mode agents (the user's own machine) and Codex agents are unchanged. |
| 2 | Auto-approve rule | The command is parsed with a real shell parser (`mvdan.cc/sh/v3/syntax`, new dependency: shfmt's parser, bash-compatible; a hand-written splitter would be the security hole). It is approved when it contains at least one `rm`/`rmdir` and **every** operand of every one resolves statically to a path **strictly inside `/workspace`**, with no `.git` component. Resolution: literal words and quotes, `$PWD`, and variables assigned a literal earlier at the top level of the same command; relative paths against the hook's `cwd` and literal `cd`s before it (after `cd x;` both the old and the new directory must pass, after `cd x &&` the new one). A glob only in the last element, and never one that matches `.git`. Refused (not auto): command or process substitution, other variables, `~`, braces, loops, functions, subshells, here-documents, `eval`, `source`, `sh -c`/`bash -c`, `xargs`, `find`, `rm` given as an argument to another command or run through a non-literal name, and options after `--` that look like paths outside. The parent directories between `/workspace` and each target must not be symlinks on the host side of the mount (Lstat; an error means not auto). |
| 3 | An auto-approval | The hook replies `allow` at once. The agent stays `working`; the event is recorded as `permission_auto`, logged with the number of targets. |
| 4 | Who decides the rest | A worker under a goal whose Tech Lead runs: the **Tech Lead**, always (the item says so; `access_decider` is about network hosts). The daemon holds the hook, sends the lead an agent-office message with the request id, the worker, the command (one line, at most 500 characters, marked as the worker's text) and its directory, and the lead answers with a new tool **`decide_command`** (`request_id`, `approve`, `reason` 1-1000 characters). Approve: `allow`. Deny: `deny` with "the Tech Lead refused: <reason>". No answer within **100 s**: `deny` with "not approved in time; use literal paths inside /workspace, or ask your Tech Lead". Each decision goes to the user's inbox as an info item (who, the command, the reason). Requests are in memory: they live only while the hook is held. One pending request per worker. While held, the worker shows "waiting for the Tech Lead" (state `waiting_permission`, activity `Bash`), not counted in Approvals; the dialog stays in its terminal, so the user can still answer there. Anyone else (no goal, no running lead, the lead itself, the Architect, QA, reviewers): no reply, the prompt waits for the user as today. |
| 5 | Hook timeouts | `PermissionRequest` gets its own hook entry: `agent-office hook --wait` (the forwarder waits 115 s instead of 8) with `timeout: 120` in the settings. The daemon's socket keeps 5 s for reading the payload and gives the handler up to 115 s. Other events are unchanged. |
| 6 | The lead's view of branches | Two new Tech Lead tools, served by the daemon from the main repository with git plumbing (the host never runs git in an agent's clone, ADR 0014): **`list_files`** (`source`, optional `path`, `recursive`) and **`read_file`** (`source`, `path`). `source` is a sub-task id of the lead's goal (its latest reported head, else its running worker's last imported head) or `"qa"` (the goal's newest QA run's integration head). Images (`png`, `jpeg`, `gif`, `webp`, by content, at most 3.75 MB) come back as MCP image content, so the model sees them; UTF-8 text up to 256 KB as text, marked as agent-written; anything else is refused with its type and size. Listings stop at 500 entries. Paths are repository-relative, cleaned, no `..`. The tech-lead role prompt names the tools. |
| 7 | Request changes on a proposed version | The same update that stores the request marks that version **superseded**; its plan inbox item is marked read. The UI shows it as "Changes requested; the Architect is revising it", without Approve. An approved version keeps its status (its work goes on); the request is stored on it as before. Approve refuses a superseded version (as today). The Architect's idle end and restore checks keep seeing an unanswered request on a superseded version (test). |
| 8 | Which failures mean the network | `StopFailure` of the main agent with `error` `server_error` or `unknown` and a `last_assistant_message` that starts with `API Error:` and names a connection loss: Claude Code's stream texts above, `Connection error`, `fetch failed`, `ECONNRESET`, `socket hang up`, `timed out`, or a `502`/`503`/`504` "status code (no body)" (the daemon's API proxy answers 502 when the upstream is unreachable). The adapter keeps only the class (`Event.NetworkLost`), never the text. Rate limits, authentication, billing, overload and invalid requests are not network. |
| 9 | Waiting for the network | A new state **`waiting_network`**, shown as "Connection lost; resumes when the network is back". One probe loop per engine while an agent waits: `GET https://api.anthropic.com/` through the API proxy's transport (so `HTTPS_PROXY` and a VPN apply), no credential, 10 s timeout, every 5 s; any HTTP response means the network is back. The stall watchdog counts the state as busy. Codex agents are unchanged (their CLI reports errors differently). |
| 10 | Resuming | Once the probe succeeds, each agent still in `waiting_network` gets one agent-office line typed into its terminal, like idle message delivery (ADR 0018: waits while the user types, Enter after a delay): "agent-office: the connection to the API was lost and is back now. Continue the task from where you stopped." Any hook first (the user typed, or the CLI recovered) ends the wait without typing. At most 5 automatic resumes per agent per hour; after that the agent goes idle and the user's inbox says why. |
| 11 | Board summary | A pure `boardSummary(tasks, qaRuns)`: the Board's counts per column for "All active goals" (sub-tasks and goals as `boardTasks`, plus QA round cards), so it matches the Board's column headers. The office bar shows five chips (To do, In progress, Review, Done, Blocked) with their counts; each opens the Board with "All active goals" picked and scrolls to and highlights that column (on a phone the column comes into view). |
| 12 | Whiteboard | The same five counts drawn in pixels on the whiteboard's top line (where the scribbled title was): a colour swatch in the column's tone and the count in a 3×5 pixel font, up to 99 (then "99+"). Drawn always, also without a lead. The whiteboard's label gets the counts in its tooltip and `aria-label`. |
| 13 | Exterior walls | A window goes only on a back wall segment with **no room behind it**: in the default plan the first band's rooms; in a custom layout the stretches of a room's top edge that are at the grid's top or have no room in the tile row above. Windows keep 4 px clear of every doorway, door, furniture item on the wall and room partition. |
| 14 | Window tests | Vitest for every preset, for the default plan at every desk grid the office uses (3 to 12 columns, 1 to 6 rows), and for **generated layouts**: `internal/office` generates 40 random valid layouts from a fixed seed (rooms in bands of random sizes and orders, doors to the room below and beside, each validated by `office.Parse`) into `web/src/office/generated.gen.json` (`go test ./internal/office -update`, like the presets; a Go test fails when the file is stale). Each test checks: every window is on a face, inside one room's back wall, on an exterior segment, and clear of every doorway and wall item. |
| 15 | Caps at save time | Project settings → Max agents shows, as the user types, the effective value for the draft: "Effective: 4. The daemon-wide cap of 4 is below this project's 8: at most 4 agents run in all projects together." with a **"Change the daemon-wide cap"** link to Settings → App. After Save, if the saved value is above the daemon-wide cap, the saved message is a warning with the same text and link instead of "Saved.". Nothing is clamped; the value is kept for when the cap rises. |
| 16 | Caps end to end | A new e2e test `TestDaemonCapFromTheUIEndToEnd` (no tokens): the real binary with `--max-agents 4` and a fake `claude` (a script that answers `--version`, sends `SessionStart` through `agent-office hook` and waits), host mode, two projects with Max agents 8. Four agents start (two per project), the fifth is refused. A Playwright script opens the real UI, raises "Max agents (all projects)" to 8 and saves; then two more agents start and six run, with the same daemon pid. |
| 17 | Network test | `TestNetworkDropEndToEnd` (e2e, haiku, sandbox): the daemon runs with `HTTPS_PROXY` pointing at a CONNECT proxy in the test. A worker gets a prompt that makes a long answer; once its stream flows, the proxy closes every tunnel and refuses new ones (Claude Code ends the turn with "Connection lost mid-response": `waiting_network`), then lets traffic through again after 20 s; the test checks that the agent is resumed without input and its turn finishes. Unit tests cover the parsing, the state, the probe and the resume rules with a fake probe. |
| 18 | Migrations | None: requests are in memory, the new state needs no schema change, and a superseded version is an existing status. |
| 19 | ADRs | 0065 dangerous rm prompts in the sandbox; 0066 the Tech Lead's view of its team's branches; 0067 network loss and resume. Amended: 0031 (change requests withdraw the version), 0045 and 0057 (board summary, whiteboard counts, exterior windows), 0061 (the cap at save time). |

## Sub-phases

1. Plan and roadmap (this file), the spike's network-cut and permission-reply additions.
2. Dangerous rm: the parser and rule (`internal/rmcheck`), the hook hold, `decide_command`. ADR 0065.
3. The lead's `list_files` and `read_file`. ADR 0066.
4. Request changes withdraws a proposed version.
5. Network loss: parsing, `waiting_network`, the probe, the resume. ADR 0067.
6. Board summary in the office bar and on the whiteboard.
7. Windows on exterior walls, generated layouts, tests.
8. Caps at save time; the caps e2e.
9. The network-drop e2e, screenshots, docs, PROGRESS, the full gates.

## Risks

- **Auto-approval is a security decision.** It only ever says yes to rm targets it resolved itself inside
  the workspace; anything it does not understand goes to the Tech Lead. Tests list both sides: commands
  approved and commands left to the lead. The agent is in the sandbox either way.
- **A held hook** keeps a goroutine and a socket per pending prompt for up to 115 s; one per worker.
- **Image content** depends on Claude Code passing MCP image results to the model (it does for its own
  screenshot tools); a unit test checks what the server returns.
- **The network e2e** depends on cutting while text streams (a cut before it makes Claude Code retry for
  about 2 minutes first; the test then waits longer). It spends a few cents.
- **A wrong network classification** would type "continue" into an agent after a real server error; the
  probe only checks reachability, so the resume limit bounds it.

## Answers to open questions

Every choice took the recommended option, as Ozan asked; they are the decisions above.

## Changes during the phase

(appended during the phase)
