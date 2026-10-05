package main

import "testing"

func validTestQuestionSet(t *testing.T) QuestionSet {
	t.Helper()
	set, err := loadQuestionSet("questions.json")
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestValidateQuestionSetRejectsInvalidIDsAndSufficiencySources(t *testing.T) {
	t.Run("unexpected control ID", func(t *testing.T) {
		set := validTestQuestionSet(t)
		set.Questions[3].ID = "q99"
		if err := validateQuestionSet(set); err == nil {
			t.Fatal("expected invalid question ID to fail")
		}
	})
	t.Run("insufficient question with expected source", func(t *testing.T) {
		set := validTestQuestionSet(t)
		set.Questions[7].CorpusSufficient = false
		if err := validateQuestionSet(set); err == nil {
			t.Fatal("expected unsupported source expectation to fail")
		}
	})
	t.Run("sufficient question without expected source", func(t *testing.T) {
		set := validTestQuestionSet(t)
		set.Questions[0].ExpectedSources = nil
		if err := validateQuestionSet(set); err == nil {
			t.Fatal("expected missing expected source to fail")
		}
	})
	t.Run("substituted question keeps IDs and counts", func(t *testing.T) {
		set := validTestQuestionSet(t)
		set.Questions[0].Question = "What is the current price of Apple shares?"
		if err := validateQuestionSet(set); err == nil {
			t.Fatal("modified question was mislabeled as the approved control set")
		}
	})
	t.Run("empty fact keeps fact count", func(t *testing.T) {
		set := validTestQuestionSet(t)
		set.Questions[0].ExpectedFacts[0] = ""
		if err := validateQuestionSet(set); err == nil {
			t.Fatal("empty fact was accepted as an approved expectation")
		}
	})
}

func TestValidateQuestionSnapshotChecksRevisionAndAssociation(t *testing.T) {
	set := QuestionSet{SnapshotID: "snapshot-a", Questions: []ControlQuestion{
		{ID: "association-case", ExpectedSources: []ExpectedSource{{DocumentID: "doc-a", SectionID: "section-a"}}},
	}}
	expected := set.Questions[0].ExpectedSources[0]
	snapshot := Snapshot{ID: "snapshot-a", Documents: []Document{
		{ID: expected.DocumentID, Sections: []Section{{ID: expected.SectionID, Path: "Guide/Power", Start: 10, End: 25}}},
		{ID: "doc-b", Sections: []Section{{ID: "section-b", Path: "Guide/Ores", Start: 30, End: 40}}},
	}}
	build := IndexBuild{SnapshotID: "snapshot-a"}

	t.Run("section attached to different document", func(t *testing.T) {
		invalid := QuestionSet{SnapshotID: set.SnapshotID, Questions: []ControlQuestion{
			{ID: "association-case", ExpectedSources: []ExpectedSource{{DocumentID: "doc-a", SectionID: "section-b"}}},
		}}
		if err := validateQuestionSnapshot(invalid, snapshot, build); err == nil {
			t.Fatal("expected cross-document section reference to fail")
		}
	})
	t.Run("snapshot revision mismatch", func(t *testing.T) {
		invalid := set
		invalid.SnapshotID = "snapshot-old"
		if err := validateQuestionSnapshot(invalid, snapshot, build); err == nil {
			t.Fatal("expected snapshot revision mismatch to fail")
		}
	})
	t.Run("active build revision mismatch", func(t *testing.T) {
		if err := validateQuestionSnapshot(set, snapshot, IndexBuild{SnapshotID: "snapshot-old"}); err == nil {
			t.Fatal("expected active build revision mismatch to fail")
		}
	})
}

func TestEvaluateLaneSourceCitationMetrics(t *testing.T) {
	question := ControlQuestion{
		ID:              "metric-case",
		ExpectedSources: []ExpectedSource{{DocumentID: "doc-a", SectionID: "section-a"}},
	}
	snapshot := Snapshot{ID: "snapshot-a", Documents: []Document{{
		ID: "doc-a",
		Sections: []Section{
			{ID: "section-a", Path: "Guide/Repeated", Start: 10, End: 20},
			{ID: "section-b", Path: "Guide/Repeated", Start: 20, End: 30},
		},
	}}}
	chunk := func(id string, start, end int, text string) Candidate {
		return Candidate{Chunk: Chunk{ID: id, SnapshotID: snapshot.ID, DocumentID: "doc-a", Start: start, End: end, Text: text}}
	}
	evaluate := func(result LaneResult) SourceEvaluation {
		t.Helper()
		return evaluateLane(question, result, snapshot).Sources[0]
	}

	t.Run("same heading path does not substitute section identity or range", func(t *testing.T) {
		otherSection := chunk("other-section", 20, 25, "other section text")
		source := evaluate(LaneResult{
			Candidates: []Candidate{otherSection},
			Context:    []Candidate{otherSection},
			Answer:     Answer{Citations: []Citation{{ChunkID: otherSection.Chunk.ID, Quote: "other section text"}}},
		})
		if source.SectionPath != "Guide/Repeated" || source.SectionStart != 10 || source.SectionEnd != 20 {
			t.Fatalf("reported section metadata = %q [%d,%d)", source.SectionPath, source.SectionStart, source.SectionEnd)
		}
		if source.CandidateHit || source.ContextHit || source.CitedHit || source.SourcePresent {
			t.Fatalf("other section was credited to expected section: %+v", source)
		}
	})

	t.Run("half-open boundary touch is not overlap but true overlap is", func(t *testing.T) {
		touching := chunk("touching", 20, 24, "boundary")
		overlap := chunk("overlap", 19, 21, "grounded phrase")
		source := evaluate(LaneResult{
			Candidates: []Candidate{touching, overlap},
			Context:    []Candidate{touching, overlap},
			Answer:     Answer{Citations: []Citation{{ChunkID: overlap.Chunk.ID, Quote: "grounded phrase"}}},
		})
		if !source.CandidateHit || !source.ContextHit || !source.CitedHit || !source.SourcePresent {
			t.Fatalf("overlapping source was not credited: %+v", source)
		}
		if len(source.Chunks) != 3 || source.Chunks[0].ChunkID != overlap.Chunk.ID || source.Chunks[1].ChunkID != overlap.Chunk.ID || source.Chunks[2].ChunkID != overlap.Chunk.ID {
			t.Fatalf("boundary-touching chunk was reported as source evidence: %+v", source.Chunks)
		}
	})

	t.Run("retrieved context without citations is not source presence", func(t *testing.T) {
		retrieved := chunk("retrieved", 11, 15, "retrieved text")
		source := evaluate(LaneResult{
			Candidates: []Candidate{retrieved},
			Context:    []Candidate{retrieved},
			Answer:     Answer{Status: "insufficient_context", Answer: "I do not have enough information."},
		})
		if !source.CandidateHit || !source.ContextHit {
			t.Fatalf("expected retrieval/context hits: %+v", source)
		}
		if source.CitedHit || source.SourcePresent || source.QuotePresent || source.QuotesExact {
			t.Fatalf("uncited context was reported as answer source: %+v", source)
		}
	})

	t.Run("all cited quotes must be exact", func(t *testing.T) {
		cited := chunk("cited", 11, 15, "a genuine quotation")
		source := evaluate(LaneResult{
			Context: []Candidate{cited},
			Answer: Answer{Citations: []Citation{
				{ChunkID: cited.Chunk.ID, Quote: "genuine quotation"},
				{ChunkID: cited.Chunk.ID, Quote: "not in the selected chunk"},
			}},
		})
		if !source.CitedHit || !source.SourcePresent || !source.QuotePresent {
			t.Fatalf("expected citation and one present quote: %+v", source)
		}
		if source.QuotesExact {
			t.Fatalf("one genuine quote concealed an inexact cited quote: %+v", source)
		}
		if len(source.Chunks) != 3 || !source.Chunks[1].QuoteExact || source.Chunks[2].QuoteExact {
			t.Fatalf("citation-level quote evidence was not recorded accurately: %+v", source.Chunks)
		}
	})
}
