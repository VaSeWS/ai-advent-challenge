package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	frontmatterDue = regexp.MustCompile(`(?m)^sr-due:\s*["']?([0-9]{4}-[0-9]{2}-[0-9]{2})["']?\s*$`)
	tagPattern     = regexp.MustCompile(`(?:^|\s)#([[:alnum:]_-]+)`)
)

type question struct {
	Title string `json:"title"`
	Topic string `json:"topic"`
	Due   string `json:"due"`
	Path  string `json:"path"`
}

type scheduledTask struct {
	ID       int       `json:"id"`
	Topic    string    `json:"topic"`
	Interval string    `json:"interval"`
	NextRun  time.Time `json:"next_run"`
}

type execution struct {
	ScheduleID int        `json:"schedule_id"`
	Topic      string     `json:"topic"`
	ExecutedAt time.Time  `json:"executed_at"`
	Count      int        `json:"count"`
	Questions  []question `json:"questions"`
	Error      string     `json:"error,omitempty"`
}

type persistedState struct {
	Schedules []scheduledTask `json:"schedules"`
	History   []execution     `json:"history"`
}

type scheduler struct {
	mu        sync.Mutex
	vaultDir  string
	statePath string
	state     persistedState
}

func newScheduler(vaultDir, statePath string) (*scheduler, error) {
	s := &scheduler{vaultDir: vaultDir, statePath: statePath, state: persistedState{Schedules: []scheduledTask{}, History: []execution{}}}
	data, err := os.ReadFile(statePath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("read scheduler state: %w", err)
		}
		return s, nil
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return nil, fmt.Errorf("decode scheduler state: %w", err)
	}
	if s.state.Schedules == nil {
		s.state.Schedules = []scheduledTask{}
	}
	if s.state.History == nil {
		s.state.History = []execution{}
	}
	for _, task := range s.state.Schedules {
		interval, err := time.ParseDuration(task.Interval)
		if err != nil || interval < time.Second || task.ID < 1 || task.NextRun.IsZero() {
			return nil, fmt.Errorf("invalid persisted schedule %d", task.ID)
		}
	}
	return s, nil
}

func (s *scheduler) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode scheduler state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.statePath), ".scheduler-*.json")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write scheduler state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync scheduler state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close scheduler state: %w", err)
	}
	if err := os.Rename(name, s.statePath); err != nil {
		return fmt.Errorf("replace scheduler state: %w", err)
	}
	return nil
}

func (s *scheduler) add(topic string, interval time.Duration, now time.Time) (scheduledTask, error) {
	if interval < time.Second {
		return scheduledTask{}, fmt.Errorf("interval must be at least 1s")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	nextID := 1
	for _, task := range s.state.Schedules {
		if task.ID >= nextID {
			nextID = task.ID + 1
		}
	}
	task := scheduledTask{ID: nextID, Topic: normalizeTopic(topic), Interval: interval.String(), NextRun: now.Add(interval)}
	s.state.Schedules = append(s.state.Schedules, task)
	if err := s.persistLocked(); err != nil {
		s.state.Schedules = s.state.Schedules[:len(s.state.Schedules)-1]
		return scheduledTask{}, err
	}
	return task, nil
}

func (s *scheduler) dueQuestions(topic string, date time.Time) ([]question, error) {
	results := make([]question, 0)
	err := filepath.WalkDir(s.vaultDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		q, ok := parseQuestion(data, path, s.vaultDir, date)
		if ok && matchesTopic(q.Topic, topic) {
			results = append(results, q)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan vault markdown: %w", err)
	}
	return results, nil
}

func parseQuestion(data []byte, path, root string, through time.Time) (question, bool) {
	text := string(data)
	frontmatter := strings.SplitN(text, "\n", 2)
	if len(frontmatter) != 2 || strings.TrimSpace(frontmatter[0]) != "---" {
		return question{}, false
	}
	headerAndBody := strings.SplitN(frontmatter[1], "\n---", 2)
	if len(headerAndBody) != 2 {
		return question{}, false
	}
	match := frontmatterDue.FindStringSubmatch(headerAndBody[0])
	if match == nil {
		return question{}, false
	}
	_, err := time.Parse("2006-01-02", match[1])
	if err != nil || match[1] > through.Format("2006-01-02") {
		return question{}, false
	}
	body := headerAndBody[1]
	title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	tags := tagPattern.FindAllStringSubmatch(body, -1)
	tagSet := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tagSet[tag[1]] = struct{}{}
	}
	topicNames := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		topicNames = append(topicNames, "#"+tag)
	}
	sort.Strings(topicNames)
	return question{Title: title, Topic: strings.Join(topicNames, ", "), Due: match[1], Path: relativePath(root, path)}, true
}

func relativePath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func normalizeTopic(topic string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(topic), "#"))
}

func matchesTopic(tags, topic string) bool {
	topic = normalizeTopic(topic)
	if topic == "" {
		return true
	}
	for _, tag := range strings.Split(tags, ", ") {
		if normalizeTopic(tag) == topic {
			return true
		}
	}
	return false
}

func (s *scheduler) runDue(now time.Time) error {
	s.mu.Lock()
	due := make([]scheduledTask, 0)
	nextRuns := make([]time.Time, len(s.state.Schedules))
	for i := range s.state.Schedules {
		nextRuns[i] = s.state.Schedules[i].NextRun
		if !s.state.Schedules[i].NextRun.After(now) {
			due = append(due, s.state.Schedules[i])
			interval, _ := time.ParseDuration(s.state.Schedules[i].Interval)
			for !s.state.Schedules[i].NextRun.After(now) {
				s.state.Schedules[i].NextRun = s.state.Schedules[i].NextRun.Add(interval)
			}
		}
	}
	if len(due) > 0 {
		if err := s.persistLocked(); err != nil {
			for i := range s.state.Schedules {
				s.state.Schedules[i].NextRun = nextRuns[i]
			}
			s.mu.Unlock()
			return err
		}
	}
	s.mu.Unlock()
	for _, task := range due {
		questions, err := s.dueQuestions(task.Topic, now)
		run := execution{ScheduleID: task.ID, Topic: task.Topic, ExecutedAt: now, Questions: questions, Count: len(questions)}
		if err != nil {
			run.Error = err.Error()
			run.Questions = nil
			run.Count = 0
		}
		s.mu.Lock()
		s.state.History = append(s.state.History, run)
		if err := s.persistLocked(); err != nil {
			s.state.History = s.state.History[:len(s.state.History)-1]
			s.mu.Unlock()
			return err
		}
		s.mu.Unlock()
	}
	return nil
}

func (s *scheduler) snapshot() persistedState {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _ := json.Marshal(s.state)
	var copy persistedState
	_ = json.Unmarshal(data, &copy)
	return copy
}
