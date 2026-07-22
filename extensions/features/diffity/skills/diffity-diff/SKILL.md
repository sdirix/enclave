---
name: diffity-diff
description: Open the diffity diff viewer in the browser to see your changes
user-invocable: true
---

# Diffity Diff Skill

You are opening the diffity diff viewer so the user can see their changes in the browser.

## Arguments

- `ref` (optional): Git ref to diff (e.g. `main..feature`, `HEAD~3`) or a GitHub PR URL (e.g. `https://github.com/owner/repo/pull/123`). Defaults to working tree changes.

## Instructions

1. Check that `diffity` is available: run `which diffity`. If not found, tell the user to start the session with `--features +diffity,+vnc` (diffity is baked into the image; do NOT try `npm install`).
2. Run `diffity <ref>` (or just `diffity` if no ref) using the Bash tool with `run_in_background: true`:
   - The CLI handles everything: if an instance is already running for this repo it reuses it and opens the browser, otherwise it starts a new server and opens the browser.
   - Do NOT use `&` or `--quiet` — let the Bash tool handle backgrounding.
3. Wait 2 seconds, then run `diffity list --json` to get the port.
4. Tell the user diffity is running. It serves container-locally and shows in the session's VNC viewer (open it on the host with `enclave vnc-viewer`), not on a host URL — don't print a URL, session IDs, hashes, or other internals. Example:

   > Diffity is running — see the session's VNC viewer.
   >
   > When you're ready:
   > - Leave comments on the diff in the viewer, then run **/diffity-resolve** to fix them
   > - Or run **/diffity-review** to get an AI code review
