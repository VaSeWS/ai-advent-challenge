package main

import (
	"errors"
	"fmt"
	"strings"
)

const insufficientAnswer = "I don't know based on this corpus. Please clarify your question."

// ValidateAnswer checks answer status and citation provenance against only the
// evidence supplied to this request. It does not assess semantic support.
func ValidateAnswer(answer Answer, mode string, context []Candidate) error {
	switch answer.Status {
	case "answer", "insufficient_context":
	default:
		return fmt.Errorf("unknown answer status %q", answer.Status)
	}
	if answer.Status == "insufficient_context" {
		if strings.TrimSpace(answer.Answer) == "" {
			return errors.New("insufficient-context answer must not be empty")
		}
		if len(answer.Citations) != 0 {
			return errors.New("insufficient-context answer must not cite sources")
		}
		return nil
	}
	if strings.TrimSpace(answer.Answer) == "" {
		return errors.New("answer must not be empty")
	}
	if mode == "grounded" && len(answer.Citations) == 0 {
		return errors.New("grounded answer requires at least one citation")
	}
	available := make(map[string]string, len(context))
	for _, candidate := range context {
		available[candidate.Chunk.ID] = candidate.Chunk.Text
	}
	for i, citation := range answer.Citations {
		text, ok := available[citation.ChunkID]
		if !ok || citation.ChunkID == "" {
			return fmt.Errorf("citation %d references chunk outside current context", i+1)
		}
		if strings.TrimSpace(citation.Quote) == "" || !strings.Contains(text, citation.Quote) {
			return fmt.Errorf("citation %d quote is empty or not an exact chunk substring", i+1)
		}
	}
	return nil
}

// RenderAnswer renders an answer and its source provenance from stored chunk
// metadata; no source metadata is taken from model output.
func RenderAnswer(result LaneResult) string {
	var b strings.Builder
	if result.Answer.Status == "insufficient_context" {
		b.WriteString(insufficientAnswer)
		modelReason := strings.TrimSpace(result.Answer.Answer)
		if modelReason != "" && modelReason != insufficientAnswer {
			fmt.Fprintf(&b, "\n\nModel explanation: %s", modelReason)
		}
	} else {
		b.WriteString(result.Answer.Answer)
	}
	if len(result.Answer.Citations) == 0 {
		if result.Settings.Mode == "no-rag" {
			b.WriteString("\n\nSources: not used")
		} else if result.Answer.Status == "insufficient_context" || len(result.Context) == 0 {
			b.WriteString("\n\nSources: none — insufficient context")
		} else {
			b.WriteString("\n\nSources: none")
		}
		return b.String()
	}
	byID := make(map[string]Chunk, len(result.Context))
	for _, candidate := range result.Context {
		byID[candidate.Chunk.ID] = candidate.Chunk
	}
	b.WriteString("\n\nSources:")
	for _, citation := range result.Answer.Citations {
		chunk, ok := byID[citation.ChunkID]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "\n- %s — %s (chunk %s; revision %d): %s\n  Quote: %q", chunk.Title, strings.Join(chunk.SectionPaths, " / "), chunk.ID, chunk.RevisionID, chunk.Permalink, citation.Quote)
	}
	return b.String()
}

// RenderContext renders original/search queries, all candidates, filtering
// decisions, and complete evidence text for inspection.
func RenderContext(result LaneResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Original query: %s\nSearch query: %s\n", result.OriginalQuery, result.SearchQuery)
	fmt.Fprintf(&b, "Similarity = 1 - cosine distance; threshold %.4f (calibrated: %t; %s), candidate K=%d, context K=%d\n", result.Retrieval.Threshold, result.Retrieval.Calibrated, result.Retrieval.Calibration, result.Retrieval.CandidateK, result.Retrieval.ContextK)
	kept := make(map[string]struct{}, len(result.Context))
	for _, candidate := range result.Context {
		kept[candidate.Chunk.ID] = struct{}{}
	}
	for _, candidate := range result.Candidates {
		_, selected := kept[candidate.Chunk.ID]
		decision := "discarded"
		if selected {
			decision = "kept"
		}
		fmt.Fprintf(&b, "\n[%s] chunk=%s distance=%.6f similarity=%.6f title=%q sections=%q revision=%d permalink=%s\n%s\n", decision, candidate.Chunk.ID, candidate.Distance, 1-candidate.Distance, candidate.Chunk.Title, candidate.Chunk.SectionPaths, candidate.Chunk.RevisionID, candidate.Chunk.Permalink, candidate.Chunk.Text)
	}
	return b.String()
}
