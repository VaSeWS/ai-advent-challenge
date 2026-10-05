package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/html"
)

const (
	defaultWikiAPI    = "https://wiki.gtnewhorizons.com/w/api.php"
	corpusLicense     = "CC BY-SA 4.0"
	corpusAttribution = "GT New Horizons Wiki, licensed under CC BY-SA 4.0 (https://creativecommons.org/licenses/by-sa/4.0/)."
)

// Snapshot is an immutable, revision-pinned collection of source documents.
type Snapshot struct {
	ID        string     `json:"id"`
	CreatedAt string     `json:"created_at"`
	Documents []Document `json:"documents"`
}

// Document stores normalized source text and its revision provenance.
type Document struct {
	ID                string    `json:"id"`
	Title             string    `json:"title"`
	URL               string    `json:"url"`
	Permalink         string    `json:"permalink"`
	RevisionTimestamp string    `json:"revision_timestamp"`
	RetrievedAt       string    `json:"retrieved_at"`
	Hash              string    `json:"hash"`
	License           string    `json:"license"`
	Attribution       string    `json:"attribution"`
	PageID            int64     `json:"page_id"`
	RevisionID        int64     `json:"revision_id"`
	Text              string    `json:"text"`
	Sections          []Section `json:"sections"`
	Warnings          []string  `json:"warnings"`
}

// Section records a heading path and its half-open word range in Document.Text.
type Section struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// FetchSnapshot resolves titles, then fetches and verifies each pinned revision.
func FetchSnapshot(ctx context.Context, endpoint string, titles []string) (Snapshot, error) {
	if len(titles) == 0 {
		return Snapshot{}, errors.New("fetch corpus: no titles provided")
	}
	if endpoint == "" {
		endpoint = defaultWikiAPI
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return Snapshot{}, fmt.Errorf("fetch corpus: invalid API endpoint %q", endpoint)
	}
	client := &http.Client{Timeout: 45 * time.Second}
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		client.Transport = transport.Clone()
	}
	docs := make([]Document, 0, len(titles))
	seen := make(map[string]struct{}, len(titles))
	retrieved := time.Now().UTC().Format(time.RFC3339)
	for _, requested := range titles {
		requested = strings.TrimSpace(requested)
		if requested == "" {
			return Snapshot{}, errors.New("fetch corpus: empty title")
		}
		doc, err := fetchWikiDocument(ctx, client, parsed, requested, retrieved)
		if err != nil {
			return Snapshot{}, fmt.Errorf("fetch corpus %q: %w", requested, err)
		}
		if _, exists := seen[doc.Title]; exists {
			return Snapshot{}, fmt.Errorf("fetch corpus: multiple titles resolve to canonical title %q", doc.Title)
		}
		seen[doc.Title] = struct{}{}
		docs = append(docs, doc)
	}
	snapshot := Snapshot{CreatedAt: retrieved, Documents: docs}
	snapshot.ID = snapshotID(docs)
	return snapshot, nil
}

func fetchWikiDocument(ctx context.Context, client *http.Client, endpoint *url.URL, title, retrieved string) (Document, error) {
	query := url.Values{
		"action": {"query"}, "format": {"json"}, "formatversion": {"2"},
		"redirects": {"1"}, "titles": {title}, "prop": {"info|revisions"},
		"rvprop": {"ids|timestamp"}, "rvlimit": {"1"},
	}
	var metadata struct {
		Query struct {
			Pages []struct {
				PageID    int64  `json:"pageid"`
				Title     string `json:"title"`
				Missing   bool   `json:"missing"`
				Revisions []struct {
					RevisionID int64  `json:"revid"`
					Timestamp  string `json:"timestamp"`
				} `json:"revisions"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := wikiJSON(ctx, client, endpoint, query, &metadata); err != nil {
		return Document{}, err
	}
	if len(metadata.Query.Pages) != 1 || metadata.Query.Pages[0].Missing {
		return Document{}, errors.New("page is missing")
	}
	page := metadata.Query.Pages[0]
	if page.PageID <= 0 || len(page.Revisions) != 1 || page.Revisions[0].RevisionID <= 0 || page.Title == "" {
		return Document{}, errors.New("page has no canonical revision metadata")
	}
	rev := page.Revisions[0]
	parseQuery := url.Values{
		"action": {"parse"}, "format": {"json"}, "formatversion": {"2"},
		"oldid": {strconv.FormatInt(rev.RevisionID, 10)}, "prop": {"text|revid|pageid"},
		"disableeditsection": {"1"},
	}
	var rendered struct {
		Parse struct {
			PageID int64  `json:"pageid"`
			RevID  int64  `json:"revid"`
			Text   string `json:"text"`
		} `json:"parse"`
	}
	if err := wikiJSON(ctx, client, endpoint, parseQuery, &rendered); err != nil {
		return Document{}, err
	}
	if rendered.Parse.PageID != page.PageID || rendered.Parse.RevID != rev.RevisionID {
		return Document{}, fmt.Errorf("revision mismatch: requested page/revision %d/%d, rendered %d/%d", page.PageID, rev.RevisionID, rendered.Parse.PageID, rendered.Parse.RevID)
	}
	text, sections, warnings, err := normalizeWikiHTML(rendered.Parse.Text, page.Title)
	if err != nil {
		return Document{}, err
	}
	if strings.TrimSpace(text) == "" || len(strings.Fields(text)) == 0 {
		return Document{}, errors.New("revision rendered no readable text")
	}
	hash := sha256.Sum256([]byte(text))
	pageURL := *endpoint
	pageURL.Path = strings.TrimSuffix(pageURL.Path, "/w/api.php") + "/wiki/" + strings.ReplaceAll(page.Title, " ", "_")
	permalink := pageURL.String() + "?oldid=" + strconv.FormatInt(rev.RevisionID, 10)
	return Document{
		ID:    stableID("doc", strconv.FormatInt(page.PageID, 10), strconv.FormatInt(rev.RevisionID, 10)),
		Title: page.Title, URL: pageURL.String(), Permalink: permalink,
		RevisionTimestamp: rev.Timestamp, RetrievedAt: retrieved, Hash: hex.EncodeToString(hash[:]),
		License: corpusLicense, Attribution: corpusAttribution, PageID: page.PageID, RevisionID: rev.RevisionID,
		Text: text, Sections: sections, Warnings: warnings,
	}, nil
}

func wikiJSON(ctx context.Context, client *http.Client, endpoint *url.URL, params url.Values, out any) error {
	u := *endpoint
	u.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "AI-Advent-Challenge/1.0 (revision-pinned corpus reader)")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wiki API returned HTTP %s", resp.Status)
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 32<<20))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode wiki API response: %w", err)
	}
	return nil
}

// SaveSnapshot writes a complete snapshot to a new file and never overwrites.
func SaveSnapshot(path string, s Snapshot) error {
	if path == "" {
		return errors.New("save snapshot: empty path")
	}
	if err := validateSnapshot(s); err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create snapshot exclusively: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(path)
		return fmt.Errorf("write snapshot: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("close snapshot: %w", err)
	}
	return nil
}

// LoadSnapshot loads and validates a previously saved normalized snapshot.
func LoadSnapshot(path string) (Snapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return Snapshot{}, err
	}
	defer file.Close()
	var s Snapshot
	if err := json.NewDecoder(io.LimitReader(file, 256<<20)).Decode(&s); err != nil {
		return Snapshot{}, fmt.Errorf("decode snapshot: %w", err)
	}
	if err := validateSnapshot(s); err != nil {
		return Snapshot{}, fmt.Errorf("invalid snapshot: %w", err)
	}
	return s, nil
}

func validateSnapshot(s Snapshot) error {
	if s.ID == "" || s.CreatedAt == "" || len(s.Documents) == 0 {
		return errors.New("snapshot requires ID, creation time, and documents")
	}
	seen := make(map[string]struct{}, len(s.Documents))
	for _, d := range s.Documents {
		if d.ID == "" || d.Title == "" || d.Permalink == "" || d.PageID <= 0 || d.RevisionID <= 0 || d.Hash == "" || len(strings.Fields(d.Text)) == 0 {
			return fmt.Errorf("document %q has incomplete metadata or empty text", d.ID)
		}
		hash := sha256.Sum256([]byte(d.Text))
		if hex.EncodeToString(hash[:]) != d.Hash {
			return fmt.Errorf("document %q text does not match its SHA-256", d.ID)
		}
		if _, ok := seen[d.ID]; ok {
			return fmt.Errorf("duplicate document ID %q", d.ID)
		}
		seen[d.ID] = struct{}{}
	}
	if expected := snapshotID(s.Documents); expected != s.ID {
		return fmt.Errorf("snapshot ID mismatch: expected %s", expected)
	}
	return nil
}

func snapshotID(docs []Document) string {
	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i] = d.ID + ":" + d.Hash
	}
	sort.Strings(ids)
	return stableID("snapshot", strings.Join(ids, "\n"))
}

func stableID(prefix string, values ...string) string {
	h := sha256.New()
	for _, value := range values {
		h.Write([]byte(strconv.Itoa(len(value))))
		h.Write([]byte{':'})
		h.Write([]byte(value))
	}
	return prefix + "-" + hex.EncodeToString(h.Sum(nil)[:16])
}

func normalizeWikiHTML(source, title string) (string, []Section, []string, error) {
	root, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return "", nil, nil, fmt.Errorf("parse rendered wiki HTML: %w", err)
	}
	content := findContentNode(root)
	if content == nil {
		content = root
	}
	var blocks []textBlock
	var warnings []string
	var headingPath []string
	var inlineRun strings.Builder
	inlineWarning := false
	pathForBlock := func() string {
		if path := strings.Join(headingPath, " > "); path != "" {
			return path
		}
		return title
	}
	appendBlock := func(text string, warningContext bool, preserveWhitespace bool) {
		if preserveWhitespace {
			text = strings.TrimSpace(text)
		} else {
			text = normalizeBlockWhitespace(text)
		}
		if text == "" {
			return
		}
		warning := warningContext || isWarningText(text)
		if warning && !strings.HasPrefix(strings.ToLower(text), "warning:") {
			text = "Warning: " + text
		}
		blocks = append(blocks, textBlock{text: text, path: pathForBlock()})
		if warning {
			warnings = append(warnings, text)
		}
	}
	flushInline := func() {
		if inlineRun.Len() == 0 {
			return
		}
		appendBlock(inlineRun.String(), inlineWarning, false)
		inlineRun.Reset()
		inlineWarning = false
	}
	var visit func(*html.Node, bool)
	visit = func(n *html.Node, warningContext bool) {
		if n.Type == html.TextNode {
			if strings.TrimSpace(n.Data) != "" {
				inlineRun.WriteString(n.Data)
				inlineRun.WriteByte(' ')
				inlineWarning = inlineWarning || warningContext
			}
			return
		}
		if n.Type != html.ElementNode {
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				visit(child, warningContext)
			}
			return
		}
		if skipWikiNode(n) {
			return
		}
		warningContext = warningContext || hasWarningMarker(n)
		name := strings.ToLower(n.Data)
		if level := headingLevel(name); level > 0 {
			flushInline()
			text := inlineText(n)
			if text != "" {
				if len(headingPath) >= level {
					headingPath = headingPath[:level-1]
				}
				for len(headingPath) < level-1 {
					headingPath = append(headingPath, title)
				}
				headingPath = append(headingPath, text)
				blocks = append(blocks, textBlock{text: text, path: strings.Join(headingPath, " > ")})
			}
			return
		}
		if isBlockElement(name) {
			flushInline()
			text := renderBlock(n)
			appendBlock(text, warningContext, name == "pre")
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child, warningContext)
		}
	}
	for child := content.FirstChild; child != nil; child = child.NextSibling {
		visit(child, false)
	}
	flushInline()
	var output strings.Builder
	sections := make([]Section, 0)
	wordOffset := 0
	for _, block := range blocks {
		if block.text == "" {
			continue
		}
		if output.Len() != 0 {
			output.WriteByte('\n')
		}
		output.WriteString(block.text)
		words := len(strings.Fields(block.text))
		if len(sections) == 0 || sections[len(sections)-1].Path != block.path {
			sections = append(sections, Section{
				ID:   stableID("section", block.path, strconv.Itoa(wordOffset)),
				Path: block.path, Start: wordOffset, End: wordOffset,
			})
		}
		sections[len(sections)-1].End = wordOffset + words
		wordOffset += words
	}
	// Keep unique warning strings in source order.
	unique := warnings[:0]
	seenWarning := make(map[string]struct{}, len(warnings))
	for _, warning := range warnings {
		if _, exists := seenWarning[warning]; !exists {
			seenWarning[warning] = struct{}{}
			unique = append(unique, warning)
		}
	}
	return output.String(), sections, unique, nil
}

type textBlock struct{ text, path string }

func findContentNode(n *html.Node) *html.Node {
	if n.Type == html.ElementNode {
		for _, attr := range n.Attr {
			if attr.Key == "id" && (attr.Val == "mw-content-text" || attr.Val == "content") {
				return n
			}
		}
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := findContentNode(child); found != nil {
			return found
		}
	}
	return nil
}

func skipWikiNode(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	switch strings.ToLower(n.Data) {
	case "script", "style", "noscript", "nav", "header", "footer", "form", "button", "svg":
		return true
	}
	for _, attr := range n.Attr {
		if attr.Key == "id" && (attr.Val == "toc" || attr.Val == "catlinks" || attr.Val == "mw-navigation" || attr.Val == "siteSub") {
			return true
		}
		if attr.Key == "class" {
			for _, class := range strings.Fields(attr.Val) {
				c := strings.ToLower(class)
				if c == "mw-editsection" || c == "navbox" || c == "metadata" || c == "portal" || c == "noprint" || c == "mw-jump-link" {
					return true
				}
			}
		}
	}
	return false
}

func headingLevel(name string) int {
	if len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6' {
		return int(name[1] - '0')
	}
	return 0
}

func isBlockElement(name string) bool {
	switch name {
	case "p", "ul", "ol", "dl", "table", "pre", "blockquote":
		return true
	}
	return false
}

func renderBlock(n *html.Node) string {
	switch strings.ToLower(n.Data) {
	case "ul", "ol":
		var lines []string
		listItems(n, &lines, 0)
		return strings.Join(lines, "\n")
	case "dl":
		var lines []string
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && (child.Data == "dt" || child.Data == "dd") {
				prefix := "• "
				if child.Data == "dt" {
					prefix = ""
				}
				if text := inlineText(child); text != "" {
					lines = append(lines, prefix+text)
				}
			}
		}
		return strings.Join(lines, "\n")
	case "table":
		return renderTable(n)
	case "pre":
		return strings.TrimSpace(preText(n))
	case "blockquote":
		return strings.TrimSpace(inlineText(n))
	default:
		return strings.TrimSpace(inlineText(n))
	}
}

func preText(n *html.Node) string {
	var text strings.Builder
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if skipWikiNode(node) {
			return
		}
		if node.Type == html.TextNode {
			text.WriteString(node.Data)
			return
		}
		if node.Type == html.ElementNode && strings.EqualFold(node.Data, "br") {
			text.WriteByte('\n')
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(n)
	return text.String()
}

func listItems(n *html.Node, lines *[]string, depth int) {
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode {
			continue
		}
		if child.Data == "li" {
			prefix := strings.Repeat("  ", depth) + "- "
			var nestedText strings.Builder
			var nestedLists []*html.Node
			for item := child.FirstChild; item != nil; item = item.NextSibling {
				if item.Type == html.ElementNode && (item.Data == "ul" || item.Data == "ol") {
					nestedLists = append(nestedLists, item)
				} else {
					nestedText.WriteString(textContent(item))
					nestedText.WriteByte(' ')
				}
			}
			if text := strings.TrimSpace(collapseWhitespace(nestedText.String())); text != "" {
				*lines = append(*lines, prefix+text)
			}
			for _, nested := range nestedLists {
				listItems(nested, lines, depth+1)
			}
		} else if child.Data == "ul" || child.Data == "ol" {
			listItems(child, lines, depth)
		}
	}
}

func renderTable(n *html.Node) string {
	var rows [][]string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "tr" {
			var cells []string
			for cell := node.FirstChild; cell != nil; cell = cell.NextSibling {
				if cell.Type == html.ElementNode && (cell.Data == "th" || cell.Data == "td") {
					value := strings.TrimSpace(collapseWhitespace(inlineText(cell)))
					if value != "" {
						cells = append(cells, value)
					}
				}
			}
			if len(cells) > 0 {
				rows = append(rows, cells)
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	if len(rows) == 0 {
		return ""
	}
	var lines []string
	var headers []string
	hasHead := false
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == "thead" {
			hasHead = true
			break
		}
	}
	headerRow := hasHead || firstTableRowHasHeaders(n)
	for rowIndex, row := range rows {
		if rowIndex == 0 && headerRow {
			headers = row
			lines = append(lines, strings.Join(row, " | "))
			continue
		}
		if len(headers) == len(row) && len(headers) > 0 {
			pairs := make([]string, len(row))
			for i := range row {
				pairs[i] = headers[i] + ": " + row[i]
			}
			lines = append(lines, strings.Join(pairs, " | "))
		} else {
			lines = append(lines, strings.Join(row, " | "))
		}
	}
	return strings.Join(lines, "\n")
}

func firstTableRowHasHeaders(table *html.Node) bool {
	var firstRow *html.Node
	var find func(*html.Node)
	find = func(node *html.Node) {
		if firstRow != nil {
			return
		}
		if node.Type == html.ElementNode && node.Data == "tr" {
			firstRow = node
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			find(child)
		}
	}
	find(table)
	if firstRow == nil {
		return false
	}
	count := 0
	for cell := firstRow.FirstChild; cell != nil; cell = cell.NextSibling {
		if cell.Type != html.ElementNode || (cell.Data != "th" && cell.Data != "td") {
			continue
		}
		if cell.Data != "th" {
			return false
		}
		count++
	}
	return count > 0
}

func inlineText(n *html.Node) string { return strings.TrimSpace(collapseWhitespace(textContent(n))) }

func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if skipWikiNode(node) {
			return
		}
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
			b.WriteByte(' ')
			return
		}
		if node.Type == html.ElementNode && (node.Data == "br" || node.Data == "li") {
			b.WriteByte(' ')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if node.Type == html.ElementNode && (node.Data == "br" || node.Data == "li") {
			b.WriteByte(' ')
		}
	}
	walk(n)
	return b.String()
}

func hasWarningMarker(n *html.Node) bool {
	for _, attr := range n.Attr {
		if attr.Key == "class" || attr.Key == "role" {
			v := strings.ToLower(attr.Val)
			if strings.Contains(v, "warning") || strings.Contains(v, "caution") || strings.Contains(v, "alert") || strings.Contains(v, "ambox") {
				return true
			}
		}
	}
	return false
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}
func normalizeBlockWhitespace(text string) string {
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(collapseWhitespace(lines[i]))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
func isWarningText(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range []string{"warning:", "caution:", "outdated:", "obsolete:", "this information may be outdated", "may be outdated", "deprecated:"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
