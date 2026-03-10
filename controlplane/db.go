package controlplane

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps SQLite for persistent state.
type DB struct {
	db *sql.DB
}

// OpenDB opens (or creates) the SQLite database.
func OpenDB(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path+"?_journal=WAL&_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	d := &DB{db: db}
	if err := d.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) migrate() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			email TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			api_key TEXT UNIQUE NOT NULL,
			owner_id TEXT NOT NULL REFERENCES users(id),
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS nodes (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			address TEXT NOT NULL,
			public_ip TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT 'custom',
			provider_id TEXT NOT NULL DEFAULT '',
			region TEXT NOT NULL DEFAULT '',
			instance_type TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'offline',
			max_sandboxes INTEGER NOT NULL DEFAULT 50,
			running_sandboxes INTEGER NOT NULL DEFAULT 0,
			last_seen TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sandboxes (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			node_id TEXT NOT NULL REFERENCES nodes(id),
			template TEXT NOT NULL DEFAULT 'base',
			status TEXT NOT NULL DEFAULT 'creating',
			vcpus INTEGER NOT NULL DEFAULT 1,
			memory_mb INTEGER NOT NULL DEFAULT 512,
			host_ip TEXT NOT NULL DEFAULT '',
			timeout_sec INTEGER NOT NULL DEFAULT 300,
			metadata TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS cloud_pools (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			provider TEXT NOT NULL,
			region TEXT NOT NULL,
			instance_type TEXT NOT NULL,
			min_nodes INTEGER NOT NULL DEFAULT 0,
			max_nodes INTEGER NOT NULL DEFAULT 10,
			current_nodes INTEGER NOT NULL DEFAULT 0,
			credentials TEXT NOT NULL DEFAULT '{}'
		)`,
	}
	for _, stmt := range statements {
		if _, err := d.db.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:40], err)
		}
	}
	return nil
}

// --- Users ---

func (d *DB) CreateUser(id, email, passwordHash string) error {
	_, err := d.db.Exec(
		`INSERT INTO users (id, email, password_hash, created_at) VALUES (?, ?, ?, ?)`,
		id, email, passwordHash, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (d *DB) GetUserByEmail(email string) (*User, error) {
	row := d.db.QueryRow(`SELECT id, email, password_hash, created_at FROM users WHERE email = ?`, email)
	u := &User{}
	var createdAt string
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &createdAt); err != nil {
		return nil, err
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return u, nil
}

func (d *DB) GetUser(id string) (*User, error) {
	row := d.db.QueryRow(`SELECT id, email, password_hash, created_at FROM users WHERE id = ?`, id)
	u := &User{}
	var createdAt string
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &createdAt); err != nil {
		return nil, err
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return u, nil
}

// --- Projects ---

func (d *DB) CreateProject(id, name, ownerID string) (string, error) {
	apiKey := generateAPIKey()
	_, err := d.db.Exec(
		`INSERT INTO projects (id, name, api_key, owner_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, name, apiKey, ownerID, time.Now().UTC().Format(time.RFC3339),
	)
	return apiKey, err
}

func (d *DB) GetProjectByAPIKey(apiKey string) (*Project, error) {
	row := d.db.QueryRow(`SELECT id, name, api_key, owner_id, created_at FROM projects WHERE api_key = ?`, apiKey)
	p := &Project{}
	var createdAt string
	if err := row.Scan(&p.ID, &p.Name, &p.APIKey, &p.OwnerID, &createdAt); err != nil {
		return nil, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return p, nil
}

func (d *DB) ListProjects(ownerID string) ([]Project, error) {
	rows, err := d.db.Query(`SELECT id, name, api_key, owner_id, created_at FROM projects WHERE owner_id = ?`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []Project
	for rows.Next() {
		p := Project{}
		var createdAt string
		if err := rows.Scan(&p.ID, &p.Name, &p.APIKey, &p.OwnerID, &createdAt); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		projects = append(projects, p)
	}
	return projects, nil
}

// --- Nodes ---

func (d *DB) UpsertNode(n *Node) error {
	_, err := d.db.Exec(`
		INSERT INTO nodes (id, name, address, public_ip, provider, provider_id, region, instance_type, status, max_sandboxes, running_sandboxes, last_seen, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			address = excluded.address,
			status = excluded.status,
			running_sandboxes = excluded.running_sandboxes,
			last_seen = excluded.last_seen`,
		n.ID, n.Name, n.Address, n.PublicIP, n.Provider, n.ProviderID, n.Region, n.InstanceType,
		n.Status, n.MaxSandboxes, n.Running, n.LastSeen.UTC().Format(time.RFC3339), n.CreatedAt.UTC().Format(time.RFC3339),
	)
	return err
}

func (d *DB) GetNode(id string) (*Node, error) {
	return d.scanNode(d.db.QueryRow(`SELECT id, name, address, public_ip, provider, provider_id, region, instance_type, status, max_sandboxes, running_sandboxes, last_seen, created_at FROM nodes WHERE id = ?`, id))
}

func (d *DB) ListNodes() ([]Node, error) {
	rows, err := d.db.Query(`SELECT id, name, address, public_ip, provider, provider_id, region, instance_type, status, max_sandboxes, running_sandboxes, last_seen, created_at FROM nodes ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []Node
	for rows.Next() {
		n := Node{}
		var lastSeen, createdAt string
		if err := rows.Scan(&n.ID, &n.Name, &n.Address, &n.PublicIP, &n.Provider, &n.ProviderID, &n.Region, &n.InstanceType, &n.Status, &n.MaxSandboxes, &n.Running, &lastSeen, &createdAt); err != nil {
			return nil, err
		}
		n.LastSeen, _ = time.Parse(time.RFC3339, lastSeen)
		n.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func (d *DB) ListOnlineNodes() ([]Node, error) {
	rows, err := d.db.Query(`SELECT id, name, address, public_ip, provider, provider_id, region, instance_type, status, max_sandboxes, running_sandboxes, last_seen, created_at FROM nodes WHERE status = 'online' ORDER BY running_sandboxes ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []Node
	for rows.Next() {
		n := Node{}
		var lastSeen, createdAt string
		if err := rows.Scan(&n.ID, &n.Name, &n.Address, &n.PublicIP, &n.Provider, &n.ProviderID, &n.Region, &n.InstanceType, &n.Status, &n.MaxSandboxes, &n.Running, &lastSeen, &createdAt); err != nil {
			return nil, err
		}
		n.LastSeen, _ = time.Parse(time.RFC3339, lastSeen)
		n.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func (d *DB) UpdateNodeStatus(id, status string) error {
	_, err := d.db.Exec(`UPDATE nodes SET status = ? WHERE id = ?`, status, id)
	return err
}

func (d *DB) UpdateNodeHeartbeat(id string, running int) error {
	_, err := d.db.Exec(`UPDATE nodes SET running_sandboxes = ?, last_seen = ?, status = 'online' WHERE id = ?`,
		running, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func (d *DB) DeleteNode(id string) error {
	_, err := d.db.Exec(`DELETE FROM nodes WHERE id = ?`, id)
	return err
}

func (d *DB) scanNode(row *sql.Row) (*Node, error) {
	n := &Node{}
	var lastSeen, createdAt string
	if err := row.Scan(&n.ID, &n.Name, &n.Address, &n.PublicIP, &n.Provider, &n.ProviderID, &n.Region, &n.InstanceType, &n.Status, &n.MaxSandboxes, &n.Running, &lastSeen, &createdAt); err != nil {
		return nil, err
	}
	n.LastSeen, _ = time.Parse(time.RFC3339, lastSeen)
	n.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return n, nil
}

// --- Sandboxes ---

func (d *DB) CreateSandbox(s *Sandbox) error {
	_, err := d.db.Exec(`
		INSERT INTO sandboxes (id, project_id, node_id, template, status, vcpus, memory_mb, host_ip, timeout_sec, metadata, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', ?, ?)`,
		s.ID, s.ProjectID, s.NodeID, s.Template, s.Status, s.VCPUs, s.MemoryMB, s.HostIP, s.TimeoutSec,
		s.CreatedAt.UTC().Format(time.RFC3339), s.ExpiresAt.UTC().Format(time.RFC3339),
	)
	return err
}

func (d *DB) GetSandbox(id string) (*Sandbox, error) {
	row := d.db.QueryRow(`SELECT id, project_id, node_id, template, status, vcpus, memory_mb, host_ip, timeout_sec, created_at, expires_at FROM sandboxes WHERE id = ?`, id)
	return d.scanSandbox(row)
}

func (d *DB) ListSandboxes(projectID string) ([]Sandbox, error) {
	rows, err := d.db.Query(`SELECT id, project_id, node_id, template, status, vcpus, memory_mb, host_ip, timeout_sec, created_at, expires_at FROM sandboxes WHERE project_id = ? ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sandboxes []Sandbox
	for rows.Next() {
		s := Sandbox{}
		var createdAt, expiresAt string
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.NodeID, &s.Template, &s.Status, &s.VCPUs, &s.MemoryMB, &s.HostIP, &s.TimeoutSec, &createdAt, &expiresAt); err != nil {
			return nil, err
		}
		s.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		s.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
		sandboxes = append(sandboxes, s)
	}
	return sandboxes, nil
}

func (d *DB) ListAllSandboxes() ([]Sandbox, error) {
	rows, err := d.db.Query(`SELECT id, project_id, node_id, template, status, vcpus, memory_mb, host_ip, timeout_sec, created_at, expires_at FROM sandboxes ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sandboxes []Sandbox
	for rows.Next() {
		s := Sandbox{}
		var createdAt, expiresAt string
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.NodeID, &s.Template, &s.Status, &s.VCPUs, &s.MemoryMB, &s.HostIP, &s.TimeoutSec, &createdAt, &expiresAt); err != nil {
			return nil, err
		}
		s.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		s.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
		sandboxes = append(sandboxes, s)
	}
	return sandboxes, nil
}

func (d *DB) UpdateSandboxStatus(id, status string) error {
	_, err := d.db.Exec(`UPDATE sandboxes SET status = ? WHERE id = ?`, status, id)
	return err
}

func (d *DB) DeleteSandbox(id string) error {
	_, err := d.db.Exec(`DELETE FROM sandboxes WHERE id = ?`, id)
	return err
}

func (d *DB) scanSandbox(row *sql.Row) (*Sandbox, error) {
	s := &Sandbox{}
	var createdAt, expiresAt string
	if err := row.Scan(&s.ID, &s.ProjectID, &s.NodeID, &s.Template, &s.Status, &s.VCPUs, &s.MemoryMB, &s.HostIP, &s.TimeoutSec, &createdAt, &expiresAt); err != nil {
		return nil, err
	}
	s.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	s.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
	return s, nil
}

// --- Cloud Pools ---

func (d *DB) UpsertCloudPool(p *CloudPool) error {
	_, err := d.db.Exec(`
		INSERT INTO cloud_pools (id, name, provider, region, instance_type, min_nodes, max_nodes, current_nodes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			min_nodes = excluded.min_nodes,
			max_nodes = excluded.max_nodes,
			current_nodes = excluded.current_nodes`,
		p.ID, p.Name, p.Provider, p.Region, p.InstanceType, p.MinNodes, p.MaxNodes, p.CurrentNodes,
	)
	return err
}

func (d *DB) ListCloudPools() ([]CloudPool, error) {
	rows, err := d.db.Query(`SELECT id, name, provider, region, instance_type, min_nodes, max_nodes, current_nodes FROM cloud_pools`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pools []CloudPool
	for rows.Next() {
		p := CloudPool{}
		if err := rows.Scan(&p.ID, &p.Name, &p.Provider, &p.Region, &p.InstanceType, &p.MinNodes, &p.MaxNodes, &p.CurrentNodes); err != nil {
			return nil, err
		}
		pools = append(pools, p)
	}
	return pools, nil
}

// --- Helpers ---

func generateAPIKey() string {
	b := make([]byte, 24)
	rand.Read(b)
	return "gvm_" + hex.EncodeToString(b)
}

// TotalCapacity returns total sandbox capacity across online nodes.
func (d *DB) TotalCapacity() (total int, used int, err error) {
	row := d.db.QueryRow(`SELECT COALESCE(SUM(max_sandboxes), 0), COALESCE(SUM(running_sandboxes), 0) FROM nodes WHERE status = 'online'`)
	err = row.Scan(&total, &used)
	return
}
