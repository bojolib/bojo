package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendLoadAndMatch(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 0 {
		t.Fatal("a missing file must load as an empty store")
	}

	e := Entry{
		Repo:    "https://github.com/Owner/Repo",
		Commit:  "9f3a2b1c4d5e6f70a1b2c3d4e5f60718293a4b5c",
		Pkg:     "web_templ",
		Hook:    "pre-build",
		Command: []string{"templ", "generate"},
	}
	if err := s.Append(e); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Allows(e.Repo, e.Commit, e.Pkg, e.Hook, e.Command) {
		t.Fatal("approved hook is not allowed after reload")
	}

	// Every single changed field must invalidate the approval.
	if reloaded.Allows(e.Repo, e.Commit, e.Pkg, e.Hook, []string{"templ", "generate", "--evil"}) {
		t.Fatal("a different command must not be allowed")
	}
	if reloaded.Allows(e.Repo, "0000000", e.Pkg, e.Hook, e.Command) {
		t.Fatal("a different commit must not be allowed")
	}
	if reloaded.Allows(e.Repo, e.Commit, e.Pkg, "post-build", e.Command) {
		t.Fatal("a different hook must not be allowed")
	}
}

func TestIDIsStableAndDistinct(t *testing.T) {
	a := Entry{Repo: "r", Commit: "c", Pkg: "p", Hook: "pre-build", Command: []string{"x", "y"}}
	b := Entry{Repo: "r", Commit: "c", Pkg: "p", Hook: "pre-build", Command: []string{"x", "y"}}
	if a.ID() != b.ID() {
		t.Fatal("identical entries must share an ID")
	}
	if len(a.ID()) != 6 {
		t.Fatalf("ID length = %d, want 6", len(a.ID()))
	}

	c := Entry{Repo: "r", Commit: "c", Pkg: "p", Hook: "pre-build", Command: []string{"x  y"}}
	if a.ID() == c.ID() {
		t.Fatal("argv [\"x\",\"y\"] and [\"x  y\"] must not collide")
	}
}

func TestFindUnknownIDFails(t *testing.T) {
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Find("deadbe"); err == nil {
		t.Fatal("unknown ID must fail")
	}
}

func TestBlockedAndStale(t *testing.T) {
	dir := t.TempDir()
	s, _ := Load(dir)

	req := Requirement{Repo: "r", Commit: "abc", Pkg: "p", Hook: "pre-build", Command: []string{"echo"}}
	if len(Blocked(s, []Requirement{req})) != 1 {
		t.Fatal("an unapproved hook must be blocked")
	}
	if err := s.Append(req.Entry()); err != nil {
		t.Fatal(err)
	}
	if len(Blocked(s, []Requirement{req})) != 0 {
		t.Fatal("an approved hook must pass the gate")
	}

	stale := s.Stale(map[string]bool{"r@different": true})
	if len(stale) != 1 {
		t.Fatal("an entry whose commit is not in the lock must be stale")
	}
	if len(s.Stale(map[string]bool{"r@abc": true})) != 0 {
		t.Fatal("an entry matching the lock must not be stale")
	}
}

func TestCoexistingMajorsFilterAndStale(t *testing.T) {
	dir := t.TempDir()
	s, _ := Load(dir)

	v1 := Entry{Repo: "https://github.com/bojolib/socket", Commit: "1111111", Pkg: "socket", Hook: "vendor", Command: []string{"cc"}}
	v2 := Entry{Repo: "https://github.com/bojolib/socket", Commit: "2222222", Pkg: "socket", Hook: "vendor", Command: []string{"cc"}}
	old := Entry{Repo: "https://github.com/bojolib/socket", Commit: "0000000", Pkg: "socket", Hook: "vendor", Command: []string{"cc"}}
	for _, e := range []Entry{v1, v2, old} {
		if err := s.Append(e); err != nil {
			t.Fatal(err)
		}
	}

	locked := map[string]bool{
		"https://github.com/bojolib/socket@1111111": true,
		"https://github.com/bojolib/socket@2222222": true,
	}
	filtered := s.Filter(locked)
	if len(filtered.Entries) != 2 {
		t.Fatalf("filtered len = %d, want 2", len(filtered.Entries))
	}
	if !filtered.Allows(v1.Repo, v1.Commit, v1.Pkg, v1.Hook, v1.Command) {
		t.Error("v1 hook must remain approved")
	}
	if !filtered.Allows(v2.Repo, v2.Commit, v2.Pkg, v2.Hook, v2.Command) {
		t.Error("v2 hook must remain approved")
	}
	stale := s.Stale(locked)
	if len(stale) != 1 || stale[0].Commit != "0000000" {
		t.Fatalf("stale = %+v, want only old commit 0000000", stale)
	}
}

func TestLoadRejectsBrokenJSONL(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{not json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("broken JSONL must fail loudly")
	}
}
