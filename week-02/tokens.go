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

// cl100kCounter approximates the Llama 3 tokenizer: both are byte-level BPE
// built on tiktoken, so cl100k_base counts slightly high — safe for a guard.
type cl100kCounter struct{ engine omnitoken.ModelEngine }

func (c cl100kCounter) Count(text string) int { return c.engine.CountTokens(text) }
func (cl100kCounter) Label() string           { return "cl100k-approx" }
func (cl100kCounter) Exact() bool             { return false }

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

	cl100kOnce   sync.Once
	cl100kEngine omnitoken.ModelEngine
	cl100kErr    error
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
	case "cl100k_base":
		cl100kOnce.Do(func() {
			cl100kEngine, cl100kErr = omnitoken.ForEncoding(omnitoken.EncodingCL100KBase)
		})
		if cl100kErr != nil {
			return nil, cl100kErr
		}
		return cl100kCounter{engine: cl100kEngine}, nil
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
