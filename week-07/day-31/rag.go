package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Chunk is a bounded, line-addressable piece of an allowed project document.
type Chunk struct {
	Path      string
	Section   string
	StartLine int
	EndLine   int
	Text      string
}

// Hit is a corpus chunk and its lexical relevance score.
type Hit struct {
	Chunk Chunk
	Score float64
}

const (
	maxChunkLines = 80
	maxChunkChars = 2000
)

var headingPattern = regexp.MustCompile(`^#{1,6}[ \t]+`)
var yamlRootPattern = regexp.MustCompile(`^([^ #][^:]*):(?:[ \t]*#.*)?[ \t]*$`)
var yamlChildPattern = regexp.MustCompile(`^  ([^ #][^:]*):(?:[ \t]*#.*)?[ \t]*$`)
var yamlComponentPattern = regexp.MustCompile(`^    ([^ #][^:]*):(?:[ \t]*#.*)?[ \t]*$`)
var localComponentRef = regexp.MustCompile(`\$ref:[ \t]*['"]?#/components/(schemas|responses)/([^'\"# \t]+)`)
var yamlOperationPattern = regexp.MustCompile(`^    (get|put|post|delete|options|head|patch|trace):[ \t]*$`)
var tokenPattern = regexp.MustCompile(`[\p{L}\p{N}]+`)

// loadCorpus reads only README.md, docs Markdown, and docs/openapi-schema.yml.
// Symlinks are skipped rather than followed, including symlinked allowlisted files.
func loadCorpus(root string) ([]Chunk, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve project root %q: %w", root, err)
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("open project root %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("project root %q is not a directory", root)
	}
	docsPath := filepath.Join(absRoot, "docs")
	docsInfo, err := os.Lstat(docsPath)
	if err != nil {
		return nil, fmt.Errorf("open project docs directory %q: %w", docsPath, err)
	}
	if docsInfo.Mode()&os.ModeSymlink != 0 || !docsInfo.IsDir() {
		return nil, fmt.Errorf("project docs path %q is not a real directory", docsPath)
	}

	var chunks []Chunk
	readAllowed := func(full, rel string, markdown bool) error {
		st, e := os.Lstat(full)
		if e != nil {
			return fmt.Errorf("inspect documentation %q: %w", rel, e)
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return nil
		}
		f, e := os.Open(full)
		if e != nil {
			return fmt.Errorf("read documentation %q: %w", rel, e)
		}
		defer f.Close()
		lines, e := readLines(f)
		if e != nil {
			return fmt.Errorf("read documentation %q: %w", rel, e)
		}
		for _, line := range lines {
			if len(line) > maxChunkChars {
				return fmt.Errorf("documentation %q contains a line exceeding %d bytes", rel, maxChunkChars)
			}
		}
		if markdown {
			chunks = append(chunks, markdownChunks(rel, lines)...)
		} else {
			chunks = append(chunks, openAPIChunks(rel, lines)...)
		}
		return nil
	}
	readme := filepath.Join(absRoot, "README.md")
	if _, e := os.Lstat(readme); e == nil {
		if err := readAllowed(readme, "README.md", true); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(e) {
		return nil, fmt.Errorf("inspect README.md: %w", e)
	}

	walkErr := filepath.WalkDir(docsPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk documentation directory: %w", walkErr)
		}
		if path == docsPath {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		relDocs, err := filepath.Rel(docsPath, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if excludedRAGPath(relDocs) {
				return filepath.SkipDir
			}
			return nil
		}
		if excludedRAGPath(relDocs) {
			return nil
		}
		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel != "docs/openapi-schema.yml" && !strings.HasPrefix(rel, "docs/") || !(strings.HasSuffix(rel, ".md") || rel == "docs/openapi-schema.yml") {
			return nil
		}
		return readAllowed(path, rel, rel != "docs/openapi-schema.yml")
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("no readable README.md or documentation found under %q", root)
	}
	sort.Slice(chunks, func(i, j int) bool {
		if chunks[i].Path != chunks[j].Path {
			return chunks[i].Path < chunks[j].Path
		}
		return chunks[i].StartLine < chunks[j].StartLine
	})
	return chunks, nil
}
func excludedRAGPath(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts {
		part = strings.ToLower(part)
		if strings.HasPrefix(part, ".") {
			return true
		}
		switch part {
		case "data", "media", "dump", "dumps", "credentials", "secret", "secrets",
			"generated", "vendor", "node_modules", "dependencies":
			return true
		}
	}

	name := strings.ToLower(parts[len(parts)-1])
	if name == "secret" || name == "secrets" || name == "credentials" ||
		strings.HasPrefix(name, ".env") || strings.Contains(name, "credential") ||
		strings.Contains(name, "secret") || strings.Contains(name, "dump") {
		return true
	}
	for _, suffix := range []string{".dump", ".sql", ".bak", ".pem", ".key", ".crt", ".p12", ".pfx", ".keystore"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func readLines(f *os.File) ([]string, error) {
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4096), 4*1024*1024)
	var lines []string
	for s.Scan() {
		lines = append(lines, s.Text())
	}
	return lines, s.Err()
}

func makeChunk(path, section string, lines []string, start int) Chunk {
	return Chunk{Path: path, Section: section, StartLine: start + 1, EndLine: start + len(lines), Text: strings.Join(lines, "\n")}
}

func markdownChunks(path string, lines []string) []Chunk {
	var out []Chunk
	var headings []string
	var headingLevels []int
	section := ""
	var body []string
	bodyStart := 0
	flush := func() {
		out = appendBoundedChunks(out, path, section, body, bodyStart)
		body = nil
	}
	for i, line := range lines {
		if headingPattern.MatchString(line) {
			flush()
			trimmed := strings.TrimSpace(line)
			level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
			for len(headingLevels) > 0 && headingLevels[len(headingLevels)-1] >= level {
				headingLevels = headingLevels[:len(headingLevels)-1]
				headings = headings[:len(headings)-1]
			}
			headings = append(headings, strings.TrimSpace(strings.TrimLeft(trimmed, "#")))
			headingLevels = append(headingLevels, level)
			section = strings.Join(headings, " > ")
			bodyStart = i
			body = append(body, line)
			continue
		}
		if len(body) == 0 {
			bodyStart = i
		}
		body = append(body, line)
	}
	flush()
	return out
}

// openAPIChunks splits at paths, operations, and component definitions. Component
// chunks remain independently addressable so every citation has a contiguous range.
func openAPIChunks(path string, lines []string) []Chunk {
	var out []Chunk
	root, section, category, pathItem := "", "OpenAPI", "", ""
	current := 0
	flush := func(end int) {
		if current < end {
			out = appendYAMLBounded(out, path, section, lines, current, end)
		}
	}
	for i, line := range lines {
		if m := yamlRootPattern.FindStringSubmatch(line); m != nil {
			flush(i)
			root = strings.Trim(m[1], " '\"")
			section = root
			category, pathItem = "", ""
			current = i
			continue
		}
		switch root {
		case "paths":
			if m := yamlChildPattern.FindStringSubmatch(line); m != nil {
				flush(i)
				pathItem = strings.Trim(m[1], " '\"")
				section = "paths " + pathItem
				current = i
				continue
			}
			if pathItem != "" {
				if m := yamlOperationPattern.FindStringSubmatch(line); m != nil {
					flush(i)
					section = "paths " + pathItem + " " + m[1]
					current = i
				}
			}
		case "components":
			if m := yamlChildPattern.FindStringSubmatch(line); m != nil {
				flush(i)
				category = strings.Trim(m[1], " '\"")
				section = "components " + category
				current = i
				continue
			}
			if category != "" {
				if m := yamlComponentPattern.FindStringSubmatch(line); m != nil {
					flush(i)
					section = "components " + category + " " + strings.Trim(m[1], " '\"")
					current = i
				}
			}
		}
	}
	flush(len(lines))
	return out
}

func appendYAMLBounded(out []Chunk, path, section string, lines []string, start, end int) []Chunk {
	return appendBoundedChunks(out, path, section, lines[start:end], start)
}

func appendBoundedChunks(out []Chunk, path, section string, lines []string, start int) []Chunk {
	for offset := 0; offset < len(lines); {
		end := offset
		size := 0
		for end < len(lines) && end-offset < maxChunkLines {
			lineSize := len(lines[end])
			if end > offset {
				lineSize++
			}
			if size+lineSize > maxChunkChars {
				break
			}
			size += lineSize
			end++
		}
		out = append(out, makeChunk(path, section, lines[offset:end], start+offset))
		offset = end
	}
	return out
}

var stopWords = wordSet("a an and are as at be by for from has have in into is it of on or that the this to was were with what which how who where when why do does did can could should would please про что как где когда почему для из к на по и или это ли в во не а об от с со получи получить покажи расскажи устроена устроен устроено находится расположена расположен расположено")

var aliases = map[string][]string{
	"структура":      {"структуры", "structure", "layout", "organization", "project"},
	"структуры":      {"структура", "structure", "layout", "organization", "project"},
	"structure":      {"структура", "структуры", "layout"},
	"project":        {"проект", "проекта", "проектом"},
	"проект":         {"project", "проекта", "проектом"},
	"проекта":        {"project", "проект", "проектом"},
	"проектом":       {"project", "проект", "проекта"},
	"ингредиент":     {"ingredient", "ingredients"},
	"ингредиенты":    {"ingredient", "ingredients"},
	"ingredient":     {"ингредиент", "ингредиенты"},
	"рецепт":         {"recipe", "recipes"},
	"рецепты":        {"recipe", "recipes"},
	"recipe":         {"рецепт", "рецепты"},
	"количество":     {"amount", "quantity"},
	"amount":         {"количество", "quantity"},
	"quantity":       {"количество", "amount"},
	"единица":        {"unit", "units", "меры"},
	"unit":           {"единица", "units", "меры"},
	"measure":        {"единица", "меры"},
	"покупок":        {"shopping", "cart", "список"},
	"покупки":        {"shopping", "cart", "список"},
	"корзина":        {"shopping", "cart", "покупки"},
	"shopping":       {"покупки", "покупок", "cart"},
	"cart":           {"покупки", "покупок", "shopping"},
	"список":         {"list"},
	"list":           {"список"},
	"авторизация":    {"authentication", "auth", "token"},
	"authentication": {"авторизация", "auth", "token"},
	"скачать":        {"download", "скачивания"},
	"download":       {"скачать", "скачивания"},
	"обновить":       {"update", "put", "patch"},
	"обновление":     {"update", "put", "patch"},
	"update":         {"обновить", "put", "patch"},
	"платеж":         {"payment", "payments"},
	"платежи":        {"payment", "payments"},
	"payment":        {"платеж", "платежи"},
}

func wordSet(words string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, word := range strings.Fields(words) {
		set[word] = struct{}{}
	}
	return set
}

func tokens(text string) []string {
	return tokenPattern.FindAllString(strings.ToLower(text), -1)
}

func retrieve(chunks []Chunk, question string, limit int) []Hit {
	if limit <= 0 || len(chunks) == 0 {
		return nil
	}
	query := make(map[string]float64)
	for _, token := range tokens(question) {
		if _, stop := stopWords[token]; stop || len([]rune(token)) < 2 {
			continue
		}
		query[token] += 1
		for _, alias := range aliases[token] {
			query[alias] += .45
		}
	}
	queryTerms := make([]string, 0, len(query))
	for term := range query {
		queryTerms = append(queryTerms, term)
	}
	sort.Strings(queryTerms)
	if len(query) == 0 {
		return nil
	}
	docFreq := make(map[string]int)
	docTokens := make([]map[string]float64, len(chunks))
	lengths := make([]int, len(chunks))
	totalLen := 0
	const headingWeight = 2.5
	for i, chunk := range chunks {
		freq := make(map[string]float64)
		bodyTokens := tokens(chunk.Text)
		headingTokens := tokens(chunk.Section)
		for _, token := range bodyTokens {
			if _, stop := stopWords[token]; !stop {
				freq[token]++
			}
		}
		for _, token := range headingTokens {
			if _, stop := stopWords[token]; !stop {
				freq[token] += headingWeight
			}
		}
		docTokens[i] = freq
		lengths[i] = len(bodyTokens) + len(headingTokens)
		totalLen += lengths[i]
		for term := range freq {
			docFreq[term]++
		}
	}
	avgLen := float64(totalLen) / float64(len(chunks))
	if avgLen == 0 {
		avgLen = 1
	}
	hits := make([]Hit, 0, len(chunks))
	const k1, b = 1.2, .75
	for i, chunk := range chunks {
		score := 0.0
		for _, term := range queryTerms {
			qtf := query[term]
			tf := docTokens[i][term]
			if tf == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(chunks)-docFreq[term])+.5)/(float64(docFreq[term])+.5))
			norm := tf + k1*(1-b+b*float64(lengths[i])/avgLen)
			score += qtf * idf * tf * (k1 + 1) / norm
		}
		if score > 0 {
			hits = append(hits, Hit{Chunk: chunk, Score: score})
		}
	}
	sortHits(hits)
	return expandLocalReferences(chunks, hits, limit)
}

func sortHits(hits []Hit) {
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Chunk.Path != hits[j].Chunk.Path {
			return hits[i].Chunk.Path < hits[j].Chunk.Path
		}
		if hits[i].Chunk.StartLine != hits[j].Chunk.StartLine {
			return hits[i].Chunk.StartLine < hits[j].Chunk.StartLine
		}
		return hits[i].Chunk.EndLine < hits[j].Chunk.EndLine
	})
}

func expandLocalReferences(chunks []Chunk, ranked []Hit, limit int) []Hit {
	type location struct {
		path string
		line int
	}
	componentByID := make(map[string][]int)
	chunkByLocation := make(map[location]int, len(chunks))
	for i, chunk := range chunks {
		chunkByLocation[location{chunk.Path, chunk.StartLine}] = i
		fields := strings.Fields(chunk.Section)
		if len(fields) >= 3 && fields[0] == "components" && (fields[1] == "schemas" || fields[1] == "responses") {
			id := fields[1] + "/" + fields[2]
			componentByID[id] = append(componentByID[id], i)
		}
	}
	selected := make(map[int]float64)
	var queue []Hit
	seedCount := len(ranked)
	if seedCount > limit {
		seedCount = limit
	}
	for _, hit := range ranked[:seedCount] {
		if i, ok := chunkByLocation[location{hit.Chunk.Path, hit.Chunk.StartLine}]; ok {
			selected[i] = hit.Score
			queue = append(queue, hit)
		}
	}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		parentIndex, ok := chunkByLocation[location{parent.Chunk.Path, parent.Chunk.StartLine}]
		if !ok {
			continue
		}
		parentFields := strings.Fields(chunks[parentIndex].Section)
		if len(parentFields) >= 3 && parentFields[0] == "components" &&
			(parentFields[1] == "schemas" || parentFields[1] == "responses") {
			id := parentFields[1] + "/" + parentFields[2]
			for _, sibling := range componentByID[id] {
				if prior, exists := selected[sibling]; exists && prior >= parent.Score {
					continue
				}
				selected[sibling] = parent.Score
				queue = append(queue, Hit{Chunk: chunks[sibling], Score: parent.Score})
			}
		}
		for _, ref := range localComponentRef.FindAllStringSubmatch(chunks[parentIndex].Text, -1) {
			dependencies := componentByID[ref[1]+"/"+ref[2]]
			depScore := parent.Score * .75
			for _, depIndex := range dependencies {
				if prior, exists := selected[depIndex]; exists && prior >= depScore {
					continue
				}
				selected[depIndex] = depScore
				queue = append(queue, Hit{Chunk: chunks[depIndex], Score: depScore})
			}
		}
	}
	expanded := make([]Hit, 0, len(selected))
	for i, score := range selected {
		expanded = append(expanded, Hit{Chunk: chunks[i], Score: score})
	}
	sortHits(expanded)
	return expanded
}
