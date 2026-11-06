package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeProject(t *testing.T, worktree bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	var err error
	if worktree {
		err = os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /unused"), 0600)
	} else {
		err = os.Mkdir(filepath.Join(root, ".git"), 0700)
	}
	if err != nil {
		t.Fatal(err)
	}
	return root, sub
}

func TestProjectRootAndTrustAcrossDirectories(t *testing.T) {
	for _, worktree := range []bool{false, true} {
		t.Run(map[bool]string{false: "repository", true: "worktree"}[worktree], func(t *testing.T) {
			root, sub := makeProject(t, worktree)
			canonical, _ := filepath.EvalSymlinks(root)
			got, err := ProjectRoot(sub)
			if err != nil || got != canonical {
				t.Fatalf("got %q %v", got, err)
			}
			store := &TrustStore{Base: t.TempDir()}
			calls := 0
			ask := func(_ context.Context, r Request) (bool, error) {
				calls++
				if !strings.Contains(r.Details, "AGENTS.md") {
					t.Error(r)
				}
				return true, nil
			}
			paths := []string{filepath.Join(root, "AGENTS.md")}
			if allow, err := store.Resolve(context.Background(), "auto", sub, paths, ask, nil); err != nil || !allow {
				t.Fatal(allow, err)
			}
			fresh := &TrustStore{Base: store.Base}
			if allow, err := fresh.Resolve(context.Background(), "auto", root, paths, nil, nil); err != nil || !allow || calls != 1 {
				t.Fatal(allow, err, calls)
			}
			alias := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(root, alias); err != nil {
				t.Fatal(err)
			}
			if got, err := ProjectRoot(alias); err != nil || got != canonical {
				t.Fatal(got, err)
			}
			info, err := os.Stat(store.path(canonical))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal(info, err)
			}
		})
	}
	cwd := t.TempDir()
	canonical, _ := filepath.EvalSymlinks(cwd)
	if root, err := ProjectRoot(cwd); err != nil || root != canonical {
		t.Fatal(root, err)
	}
}

func TestTrustOverridesDenialsAndMalformedRecords(t *testing.T) {
	root, _ := makeProject(t, false)
	canonical, _ := ProjectRoot(root)
	store := &TrustStore{Base: t.TempDir()}
	paths := []string{filepath.Join(root, "AGENTS.md")}
	deny := func(context.Context, Request) (bool, error) { return false, nil }
	if allow, err := store.Resolve(context.Background(), "auto", root, paths, deny, nil); err != nil || allow {
		t.Fatal(allow, err)
	}
	fresh := &TrustStore{Base: store.Base}
	if allow, found, err := fresh.Get(canonical); err != nil || !found || allow {
		t.Fatal(allow, found, err)
	}
	if allow, err := fresh.Resolve(context.Background(), "yes", root, paths, nil, nil); err != nil || !allow {
		t.Fatal(allow, err)
	}
	if allow, _, _ := fresh.Get(canonical); allow {
		t.Fatal("override modified remembered denial")
	}
	asked := false
	if allow, err := fresh.Resolve(context.Background(), "ask", root, paths, func(context.Context, Request) (bool, error) { asked = true; return true, nil }, nil); err != nil || !allow || !asked {
		t.Fatal(allow, err)
	}
	if err := os.WriteFile(store.path(canonical), []byte(`{"version":999}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&TrustStore{Base: store.Base}).Get(canonical); err == nil {
		t.Fatal("malformed record silently accepted")
	}
	for _, data := range []string{`not json`, `null`, `{"version":1,"root":"wrong","instructions":true}`} {
		os.WriteFile(store.path(canonical), []byte(data), 0600)
		if _, _, err := (&TrustStore{Base: store.Base}).Get(canonical); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}

func TestUnknownTrustWithoutUIAndFailedSave(t *testing.T) {
	root, _ := makeProject(t, false)
	canonical, _ := ProjectRoot(root)
	paths := []string{"AGENTS.md"}
	store := &TrustStore{Base: t.TempDir()}
	warnings := 0
	warn := func(error) { warnings++ }
	if allow, err := store.Resolve(context.Background(), "auto", root, paths, nil, warn); err != nil || allow || warnings != 1 {
		t.Fatal(allow, err, warnings)
	}
	if _, found, _ := store.Get(canonical); found {
		t.Fatal("missing UI saved an implicit decision")
	}
	if _, err := store.Resolve(context.Background(), "auto", root, nil, func(context.Context, Request) (bool, error) {
		t.Fatal("no resources should not ask")
		return false, nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocked, []byte("not a directory"), 0600)
	broken := &TrustStore{Base: blocked}
	allow, err := broken.Resolve(context.Background(), "ask", root, paths, func(context.Context, Request) (bool, error) { return true, nil }, warn)
	if err != nil || !allow || warnings != 2 {
		t.Fatal(allow, err, warnings)
	}
	if allow, found, err := broken.Get(canonical); err != nil || !found || !allow {
		t.Fatal("failed save lost current-process decision")
	}
}
