---
name: diffity-tree
description: Open the diffity file tree browser to browse and comment on repository files
user-invocable: true
---

# Diffity Tree Skill

You are opening the diffity file tree browser so the user can browse repository files in the browser.

## Instructions

1. Check that `diffity` is available: run `which diffity`. If not found, tell the user to start the session with `--features +diffity,+vnc` (diffity is baked into the image; do NOT try `npm install`).
2. Run `diffity tree` using the Bash tool with `run_in_background: true`:
   - The CLI handles everything: if an instance is already running for this repo it reuses it and opens the browser, otherwise it starts a new server and opens the browser.
   - Do NOT use `&` or `--quiet` — let the Bash tool handle backgrounding.
3. Wait 2 seconds, then run `diffity list --json` to get the port.
4. Tell the user diffity tree is running. It serves container-locally and shows in the session's VNC viewer (open it on the host with `enclave vnc-viewer`), not on a host URL — don't print a URL, session IDs, hashes, or other internals. Example:

   > Diffity tree is running — see the session's VNC viewer.
   >
   > When you're ready:
   > - Leave comments on any file in the viewer, then run **/diffity-resolve-tree** to fix them
