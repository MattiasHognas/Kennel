package main

import (
	"path/filepath"
	"testing"

	repository "MattiasHognas/Kennel/internal/data"
	model "MattiasHognas/Kennel/internal/logic"
)

func TestLoadProjectsSeedsRepositoryOnce(t *testing.T) {
	repo := newTestRepository(t)

	loaded := loadProjects(repo)
	want := sampleProjects()
	assertProjectShape(t, loaded, want)

	stored, err := repo.ReadProjects()
	if err != nil {
		t.Fatalf("read seeded projects: %v", err)
	}
	assertStoredProjectShape(t, stored, want)

	loadedAgain := loadProjects(repo)
	assertProjectShape(t, loadedAgain, want)

	storedAgain, err := repo.ReadProjects()
	if err != nil {
		t.Fatalf("read projects after reload: %v", err)
	}
	assertStoredProjectShape(t, storedAgain, want)
}

func newTestRepository(t *testing.T) *repository.SQLiteRepository {
	t.Helper()

	repo, err := repository.NewSQLiteRepository(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.Close()
	})

	return repo
}

func assertProjectShape(t *testing.T, got, want []model.Project) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("project count = %d, want %d", len(got), len(want))
	}

	for i := range want {
		if got[i].Name != want[i].Name {
			t.Fatalf("project %d name = %q, want %q", i, got[i].Name, want[i].Name)
		}
		if len(got[i].Agents) != len(want[i].Agents) {
			t.Fatalf("project %q agent count = %d, want %d", got[i].Name, len(got[i].Agents), len(want[i].Agents))
		}
		if len(got[i].Activities) != len(want[i].Activities) {
			t.Fatalf("project %q activity count = %d, want %d", got[i].Name, len(got[i].Activities), len(want[i].Activities))
		}
	}
}

func assertStoredProjectShape(t *testing.T, got []repository.Project, want []model.Project) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("stored project count = %d, want %d", len(got), len(want))
	}

	for i := range want {
		if got[i].Name != want[i].Name {
			t.Fatalf("stored project %d name = %q, want %q", i, got[i].Name, want[i].Name)
		}
		if len(got[i].Agents) != len(want[i].Agents) {
			t.Fatalf("stored project %q agent count = %d, want %d", got[i].Name, len(got[i].Agents), len(want[i].Agents))
		}
		if len(got[i].Activities) != len(want[i].Activities) {
			t.Fatalf("stored project %q activity count = %d, want %d", got[i].Name, len(got[i].Activities), len(want[i].Activities))
		}
	}
}
