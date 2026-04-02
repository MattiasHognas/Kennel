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

func TestAddAgentToStreamPersistsStreamFields(t *testing.T) {
	repo, err := NewSQLiteRepository(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.Close()
	})

	project, err := repo.CreateProject(context.Background(), "Project One", "/tmp/project-one", "ship it")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	agent, err := repo.AddAgentToStream(context.Background(), project.ID, 2, "frontend-developer", "s2:t0", "project/run/stream-2")
	if err != nil {
		t.Fatalf("add agent to stream: %v", err)
	}

	if !agent.StreamID.Valid || agent.StreamID.Int64 != 2 {
		t.Fatalf("stream id = %#v, want valid 2", agent.StreamID)
	}
	if agent.BranchName != "project/run/stream-2" {
		t.Fatalf("branch name = %q, want %q", agent.BranchName, "project/run/stream-2")
	}

	storedProject, err := repo.ReadProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if len(storedProject.Agents) != 1 {
		t.Fatalf("agent count = %d, want 1", len(storedProject.Agents))
	}
	if !storedProject.Agents[0].StreamID.Valid || storedProject.Agents[0].StreamID.Int64 != 2 {
		t.Fatalf("stored stream id = %#v, want valid 2", storedProject.Agents[0].StreamID)
	}
	if storedProject.Agents[0].BranchName != "project/run/stream-2" {
		t.Fatalf("stored branch name = %q, want %q", storedProject.Agents[0].BranchName, "project/run/stream-2")
	}
}
