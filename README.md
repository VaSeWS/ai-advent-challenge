# AI Advent Challenge #9

This repo holds homework code for a multi-week AI course, 5 tasks per week, one folder per week/day.

## Setup

Requires Go 1.25.5.

Export your key in the shell before running anything:

```
export GROQ_API_KEY=your-groq-api-key-here
```

Get a free key at https://console.groq.com/keys. `.env.example` shows the expected variable
name for reference — the code reads it from the real environment, not from a `.env` file.

## week-02

See [week-02/README.md](week-02/README.md) for the Context Agent task.

## week-05

См. [week-05/README.md](week-05/README.md): дни 21–25 работают через один общий standalone Go-пакет `week-05` (`go run ./week-05`); подпапки `day-NN/video` содержат демо-скрипты. Документация включает setup, команды, результаты полного control и сценариев, agent-authored reviews и ограничения качества. Генерация по умолчанию использует Groq; DeepSeek задаётся явно `-provider deepseek`. Ключ читается только из `GROQ_API_KEY` или `DEEPSEEK_API_KEY` в окружении; `.env` автоматически не загружается. Live-вызовы передают запрос и найденные фрагменты выбранному внешнему провайдеру. Ссылки на пять опубликованных видео — в README недели.
