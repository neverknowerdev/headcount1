-- name: CreateCompany :one
INSERT INTO companies (name) VALUES ($1) RETURNING *;

-- name: ListCompanies :many
SELECT * FROM companies ORDER BY id;

-- name: CreateProject :one
INSERT INTO projects (company_id, name, description) VALUES ($1, $2, $3) RETURNING *;

-- name: ListProjectsByCompany :many
SELECT * FROM projects WHERE company_id = $1 ORDER BY id;

-- name: CreateAgent :one
INSERT INTO agents (company_id, name, description, system_prompt, provider_id, model)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING *;

-- name: ListAgentsByCompany :many
SELECT * FROM agents WHERE company_id = $1 ORDER BY id;

-- name: GetAgent :one
SELECT * FROM agents WHERE id = $1;

-- name: CreateTask :one
INSERT INTO tasks (project_id, agent_id, title, description, status)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: ListTasksByProject :many
SELECT * FROM tasks WHERE project_id = $1 ORDER BY id;

-- name: UpdateTaskStatus :one
UPDATE tasks SET status = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1 RETURNING *;

-- name: GetTask :one
SELECT * FROM tasks WHERE id = $1;

-- name: CreateComment :one
INSERT INTO comments (task_id, author_type, author_id, content)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: ListCommentsByTask :many
SELECT * FROM comments WHERE task_id = $1 ORDER BY created_at ASC;

-- name: CreateAttachment :one
INSERT INTO attachments (task_id, comment_id, filename, file_path, mime_type)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: ListAttachmentsByTask :many
SELECT * FROM attachments WHERE task_id = $1 ORDER BY created_at ASC;

-- name: CreateRun :one
INSERT INTO runs (task_id, agent_id, status)
VALUES ($1, $2, $3) RETURNING *;

-- name: UpdateRunLog :exec
UPDATE runs SET log_content = $2, status = $3, ended_at = $4 WHERE id = $1;

-- name: CreateLLMProvider :one
INSERT INTO llm_providers (name, base_url, api_key)
VALUES ($1, $2, $3) RETURNING *;

-- name: CreateSkill :one
INSERT INTO skills (company_id, name, description, source_url, local_path)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: AssignSkillToAgent :exec
INSERT INTO agent_skills (agent_id, skill_id) VALUES ($1, $2);

-- name: GetLLMProvider :one
SELECT * FROM llm_providers WHERE id = $1;

-- name: ListLLMProviders :many
SELECT * FROM llm_providers ORDER BY id;
