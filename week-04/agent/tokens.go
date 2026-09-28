package main

import (
	"fmt"
	"sync"
	"unicode/utf8"

	"github.com/ron2111/omnitoken"
)

type TokenCounter interface {
	Count(string) int
	Label() string
	Exact() bool
}

type omniCounter struct{ engine omnitoken.ModelEngine }

func (c omniCounter) Count(text string) int { return c.engine.CountTokens(text) }
func (omniCounter) Label() string           { return "o200k_harmony" }
func (omniCounter) Exact() bool             { return true }

type deepSeekCounter struct{}

func (deepSeekCounter) Count(text string) int {
	ascii, nonASCII := 0, 0
	for _, r := range text {
		if r <= 127 {
			ascii++
		} else {
			nonASCII++
		}
	}
	estimate := (3*ascii+9)/10 + (3*nonASCII+4)/5
	return max(estimate, utf8.RuneCountInString(text))
}

func (deepSeekCounter) Label() string { return "deepseek-v4-estimate" }
func (deepSeekCounter) Exact() bool   { return false }

var (
	o200kHarmonyOnce   sync.Once
	o200kHarmonyEngine omnitoken.ModelEngine
	o200kHarmonyErr    error
)

func newTokenCounter(profile ProviderProfile) (TokenCounter, error) {
	switch profile.CounterEncoding {
	case "deepseek-v4-estimate":
		return deepSeekCounter{}, nil
	case "o200k_harmony":
		o200kHarmonyOnce.Do(func() {
			o200kHarmonyEngine, o200kHarmonyErr = omnitoken.ForEncoding(omnitoken.EncodingO200KHarmony)
		})
		if o200kHarmonyErr != nil {
			return nil, o200kHarmonyErr
		}
		return omniCounter{engine: o200kHarmonyEngine}, nil
	default:
		return nil, fmt.Errorf("unsupported token counter %q", profile.CounterEncoding)
	}
}

func countMessages(counter TokenCounter, messages []CompletionMessage) int {
	n := 0
	for _, message := range messages {
		n += counter.Count(message.Role) + counter.Count(message.Content)
	}
	return n
}
