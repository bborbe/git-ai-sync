// Package conflict resolves git merge and rebase conflicts through the Claude CLI.
package conflict

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bborbe/errors"

	"github.com/bborbe/git-ai-sync/internal/git"
)

// conflictMarkerPattern matches one conflict block and captures the two sides.
// The sides are optional on purpose: a conflict where one side deleted the hunk
// has an empty ours or theirs, and requiring a newline after each side made such
// a block parse as zero markers — which sent it down the no-marker short-circuit
// and staged it unchanged, committing conflict markers back into the repository.
var conflictMarkerPattern = regexp.MustCompile(`(?s)<{7} [^\n]*\n(.*?)={7}\n(.*?)>{7} [^\n]*\n`)

// conflictBlock is the ours/theirs pair parsed out of one conflict block.
type conflictBlock struct {
	ours   string
	theirs string
}

// envelope is the JSON result envelope the Claude CLI emits.
type envelope struct {
	Type    string `json:"type"`
	IsError bool   `json:"is_error"`
	Subtype string `json:"subtype"`
	Result  string `json:"result"`
}

// ResolveFile reads one conflicted file, resolves it through the Claude CLI when
// its content contains conflict-marker blocks, writes the resolved content
// atomically, and stages it. A failed resolution leaves the file byte-identical.
// A file whose content parses to zero marker blocks is staged unchanged and the
// model is not invoked.
func ResolveFile(ctx context.Context, repo string, path string) error {
	full := filepath.Join(repo, path)
	original, err := os.ReadFile(full)
	if err != nil {
		return errors.Wrapf(ctx, err, "read conflicted file %s", path)
	}

	if len(parseConflictMarkers(string(original))) == 0 {
		if err := git.StageFile(ctx, repo, path); err != nil {
			return errors.Wrapf(ctx, err, "stage unchanged file %s", path)
		}
		return nil
	}

	resolved, err := resolveContent(ctx, path, original)
	if err != nil {
		return errors.Wrapf(ctx, err, "resolve %s", path)
	}

	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(full); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := writeFileAtomic(ctx, full, resolved, mode); err != nil {
		return errors.Wrapf(ctx, err, "write resolved file %s", path)
	}

	if err := git.StageFile(ctx, repo, path); err != nil {
		return errors.Wrapf(ctx, err, "stage resolved file %s", path)
	}
	return nil
}

// ResolveMarkerFlaggedFiles resolves each path and re-stages it. It stops at the
// first failure and returns an error naming the path.
func ResolveMarkerFlaggedFiles(ctx context.Context, repo string, paths []string) error {
	for _, path := range paths {
		if err := ResolveFile(ctx, repo, path); err != nil {
			return errors.Wrapf(ctx, err, "failed to resolve marker-flagged file %s", path)
		}
	}
	return nil
}

// resolveContent asks the Claude CLI for a merged file and applies every
// fail-closed check before returning the content that may be written.
func resolveContent(ctx context.Context, path string, original []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "claude", "-p", "--output-format", "json")
	cmd.Stdin = strings.NewReader(buildPrompt(path, original))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "claude CLI failed for %s: %s", path, strings.TrimSpace(stderr.String()))
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, errors.Errorf(ctx, "claude returned empty response for %s", path)
	}

	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, errors.Wrapf(ctx, err, "parse claude response for %s", path)
	}
	if env.IsError {
		return nil, errors.Errorf(ctx, "claude failed to resolve %s (subtype: %s)", path, env.Subtype)
	}
	if strings.Contains(env.Result, "Failed to authenticate") {
		return nil, errors.Errorf(ctx, "claude failed to authenticate while resolving %s", path)
	}
	if strings.TrimSpace(env.Result) == "" {
		return nil, errors.Errorf(ctx, "claude returned empty response for %s", path)
	}

	resolved := stripCodeFence(strings.TrimSpace(env.Result))
	if len(parseConflictMarkers(resolved)) > 0 {
		return nil, errors.Errorf(ctx, "resolution of %s still contains conflict markers; refusing to write", path)
	}
	if strings.HasSuffix(path, ".md") && bytes.HasPrefix(original, []byte("---")) && !strings.HasPrefix(resolved, "---") {
		return nil, errors.Errorf(ctx, "resolution of %s dropped its markdown frontmatter; refusing to write", path)
	}
	return []byte(resolved), nil
}

// buildPrompt renders the resolution prompt, keeping parity with the Python
// reference. The file content is embedded between triple-backtick fences, so the
// prompt is assembled without a raw string literal.
func buildPrompt(path string, original []byte) string {
	fileType := filepath.Ext(path)
	if fileType == "" {
		fileType = "unknown"
	}

	var b strings.Builder
	b.WriteString("You are resolving a git merge conflict in a file.\n")
	b.WriteString("\n")
	b.WriteString("File: " + path + "\n")
	b.WriteString("Type: " + fileType + "\n")
	b.WriteString("\n")
	b.WriteString("**Resolution Strategy:**\n")
	b.WriteString("- Preserve ALL meaningful changes from both sides when possible\n")
	b.WriteString("- For timestamps/dates: prefer the more recent one\n")
	b.WriteString("- For additions: include both additions\n")
	b.WriteString("- For contradictory edits: use judgment based on context\n")
	b.WriteString("- Preserve file structure (frontmatter, formatting, headers)\n")
	b.WriteString("- Never remove content unless it's clearly a deletion\n")
	b.WriteString("- For Markdown: preserve frontmatter and heading structure\n")
	b.WriteString("\n")
	b.WriteString("**File Content with Conflicts:**\n")
	b.WriteString("\n")
	b.WriteString("```\n")
	b.Write(original)
	b.WriteString("\n```\n")
	b.WriteString("\n")
	b.WriteString("**Task:**\n")
	b.WriteString("Return the COMPLETE resolved file content with NO conflict markers.\n")
	b.WriteString("Return ONLY the file content, no explanations, no markdown code blocks.\n")
	return b.String()
}

// stripCodeFence removes one leading and one trailing markdown code fence.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], "```") {
		lines = lines[1:]
	}
	if len(lines) > 0 && strings.HasPrefix(lines[len(lines)-1], "```") {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// parseConflictMarkers returns every conflict block in content.
func parseConflictMarkers(content string) []conflictBlock {
	matches := conflictMarkerPattern.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	blocks := make([]conflictBlock, 0, len(matches))
	for _, match := range matches {
		// The pattern no longer consumes the newline that precedes each
		// delimiter, so trim it here to keep the captured sides as content.
		blocks = append(blocks, conflictBlock{
			ours:   strings.TrimSuffix(match[1], "\n"),
			theirs: strings.TrimSuffix(match[2], "\n"),
		})
	}
	return blocks
}

// writeFileAtomic writes content to path through a temporary file in the same
// directory, so the destination is replaced atomically or not at all.
func writeFileAtomic(ctx context.Context, path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".conflict-resolve-*")
	if err != nil {
		return errors.Wrapf(ctx, err, "create temp file for %s", path)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return errors.Wrapf(ctx, err, "write temp file for %s", path)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return errors.Wrapf(ctx, err, "chmod temp file for %s", path)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return errors.Wrapf(ctx, err, "sync temp file for %s", path)
	}
	if err := tmp.Close(); err != nil {
		return errors.Wrapf(ctx, err, "close temp file for %s", path)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return errors.Wrapf(ctx, err, "rename temp file to %s", path)
	}
	return nil
}
