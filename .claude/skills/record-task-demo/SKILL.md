---
name: record-task-demo
description: Record a silent screencast demo of a day's task in this repo (week-NN/day-NN) — plan the shots, get the plan approved, write a self-typing terminal demo, dry-run it, capture its Terminal window with macOS ScreenCaptureKit, upload the .mp4 to Yandex.Disk, commit and push the demo script, and report "Код: <github link> / Видео: <yandex link>". Use this skill whenever the user asks for a video, demo, screencast, recording, GIF or "покажи, как работает" for a task — including phrasings like "сделай видео для дня 3", "запиши демо", "надо показать задание" — even if they never say the word "skill" or name a tool.
---

# Record a task demo

The course wants a short video per day showing that the task works. This
skill turns a day folder into a ~40-90 second silent screencast: a terminal
that types its own commands, runs the real code, and shows real model
output.

## Workflow

Follow the steps in order. Step 2 is a hard gate — never record before the
user approves the plan, because a take costs a minute of their screen and a
wrong take costs another.

Before starting, create a todo list (`TaskCreate`) with one item per step
below, including step 7's exact report shape as its own item (`Report "Код:
.../tree/main/... / Видео: ..."`) — not just "commit and push". The report
format is the easiest step to get right in spirit and wrong in the letter
(a commit link instead of a tree link, a missing line); a todo item that
names the literal string is what catches that before it ships.

### 1. Read the task

Read `week-NN/day-NN/README.md` (the `## Задание` section is the contract)
and the day's `main.go` for the actual flags and arguments. If the day has a
`REPORT.md`, skim it — it usually names the comparison the task is about.

The question to answer: **what does the task ask to demonstrate?** Day 2 is
about the difference between a constrained and unconstrained answer, day 3
about four reasoning strategies. The video has to make that difference
visible, not merely prove the binary runs.

### 2. Plan the shots and get approval

Present the plan in this exact shape, then stop and wait:

```
## План видео — week-NN/day-NN
Задание: <одна строка>
Кадры:
1. <подпись в кадре> → `<команда>`
2. <подпись в кадре> → `<команда>`
Длительность: ~NN с
Файл: week-NN/day-NN/video/day-NN-demo.mp4
Диск: /ai-advent-challenge/week-N/day-NN-demo.mp4
```

Keep it to 3-6 shots. Estimate the duration as: 0.025 s per typed character,
plus ~2 s per shot of pauses, plus the real runtime of each command (a Groq
call is 1-4 s), plus 7 s for the title and outro cards.

### 3. Write the demo script

Copy `assets/demo-template.sh` to `week-NN/day-NN/video/demo.sh`, make it
executable, and fill in the shots with `demo_note` / `demo_run` pairs from
`scripts/demolib.sh`:

| Helper | Use |
|---|---|
| `demo_title "…" "…"` | opening card: day and what is being shown |
| `demo_note '…'` | the narration line above a command — there is no audio |
| `demo_run '…'` | type a command, then run it |
| `demo_run_masked 'shown' 'real'` | type one thing, run another (secrets only) |
| `demo_pause [s]` | hold on long output so it can be read |
| `demo_outro 'week-NN/day-NN'` | closing card |

Notes are Russian (the repo and the course are), commands and code stay as
they are.

### 4. Dry run

```
.claude/skills/record-task-demo/scripts/dryrun.sh week-NN/day-NN/video/demo.sh
```

This plays every shot instantly and really calls the API. Read the whole
output: a wrong flag, an empty answer or a rate-limit error is far cheaper to
find here than in a take. Fix and repeat until it is clean.

### 5. Record

```
.claude/skills/record-task-demo/scripts/record.sh week-NN/day-NN/video/demo.sh
```

Run it from an owned interactive Terminal tab for the direct path. If a
standard tool invocation has no TTY on stdin or stdout, `record.sh` acquires
the same fixed project-wide screen lease before any Terminal query or tab
change. A tab whose Terminal `processes` list is empty is completed and may
retain its old TTY; only tabs with a nonempty process list participate in live
TTY uniqueness checks. The newly created tab must itself have live process
evidence for those checks, so startup that has not produced a process fails
closed rather than retrying or treating an empty-process tab as live.
The owned window ID is still checked against every pre-call window, including
completed tabs. Before sending the supervisor command, it
rechecks that the exact original TTY is still the selected tab, the owned
window is still frontmost, and the new tab's bounded shell process list
contains only an allowlisted local shell while Terminal reports it idle. It
launches only in that exact tab and requires the returned TTY and window
identity to match. No existing tab receives a command, and the window is
never resized or moved. The one-use private local socket transfers the
already-held lease to the real tab. Kernel-authenticated peer PID and a ticket
bind demo/output, both process start identities, host, lease token/inode,
Terminal identity, and actual TTY. The caller's environment is sent only over
that authenticated private channel so the owned tab runs with the same
exported settings and credentials; it is never put in the command line, lease
metadata, or logs. The rendezvous is removed under the lease before the
descriptor transfer. The parent waits for the child's adoption and
relinquishment acknowledgments and final status; it does not release the lease
after handoff. Busy or unreconciled ownership fails before any Terminal query
or tab creation. A missing, mismatched, interrupted, or partial handoff
retains the lease metadata for explicit reconciliation; a pending rendezvous
is retained only until it is safely removed before transfer. Never kill a PID
based only on metadata.

The child watches the authenticated parent channel while setting up and
capturing. Parent SIGINT/SIGTERM requests cancellation; the child stops only
its retained direct children, finalizes or retains uncertain capture/work
files, and reports whether lease release was confirmed. Channel loss follows
the same owned cleanup path; unconfirmed cleanup keeps the lease for
reconciliation.

The new owned tab is measured and captured using the existing viewport
guard. No existing tab is resized or moved. The viewport excludes the native
tab header (macOS 15+ and Xcode Command Line Tools). Ambiguous tab identity,
unsupported geometry, or changed selection fails closed. The demo starts only
after real recorder readiness.
Do not switch tabs, close the tab, change its font, or resize its window during
the take. `DEMO_TERMINAL_PROFILE` optionally applies an existing profile to
the owned tab. `CAPTURE_WINDOW=0` and `W/H/X/Y` overrides are rejected.
Successful output is atomically published without overwriting any existing
path. A finalization cleanup error still returns nonzero even if that output
already exists; reconcile before treating the take as accepted or uploading.
Failed or unconfirmed takes remain local and must not be uploaded.

The supervisor exclusively creates one fixed project-wide
`.omp/task-execute/runs/.screen-recording.lock` before any Terminal identity
query, tab/profile change, viewport measurement, or compiler/recorder/demo
launch. Different days and output paths compete for the same lease; there is
no environment override or bypass. Its private metadata records a random
ownership token, PID, host, demo/task, acquisition time, and lifecycle paths
and child PIDs as they become known, never credentials.
Busy or unreconciled ownership fails closed before protected actions. Queue
the next take and retry only after the owner confirms release; do not poll the
recorder or steal a lease based on its age or PID.
The lease lasts through child and private-workload cleanup. Direct children
receive the caller's pre-spawn signal mask before their workload is exec'd;
the supervisor retains each actual child handle while protecting publication
from pending SIGINT/SIGTERM. Confirmed stop allows release only when file
identity and token still match. Unknown shutdown, metadata mismatch, crash, or
SIGKILL retains the lease for explicit reconciliation. Reconciliation must
establish that the owner and referenced compiler, recorder, demo, and private
workload have stopped before removing the recorded lease; never replace or
edit it while the owner may still be running. This protocol coordinates
cooperating recording processes, not hostile processes: identity/token checks
do not make filesystem rename/unlink races atomic against an out-of-band
replacer. A retained failed take is not acceptance or permission to upload.

Each TUI demo starts its own foreground tmux server on a random mode-0700
socket directory; client commands use `-N` so a missing server is never
respawned. A private `.owner` sidecar records the server and pane PIDs with
their start identities, the pane process group, and the socket device/inode.
Shutdown joins the retained server child and confirms the pane process/group
has stopped. A stale socket is removed only when no process holds it and its
recorded device/inode still matches; unknown process state, a holder, or an
identity mismatch leaves the private-workload state and files for
reconciliation.

With recording, the sidecar is included in required supervisor-owned private
work cleanup. `stop_confirmed` becomes true only after that cleanup succeeds.
Failure cleaning work or an otherwise-disposable capture directory keeps the
project lease, reports the exact path, and returns nonzero even if output was
already published. Standalone helpers remove the sidecar after the empty TUI
directory. Global or user tmux sessions are not stopped.

`record-supervisor.py --permission-preflight` checks only authorization and
does not acquire this lease.

If the permission is missing the script says so and does nothing else: the
user has to enable their terminal app in System Settings > Privacy & Security
> Screen & System Audio Recording and restart that app. This is theirs to do,
not something to work around.

### 6. Publish through the local Yandex Disk MCP server

```
.claude/skills/record-task-demo/scripts/upload_yadisk.sh week-NN/day-NN/video/day-NN-demo.mp4
```

Use this uploader for every recording. It invokes
`go run ./week-04/day-17 upload <file>` from the repository root: the Go
client connects to the local stdio MCP server, calls `publish_video`, and
prints the returned public URL. Do not replace this MCP call with direct
Yandex Disk REST requests, manual `curl` uploads, or a third-party MCP server.

The uploader reads `YANDEX_DISK_TOKEN` from the environment, falling back
to the gitignored `<repo>/.env` (obtain the token from
https://yandex.ru/dev/disk/poligon/). The Go code reads only the environment.
The MCP tool derives `/ai-advent-challenge/week-N/day-NN-demo.mp4` from the
standard local video path, creates missing folders, uploads and publishes the
file, and returns its public link. The uploader deletes the local `.mp4`
**only after** the MCP call succeeds; on failure retain it for another try.
If no token is available, leave the local file untouched and ask for a token
or a manual upload; request the resulting public link before reporting.

### 7. Commit, push, and report the two links

The demo script (never the `.mp4`) is real content of the day — commit it
and push straight to `main` per this repo's workflow (see the repo's
`CLAUDE.md`: solo homework repo, no branches, no PR review):

```
git add week-NN/day-NN/video/demo.sh
git commit -m "..."
git push origin <branch>:main   # or `git push` if already on main
```

Then derive the GitHub link from the remote and report both links in
exactly this shape — nothing else, no extra commentary:

```
Код: https://github.com/<owner>/<repo>/tree/main/week-NN/day-NN
Видео: <public link returned by the MCP upload>
```

Get `<owner>/<repo>` from `git remote get-url origin` (strip `.git` and any
`git@host:`/`https://host/` prefix). If the video was uploaded manually
(no token, user dragged the file in) ask the user for the public link
before printing this block — don't guess it.

## What belongs in the video

- **The functionality the task asks for.** The task text is the shot list.
- **Real runs and real output.** Nothing staged, nothing pre-recorded.
- **No source code.** No `cat main.go`, no `sed -n` over sources, no
  walkthrough of structs or handlers. The video shows behaviour; the code is
  in the repo for whoever wants it.
- **No conclusions or summaries on screen** unless the task itself asks for
  them (for example a day whose deliverable is a comparison verdict).
- **Failure cases only when the task is about them.** A missing-key error is
  not interesting unless the day is about error handling.

## Key safety

Credentials (`GROQ_API_KEY`, `DEEPSEEK_API_KEY`, `YANDEX_DISK_TOKEN`,
`DAY30_SERVICE_TOKEN`, `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`) must never
appear in a frame or process argument. Export required values before starting:
`record.sh` does not load `.env`. The Python supervisor and demo workload inherit
the exported environment; compiler, recorder, and Terminal automation helpers
remove the six listed variables. Keep credentials out of typed commands,
process arguments, logs, and frames. Show configured keys only as masked text:

```bash
demo_run_masked 'export GROQ_API_KEY=gsk_••••••••••••••••••••••••' 'true'
```

Never `echo` the variable, never `env | grep`, never `cat .env`.

## Files

```
.claude/skills/record-task-demo/
├── SKILL.md
├── assets/demo-template.sh      starting point for a day's demo
└── scripts/
    ├── demolib.sh               typing effect, prompt, shot helpers
    ├── dryrun.sh                instant replay of a demo, real commands
    ├── record.sh                selected Terminal text viewport + lifecycle
    ├── record-window.swift      desktop-independent ScreenCaptureKit → .mp4
    └── upload_yadisk.sh         local MCP client invocation + public link
```

The recordings themselves stay out of git (`*.mp4` is ignored under each
day's `video/`); only the demo script is committed.

## Troubleshooting

- **Empty or missing .mp4** — Screen Recording permission, see step 5.
- **Text too small to read** — set the owned tab's actual font to 16–18 pt
  before recording; shared-window layout overrides are unsupported.
- **Demo runs past the frame** — paginate or scroll real output readably;
  do not truncate a required answer merely to shorten the video.
- **Unconfirmed shutdown** — inspect the retained take and lifecycle state;
  do not upload it or start another generation until ownership is reconciled.
- **Mouse pointer in frame** — the ScreenCaptureKit recorder hides it.
