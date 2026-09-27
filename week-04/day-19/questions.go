package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	dueFrontmatter = regexp.MustCompile(`^sr-due:\s*([0-9]{4}-[0-9]{2}-[0-9]{2})\s*$`)
	hashtag        = regexp.MustCompile(`(?:^|[^[:alnum:]_#])#([[:alnum:]_-]+)`)
)

type question struct {
	Title string `json:"title" jsonschema:"Question note title"`
	Topic string `json:"topic" jsonschema:"Matched hashtag topic"`
	Due   string `json:"due" jsonschema:"Due date in YYYY-MM-DD format"`
	Path  string `json:"path" jsonschema:"Path relative to the Obsidian vault"`
}

type findQuestionsInput struct {
	Topic string `json:"topic" jsonschema:"Exact hashtag topic; empty means all topics"`
	Date  string `json:"date" jsonschema:"Include questions due on or before this YYYY-MM-DD date"`
}

type findQuestionsOutput struct {
	Questions []question `json:"questions" jsonschema:"Matching questions sorted from oldest due date to newest"`
}

func parseDate(value string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return time.Time{}, fmt.Errorf("date must be YYYY-MM-DD: %q", value)
	}
	return parsed, nil
}

func normalizeTopic(topic string) string {
	topic = strings.TrimSpace(topic)
	topic = strings.TrimPrefix(topic, "#")
	if strings.EqualFold(topic, "all") {
		return ""
	}
	return topic
}

func hasTopic(contents, topic string) bool {
	if topic == "" {
		return true
	}
	for _, match := range hashtag.FindAllStringSubmatch(contents, -1) {
		if strings.EqualFold(match[1], topic) {
			return true
		}
	}
	return false
}

func dueDate(contents []byte) (string, bool) {
	text := string(contents)
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSuffix(lines[0], "\r") != "---" {
		return "", false
	}
	for _, line := range lines[1:] {
		line = strings.TrimSuffix(line, "\r")
		if line == "---" {
			return "", false
		}
		if match := dueFrontmatter.FindStringSubmatch(line); match != nil {
			if _, err := parseDate(match[1]); err == nil {
				return match[1], true
			}
			return "", false
		}
	}
	return "", false
}

func findDueQuestions(ctx context.Context, vault, topic, date string) ([]question, int, error) {
	cutoff, err := parseDate(date)
	if err != nil {
		return nil, 0, err
	}
	topic = normalizeTopic(topic)
	questions := make([]question, 0)
	missingDue := 0
	err = filepath.WalkDir(vault, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read question note %q: %w", path, err)
		}
		due, ok := dueDate(contents)
		if !ok {
			missingDue++
			return nil
		}
		dueAt, err := parseDate(due)
		if err != nil {
			return err
		}
		if dueAt.After(cutoff) || !hasTopic(string(contents), topic) {
			return nil
		}
		title := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		relative, err := filepath.Rel(vault, path)
		if err != nil {
			return fmt.Errorf("make vault-relative path: %w", err)
		}
		matchedTopic := topic
		if matchedTopic == "" {
			for _, match := range hashtag.FindAllStringSubmatch(string(contents), -1) {
				if len(match) > 1 {
					matchedTopic = match[1]
					break
				}
			}
		}
		questions = append(questions, question{Title: title, Topic: matchedTopic, Due: due, Path: filepath.ToSlash(relative)})
		return nil
	})
	if err != nil {
		return nil, missingDue, fmt.Errorf("scan Obsidian vault: %w", err)
	}
	sort.Slice(questions, func(i, j int) bool {
		if questions[i].Due != questions[j].Due {
			return questions[i].Due < questions[j].Due
		}
		if !strings.EqualFold(questions[i].Title, questions[j].Title) {
			return strings.ToLower(questions[i].Title) < strings.ToLower(questions[j].Title)
		}
		return questions[i].Path < questions[j].Path
	})
	return questions, missingDue, nil
}
