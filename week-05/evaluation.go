package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ControlQuestion is one approved question and its reviewable expectations.
type ControlQuestion struct {
	ID               string           `json:"id"`
	Question         string           `json:"question"`
	ExpectedFacts    []string         `json:"expected_facts"`
	ExpectedSources  []ExpectedSource `json:"expected_sources"`
	CorpusSufficient bool             `json:"corpus_sufficient"`
}

// ExpectedSource identifies one expected document section.
type ExpectedSource struct {
	DocumentID string `json:"document_id"`
	SectionID  string `json:"section_id"`
}

// QuestionSet is pinned to the snapshot used for evaluation.
type QuestionSet struct {
	SnapshotID string            `json:"snapshot_id"`
	Questions  []ControlQuestion `json:"questions"`
}

type evaluationComparison struct {
	QuestionID string             `json:"question_id"`
	Pair       string             `json:"pair"`
	Control    ControlQuestion    `json:"control_question"`
	Run        *ComparisonRun     `json:"run,omitempty"`
	Observed   [2]*LaneResult     `json:"observed_lanes,omitempty"`
	Metrics    [2]*LaneEvaluation `json:"lane_metrics,omitempty"`
	Error      string             `json:"error,omitempty"`
}

type LaneEvaluation struct {
	ExpectedFacts []FactEvaluation   `json:"expected_facts"`
	Sources       []SourceEvaluation `json:"expected_sources"`
	Manual        ManualReview       `json:"manual_review"`
}

type FactEvaluation struct {
	Fact    string `json:"fact"`
	Present bool   `json:"exact_substring_present"`
}

type SourceEvaluation struct {
	ExpectedSource
	SectionPath   string          `json:"section_path"`
	SectionStart  int             `json:"section_start_word"`
	SectionEnd    int             `json:"section_end_word"`
	CandidateHit  bool            `json:"candidate_hit"`
	ContextHit    bool            `json:"context_hit"`
	CitedHit      bool            `json:"cited_hit"`
	SourcePresent bool            `json:"source_present"`
	QuotePresent  bool            `json:"quote_present"`
	QuotesExact   bool            `json:"quotes_exact"`
	Chunks        []ChunkEvidence `json:"chunks,omitempty"`
}

type ChunkEvidence struct {
	Role       string `json:"role"`
	ChunkID    string `json:"chunk_id"`
	Start      int    `json:"start_word"`
	End        int    `json:"end_word"`
	Quote      string `json:"quote,omitempty"`
	QuoteExact bool   `json:"quote_exact,omitempty"`
}

type ManualReview struct {
	Correctness        string `json:"correctness"`
	SemanticSupport    string `json:"semantic_support"`
	RefusalCorrectness string `json:"refusal_correctness"`
}

type evaluationReport struct {
	Status      string                 `json:"status"`
	StartedAt   string                 `json:"started_at"`
	FinishedAt  string                 `json:"finished_at,omitempty"`
	SnapshotID  string                 `json:"snapshot_id,omitempty"`
	BuildID     string                 `json:"build_id,omitempty"`
	QuestionSet string                 `json:"question_set,omitempty"`
	Experiment  bool                   `json:"experiment"`
	Settings    evaluationSettings     `json:"settings"`
	Comparisons []evaluationComparison `json:"comparisons"`
	Error       string                 `json:"error,omitempty"`
}

type evaluationSettings struct {
	OllamaEndpoint       string               `json:"ollama_endpoint"`
	EmbeddingModel       string               `json:"embedding_model"`
	Dimensions           int                  `json:"embedding_dimensions"`
	EmbeddingFingerprint EmbeddingFingerprint `json:"embedding_fingerprint"`
	Provider             string               `json:"provider"`
	Endpoint             string               `json:"endpoint"`
	Generation           GenerationSettings   `json:"generation"`
	Retrieval            RetrievalSettings    `json:"retrieval"`
	Strategy             string               `json:"strategy"`
	Mode                 string               `json:"mode"`
	Interval             string               `json:"interval"`
}

// SHA-256 of the canonical QuestionSet approved on 2026-10-04.
// Bind full questions and facts, not a duplicated subset of IDs/counts.
var approvedQuestionSetDigest = [sha256.Size]byte{
	0x62, 0x96, 0x21, 0xf1, 0x74, 0x65, 0xb6, 0xd6,
	0x6c, 0x97, 0xce, 0x2b, 0xd4, 0x71, 0x2e, 0xff,
	0x14, 0x37, 0x42, 0xd4, 0x21, 0x6e, 0x67, 0x77,
	0x3d, 0xb4, 0x11, 0x47, 0xbf, 0x63, 0x8f, 0x5d,
}

func loadQuestionSet(path string) (QuestionSet, error) {
	file, err := os.Open(path)
	if err != nil {
		return QuestionSet{}, fmt.Errorf("open question set: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4<<20))
	if err != nil {
		return QuestionSet{}, fmt.Errorf("read question set: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return QuestionSet{}, fmt.Errorf("decode question set: %w", err)
	}
	if err := requireJSONFields(fields, "snapshot_id", "questions"); err != nil {
		return QuestionSet{}, fmt.Errorf("question set: %w", err)
	}
	var records []map[string]json.RawMessage
	if err := json.Unmarshal(fields["questions"], &records); err != nil {
		return QuestionSet{}, fmt.Errorf("decode question records: %w", err)
	}
	for index, record := range records {
		if err := requireJSONFields(record, "id", "question", "expected_facts", "expected_sources", "corpus_sufficient"); err != nil {
			return QuestionSet{}, fmt.Errorf("question %d: %w", index+1, err)
		}
		var sources []map[string]json.RawMessage
		if err := json.Unmarshal(record["expected_sources"], &sources); err != nil {
			return QuestionSet{}, fmt.Errorf("question %d expected_sources: %w", index+1, err)
		}
		for sourceIndex, source := range sources {
			if err := requireJSONFields(source, "document_id", "section_id"); err != nil {
				return QuestionSet{}, fmt.Errorf("question %d expected source %d: %w", index+1, sourceIndex+1, err)
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var set QuestionSet
	if err := decoder.Decode(&set); err != nil {
		return QuestionSet{}, fmt.Errorf("decode question set: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return QuestionSet{}, errors.New("question set must contain exactly one JSON value")
	}
	if err := validateQuestionSet(set); err != nil {
		return QuestionSet{}, err
	}
	return set, nil
}

func requireJSONFields(object map[string]json.RawMessage, fields ...string) error {
	for _, field := range fields {
		if _, exists := object[field]; !exists {
			return fmt.Errorf("required JSON field %q is missing", field)
		}
	}
	return nil
}

func validateQuestionSet(set QuestionSet) error {
	canonical, err := json.Marshal(set)
	if err != nil {
		return fmt.Errorf("encode control questions: %w", err)
	}
	if sha256.Sum256(canonical) != approvedQuestionSetDigest {
		return errors.New("control questions and expectations differ from the approved ten-question set; use -question for an explicit experiment")
	}
	return nil
}

func validateQuestionSnapshot(set QuestionSet, snapshot Snapshot, build IndexBuild) error {
	if set.SnapshotID != snapshot.ID {
		return fmt.Errorf("question set snapshot %s does not match loaded snapshot %s", set.SnapshotID, snapshot.ID)
	}
	if build.SnapshotID != snapshot.ID {
		return fmt.Errorf("active index build snapshot %s does not match loaded snapshot %s", build.SnapshotID, snapshot.ID)
	}
	sections := make(map[string]map[string]Section, len(snapshot.Documents))
	for _, doc := range snapshot.Documents {
		byID := make(map[string]Section, len(doc.Sections))
		for _, section := range doc.Sections {
			byID[section.ID] = section
		}
		sections[doc.ID] = byID
	}
	for _, question := range set.Questions {
		for _, expected := range question.ExpectedSources {
			byID, exists := sections[expected.DocumentID]
			if !exists {
				return fmt.Errorf("question %s expected document %s is not in snapshot", question.ID, expected.DocumentID)
			}
			if _, exists := byID[expected.SectionID]; !exists {
				return fmt.Errorf("question %s expected section %s is not in document %s", question.ID, expected.SectionID, expected.DocumentID)
			}
		}
	}
	return nil
}

func evaluateLane(question ControlQuestion, result LaneResult, snapshot Snapshot) LaneEvaluation {
	out := LaneEvaluation{Manual: ManualReview{Correctness: "unreviewed", SemanticSupport: "unreviewed", RefusalCorrectness: "unreviewed"}}
	answer := result.Answer.Answer
	for _, fact := range question.ExpectedFacts {
		out.ExpectedFacts = append(out.ExpectedFacts, FactEvaluation{Fact: fact, Present: strings.Contains(answer, fact)})
	}
	for _, expected := range question.ExpectedSources {
		source := SourceEvaluation{ExpectedSource: expected}
		for _, doc := range snapshot.Documents {
			if doc.ID != expected.DocumentID {
				continue
			}
			for _, section := range doc.Sections {
				if section.ID == expected.SectionID {
					source.SectionPath, source.SectionStart, source.SectionEnd = section.Path, section.Start, section.End
				}
			}
		}
		for _, candidate := range result.Candidates {
			if candidate.Chunk.DocumentID == expected.DocumentID && source.SectionEnd > source.SectionStart && candidate.Chunk.Start < source.SectionEnd && candidate.Chunk.End > source.SectionStart {
				source.CandidateHit = true
				source.Chunks = append(source.Chunks, ChunkEvidence{Role: "candidate", ChunkID: candidate.Chunk.ID, Start: candidate.Chunk.Start, End: candidate.Chunk.End})
			}
		}
		var hasCitedQuote, allCitedQuotesExact bool
		for _, candidate := range result.Context {
			if candidate.Chunk.DocumentID != expected.DocumentID || source.SectionEnd <= source.SectionStart || candidate.Chunk.Start >= source.SectionEnd || candidate.Chunk.End <= source.SectionStart {
				continue
			}
			source.ContextHit = true
			source.Chunks = append(source.Chunks, ChunkEvidence{Role: "context", ChunkID: candidate.Chunk.ID, Start: candidate.Chunk.Start, End: candidate.Chunk.End})
			for _, citation := range result.Answer.Citations {
				if citation.ChunkID != candidate.Chunk.ID {
					continue
				}
				source.CitedHit, source.SourcePresent = true, true
				exact := citation.Quote != "" && strings.Contains(candidate.Chunk.Text, citation.Quote)
				source.QuotePresent = source.QuotePresent || exact
				if !hasCitedQuote {
					allCitedQuotesExact = true
				}
				hasCitedQuote = true
				allCitedQuotesExact = allCitedQuotesExact && exact
				source.Chunks = append(source.Chunks, ChunkEvidence{Role: "citation", ChunkID: candidate.Chunk.ID, Start: candidate.Chunk.Start, End: candidate.Chunk.End, Quote: citation.Quote, QuoteExact: exact})
			}
		}
		source.QuotesExact = hasCitedQuote && allCitedQuotesExact
		out.Sources = append(out.Sources, source)
	}
	return out
}

func applyManualReview(report *evaluationReport, path string) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read manual review: %w", err)
	}
	var reviews map[string][2]ManualReview
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reviews); err != nil {
		return fmt.Errorf("decode manual review: %w", err)
	}
	for i := range report.Comparisons {
		comparison := &report.Comparisons[i]
		review, ok := reviews[comparison.Pair+"/"+comparison.QuestionID]
		if !ok {
			continue
		}
		for lane := 0; lane < 2; lane++ {
			if comparison.Metrics[lane] == nil {
				continue
			}
			if review[lane].Correctness != "" {
				comparison.Metrics[lane].Manual.Correctness = review[lane].Correctness
			}
			if review[lane].SemanticSupport != "" {
				comparison.Metrics[lane].Manual.SemanticSupport = review[lane].SemanticSupport
			}
			if review[lane].RefusalCorrectness != "" {
				comparison.Metrics[lane].Manual.RefusalCorrectness = review[lane].RefusalCorrectness
			}
		}
	}
	return nil
}

func writeEvaluationReport(dir string, report evaluationReport) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create evaluation output directory: %w", err)
	}
	base := "evaluation-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode evaluation report: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, base+".json"), data, 0o600); err != nil {
		return fmt.Errorf("write evaluation JSON report: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, base+".md"), []byte(renderEvaluationMarkdown(report)), 0o600); err != nil {
		return fmt.Errorf("write evaluation Markdown report: %w", err)
	}
	return nil
}

func renderEvaluationMarkdown(report evaluationReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Week 05 evaluation — %s\n\nStarted: %s\n", report.Status, report.StartedAt)
	if report.FinishedAt != "" {
		fmt.Fprintf(&b, "Finished: %s\n", report.FinishedAt)
	}
	fmt.Fprintf(&b, "\nSnapshot: %s\nBuild: %s\nQuestion set: %s\n\nSettings: `%+v`\n", report.SnapshotID, report.BuildID, report.QuestionSet, report.Settings)
	if report.Error != "" {
		fmt.Fprintf(&b, "\nIncomplete: %s\n", report.Error)
	}
	for _, comparison := range report.Comparisons {
		fmt.Fprintf(&b, "\n## %s / %s\n\n", comparison.Pair, comparison.QuestionID)
		if comparison.Control.ID != "" {
			fmt.Fprintf(&b, "Question: %s\nCorpus sufficient: %t\nExpected facts:\n", comparison.Control.Question, comparison.Control.CorpusSufficient)
			for _, fact := range comparison.Control.ExpectedFacts {
				fmt.Fprintf(&b, "- %s\n", fact)
			}
			for _, source := range comparison.Control.ExpectedSources {
				fmt.Fprintf(&b, "- expected source %s/%s\n", source.DocumentID, source.SectionID)
			}
		}
		if comparison.Error != "" {
			fmt.Fprintf(&b, "Error: %s\n\n", comparison.Error)
		}
		if comparison.Run != nil {
			fmt.Fprintf(&b, "Saved comparison ID: `%s` — %s\n", comparison.Run.ID, comparison.Run.CreatedAt)
		}
		for lane := 0; lane < 2; lane++ {
			var result *LaneResult
			if comparison.Run != nil {
				result = &comparison.Run.Lanes[lane]
			} else {
				result = comparison.Observed[lane]
			}
			if result == nil {
				continue
			}
			fmt.Fprintf(&b, "\n### Lane %d — %s / %s\n\nGeneration: `%+v`\nRetrieval: `%+v`\nBuild: `%+v`\nDuration: %d ms\nOriginal query: %s\nSearch query: %s\n\nStatus: %s\n\nAnswer:\n\n%s\n\nCandidates: %d; context: %d\n", lane+1, result.Settings.Mode, result.Settings.Strategy, result.Generation, result.Retrieval, result.Build, result.DurationMS, result.OriginalQuery, result.SearchQuery, result.Answer.Status, result.Answer.Answer, len(result.Candidates), len(result.Context))
			for _, candidate := range result.Candidates {
				fmt.Fprintf(&b, "- candidate `%s` doc=%s words=%d-%d distance=%.6f similarity=%.6f sections=%v text=%q\n", candidate.Chunk.ID, candidate.Chunk.DocumentID, candidate.Chunk.Start, candidate.Chunk.End, candidate.Distance, 1-candidate.Distance, candidate.Chunk.SectionPaths, candidate.Chunk.Text)
			}
			for _, contextChunk := range result.Context {
				fmt.Fprintf(&b, "- context `%s` doc=%s words=%d-%d text=%q\n", contextChunk.Chunk.ID, contextChunk.Chunk.DocumentID, contextChunk.Chunk.Start, contextChunk.Chunk.End, contextChunk.Chunk.Text)
			}
			for _, citation := range result.Answer.Citations {
				fmt.Fprintf(&b, "- cited `%s`: %q\n", citation.ChunkID, citation.Quote)
			}
			if comparison.Metrics[lane] != nil {
				metrics := comparison.Metrics[lane]
				fmt.Fprintf(&b, "\nManual review: correctness=%s; semantic support=%s; refusal correctness=%s\n", metrics.Manual.Correctness, metrics.Manual.SemanticSupport, metrics.Manual.RefusalCorrectness)
				for _, fact := range metrics.ExpectedFacts {
					fmt.Fprintf(&b, "- fact exact substring present=%t: %s\n", fact.Present, fact.Fact)
				}
				for _, source := range metrics.Sources {
					fmt.Fprintf(&b, "- expected %s/%s (%s, words %d-%d): candidate=%t context=%t cited=%t source=%t quote=%t exact=%t\n", source.DocumentID, source.SectionID, source.SectionPath, source.SectionStart, source.SectionEnd, source.CandidateHit, source.ContextHit, source.CitedHit, source.SourcePresent, source.QuotePresent, source.QuotesExact)
					for _, chunk := range source.Chunks {
						fmt.Fprintf(&b, "  - %s `%s` words=%d-%d quote=%q exact=%t\n", chunk.Role, chunk.ChunkID, chunk.Start, chunk.End, chunk.Quote, chunk.QuoteExact)
					}
				}
			}
		}
	}
	return b.String()
}
