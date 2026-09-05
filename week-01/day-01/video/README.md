# Видео-демо дня 1

`demo.sh` — сценарий записи: терминал сам печатает команды и делает три
живых запроса в Groq, показывая то, что требует задание — prompt уходит в
LLM через API, ответ печатается в консоль.

Скрипты записи и заливки общие для всех дней и лежат в скилле
`.claude/skills/record-task-demo/`:

```
# прогон без записи, проверить что всё работает
.claude/skills/record-task-demo/scripts/dryrun.sh week-01/day-01/video/demo.sh

# запись экрана в day-01-demo.mp4
.claude/skills/record-task-demo/scripts/record.sh week-01/day-01/video/demo.sh

# заливка на Яндекс.Диск, публичная ссылка
export YANDEX_DISK_TOKEN=...
.claude/skills/record-task-demo/scripts/upload_yadisk.sh week-01/day-01/video/day-01-demo.mp4
```

Сам `.mp4` в git не коммитится.
