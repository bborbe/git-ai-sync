package conflict

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fixture struct {
	repo string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init")
	configure(t, repo)
	writeFile(t, repo, "seed.txt", "seed\n")
	runGit(t, repo, "add", "seed.txt")
	runGit(t, repo, "commit", "-m", "init")
	return fixture{repo: repo}
}

func configure(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	hooksPath := filepath.Join(runGit(t, dir, "rev-parse", "--absolute-git-dir"), "hooks")
	runGit(t, dir, "config", "--local", "core.hooksPath", hooksPath)
	t.Setenv("GIT_EDITOR", "true")
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeStub writes an executable `claude` script into a fresh temp dir,
// prepends that dir to PATH, and returns the dir. The script drains stdin first
// so the CLI never blocks on a closed pipe, then runs body.
func writeStub(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null\n" + body
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write claude stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// writeResponse serialises env to a temp file and points the stub at it.
func writeResponse(t *testing.T, env envelope) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "response.json")
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write response: %v", err)
	}
	t.Setenv("CLAUDE_STUB_RESPONSE", path)
	return path
}

// stubResponse returns a stub body that prints the response file.
func stubResponse() string {
	return "cat \"$CLAUDE_STUB_RESPONSE\"\n"
}

func readFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

func stagedFiles(t *testing.T, dir string) string {
	t.Helper()
	return runGit(t, dir, "diff", "--cached", "--name-only")
}

const markerContent = "before\n<<<<<<< HEAD\nfrom-ours\n=======\nfrom-theirs\n>>>>>>> other\nafter\n"

func TestResolveFileWritesBothSidesAndStripsFence(t *testing.T) {
	f := newFixture(t)
	writeFile(t, f.repo, "note.txt", markerContent)
	writeResponse(t, envelope{
		Type:    "result",
		IsError: false,
		Subtype: "success",
		Result:  "```\nbefore\nfrom-ours\nfrom-theirs\nafter\n```",
	})
	writeStub(t, stubResponse())

	if err := ResolveFile(context.Background(), f.repo, "note.txt"); err != nil {
		t.Fatalf("ResolveFile: %v", err)
	}

	got := string(readFile(t, f.repo, "note.txt"))
	if !strings.Contains(got, "from-ours") {
		t.Errorf("resolved file missing from-ours: %q", got)
	}
	if !strings.Contains(got, "from-theirs") {
		t.Errorf("resolved file missing from-theirs: %q", got)
	}
	if strings.Contains(got, "<<<<<<<") {
		t.Errorf("resolved file still has conflict markers: %q", got)
	}
	if strings.Contains(got, "```") {
		t.Errorf("resolved file still has a code fence: %q", got)
	}
	if staged := stagedFiles(t, f.repo); !strings.Contains(staged, "note.txt") {
		t.Errorf("staged files = %q, want note.txt", staged)
	}
}

func TestResolveFileNoMarkersStagesWithoutModel(t *testing.T) {
	f := newFixture(t)
	const content = "plain content without markers\n"
	writeFile(t, f.repo, "note.txt", content)

	logPath := filepath.Join(t.TempDir(), "invocations.log")
	t.Setenv("CLAUDE_STUB_LOG", logPath)
	writeStub(t, "printf invoked >> \"$CLAUDE_STUB_LOG\"\n")

	if err := ResolveFile(context.Background(), f.repo, "note.txt"); err != nil {
		t.Fatalf("ResolveFile: %v", err)
	}

	if got := string(readFile(t, f.repo, "note.txt")); got != content {
		t.Errorf("file changed to %q, want %q", got, content)
	}
	if staged := stagedFiles(t, f.repo); !strings.Contains(staged, "note.txt") {
		t.Errorf("staged files = %q, want note.txt", staged)
	}
	if data, err := os.ReadFile(logPath); err == nil && len(data) > 0 {
		t.Errorf("model was invoked: %s", data)
	}
}

func TestResolveFileCLIExitsNonZeroLeavesFileUntouched(t *testing.T) {
	f := newFixture(t)
	writeFile(t, f.repo, "note.txt", markerContent)
	writeStub(t, "exit 1\n")

	if err := ResolveFile(context.Background(), f.repo, "note.txt"); err == nil {
		t.Fatal("ResolveFile succeeded, want error")
	}
	if got := string(readFile(t, f.repo, "note.txt")); got != markerContent {
		t.Errorf("file changed to %q", got)
	}
}

func TestResolveFileEmptyEnvelopeLeavesFileUntouched(t *testing.T) {
	f := newFixture(t)
	writeFile(t, f.repo, "note.txt", markerContent)
	writeResponse(t, envelope{Type: "result", IsError: false, Subtype: "success", Result: ""})
	writeStub(t, stubResponse())

	err := ResolveFile(context.Background(), f.repo, "note.txt")
	if err == nil {
		t.Fatal("ResolveFile succeeded, want error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "empty") {
		t.Errorf("error = %q, want it to name an empty response", err)
	}
	if got := string(readFile(t, f.repo, "note.txt")); got != markerContent {
		t.Errorf("file changed to %q", got)
	}
}

func TestResolveFileMissingEnvelopeLeavesFileUntouched(t *testing.T) {
	f := newFixture(t)
	writeFile(t, f.repo, "note.txt", markerContent)
	writeStub(t, "exit 0\n")

	if err := ResolveFile(context.Background(), f.repo, "note.txt"); err == nil {
		t.Fatal("ResolveFile succeeded, want error")
	}
	if got := string(readFile(t, f.repo, "note.txt")); got != markerContent {
		t.Errorf("file changed to %q", got)
	}
}

func TestResolveFileAuthenticationFailureLeavesFileUntouched(t *testing.T) {
	f := newFixture(t)
	writeFile(t, f.repo, "note.txt", markerContent)
	writeResponse(t, envelope{Type: "result", IsError: false, Subtype: "success", Result: "Failed to authenticate"})
	writeStub(t, stubResponse())

	err := ResolveFile(context.Background(), f.repo, "note.txt")
	if err == nil {
		t.Fatal("ResolveFile succeeded, want error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "authenticat") {
		t.Errorf("error = %q, want it to name authentication", err)
	}
	if got := string(readFile(t, f.repo, "note.txt")); got != markerContent {
		t.Errorf("file changed to %q", got)
	}
}

func TestResolveFileIsErrorEnvelopeLeavesFileUntouched(t *testing.T) {
	f := newFixture(t)
	writeFile(t, f.repo, "note.txt", markerContent)
	writeResponse(t, envelope{Type: "result", IsError: true, Subtype: "error_during_execution", Result: "boom"})
	writeStub(t, stubResponse())

	err := ResolveFile(context.Background(), f.repo, "note.txt")
	if err == nil {
		t.Fatal("ResolveFile succeeded, want error")
	}
	if !strings.Contains(err.Error(), "error_during_execution") {
		t.Errorf("error = %q, want it to name the subtype", err)
	}
	if got := string(readFile(t, f.repo, "note.txt")); got != markerContent {
		t.Errorf("file changed to %q", got)
	}
}

const noteWithFrontmatter = "---\ntitle: x\n<<<<<<< HEAD\nfrom-ours\n=======\nfrom-theirs\n>>>>>>> other\n"

func TestResolveFileRefusesFrontmatterLoss(t *testing.T) {
	f := newFixture(t)
	writeFile(t, f.repo, "note.md", noteWithFrontmatter)
	writeResponse(t, envelope{Type: "result", IsError: false, Subtype: "success", Result: "title: x\nfrom-ours\nfrom-theirs\n"})
	writeStub(t, stubResponse())

	err := ResolveFile(context.Background(), f.repo, "note.md")
	if err == nil {
		t.Fatal("ResolveFile succeeded, want error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "frontmatter") {
		t.Errorf("error = %q, want it to name the frontmatter refusal", err)
	}
	if got := string(readFile(t, f.repo, "note.md")); got != noteWithFrontmatter {
		t.Errorf("file changed to %q", got)
	}
}

func TestResolveFileKeepsFrontmatterFromFencedAnswer(t *testing.T) {
	f := newFixture(t)
	writeFile(t, f.repo, "note.md", noteWithFrontmatter)
	writeResponse(t, envelope{
		Type:    "result",
		IsError: false,
		Subtype: "success",
		Result:  "```\n---\ntitle: x\nfrom-ours\nfrom-theirs\n```",
	})
	writeStub(t, stubResponse())

	if err := ResolveFile(context.Background(), f.repo, "note.md"); err != nil {
		t.Fatalf("ResolveFile: %v", err)
	}

	got := string(readFile(t, f.repo, "note.md"))
	if first := strings.SplitN(got, "\n", 2)[0]; first != "---" {
		t.Errorf("first line = %q, want ---", first)
	}
	if !strings.Contains(got, "from-ours") {
		t.Errorf("resolved file missing from-ours: %q", got)
	}
	if !strings.Contains(got, "from-theirs") {
		t.Errorf("resolved file missing from-theirs: %q", got)
	}
	if strings.Contains(got, "```") {
		t.Errorf("resolved file still has a code fence: %q", got)
	}
}

func TestResolveMarkerFlaggedFilesResolvesAndRestages(t *testing.T) {
	f := newFixture(t)
	writeFile(t, f.repo, "note.txt", markerContent)
	runGit(t, f.repo, "add", "note.txt")
	writeResponse(t, envelope{Type: "result", IsError: false, Subtype: "success", Result: "from-ours\nfrom-theirs"})
	writeStub(t, stubResponse())

	if err := ResolveMarkerFlaggedFiles(context.Background(), f.repo, []string{"note.txt"}); err != nil {
		t.Fatalf("ResolveMarkerFlaggedFiles: %v", err)
	}

	got := string(readFile(t, f.repo, "note.txt"))
	if !strings.Contains(got, "from-ours") {
		t.Errorf("resolved file missing from-ours: %q", got)
	}
	if !strings.Contains(got, "from-theirs") {
		t.Errorf("resolved file missing from-theirs: %q", got)
	}
	if strings.Contains(got, "<<<<<<<") {
		t.Errorf("resolved file still has conflict markers: %q", got)
	}
	cmd := exec.Command("git", "ls-files", "--error-unmatch", "note.txt")
	cmd.Dir = f.repo
	if err := cmd.Run(); err != nil {
		t.Errorf("note.txt is not in the index: %v", err)
	}
	if staged := stagedFiles(t, f.repo); !strings.Contains(staged, "note.txt") {
		t.Errorf("staged files = %q, want note.txt", staged)
	}
}

func TestParseConflictMarkers(t *testing.T) {
	blocks := parseConflictMarkers(markerContent)
	if len(blocks) != 1 {
		t.Fatalf("parseConflictMarkers returned %d blocks, want 1", len(blocks))
	}
	if blocks[0].ours != "from-ours" {
		t.Errorf("ours = %q, want from-ours", blocks[0].ours)
	}
	if blocks[0].theirs != "from-theirs" {
		t.Errorf("theirs = %q, want from-theirs", blocks[0].theirs)
	}

	if blocks := parseConflictMarkers("plain content\n"); len(blocks) != 0 {
		t.Errorf("parseConflictMarkers returned %d blocks for marker-free content, want 0", len(blocks))
	}
}
