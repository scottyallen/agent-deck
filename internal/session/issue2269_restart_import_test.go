package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #2269: `session restart` fell back to `--session-id <id>` (a blank
// conversation under a real id) when the transcript lived in another config
// dir. sessionHasConversationData must locate it in ~/.claude (or any
// profile's config_dir), copy it into the dir the new process will use, and
// report true so the launcher emits `--resume`.
func TestSessionHasConversationData_ImportsTranscriptFromOtherConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	accountDir := filepath.Join(home, ".claude-account2")
	t.Setenv("CLAUDE_CONFIG_DIR", accountDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	projectPath := filepath.Join(home, "code", "proj")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	encoded := ConvertToClaudeDirName(projectPath)
	sessionID := "11111111-2222-3333-4444-555555555555"

	// Canonical transcript only under the default ~/.claude, with a stale
	// history-suppression record from a previous cross-account restart.
	defaultProj := filepath.Join(home, ".claude", "projects", encoded)
	if err := os.MkdirAll(defaultProj, 0o755); err != nil {
		t.Fatal(err)
	}
	keep1 := `{"type":"user","sessionId":"` + sessionID + `","message":{"role":"user","content":"hi"}}` + "\n"
	drop1 := `{"type":"history-suppression","cause":"restored_owner_mismatch","sessionId":"` + sessionID + `"}` + "\n"
	drop2 := `{"type":"bridge-session","sessionId":"` + sessionID + `"}` + "\n"
	keep2 := `{"type":"assistant","sessionId":"` + sessionID + `","message":{"role":"assistant","content":"hello"}}` + "\n"
	src := filepath.Join(defaultProj, sessionID+".jsonl")
	if err := os.WriteFile(src, []byte(keep1+drop1+drop2+keep2), 0o600); err != nil {
		t.Fatal(err)
	}

	inst := NewInstance("restart-import", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = sessionID

	if !sessionHasConversationData(inst, sessionID) {
		t.Fatal("expected true: transcript exists in ~/.claude and must be imported, not replaced by --session-id")
	}

	dst := filepath.Join(accountDir, "projects", encoded, sessionID+".jsonl")
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("transcript was not copied into the account config dir: %v", err)
	}
	if string(got) != keep1+keep2 {
		t.Errorf("imported transcript should keep only the conversation records\n got: %q\nwant: %q", got, keep1+keep2)
	}
	if orig, _ := os.ReadFile(src); string(orig) != keep1+drop1+drop2+keep2 {
		t.Error("source transcript must be left untouched (copy-only)")
	}

	// Second call: the file is now in place, no import needed, still true.
	if !sessionHasConversationData(inst, sessionID) {
		t.Error("expected true on the second call with the transcript already imported")
	}
}

func TestSessionHasConversationData_NoImportWhenNowhere(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	accountDir := filepath.Join(home, ".claude-account2")
	t.Setenv("CLAUDE_CONFIG_DIR", accountDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	projectPath := filepath.Join(home, "code", "proj")
	inst := NewInstance("restart-nowhere", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = "aaaaaaaa-0000-0000-0000-000000000000"

	if sessionHasConversationData(inst, inst.ClaudeSessionID) {
		t.Error("expected false when the transcript exists in no config dir")
	}
	if entries, _ := os.ReadDir(filepath.Join(accountDir, "projects")); len(entries) != 0 {
		t.Errorf("nothing should be written when there is nothing to import, got %v", entries)
	}
}

// The scan keys on the instance's bound id; with an id that is not the
// instance's own, LocateConversationConfigDir must not be consulted (its
// empty-id fallback picks the newest sibling conversation).
func TestImportConversationFromOtherConfigDir_RequiresBoundID(t *testing.T) {
	inst := NewInstance("unbound", t.TempDir())
	inst.Tool = "claude"
	if got := importConversationFromOtherConfigDir(inst, "some-id", t.TempDir()); got != "" {
		t.Errorf("expected no import for an id the instance is not bound to, got %q", got)
	}
}

func TestCopyConversationStripped(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jsonl")
	dst := filepath.Join(dir, "dst.jsonl")

	t.Run("drops suppression records and size-checks the remainder", func(t *testing.T) {
		lines := []string{
			`{"type":"summary","leafUuid":"abc"}`,
			`{"type":"history-suppression","cause":"restored_owner_mismatch"}`,
			`{"type":"user","sessionId":"x","text":"history-suppression mentioned in text stays"}`,
			`{"type":"bridge-session","id":"b"}`,
			`{"type":"assistant","sessionId":"x"}`, // no trailing newline
		}
		content := strings.Join(lines, "\n")
		if err := os.WriteFile(src, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		written, err := copyConversationStripped(src, dst)
		if err != nil {
			t.Fatal(err)
		}
		want := lines[0] + "\n" + lines[2] + "\n" + lines[4]
		got, _ := os.ReadFile(dst)
		if string(got) != want {
			t.Errorf("got %q\nwant %q", got, want)
		}
		if written != int64(len(want)) {
			t.Errorf("written = %d, want %d", written, len(want))
		}
		info, _ := os.Stat(dst)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("dst mode = %o, want 0600", info.Mode().Perm())
		}
	})

	t.Run("plain transcript copies byte for byte", func(t *testing.T) {
		content := "{\"type\":\"user\",\"sessionId\":\"y\"}\n{\"type\":\"assistant\"}\n"
		if err := os.WriteFile(src, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		written, err := copyConversationStripped(src, dst)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(dst)
		if string(got) != content || written != int64(len(content)) {
			t.Errorf("got %q (%d bytes), want %q", got, written, content)
		}
	})

	t.Run("refuses symlink destination", func(t *testing.T) {
		link := filepath.Join(dir, "link.jsonl")
		if err := os.Symlink(dst, link); err != nil {
			t.Skip("symlinks unavailable")
		}
		if _, err := copyConversationStripped(src, link); err == nil {
			t.Error("expected error for symlink destination")
		}
	})
}
