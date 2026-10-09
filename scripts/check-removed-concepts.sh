#!/usr/bin/env bash
# Fails if a name from the engine that the task workflow replaced comes back.
#
# Tasks used to be run by a cheap "orchestrator" model driving "worker"
# sessions through one long chat. That design, its tools, its tables and its
# settings are gone; this check keeps them gone. A name is allowed only where
# it has to appear: the migrations that drop it, the tests that prove those
# migrations and old backups still work, and this file.
set -euo pipefail
cd "$(dirname "$0")/.."

removed=(
  # the orchestrator and its worker sessions
  'task_orchestrator' 'helper_worker' 'orchestrator_run_id' 'RunKind' 'parent_run_id' 'root_run_id'
  'run_new_session' 'run_worker' 'worker_list' 'get_worker_info' 'stop_worker'
  # its messaging and status tables
  'run_events' 'run_status_reports' 'latest_reported_status' 'RunEvent' 'RunStatusReport'
  # its tools
  'finish_task' 'finish_task_execution' 'report_status' 'ask_task_owner' 'ask_ceo' 'answer_message'
  'create_subtask' 'ConfigurableToolNames'
  # per-agent models, tool policy and MCP access
  'proxy_request_logs' 'ProxyRequestLog' 'agent_mcp_' 'AgentMCP' 'can_use_workers' 'CanUseWorkers'
  'worker_permissions' 'worker_allowed_mcps' 'allowed_mcps' 'chat_type' 'ChatType' 'reasoning_level'
  '/proxy/agent/' 'X-Proxy-Log-Mode'
  # the status that preceded the refine phase
  "'refinement'" '"refinement"' 'TaskStatusRefinement'
)

allowed=(
  ':!db/migrations/sqlite' ':!db/migrations/postgres' ':!db/migrations/migrator_test.go'
  ':!pkg/backup' ':!scripts/check-removed-concepts.sh'
  # tests that assert these names are gone or refused
  ':!server/tasks_update_test.go' ':!server/agents_crud_test.go' ':!e2e/tests/01_app.spec.ts'
)

status=0
for name in "${removed[@]}"; do
  if hits=$(git grep --untracked -n -F -- "$name" -- '*.go' '*.ts' '*.tsx' '*.yaml' '*.yml' '*.md' '*.sql' "${allowed[@]}" 2>/dev/null); then
    echo "Removed concept \"$name\" is back:"
    echo "$hits" | sed 's/^/  /'
    status=1
  fi
done

if [ "$status" -ne 0 ]; then
  echo
  echo "These names belong to the engine the task workflow replaced (see README, \"How a task runs\")."
  exit 1
fi
echo "No removed concept has come back."
