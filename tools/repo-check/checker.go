package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	documentationPolicy = "CONTRIBUTING.md#documentation"
	decisionPolicy      = "docs/decisions/README.md#naming-and-lifecycle"
	issuePolicy         = "CONTRIBUTING.md#milestones-and-required-issue"
	pullRequestPolicy   = "CONTRIBUTING.md#pull-request-description"
	actionPolicy        = "docs/development/dependency-policy.md#automation-policy"
)

var (
	headingPattern     = regexp.MustCompile(`^(#{1,6})[\t ]+(.+?)[\t ]*#*[\t ]*$`)
	inlineLinkPattern  = regexp.MustCompile(`!?\[[^]]*\]\([\t ]*(<[^>]+>|[^\t )]+)(?:[\t ]+[^)]*)?\)`)
	referencePattern   = regexp.MustCompile(`^[ ]{0,3}\[[^]]+\]:[\t ]*(<[^>]+>|[^\t ]+)`)
	wordPattern        = regexp.MustCompile(`[\p{L}\p{N}]+(?:['_-][\p{L}\p{N}]+)*`)
	adrFilePattern     = regexp.MustCompile(`^(\d{4})-([a-z0-9]+(?:-[a-z0-9]+)*)\.md$`)
	adrHeadingPattern  = regexp.MustCompile(`^# ADR-(\d{4}): (\S.*)$`)
	adrMetadataPattern = regexp.MustCompile(`^- ([A-Za-z][A-Za-z ]+):[\t ]*(.*)$`)
	adrIndexPattern    = regexp.MustCompile(`^\| \[(\d{4})\]\(([^)]+)\) \| ([^|]+) \| ([^|]+) \|$`)
	milestonePattern   = regexp.MustCompile(`^M\d{2} — \S.*$`)
	actionUsePattern   = regexp.MustCompile(`^[\t ]*(?:-[\t ]*)?uses:[\t ]*(.*?)[\t ]*$`)
	blockScalarPattern = regexp.MustCompile(`^[\t ]*(?:-[\t ]*)?[^:#]+:[\t ]*[|>][0-9+-]*[\t ]*(?:#.*)?$`)
	majorActionPattern = regexp.MustCompile(`^v[0-9]+$`)
)

type violation struct {
	path, rule, message, fix, policy string
	line                             int
}

func (v violation) String() string {
	return fmt.Sprintf("%s:%d: [%s] %s; fix: %s; policy: %s", v.path, v.line, v.rule, v.message, v.fix, v.policy)
}

type heading struct {
	level int
	text  string
	line  int
}

type link struct {
	target string
	line   int
}

type document struct {
	path      string
	lines     []string
	headings  []heading
	anchors   map[string]struct{}
	links     []link
	wordCount int
}

type repository struct {
	root string
	docs map[string]*document
}

func checkRepository(root string) ([]violation, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	repo := &repository{root: abs, docs: map[string]*document{}}
	if err := repo.load(); err != nil {
		return nil, err
	}

	var violations []violation
	violations = append(violations, repo.checkLinks()...)
	violations = append(violations, repo.checkSummaries()...)
	violations = append(violations, repo.checkADRs()...)
	violations = append(violations, repo.checkTemplates()...)
	actionViolations, err := repo.checkActions()
	if err != nil {
		return nil, err
	}
	violations = append(violations, actionViolations...)
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].path != violations[j].path {
			return violations[i].path < violations[j].path
		}
		if violations[i].line != violations[j].line {
			return violations[i].line < violations[j].line
		}
		return violations[i].rule < violations[j].rule
	})

	return violations, nil
}

func (r *repository) load() error {
	return filepath.WalkDir(r.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && path != r.root && skipDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}
		rel, err := filepath.Rel(r.root, path)
		if err != nil {
			return err
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		r.docs[rel] = parseDocument(rel, string(bytes))
		return nil
	})
}

func skipDirectory(name string) bool {
	switch name {
	case ".git", ".idea", ".local", "bin", "node_modules", "testdata", "vendor":
		return true
	default:
		return false
	}
}

type actionReference struct {
	value string
	line  int
}

func (r *repository) checkActions() ([]violation, error) {
	paths, err := r.actionFiles()
	if err != nil {
		return nil, err
	}

	var violations []violation
	for _, path := range paths {
		content, err := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(path)))
		if err != nil {
			return nil, fmt.Errorf("read GitHub Actions file %s: %w", path, err)
		}
		for _, reference := range actionReferences(string(content)) {
			if strings.HasPrefix(reference.value, "./") || strings.HasPrefix(reference.value, "docker://") {
				continue
			}
			separator := strings.LastIndex(reference.value, "@")
			if separator > 0 && majorActionPattern.MatchString(reference.value[separator+1:]) {
				continue
			}
			violations = append(violations, docViolation(
				path,
				reference.line,
				"workflow.action-version",
				fmt.Sprintf("action reference %q is not a semantic major tag", reference.value),
				"use `owner/action@vN`; keep immutable hashes in product artifact and deployment fields",
				actionPolicy,
			))
		}
	}
	return violations, nil
}

func (r *repository) actionFiles() ([]string, error) {
	var paths []string
	roots := []struct {
		path      string
		candidate func(string) bool
	}{
		{".github/workflows", func(name string) bool {
			extension := strings.ToLower(filepath.Ext(name))
			return extension == ".yml" || extension == ".yaml"
		}},
		{".github/actions", func(name string) bool {
			return name == "action.yml" || name == "action.yaml"
		}},
	}

	for _, root := range roots {
		absolute := filepath.Join(r.root, filepath.FromSlash(root.path))
		if _, err := os.Stat(absolute); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", root.path, err)
		}
		err := filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !root.candidate(entry.Name()) {
				return nil
			}
			relative, err := filepath.Rel(r.root, path)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(relative))
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("discover GitHub Actions files below %s: %w", root.path, err)
		}
	}

	sort.Strings(paths)
	return paths, nil
}

func actionReferences(content string) []actionReference {
	content = strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	var references []actionReference
	blockIndent := -1
	for index, lineText := range strings.Split(content, "\n") {
		trimmed := strings.TrimLeft(lineText, " \t")
		indent := len(lineText) - len(trimmed)
		if blockIndent >= 0 {
			if trimmed == "" || indent > blockIndent {
				continue
			}
			blockIndent = -1
		}
		match := actionUsePattern.FindStringSubmatch(lineText)
		if match != nil {
			references = append(references, actionReference{value: yamlScalar(match[1]), line: index + 1})
			continue
		}
		if blockScalarPattern.MatchString(lineText) {
			blockIndent = indent
		}
	}
	return references
}

func yamlScalar(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	if raw[0] == '\'' || raw[0] == '"' {
		quote := raw[0]
		for index := 1; index < len(raw); index++ {
			if raw[index] == '\\' && quote == '"' {
				index++
				continue
			}
			if raw[index] == quote {
				return raw[1:index]
			}
		}
		return raw
	}
	if comment := strings.IndexByte(raw, '#'); comment >= 0 {
		raw = raw[:comment]
	}
	return strings.TrimSpace(raw)
}

func parseDocument(path, content string) *document {
	content = strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	doc := &document{path: path, lines: strings.Split(content, "\n"), anchors: map[string]struct{}{}}
	anchorCount := map[string]int{}
	inFence := false
	var fence byte
	var width int
	for index, lineText := range doc.lines {
		trimmed := strings.TrimLeft(lineText, " \t")
		if marker, markerWidth, ok := fenceMarker(trimmed); ok {
			if !inFence {
				inFence, fence, width = true, marker, markerWidth
			} else if marker == fence && markerWidth >= width {
				inFence = false
			}
			continue
		}
		if inFence {
			continue
		}
		prose := stripInlineCode(lineText)
		doc.wordCount += len(wordPattern.FindAllString(prose, -1))
		for _, match := range inlineLinkPattern.FindAllStringSubmatch(prose, -1) {
			doc.links = append(doc.links, link{cleanDestination(match[1]), index + 1})
		}
		if match := referencePattern.FindStringSubmatch(lineText); match != nil {
			doc.links = append(doc.links, link{cleanDestination(match[1]), index + 1})
		}
		match := headingPattern.FindStringSubmatch(trimmed)
		if match == nil {
			continue
		}
		h := heading{len(match[1]), strings.TrimSpace(match[2]), index + 1}
		doc.headings = append(doc.headings, h)
		base := githubAnchor(h.text)
		anchor := base
		if count := anchorCount[base]; count > 0 {
			anchor += "-" + strconv.Itoa(count)
		}
		anchorCount[base]++
		doc.anchors[anchor] = struct{}{}
	}
	return doc
}

func fenceMarker(line string) (byte, int, bool) {
	if len(line) < 3 || line[0] != '`' && line[0] != '~' {
		return 0, 0, false
	}
	width := 0
	for width < len(line) && line[width] == line[0] {
		width++
	}
	return line[0], width, width >= 3
}

func stripInlineCode(line string) string {
	var output strings.Builder
	inCode := false
	for _, character := range line {
		if character == '`' {
			inCode = !inCode
			output.WriteByte(' ')
		} else if !inCode {
			output.WriteRune(character)
		}
	}
	return output.String()
}

func cleanDestination(value string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(value), "<"), ">")
}

func githubAnchor(text string) string {
	text = strings.ToLower(text)
	var output strings.Builder
	space := false
	for _, character := range text {
		switch {
		case unicode.IsLetter(character), unicode.IsDigit(character), character == '_', character == '-':
			if space && output.Len() > 0 {
				output.WriteByte('-')
			}
			space = false
			output.WriteRune(character)
		case unicode.IsSpace(character):
			space = true
		}
	}
	return output.String()
}

func (r *repository) checkLinks() []violation {
	var violations []violation
	for _, doc := range r.docs {
		for _, candidate := range doc.links {
			parsed, err := url.Parse(candidate.target)
			if err != nil || parsed.Scheme != "" || parsed.Host != "" || strings.HasPrefix(candidate.target, "//") {
				continue
			}
			decodedPath, err := url.PathUnescape(parsed.Path)
			if err != nil {
				violations = append(violations, docViolation(doc.path, candidate.line, "docs.internal-link", "link has invalid path escaping", "correct its percent encoding", documentationPolicy))
				continue
			}
			target := filepath.FromSlash(decodedPath)
			if filepath.IsAbs(target) || strings.HasPrefix(decodedPath, "/") {
				target = strings.TrimLeft(target, `\/`)
			} else {
				target = filepath.Join(filepath.Dir(filepath.FromSlash(doc.path)), target)
			}
			if decodedPath == "" {
				target = filepath.FromSlash(doc.path)
			}
			target = filepath.Clean(target)
			inside, relErr := filepath.Rel(r.root, filepath.Join(r.root, target))
			if relErr != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
				violations = append(violations, docViolation(doc.path, candidate.line, "docs.internal-link", "link escapes the repository", "link to a checked-in path", documentationPolicy))
				continue
			}
			if !exactPathExists(r.root, target) {
				violations = append(violations, docViolation(doc.path, candidate.line, "docs.internal-link", fmt.Sprintf("link %q does not resolve with exact letter case", candidate.target), "correct the path or capitalization", documentationPolicy))
				continue
			}
			if parsed.Fragment == "" {
				continue
			}
			targetDoc, found := r.docs[filepath.ToSlash(target)]
			if !found {
				continue
			}
			fragment, err := url.PathUnescape(parsed.Fragment)
			if err != nil {
				continue
			}
			if _, found := targetDoc.anchors[fragment]; !found {
				violations = append(violations, docViolation(doc.path, candidate.line, "docs.internal-link", fmt.Sprintf("link %q names a missing heading", candidate.target), "use an existing generated heading anchor", documentationPolicy))
			}
		}
	}
	return violations
}

func exactPathExists(root, relative string) bool {
	current := root
	clean := filepath.Clean(relative)
	if clean == "." {
		return true
	}
	for _, segment := range strings.Split(clean, string(filepath.Separator)) {
		entries, err := os.ReadDir(current)
		if err != nil {
			return false
		}
		found := false
		for _, entry := range entries {
			if entry.Name() == segment {
				found = true
				break
			}
		}
		if !found {
			return false
		}
		current = filepath.Join(current, segment)
	}
	return true
}

func (r *repository) checkSummaries() []violation {
	var violations []violation
	for _, doc := range r.docs {
		if strings.HasPrefix(doc.path, ".github/") || !requiresSummary(doc) {
			continue
		}
		firstSecondLevel := ""
		line := 1
		for _, h := range doc.headings {
			if h.level == 2 {
				firstSecondLevel, line = h.text, h.line
				break
			}
		}
		if firstSecondLevel != "TL;DR" {
			violations = append(violations, docViolation(doc.path, line, "docs.tldr", "long or selectively consulted documentation does not start with `## TL;DR`", "add a concise TL;DR as the first second-level section", documentationPolicy))
		}
	}
	return violations
}

func requiresSummary(doc *document) bool {
	sections := 0
	for _, h := range doc.headings {
		if h.level == 2 {
			sections++
		}
	}
	lower := strings.ToLower(doc.path)
	return doc.wordCount >= 800 || sections > 5 || lower == "security.md" || strings.Contains(lower, "migration") || strings.Contains(lower, "operations") || strings.Contains(lower, "runbook")
}

type adr struct {
	number, file, title, status, date string
}

func (r *repository) checkADRs() []violation {
	records := map[string]adr{}
	var violations []violation
	for path, doc := range r.docs {
		if filepath.ToSlash(filepath.Dir(path)) != "docs/decisions" || filepath.Base(path) == "README.md" {
			continue
		}
		match := adrFilePattern.FindStringSubmatch(filepath.Base(path))
		if match == nil {
			violations = append(violations, docViolation(path, 1, "adr.filename", "ADR filename is not four-digit kebab-case", "rename it and update the index", decisionPolicy))
			continue
		}
		number := match[1]
		headingMatch := adrHeadingPattern.FindStringSubmatch(doc.lines[0])
		if headingMatch == nil || headingMatch[1] != number {
			violations = append(violations, docViolation(path, 1, "adr.heading", "ADR heading does not match its filename number", "use `# ADR-NNNN: Decision title`", decisionPolicy))
			continue
		}
		metadata := map[string]string{}
		for _, lineText := range doc.lines[1:] {
			if strings.HasPrefix(lineText, "## ") {
				break
			}
			if meta := adrMetadataPattern.FindStringSubmatch(strings.TrimSpace(lineText)); meta != nil {
				metadata[meta[1]] = strings.TrimSpace(meta[2])
			}
		}
		for _, field := range []string{"Status", "Date", "Milestone", "Deciders", "Supersedes", "Superseded by"} {
			if _, found := metadata[field]; !found {
				violations = append(violations, docViolation(path, 1, "adr.metadata", "ADR is missing "+field, "restore the canonical metadata field", decisionPolicy))
			}
		}
		if _, err := time.Parse("2006-01-02", metadata["Date"]); err != nil {
			violations = append(violations, docViolation(path, 1, "adr.date", "ADR date is not YYYY-MM-DD", "use an ISO calendar date", decisionPolicy))
		}
		if !milestonePattern.MatchString(metadata["Milestone"]) {
			violations = append(violations, docViolation(path, 1, "adr.milestone", "ADR milestone does not match `MNN — Outcome`", "use the exact published milestone title", decisionPolicy))
		}
		records[number] = adr{number, path, strings.TrimSpace(headingMatch[2]), metadata["Status"], metadata["Date"]}
	}

	for index := 1; index <= len(records); index++ {
		number := fmt.Sprintf("%04d", index)
		if _, found := records[number]; !found {
			violations = append(violations, docViolation("docs/decisions/README.md", 1, "adr.sequence", "ADR sequence has a gap at "+number, "renumber an unpublished collision or restore the indexed record", decisionPolicy))
		}
	}
	index := r.docs["docs/decisions/README.md"]
	entries := map[string]adr{}
	if index != nil {
		for _, lineText := range index.lines {
			match := adrIndexPattern.FindStringSubmatch(lineText)
			if match != nil {
				entries[match[1]] = adr{number: match[1], file: match[2], title: strings.TrimSpace(match[3]), status: strings.TrimSpace(match[4])}
			}
		}
	}
	for number, record := range records {
		entry, found := entries[number]
		if !found {
			violations = append(violations, docViolation(record.file, 1, "adr.index", "ADR is not registered in the decision index", "add its exact number, link, title and status", decisionPolicy))
			continue
		}
		if filepath.Base(entry.file) != filepath.Base(record.file) || entry.title != record.title || entry.status != record.status {
			violations = append(violations, docViolation("docs/decisions/README.md", 1, "adr.index", "ADR index entry disagrees with the record", "synchronize its link, title and status", decisionPolicy))
		}
	}
	for number := range entries {
		if _, found := records[number]; !found {
			violations = append(violations, docViolation("docs/decisions/README.md", 1, "adr.index", "ADR index references missing ADR "+number, "restore the file or remove an unpublished stale row", decisionPolicy))
		}
	}
	return violations
}

func (r *repository) checkTemplates() []violation {
	checks := []struct {
		path, rule, policy string
		required           []string
	}{
		{".github/ISSUE_TEMPLATE/work-item.md", "template.issue", issuePolicy, []string{"**Repository:** perfeng-control-plane", "**Milestone:** MNN — Outcome", "## Goal", "## Scope", "## Design decisions", "## Security and operations", "## Acceptance criteria", "## Verification", "## Out of scope"}},
		{".github/PULL_REQUEST_TEMPLATE.md", "template.pull-request", pullRequestPolicy, []string{"## Linked work", "## Outcome and scope", "## Design, compatibility and operations", "## Verification", "Checks not run, blocking reason and residual risk:", "## Limitations and review"}},
	}
	var violations []violation
	for _, check := range checks {
		doc := r.docs[check.path]
		if doc == nil {
			violations = append(violations, docViolation(check.path, 1, check.rule, "required template is missing", "restore the canonical template", check.policy))
			continue
		}
		content := strings.Join(doc.lines, "\n")
		position := 0
		for _, required := range check.required {
			next := strings.Index(content[position:], required)
			if next < 0 {
				violations = append(violations, docViolation(check.path, 1, check.rule, "template is missing or reorders "+required, "restore the canonical field order", check.policy))
				break
			}
			position += next + len(required)
		}
	}
	return violations
}

func docViolation(path string, line int, rule, message, fix, policy string) violation {
	return violation{path: path, line: line, rule: rule, message: message, fix: fix, policy: policy}
}
