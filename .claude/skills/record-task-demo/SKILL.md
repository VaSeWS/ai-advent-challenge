---
name: record-task-demo
description: Record a silent screencast demo of a day's task in this repo (week-NN/day-NN) — plan the shots, get the plan approved, write a self-typing terminal demo, dry-run it, capture the screen with the macOS screencapture, upload the .mp4 to Yandex.Disk, commit and push the demo script, and report "Код: <github link> / Видео: <yandex link>". Use this skill whenever the user asks for a video, demo, screencast, recording, GIF or "покажи, как работает" for a task — including phrasings like "сделай видео для дня 3", "запиши демо", "надо показать задание" — even if they never say the word "skill" or name a tool.
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

It checks the Screen Recording permission, opens a 1280×800 Terminal window,
plays the demo, captures that rect and stops when the demo ends. It prints
the output path and the matching Yandex.Disk path. Tell the user not to touch
the screen while it runs.

If the permission is missing the script says so and does nothing else: the
user has to enable their terminal app in System Settings > Privacy & Security
> Screen & System Audio Recording and restart that app. This is theirs to do,
not something to work around.

### 6. Upload to Yandex.Disk

```
.claude/skills/record-task-demo/scripts/upload_yadisk.sh week-NN/day-NN/video/day-NN-demo.mp4
```

`YANDEX_DISK_TOKEN` is read from the environment, falling back to `<repo>/.env`
(same gitignored file as `GROQ_API_KEY` — add a `YANDEX_DISK_TOKEN=...` line
there once, get the token from https://yandex.ru/dev/disk/poligon/). The
remote path is derived automatically as `/ai-advent-challenge/week-N/day-NN-demo.mp4`,
missing folders are created, and the script prints a public link. Once the
upload succeeds the script deletes the local `.mp4` — the file only ever
lives on Yandex.Disk after this step. Without a token anywhere, tell the
user to drag the file into disk.yandex.ru — do not stall the rest of the
work on it, and skip the auto-delete (the local file is their only copy
until the manual upload finishes).

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
Видео: <public link from upload_yadisk.sh>
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

`GROQ_API_KEY` must never appear in a frame. The key lives in `.env` and
`record.sh` passes it through a temporary launcher, so it never reaches the
Terminal window, the AppleScript, or a process argument. When the demo needs
to show that a key is being used, type a masked line instead:

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
    ├── record.sh                Terminal window + screencapture → .mp4
    └── upload_yadisk.sh         upload + public link
```

The recordings themselves stay out of git (`*.mp4` is ignored under each
day's `video/`); only the demo script is committed.

## Troubleshooting

- **Empty or missing .mp4** — Screen Recording permission, see step 5.
- **Text too small to read** — raise the Terminal profile font to 16-18 pt
  before recording, or shrink the window with `W=1100 H=700 record.sh …`.
- **Demo runs past the frame** — long model answers scroll; cut the prompt or
  add `-max-tokens` where the day supports it.
- **Recording longer than the demo** — `record.sh` stops one second after the
  demo exits; a hung command is the usual cause, check with `dryrun.sh`.
- **Mouse pointer in frame** — move the pointer out of the capture rect
  before recording; `screencapture` always draws the cursor.
