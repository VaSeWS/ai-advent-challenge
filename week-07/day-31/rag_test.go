package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCorpusKeepsOnlyAllowedDocumentsAndRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	writeRAGFixture(t, root, "README.md", "# Foodgram\nRecipe project overview.\n")
	writeRAGFixture(t, root, "docs/guide.md", "# Guide\nTrusted documentation.\n")
	writeRAGFixture(t, root, "docs/private.env", "secret-marker\n")
	writeRAGFixture(t, root, "docs/dump.json", "dataset-marker\n")
	writeRAGFixture(t, root, "other.md", "outside corpus\n")

	outside := filepath.Join(t.TempDir(), "escape.md")
	if err := os.WriteFile(outside, []byte("escaped-marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "docs", "escape.md")); err != nil {
		t.Fatalf("create corpus symlink: %v", err)
	}

	chunks, err := loadCorpus(root)
	if err != nil {
		t.Fatalf("loadCorpus: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("loadCorpus returned no documentation chunks")
	}
	var readme, guide bool
	for _, chunk := range chunks {
		switch chunk.Path {
		case "README.md":
			readme = true
		case "docs/guide.md":
			guide = true
		}
		if strings.Contains(chunk.Text, "secret-marker") || strings.Contains(chunk.Text, "dataset-marker") || strings.Contains(chunk.Text, "escaped-marker") {
			t.Errorf("non-document or escaped content entered corpus: %q", chunk.Path)
		}
		if chunk.Path == "docs/escape.md" {
			t.Errorf("symlinked document entered corpus: %q", chunk.Path)
		}
		if chunk.Path == "other.md" {
			t.Errorf("document outside allowed corpus entered corpus: %q", chunk.Path)
		}
	}
	if !readme || !guide {
		t.Fatalf("allowed documents missing (README=%t docs guide=%t)", readme, guide)
	}
}

func TestLoadCorpusSkipsSensitiveComponentsAndFiles(t *testing.T) {
	root := t.TempDir()
	writeRAGFixture(t, root, "README.md", "# Project\nOrdinary project overview.\n")
	writeRAGFixture(t, root, "docs/guides/nested.md", "# Nested guide\nbenign-nested-marker ordinary retrieval evidence.\n")
	excluded := []string{
		"docs/.hidden/hidden.md",
		"docs/.env/notes.md",
		"docs/data/export.md",
		"docs/media/private.md",
		"docs/dump/export.md",
		"docs/dumps/export.md",
		"docs/credentials/notes.md",
		"docs/secrets/notes.md",
		"docs/generated/output.md",
		"docs/vendor/dependency.md",
		"docs/node_modules/package.md",
		"docs/dependencies/copy.md",
		"docs/secret-notes.md",
		"docs/credentials-backup.md",
		"docs/database.sql",
		"docs/archive.dump",
		"docs/private.pem",
	}
	for i, path := range excluded {
		writeRAGFixture(t, root, path, fmt.Sprintf("excluded-marker-%02d secret-marker-%02d\n", i, i))
	}

	chunks, err := loadCorpus(root)
	if err != nil {
		t.Fatalf("loadCorpus: %v", err)
	}
	for _, chunk := range chunks {
		if strings.Contains(chunk.Text, "excluded-marker") || strings.Contains(chunk.Text, "secret-marker") {
			t.Errorf("excluded content entered corpus from %q", chunk.Path)
		}
	}
	if !hitsContain(retrieve(chunks, "benign nested ordinary retrieval evidence", 5), "benign-nested-marker") {
		t.Fatal("ordinary nested documentation was not retrievable")
	}
	for _, path := range excluded {
		if hitsContain(retrieve(chunks, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), 5), "excluded-marker") {
			t.Errorf("retrieval surfaced excluded content from %q", path)
		}
	}
}

func TestLoadCorpusBoundsChunksAtSourceLines(t *testing.T) {
	root := t.TempDir()
	var content strings.Builder
	content.WriteString("# Long section\n")
	for i := range 34 {
		fmt.Fprintf(&content, "filler line %02d %s\n", i, strings.Repeat("x", 70))
	}
	content.WriteString("unique evidence marker near the end of this long section.\n")
	writeRAGFixture(t, root, "docs/long.md", content.String())

	chunks, err := loadCorpus(root)
	if err != nil {
		t.Fatalf("loadCorpus: %v", err)
	}
	for _, chunk := range chunks {
		if len(chunk.Text) > maxChunkChars || chunk.EndLine-chunk.StartLine+1 > maxChunkLines {
			t.Errorf("chunk exceeds bounds: %d bytes, lines %d-%d", len(chunk.Text), chunk.StartLine, chunk.EndLine)
		}
	}
	hits := retrieve(chunks, "unique evidence marker", 5)
	if !hitsContain(hits, "unique evidence marker near the end") {
		t.Fatal("evidence near the end of a long section was not retained in a retrievable chunk")
	}
	var found bool
	for _, hit := range hits {
		if strings.Contains(hit.Chunk.Text, "unique evidence marker") {
			found = true
			if hit.Chunk.EndLine != 36 {
				t.Errorf("evidence chunk range ends at %d, want source line 36", hit.Chunk.EndLine)
			}
		}
	}
	if !found {
		t.Fatal("retrieval did not return the evidence chunk")
	}
}

func TestLoadCorpusRejectsOversizedSourceLine(t *testing.T) {
	root := t.TempDir()
	writeRAGFixture(t, root, "docs/oversized.md", strings.Repeat("x", maxChunkChars+1))
	if _, err := loadCorpus(root); err == nil || !strings.Contains(err.Error(), "line exceeding") {
		t.Fatalf("loadCorpus error = %v, want clear oversized-line error", err)
	}
}

func TestLoadCorpusPreservesMarkdownCitationRanges(t *testing.T) {
	root := t.TempDir()
	content := "# Guide\n\nIntroduction.\n## Ingredient amount\n\nThe junction stores amount and unit together.\n"
	writeRAGFixture(t, root, "docs/guide.md", content)

	chunks, err := loadCorpus(root)
	if err != nil {
		t.Fatalf("loadCorpus: %v", err)
	}
	var found bool
	for _, chunk := range chunks {
		if !strings.Contains(chunk.Text, "junction stores amount and unit") {
			continue
		}
		found = true
		lines := strings.Split(content, "\n")
		if chunk.StartLine < 1 || chunk.EndLine < chunk.StartLine || chunk.EndLine > len(lines) {
			t.Fatalf("invalid citation range %d-%d for %d source lines", chunk.StartLine, chunk.EndLine, len(lines))
		}
		cited := strings.Join(lines[chunk.StartLine-1:chunk.EndLine], "\n")
		if !strings.Contains(cited, "junction stores amount and unit together") {
			t.Fatalf("citation range %d-%d does not cover its evidence", chunk.StartLine, chunk.EndLine)
		}
		if !strings.Contains(strings.ToLower(chunk.Section), "ingredient amount") {
			t.Errorf("citation section %q does not identify the heading", chunk.Section)
		}
	}
	if !found {
		t.Fatal("ingredient evidence was not indexed")
	}
}

func TestRetrieveKeepsReferencesBeyondSeedLimit(t *testing.T) {
	chunks := make([]Chunk, 0, 12)
	for i := range 6 {
		chunks = append(chunks, Chunk{
			Path: "docs/openapi-schema.yml", Section: fmt.Sprintf("paths /lookup%d get", i),
			StartLine: i + 1, EndLine: i + 1,
			Text: fmt.Sprintf("lookup operation $ref: '#/components/schemas/Schema%d'", i),
		})
	}
	for i := range 6 {
		chunks = append(chunks, Chunk{
			Path: "docs/openapi-schema.yml", Section: fmt.Sprintf("components schemas Schema%d", i),
			StartLine: 100 + i, EndLine: 100 + i, Text: fmt.Sprintf("schema evidence marker %d", i),
		})
	}
	for _, limit := range []int{5, 6} {
		t.Run(fmt.Sprintf("limit-%d", limit), func(t *testing.T) {
			hits := retrieve(chunks, "lookup", limit)
			seeds := 0
			for _, hit := range hits {
				if strings.HasPrefix(hit.Chunk.Section, "paths ") {
					seeds++
				}
			}
			if seeds != limit {
				t.Fatalf("got %d operation seeds, want %d", seeds, limit)
			}
			if !hitsSectionContains(hits, "components schemas Schema") {
				t.Fatalf("reference dependencies were dropped at limit %d: %#v", limit, hits)
			}
			for _, hit := range hits {
				if !strings.HasPrefix(hit.Chunk.Section, "paths ") {
					continue
				}
				for _, candidate := range chunks {
					if strings.HasPrefix(candidate.Section, "components schemas ") &&
						strings.Contains(hit.Chunk.Text, candidate.Section[len("components schemas "):]) &&
						!hitsSectionContains(hits, candidate.Section) {
						t.Errorf("seed %q lost dependency %q", hit.Chunk.Section, candidate.Section)
					}
				}
			}
		})
	}
}

func TestRetrieveScoresAndOrdersDeterministically(t *testing.T) {
	chunks := []Chunk{
		{Path: "docs/b.md", Section: "Guide", StartLine: 1, EndLine: 1, Text: "ingredient amount unit shopping cart"},
		{Path: "docs/a.md", Section: "Guide", StartLine: 1, EndLine: 1, Text: "ingredient amount unit shopping cart"},
	}
	first := retrieve(chunks, "ingredient amount unit shopping cart", 5)
	for attempt := range 20 {
		got := retrieve(chunks, "ingredient amount unit shopping cart", 5)
		if len(got) != len(first) {
			t.Fatalf("attempt %d returned %d hits, want %d", attempt, len(got), len(first))
		}
		for i := range first {
			if got[i].Chunk.Path != first[i].Chunk.Path || got[i].Score != first[i].Score {
				t.Fatalf("attempt %d differs at hit %d: %#v versus %#v", attempt, i, got[i], first[i])
			}
		}
	}
}

func TestOpenAPIRetrievalKeepsPathMethodsAndRecursiveReferences(t *testing.T) {
	root := t.TempDir()
	var spec strings.Builder
	spec.WriteString(`openapi: 3.0.3
paths:
  /api/recipes/download_shopping_cart/:
    get:
      operationId: firstCartReadOperation
      responses:
        '200':
          description: Cart details
    post:
      operationId: firstCartCreateOperation
      responses:
        '201':
          description: Cart created
  /api/recipes/{id}/:
    get:
      operationId: laterRecipeReadOperation
      responses:
        '200':
          description: Recipe details
    patch:
      operationId: laterRecipePatchOperation
      responses:
        '200':
          $ref: '#/components/responses/UpdatedRecipe'
components:
  schemas:
    LargeRoot:
      type: object
      properties:
`)
	for i := range 45 {
		fmt.Fprintf(&spec, "        filler%02d:\n          type: string\n", i)
	}
	spec.WriteString(`        deep:
          $ref: '#/components/schemas/DeepLeaf'
    DeepLeaf:
      type: object
      properties:
        transitiveLeafMarker:
          type: string
  responses:
    UpdatedRecipe:
      description: Updated recipe
      content:
        application/json:
          schema:
            $ref: '#/components/schemas/LargeRoot'
`)
	writeRAGFixture(t, root, "docs/openapi-schema.yml", spec.String())

	chunks, err := loadCorpus(root)
	if err != nil {
		t.Fatalf("loadCorpus: %v", err)
	}
	firstPost := retrieve(chunks, "firstCartCreateOperation", 10)
	if !hasOperationIdentity(firstPost, "firstCartCreateOperation", "/api/recipes/download_shopping_cart/", "post") {
		t.Fatalf("first path POST operation lost its identity: %#v", firstPost)
	}

	laterPatch := retrieve(chunks, "laterRecipePatchOperation", 10)
	if !hasOperationIdentity(laterPatch, "laterRecipePatchOperation", "/api/recipes/{id}/", "patch") {
		t.Fatalf("later path PATCH operation lost its path or method identity: %#v", laterPatch)
	}
	if hitsContain(laterPatch, "firstCartCreateOperation") || hitsContain(laterPatch, "firstCartReadOperation") {
		t.Fatalf("later path retrieval included a previous operation: %#v", laterPatch)
	}
	if !hitsSectionContains(laterPatch, "components responses UpdatedRecipe") {
		t.Fatalf("operation lookup omitted its referenced response component: %#v", laterPatch)
	}
	var foundLaterBoundedRef bool
	for _, hit := range laterPatch {
		if strings.Contains(hit.Chunk.Text, "#/components/schemas/DeepLeaf") {
			foundLaterBoundedRef = hit.Chunk.StartLine > 80
		}
	}
	if !foundLaterBoundedRef {
		t.Fatalf("recursive reference in later bounded chunk was not surfaced: %#v", laterPatch)
	}
	if !hitsSectionContains(laterPatch, "components schemas DeepLeaf") || !hitsContain(laterPatch, "transitiveLeafMarker") {
		t.Fatalf("later operation did not surface its transitive schema evidence: %#v", laterPatch)
	}
}

func hasOperationIdentity(hits []Hit, operation, path, method string) bool {
	for _, hit := range hits {
		section := strings.ToLower(hit.Chunk.Section)
		if strings.Contains(hit.Chunk.Text, operation) &&
			strings.Contains(section, strings.ToLower(path)) &&
			strings.Contains(section, method) {
			return true
		}
	}
	return false
}

func hitsSectionContains(hits []Hit, section string) bool {
	for _, hit := range hits {
		if strings.Contains(hit.Chunk.Section, section) {
			return true
		}
	}
	return false
}

func TestRussianQueriesRetrieveIngredientAndShoppingCartEvidence(t *testing.T) {
	root := t.TempDir()
	writeRAGFixture(t, root, "docs/data-model.md", "# Модель данных\n\nСвязь рецепта и ингредиента хранит количество и единицу измерения.\n")
	writeRAGFixture(t, root, "docs/api-guide.md", "# Корзина покупок\n\nСкачать список покупок можно через API корзины рецепта.\n")
	chunks, err := loadCorpus(root)
	if err != nil {
		t.Fatalf("loadCorpus: %v", err)
	}

	ingredient := retrieve(chunks, "какое количество и единица измерения хранятся для ингредиента рецепта", 5)
	if !hitsContain(ingredient, "единицу измерения") {
		t.Fatalf("Russian ingredient query missed evidence: %#v", ingredient)
	}
	cart := retrieve(chunks, "как скачать список покупок из корзины", 5)
	if !hitsContain(cart, "Скачать список покупок") {
		t.Fatalf("Russian shopping-cart query missed evidence: %#v", cart)
	}
}
func TestRussianProjectStructureQueryFindsChildModuleMapAheadOfSchemas(t *testing.T) {
	root := t.TempDir()
	content := `# Структура проекта Foodgram
Проект разделён на серверные и клиентские модули.
## Backend
Каталог backend/api/ содержит REST-маршруты, представления и сериализаторы.
### Recipes
Модуль backend/recipes/ хранит модели рецептов, ингредиентов и тегов.
## Frontend
Каталог frontend/src/ содержит клиент React, страницы и компоненты.
## Infrastructure
Каталог infra/ содержит Docker Compose и конфигурацию nginx.
`
	writeRAGFixture(t, root, "docs/project-structure.md", content)
	var schemas strings.Builder
	schemas.WriteString("components:\n  schemas:\n")
	for i := range 8 {
		fmt.Fprintf(&schemas, "    APIResponse%d:\n      description: API schema response payload fields.\n", i)
	}
	writeRAGFixture(t, root, "docs/openapi-schema.yml", schemas.String())
	writeRAGFixture(t, root, "docs/api-guide.md", "# API schemas\n\n"+
		"API schema fields describe recipe responses, ingredient responses, user responses, "+
		"pagination responses, authentication responses, and validation responses.\n")
	chunks, err := loadCorpus(root)
	if err != nil {
		t.Fatalf("loadCorpus: %v", err)
	}

	var nestedHeading bool
	for _, chunk := range chunks {
		if strings.Contains(chunk.Text, "backend/recipes/") {
			nestedHeading = strings.Contains(chunk.Section, "Структура проекта Foodgram > Backend > Recipes")
		}
	}
	if !nestedHeading {
		t.Fatal("nested module chunk did not retain its full heading ancestry")
	}

	hits := retrieve(chunks, "Как устроена структура проекта Foodgram и где находится API?", 6)
	var moduleEvidence, frontendEvidence bool
	for _, hit := range hits {
		if hit.Chunk.Path != "docs/project-structure.md" {
			continue
		}
		if strings.Contains(hit.Chunk.Text, "backend/api/") {
			moduleEvidence = true
			if !strings.Contains(hit.Chunk.Section, "Структура проекта Foodgram") {
				t.Errorf("module chunk did not inherit its project heading: %q", hit.Chunk.Section)
			}
		}
		if strings.Contains(hit.Chunk.Text, "frontend/src/") {
			frontendEvidence = true
		}
	}
	if !moduleEvidence || !frontendEvidence {
		t.Fatalf("structure query did not surface substantive backend and frontend map evidence in six seeds: %#v", hits)
	}
	for _, chunk := range chunks {
		if chunk.Path != "docs/project-structure.md" || !strings.Contains(chunk.Text, "backend/api/") {
			continue
		}
		if !strings.Contains(chunk.Text, "Каталог backend/api/ содержит REST-маршруты") {
			t.Fatalf("hierarchy metadata altered source chunk text: %q", chunk.Text)
		}
		if chunk.StartLine != 3 || chunk.EndLine != 4 {
			t.Fatalf("hierarchy metadata altered source line range: %d-%d", chunk.StartLine, chunk.EndLine)
		}
		return
	}
	t.Fatal("backend module fixture chunk was not loaded")
}

func TestRetrieveDoesNotTreatIrrelevantQueryAsEvidence(t *testing.T) {
	chunks := []Chunk{{Path: "docs/guide.md", Section: "Guide", StartLine: 1, EndLine: 1, Text: "Recipe ingredients and shopping cart download."}}
	hits := retrieve(chunks, "zxqv-irrelevant-92817", 5)
	for _, hit := range hits {
		if hit.Score > 0 {
			t.Fatalf("irrelevant query received a positive relevance score: %#v", hit)
		}
	}
}

func TestReloadingCorpusSeesDocumentationChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "docs", "guide.md")
	writeRAGFixture(t, root, "docs/guide.md", "# Guide\nOld documentation.\n")
	first, err := loadCorpus(root)
	if err != nil {
		t.Fatalf("first loadCorpus: %v", err)
	}
	if err := os.WriteFile(path, []byte("# Guide\nNew documentation about pantry storage.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := loadCorpus(root)
	if err != nil {
		t.Fatalf("second loadCorpus: %v", err)
	}
	if hitsContain(retrieve(first, "pantry storage", 5), "pantry storage") {
		t.Fatal("first corpus unexpectedly contains a later documentation change")
	}
	if !hitsContain(retrieve(second, "pantry storage", 5), "pantry storage") {
		t.Fatal("reloaded corpus did not observe changed documentation")
	}
}

func writeRAGFixture(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hitsContain(hits []Hit, text string) bool {
	for _, hit := range hits {
		if strings.Contains(hit.Chunk.Text, text) {
			return true
		}
	}
	return false
}
