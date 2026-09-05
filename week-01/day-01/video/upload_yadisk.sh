#!/usr/bin/env bash
# Uploads a file to Yandex.Disk and prints a public link.
#
#   export YANDEX_DISK_TOKEN=...
#   ./week-01/day-01/video/upload_yadisk.sh week-01/day-01/video/day-01-demo.mp4 [/remote/path.mp4]
#
# Get a token at https://yandex.ru/dev/disk/poligon/ (OAuth token for the
# Disk REST API). Pass NO_PUBLISH=1 to upload without making the file public.

set -euo pipefail

API=https://cloud-api.yandex.net/v1/disk/resources
: "${YANDEX_DISK_TOKEN:?export YANDEX_DISK_TOKEN first (https://yandex.ru/dev/disk/poligon/)}"

FILE=${1:?usage: upload_yadisk.sh <file> [remote-path]}
[ -f "$FILE" ] || { echo "error: no such file: $FILE" >&2; exit 1; }
REMOTE=${2:-"/$(basename "$FILE")"}

auth=(-H "Authorization: OAuth $YANDEX_DISK_TOKEN")

href=$(curl -sS "${auth[@]}" -G \
    --data-urlencode "path=$REMOTE" --data-urlencode "overwrite=true" \
    "$API/upload" | jq -r '.href // empty')
[ -n "$href" ] || { echo "error: Yandex.Disk refused the upload URL for $REMOTE" >&2; exit 1; }

curl -sS --fail -T "$FILE" "$href"
echo "uploaded: $REMOTE"

if [ "${NO_PUBLISH:-0}" = "1" ]; then
    exit 0
fi

curl -sS -X PUT "${auth[@]}" -G --data-urlencode "path=$REMOTE" "$API/publish" >/dev/null
curl -sS "${auth[@]}" -G --data-urlencode "path=$REMOTE" --data-urlencode "fields=public_url" "$API" |
    jq -r '"public link: " + (.public_url // "not published")'
