# Claude Code workspace trust (Phase 1 findings, input for Phase 2)

Measured 2026-11-06 with Claude Code 2.1.319, interactive sessions started by agent-office
(`claude --session-id … --settings …` in a PTY). Trust was judged from the PTY output (dialog text)
and from whether the SessionStart hook arrived. The trust store itself (`~/.claude.json`) was not
read; it is a protected file.

## What happens in an untrusted folder

- Claude Code shows "Accessing workspace: <path> … Is this a project you created or one you
  trust?" with the options **"No, exit"** (preselected) and "Yes, I trust this folder".
- **Hooks are held back** until the folder is trusted. Before that, no SessionStart arrives, so the
  agent stays in `starting`. Once the user picks "Yes" in the terminal, SessionStart fires
  immediately and everything works normally.
- Pressing Enter blindly would pick "No, exit". agent-office never answers this dialog. The UI
  shows a hint while an agent is still `starting`.

## Which paths inherit trust

| Folder | Trust dialog? |
|---|---|
| `~/projects/agent-office-playground` (accepted once by Ozan) | no (after accepting) |
| git worktree **inside** the trusted repo: `<playground>/.worktrees/trust-check` | **no** |
| git worktree of the trusted repo **outside** its directory (in a temp dir) | **no** |
| new plain folder next to that worktree (control, never trusted) | **yes** |
| subfolder of a trusted project (`agent-office/spikes/hooks`, Phase 0) | no |

Conclusions:
1. **Subfolders of a trusted folder are trusted.**
2. **Worktrees inherit the trust of their main repository, wherever they live.** The control folder
   in the same temp directory still showed the dialog, so this is not path-based. Claude Code
   evidently resolves a worktree to its main repository.

## Consequence for Phase 2 (to be decided in its plan)

The user trusts each **repository** once (by opening it in Claude Code, or via the first agent's
terminal). After that, worktrees for that repo can be created anywhere without new dialogs: under
the repo (`<repo>/.worktrees/`, which then needs a gitignore entry) or under an agent-office data
directory. A non-git folder or a new repo will still show the dialog once. The UI hint covers that.

Caveat: the trust behaviour is undocumented and may change between Claude Code versions. Phase 2
should still detect a stuck `starting` state instead of relying on this.
