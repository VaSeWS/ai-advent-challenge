# Shared helpers for day demo scripts. Source it, then describe the shots:
#
#   source "$(dirname "${BASH_SOURCE[0]}")/../../../.claude/skills/record-task-demo/scripts/demolib.sh"
#   demo_title "AI Advent — День 2" "Контроль формата ответа"
#   demo_note  "Без ограничений — модель отвечает как хочет."
#   demo_run   'go run ./week-01/day-02 "..."'
#   demo_outro "week-01/day-02"
#
# Knobs (env): SPEED (per-character delay), PAUSE (beat after each step),
# PROMPT_LABEL (the path shown in the fake prompt).
#
# A dry run sets SPEED=0 PAUSE=0 and stubs sleep, so the same script plays
# instantly when you are only checking that the commands work.

SPEED=${SPEED:-0.025}
PAUSE=${PAUSE:-1.0}
PROMPT_LABEL=${PROMPT_LABEL:-'~/DevProjects/ai-advent-challenge'}

DEMO_BOLD=$'\033[1m'; DEMO_DIM=$'\033[2m'; DEMO_RESET=$'\033[0m'
DEMO_GREEN=$'\033[32m'; DEMO_BLUE=$'\033[34m'; DEMO_RED=$'\033[31m'; DEMO_CYAN=$'\033[36m'

demo_nap() { [ "$1" != "0" ] && sleep "$1"; }

# Print a string one character at a time, then a newline.
demo_typewrite() {
    local text=$1 i
    for ((i = 0; i < ${#text}; i++)); do
        printf '%s' "${text:i:1}"
        demo_nap "$SPEED"
    done
    printf '\n'
}

demo_prompt() {
    printf '%s%s%s %s$%s ' "$DEMO_BLUE" "$PROMPT_LABEL" "$DEMO_RESET" "$DEMO_GREEN" "$DEMO_RESET"
}

# Type a command at the prompt, then run it.
demo_run() {
    demo_prompt
    demo_typewrite "$1"
    demo_nap 0.3
    eval "$1"
    demo_nap "$PAUSE"
}

# Type one command but run another. The only honest use is hiding a secret:
# type `export GROQ_API_KEY=gsk_••••` while the real value stays in the env.
demo_run_masked() {
    demo_prompt
    demo_typewrite "$1"
    demo_nap 0.3
    eval "$2"
    demo_nap "$PAUSE"
}

# A dim comment line above a step — this is the narration, there is no audio.
demo_note() {
    printf '%s# %s%s\n' "$DEMO_DIM" "$1" "$DEMO_RESET"
    demo_nap 0.6
}

demo_pause() { demo_nap "${1:-1.5}"; }

# Opening card: clears the screen, states which day and what it shows.
demo_title() {
    clear
    printf '%s%s%s%s\n' "$DEMO_BOLD" "$DEMO_CYAN" "$1" "$DEMO_RESET"
    [ $# -gt 1 ] && printf '%s%s%s\n' "$DEMO_DIM" "$2" "$DEMO_RESET"
    printf '\n'
    demo_nap 2.5
}

# Closing card. Give it the day's path; it holds long enough to be readable.
demo_outro() {
    printf '\n%s%sГотово.%s %s%s%s\n' \
        "$DEMO_BOLD" "$DEMO_GREEN" "$DEMO_RESET" "$DEMO_DIM" "${1:-}" "$DEMO_RESET"
    demo_nap 4
}

# Fail before the camera rolls, not halfway through a take.
demo_require_env() {
    local var
    for var in "$@"; do
        if [ -z "${!var:-}" ]; then
            printf '%serror: %s is not set%s\n' "$DEMO_RED" "$var" "$DEMO_RESET" >&2
            exit 1
        fi
    done
}

# Absolute repo root, so demos can cd there regardless of where they live.
demo_repo_root() {
    local dir=${1:-$PWD}
    (cd "$dir" && git rev-parse --show-toplevel 2>/dev/null) || printf '%s' "$dir"
}
