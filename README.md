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

## Usage

### week-01/day-01

```
go run ./week-01/day-01 "your question here"
```

Sends the prompt to the Groq API and prints the model's response to stdout.
