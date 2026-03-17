package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrProjectNotFound = errors.New("project not found")

type Project struct {
	ID           int64
	Name         string
	State        string
	Workplace    string
	Instructions string
	CreatedAt    time.Time
	Agents       []Agent
	Activities   []Activity
}

type Agent struct {
	ID        int64
	ProjectID int64
	Name      string
	State     string
	CreatedAt time.Time
}

type Activity struct {
	ID        int64
	ProjectID int64
	AgentID   sql.NullInt64
	Text      string
	CreatedAt time.Time
}

type SQLiteRepository struct {
	db *sql.DB
}

func NewSQLiteRepository(dsn string) (*SQLiteRepository, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("sqlite dsn cannot be empty")
	}

	if err := ensureDatabaseDirectory(dsn); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite connection: %w", err)
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite connection: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.Exec(`PRAGMA foreign_keys = ON;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	repository := &SQLiteRepository{db: db}
	if err := repository.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return repository, nil
}

func (r *SQLiteRepository) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

func (r *SQLiteRepository) ProjectExists(projectID int64) error {
	if projectID <= 0 {
		return errors.New("project id must be positive")
	}
	var exists bool
	if err := r.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM projects WHERE id = ?)`, projectID).Scan(&exists); err != nil {
		return fmt.Errorf("check project existence: %w", err)
	}
	if !exists {
		return ErrProjectNotFound
	}
	return nil
}

func (r *SQLiteRepository) CreateProject(name string) (Project, error) {
	return r.CreateProjectConfiguration(name, "", "")
}

func (r *SQLiteRepository) CreateProjectConfiguration(name, workplace, instructions string) (Project, error) {
	name = strings.TrimSpace(name)
	workplace = strings.TrimSpace(workplace)
	instructions = strings.TrimSpace(instructions)
	if name == "" {
		return Project{}, errors.New("project name cannot be empty")
	}

	result, err := r.db.Exec(`
		INSERT INTO projects(name, state, workplace, instructions)
		VALUES (?, ?, ?, ?)
	`, name, "stopped", workplace, instructions)
	if err != nil {
		return Project{}, fmt.Errorf("insert project: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return Project{}, fmt.Errorf("fetch project id: %w", err)
	}

	return r.ReadProject(id)
}

func (r *SQLiteRepository) AddAgentToProject(projectID int64, name string) (Agent, error) {
	name = strings.TrimSpace(name)
	if projectID <= 0 {
		return Agent{}, errors.New("project id must be positive")
	}
	if name == "" {
		return Agent{}, errors.New("agent name cannot be empty")
	}

	if err := r.ProjectExists(projectID); err != nil {
		return Agent{}, err
	}

	result, err := r.db.Exec(`
		INSERT INTO agents(project_id, name, state)
		VALUES (?, ?, ?)
	`, projectID, name, "stopped")
	if err != nil {
		return Agent{}, fmt.Errorf("insert agent: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return Agent{}, fmt.Errorf("fetch agent id: %w", err)
	}

	row := r.db.QueryRow(`
		SELECT id, project_id, name, state, created_at
		FROM agents
		WHERE id = ?
	`, id)

	var agent Agent
	if err := row.Scan(&agent.ID, &agent.ProjectID, &agent.Name, &agent.State, &agent.CreatedAt); err != nil {
		return Agent{}, fmt.Errorf("read inserted agent: %w", err)
	}

	return agent, nil
}

func (r *SQLiteRepository) UpdateProjectConfiguration(projectID int64, name, workplace, instructions string) error {
	if projectID <= 0 {
		return errors.New("project id must be positive")
	}

	name = strings.TrimSpace(name)
	workplace = strings.TrimSpace(workplace)
	instructions = strings.TrimSpace(instructions)
	if name == "" {
		return errors.New("project name cannot be empty")
	}

	result, err := r.db.Exec(`
		UPDATE projects
		SET name = ?, workplace = ?, instructions = ?
		WHERE id = ?
	`, name, workplace, instructions, projectID)
	if err != nil {
		return fmt.Errorf("update project configuration: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated rows: %w", err)
	}
	if affected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (r *SQLiteRepository) UpdateProjectState(projectID int64, state string) error {
	if projectID <= 0 {
		return errors.New("project id must be positive")
	}

	state = normalizeState(state)

	result, err := r.db.Exec(`
		UPDATE projects
		SET state = ?
		WHERE id = ?
	`, state, projectID)
	if err != nil {
		return fmt.Errorf("update project state: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated rows: %w", err)
	}
	if affected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (r *SQLiteRepository) UpdateAgentState(agentID int64, state string) error {
	if agentID <= 0 {
		return errors.New("agent id must be positive")
	}

	state = normalizeState(state)

	result, err := r.db.Exec(`
		UPDATE agents
		SET state = ?
		WHERE id = ?
	`, state, agentID)
	if err != nil {
		return fmt.Errorf("update agent state: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated rows: %w", err)
	}
	if affected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (r *SQLiteRepository) NewActivity(projectID int64, agentID sql.NullInt64, text string) (Activity, error) {
	text = strings.TrimSpace(text)
	if projectID <= 0 {
		return Activity{}, errors.New("project id must be positive")
	}
	if text == "" {
		return Activity{}, errors.New("activity text cannot be empty")
	}

	if err := r.ProjectExists(projectID); err != nil {
		return Activity{}, err
	}

	if agentID.Valid {
		var exists bool
		if err := r.db.QueryRow(`
			SELECT EXISTS(
				SELECT 1 FROM agents WHERE id = ? AND project_id = ?
			)
		`, agentID.Int64, projectID).Scan(&exists); err != nil {
			return Activity{}, fmt.Errorf("verify agent for activity: %w", err)
		}
		if !exists {
			return Activity{}, errors.New("agent does not belong to project")
		}
	}

	result, err := r.db.Exec(`
		INSERT INTO activities(project_id, agent_id, text)
		VALUES (?, ?, ?)
	`, projectID, agentID, text)
	if err != nil {
		return Activity{}, fmt.Errorf("insert activity: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return Activity{}, fmt.Errorf("fetch activity id: %w", err)
	}

	row := r.db.QueryRow(`
		SELECT id, project_id, agent_id, text, created_at
		FROM activities
		WHERE id = ?
	`, id)

	var activity Activity
	if err := row.Scan(&activity.ID, &activity.ProjectID, &activity.AgentID, &activity.Text, &activity.CreatedAt); err != nil {
		return Activity{}, fmt.Errorf("read inserted activity: %w", err)
	}

	return activity, nil
}

func (r *SQLiteRepository) ReadProject(projectID int64) (Project, error) {
	if projectID <= 0 {
		return Project{}, errors.New("project id must be positive")
	}

	row := r.db.QueryRow(`
		SELECT id, name, state, workplace, instructions, created_at
		FROM projects
		WHERE id = ?
	`, projectID)

	var project Project
	if err := row.Scan(&project.ID, &project.Name, &project.State, &project.Workplace, &project.Instructions, &project.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Project{}, ErrProjectNotFound
		}
		return Project{}, fmt.Errorf("read project: %w", err)
	}
	project.State = normalizeState(project.State)

	agents, err := r.readAgents(project.ID)
	if err != nil {
		return Project{}, err
	}
	activities, err := r.readActivities(project.ID)
	if err != nil {
		return Project{}, err
	}

	project.Agents = agents
	project.Activities = activities

	return project, nil
}

func (r *SQLiteRepository) ReadProjects() ([]Project, error) {
	rows, err := r.db.Query(`
		SELECT id
		FROM projects
		ORDER BY created_at ASC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query projects: %w", err)
	}

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan project id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	rows.Close()

	projects := make([]Project, 0, len(ids))
	for _, id := range ids {
		project, err := r.ReadProject(id)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}

	return projects, nil
}

func (r *SQLiteRepository) readAgents(projectID int64) ([]Agent, error) {
	rows, err := r.db.Query(`
		SELECT id, project_id, name, state, created_at
		FROM agents
		WHERE project_id = ?
		ORDER BY created_at ASC, id ASC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query project agents: %w", err)
	}
	defer rows.Close()

	agents := make([]Agent, 0)
	for rows.Next() {
		var agent Agent
		if err := rows.Scan(&agent.ID, &agent.ProjectID, &agent.Name, &agent.State, &agent.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan project agent: %w", err)
		}
		agent.State = normalizeState(agent.State)
		agents = append(agents, agent)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project agents: %w", err)
	}

	return agents, nil
}

func (r *SQLiteRepository) readActivities(projectID int64) ([]Activity, error) {
	rows, err := r.db.Query(`
		SELECT id, project_id, agent_id, text, created_at
		FROM activities
		WHERE project_id = ?
		ORDER BY created_at ASC, id ASC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query project activities: %w", err)
	}
	defer rows.Close()

	activities := make([]Activity, 0)
	for rows.Next() {
		var activity Activity
		if err := rows.Scan(&activity.ID, &activity.ProjectID, &activity.AgentID, &activity.Text, &activity.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan project activity: %w", err)
		}
		activities = append(activities, activity)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project activities: %w", err)
	}

	return activities, nil
}

func (r *SQLiteRepository) ensureSchema() error {
	const schema = `
	CREATE TABLE IF NOT EXISTS projects (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		state TEXT NOT NULL DEFAULT 'stopped',
		workplace TEXT NOT NULL DEFAULT '',
		instructions TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS agents (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		state TEXT NOT NULL DEFAULT 'stopped',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS activities (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id INTEGER NOT NULL,
		agent_id INTEGER,
		text TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE,
		FOREIGN KEY(agent_id) REFERENCES agents(id) ON DELETE SET NULL
	);

	CREATE INDEX IF NOT EXISTS idx_agents_project_id ON agents(project_id);
	CREATE INDEX IF NOT EXISTS idx_activities_project_id ON activities(project_id);
	CREATE INDEX IF NOT EXISTS idx_activities_agent_id ON activities(agent_id);
	`

	if _, err := r.db.Exec(schema); err != nil {
		return fmt.Errorf("create sqlite schema: %w", err)
	}

	var hasProjectStateColumn bool
	var hasWorkplaceColumn bool
	var hasInstructionsColumn bool
	rows, err := r.db.Query(`PRAGMA table_info(projects)`)
	if err != nil {
		return fmt.Errorf("check projects table schema: %w", err)
	}
	for rows.Next() {
		var cid int
		var name string
		var typ, notnull, dfltValue, pk interface{}
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("scan table_info: %w", err)
		}
		if name == "state" {
			hasProjectStateColumn = true
		}
		if name == "workplace" {
			hasWorkplaceColumn = true
		}
		if name == "instructions" {
			hasInstructionsColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate table_info: %w", err)
	}
	rows.Close()

	if !hasProjectStateColumn {
		if _, err := r.db.Exec(`ALTER TABLE projects ADD COLUMN state TEXT NOT NULL DEFAULT 'stopped'`); err != nil {
			return fmt.Errorf("migrate projects table with state column: %w", err)
		}
	}

	if !hasWorkplaceColumn {
		if _, err := r.db.Exec(`ALTER TABLE projects ADD COLUMN workplace TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("migrate projects table with workplace column: %w", err)
		}
	}

	if !hasInstructionsColumn {
		if _, err := r.db.Exec(`ALTER TABLE projects ADD COLUMN instructions TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("migrate projects table with instructions column: %w", err)
		}
	}

	var hasStateColumn bool
	rows, err = r.db.Query(`PRAGMA table_info(agents)`)
	if err != nil {
		return fmt.Errorf("check agents table schema: %w", err)
	}
	for rows.Next() {
		var cid int
		var name string
		var typ, notnull, dfltValue, pk interface{}
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("scan table_info: %w", err)
		}
		if name == "state" {
			hasStateColumn = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate table_info: %w", err)
	}
	rows.Close()

	if !hasStateColumn {
		if _, err := r.db.Exec(`ALTER TABLE agents ADD COLUMN state TEXT NOT NULL DEFAULT 'stopped'`); err != nil {
			return fmt.Errorf("migrate agents table with state column: %w", err)
		}
	}

	if _, err := r.db.Exec(`UPDATE agents SET state = 'stopped' WHERE lower(state) = 'paused'`); err != nil {
		return fmt.Errorf("migrate legacy paused states: %w", err)
	}

	if _, err := r.db.Exec(`UPDATE projects SET state = 'stopped' WHERE lower(state) = 'paused' OR trim(state) = ''`); err != nil {
		return fmt.Errorf("migrate legacy project states: %w", err)
	}

	return nil
}

func normalizeState(state string) string {
	trimmed := strings.ToLower(strings.TrimSpace(state))
	switch trimmed {
	case "running", "stopped", "completed":
		return trimmed
	default:
		return "stopped"
	}
}

func ensureDatabaseDirectory(dsn string) error {
	if strings.HasPrefix(dsn, ":") || strings.HasPrefix(strings.ToLower(dsn), "file:") {
		return nil
	}

	dir := filepath.Dir(dsn)
	if dir == "." || dir == "" {
		return nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sqlite directory %q: %w", dir, err)
	}

	return nil
}
