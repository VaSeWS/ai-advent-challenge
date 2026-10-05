package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultCorpusTitles = "Tier|LV|MV|HV|EV|IV|LuV|ZPM|UV|UHV|UEV|UIV|UMV|UXV|Electricity|Power Generation|Ore Processing Concepts|Rockets|Planets"

// RunCorpus fetches a revision-pinned wiki snapshot or reports an existing one.
func RunCorpus(ctx context.Context, args []string) error {
	date := time.Now().UTC().Format("20060102")
	flags := flag.NewFlagSet("corpus", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	wikiAPI := flags.String("wiki-api", defaultWikiAPI, "MediaWiki API endpoint")
	titlesFlag := flags.String("titles", defaultCorpusTitles, "pipe-separated wiki titles")
	out := flags.String("out", filepath.Join("week-05", "corpus", "gtnh-"+date+".json"), "new snapshot output path (never overwritten)")
	report := flags.String("report", "", "report an existing snapshot without fetching")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: corpus [flags]")
		flags.SetOutput(os.Stderr)
		flags.PrintDefaults()
		flags.SetOutput(io.Discard)
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("corpus: unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}

	var snapshot Snapshot
	var err error
	if *report != "" {
		snapshot, err = LoadSnapshot(*report)
		if err != nil {
			return fmt.Errorf("load snapshot report: %w", err)
		}
		fmt.Printf("Snapshot report (no wiki fetch): %s\n", *report)
	} else {
		var titles []string
		for _, title := range strings.Split(*titlesFlag, "|") {
			title = strings.TrimSpace(title)
			if title != "" {
				titles = append(titles, title)
			}
		}
		snapshot, err = FetchSnapshot(ctx, *wikiAPI, titles)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
			return fmt.Errorf("create snapshot directory: %w", err)
		}
		if err := SaveSnapshot(*out, snapshot); err != nil {
			return err
		}
		fmt.Printf("Saved new snapshot: %s\n", *out)
	}

	words := 0
	fmt.Printf("Snapshot ID: %s\nCaptured: %s\nDocuments: %d\n", snapshot.ID, snapshot.CreatedAt, len(snapshot.Documents))
	fmt.Println("Source manifest:")
	for _, doc := range snapshot.Documents {
		docWords := len(strings.Fields(doc.Text))
		words += docWords
		fmt.Printf("- %s | words: %d | page: %d | revision: %d (%s) | retrieved: %s\n  URL: %s\n  Pinned revision: %s\n  SHA-256: %s\n  License: %s\n  Attribution: %s\n", doc.Title, docWords, doc.PageID, doc.RevisionID, doc.RevisionTimestamp, doc.RetrievedAt, doc.URL, doc.Permalink, doc.Hash, doc.License, doc.Attribution)
		if len(doc.Sections) == 0 {
			fmt.Println("  Sections: none recorded")
		} else {
			fmt.Println("  Sections (word offsets):")
			for _, section := range doc.Sections {
				fmt.Printf("    %s [%d,%d)\n", section.Path, section.Start, section.End)
			}
		}
		if len(doc.Warnings) == 0 {
			fmt.Println("  Extracted warning metadata: none (warning wording may still be present in text)")
		} else {
			fmt.Printf("  Extracted warnings: %s\n", strings.Join(doc.Warnings, "; "))
		}
	}
	fmt.Printf("Normalized source words (before chunk overlap): %d\nPage-equivalent estimate (500 words/page): %.3f\n", words, float64(words)/500)
	fmt.Println("Limits: this is normalized text and recorded sections, not complete wiki pages; images/image content, NEI or Quest Book recipes, and absent game data are not available evidence. Revision timestamps are not modpack versions. Wiki content may be outdated; inspect pinned sources and verify version-sensitive claims independently. Corpus text may be sent to Groq when retrieved in chat.")
	if words < 15000 {
		return fmt.Errorf("corpus acceptance failed: %d source words; at least 15000 required", words)
	}
	fmt.Println("Corpus size gate: PASS (at least 15000 source words).")
	return nil
}
