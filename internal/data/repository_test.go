package data

import (
	"context"
	"path/filepath"
	"testing"
)

func TestUpdateProjectConfigurationPersistsValues(t *testing.T) {
	repo, err := NewSQLiteRepository(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.Close()
	})

	project, err := repo.CreateProject(context.Background(), "Project One", `C:\src\project-one`, "first line\nsecond line")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	if err := repo.UpdateProjectConfiguration(context.Background(), project.ID, "Project One Updated", `C:\src\project-one`, "first line\nsecond line"); err != nil {
		t.Fatalf("update project configuration: %v", err)
	}

	storedProject, err := repo.ReadProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	if storedProject.Name != "Project One Updated" {
		t.Fatalf("name = %q, want %q", storedProject.Name, "Project One Updated")
	}
	if storedProject.Workplace != `C:\src\project-one` {
		t.Fatalf("workplace = %q, want %q", storedProject.Workplace, `C:\src\project-one`)
	}
	if storedProject.Instructions != "first line\nsecond line" {
		t.Fatalf("instructions = %q, want %q", storedProject.Instructions, "first line\nsecond line")
	}
}
