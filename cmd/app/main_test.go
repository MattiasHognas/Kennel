package main

import (
	"context"
	"path/filepath"
	"testing"

	repository "MattiasHognas/Kennel/internal/data"
	model "MattiasHognas/Kennel/internal/logic"
)

func TestLoadProjectsSeedsRepositoryOnce(t *testing.T) {
	repo := newTestRepository(t)

	loaded, err := loadProjects(repo)
	if err != nil {
		t.Fatalf("load projects: %v", err)
	}
	want, err := sampleProjects()
	if err != nil {
		t.Fatalf("sample projects: %v", err)
	}
	assertProjectShape(t, loaded, want)

	stored, err := repo.ReadProjects(context.Background())
	if err != nil {
		t.Fatalf("read seeded projects: %v", err)
	}
	assertStoredProjectShape(t, stored, want)

	loadedAgain, err := loadProjects(repo)
	if err != nil {
		t.Fatalf("load projects again: %v", err)
	}
	assertProjectShape(t, loadedAgain, want)

	storedAgain, err := repo.ReadProjects(context.Background())
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
		if got[i].Config.Name != want[i].Config.Name {
			t.Fatalf("project %d name = %q, want %q", i, got[i].Config.Name, want[i].Config.Name)
		}
		if got[i].Config.Workplace != want[i].Config.Workplace {
			t.Fatalf("project %q workplace = %q, want %q", got[i].Config.Name, got[i].Config.Workplace, want[i].Config.Workplace)
		}
		if got[i].Config.Instructions != want[i].Config.Instructions {
			t.Fatalf("project %q instructions = %q, want %q", got[i].Config.Name, got[i].Config.Instructions, want[i].Config.Instructions)
		}
		if len(got[i].Runtime.Agents) != len(want[i].Runtime.Agents) {
			t.Fatalf("project %q agent count = %d, want %d", got[i].Config.Name, len(got[i].Runtime.Agents), len(want[i].Runtime.Agents))
		}
		if len(got[i].Runtime.Activities) != len(want[i].Runtime.Activities) {
			t.Fatalf("project %q activity count = %d, want %d", got[i].Config.Name, len(got[i].Runtime.Activities), len(want[i].Runtime.Activities))
		}
	}
}

func assertStoredProjectShape(t *testing.T, got []repository.Project, want []model.Project) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("stored project count = %d, want %d", len(got), len(want))
	}

	for i := range want {
		if got[i].Name != want[i].Config.Name {
			t.Fatalf("stored project %d name = %q, want %q", i, got[i].Name, want[i].Config.Name)
		}
		if got[i].Workplace != want[i].Config.Workplace {
			t.Fatalf("stored project %q workplace = %q, want %q", got[i].Name, got[i].Workplace, want[i].Config.Workplace)
		}
		if got[i].Instructions != want[i].Config.Instructions {
			t.Fatalf("stored project %q instructions = %q, want %q", got[i].Name, got[i].Instructions, want[i].Config.Instructions)
		}
		if len(got[i].Agents) != len(want[i].Runtime.Agents) {
			t.Fatalf("stored project %q agent count = %d, want %d", got[i].Name, len(got[i].Agents), len(want[i].Runtime.Agents))
		}
		if len(got[i].Activities) != len(want[i].Runtime.Activities) {
			t.Fatalf("stored project %q activity count = %d, want %d", got[i].Name, len(got[i].Activities), len(want[i].Runtime.Activities))
		}
	}
}
