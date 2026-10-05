package main

import (
	"strings"
	"testing"
)

func TestRenderAnswerCanonicalInsufficientContextRefusal(t *testing.T) {
	tests := []struct {
		name        string
		modelAnswer string
		wantReason  bool
	}{
		{name: "canonical response", modelAnswer: insufficientAnswer},
		{name: "different model reason is retained", modelAnswer: "The source does not identify that item.", wantReason: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rendered := RenderAnswer(LaneResult{
				Settings: LaneSettings{Mode: "grounded"},
				Answer:   Answer{Status: "insufficient_context", Answer: test.modelAnswer},
			})
			if !strings.Contains(rendered, insufficientAnswer) {
				t.Fatalf("RenderAnswer() = %q, missing canonical refusal %q", rendered, insufficientAnswer)
			}
			if gotReason := strings.Contains(rendered, test.modelAnswer) && test.modelAnswer != insufficientAnswer; gotReason != test.wantReason {
				t.Fatalf("RenderAnswer() = %q, model reason presence = %t, want %t", rendered, gotReason, test.wantReason)
			}
		})
	}
}

func TestValidateAnswerCitationProvenance(t *testing.T) {
	context := []Candidate{{Chunk: Chunk{ID: "retrieved", Text: "The voltage tier is 32 EU."}}}
	tests := []struct {
		name      string
		answer    Answer
		mode      string
		wantError bool
	}{
		{name: "exact current context quote", mode: "grounded", answer: Answer{Status: "answer", Answer: "It is 32 EU.", Citations: []Citation{{ChunkID: "retrieved", Quote: "voltage tier is 32 EU"}}}},
		{name: "indexed but not current context", mode: "grounded", answer: Answer{Status: "answer", Answer: "It is 32 EU.", Citations: []Citation{{ChunkID: "other-indexed-chunk", Quote: "The voltage tier is 32 EU."}}}, wantError: true},
		{name: "paraphrase is not exact quote", mode: "grounded", answer: Answer{Status: "answer", Answer: "It is 32 EU.", Citations: []Citation{{ChunkID: "retrieved", Quote: "voltage tier equals thirty-two EU"}}}, wantError: true},
		{name: "grounded content requires citation", mode: "grounded", answer: Answer{Status: "answer", Answer: "It is 32 EU.", Citations: []Citation{}}, wantError: true},
		{name: "baseline may omit citations", mode: "no-rag", answer: Answer{Status: "answer", Answer: "I can answer without a source.", Citations: []Citation{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateAnswer(test.answer, test.mode, context)
			if (err != nil) != test.wantError {
				t.Fatalf("ValidateAnswer() error = %v, wantError %t", err, test.wantError)
			}
		})
	}
}
