import { tool } from "@opencode-ai/plugin"

export default tool({
    description: "Update the status of the current task. Use this to mark progress.",
    args: {
        status: tool.schema.enum(['refinement', 'in-progress', 'blocked', 'in-review']).describe("The new status for the task."),
    },
    async execute(args, context) {
        try {
            // Retrieve Orchestrator Server URL from config
            const fs = require('fs');
            const path = require('path');
            const os = require('os');
            
            // Check for E2E_PAPERCLIP_HOME first, then fall back to real home
            const paperclipHome = process.env.E2E_PAPERCLIP_HOME || os.homedir();
            const serverUrlPath = path.join(paperclipHome, '.paperclip2', 'server_url.txt');
            if (!fs.existsSync(serverUrlPath)) {
                return `Error: Cannot find Orchestrator server URL at ${serverUrlPath}`;
            }
            const serverUrl = fs.readFileSync(serverUrlPath, 'utf8').trim();

            const sessionID = context.sessionID;
            if (!sessionID) {
                return "Error: No session ID found in context.";
            }

            // GET /api/runs/session/{sessionID}
            const runRes = await fetch(`${serverUrl}/api/runs/session/${sessionID}`);
            if (!runRes.ok) {
                return `Error: Failed to fetch run for session ${sessionID}. Status: ${runRes.status}`;
            }
            const runData = await runRes.json();
            const taskId = runData.task_id;

            if (!taskId) {
                return `Error: No task found for session ${sessionID}.`;
            }

            // PUT /api/tasks/{taskId}
            const updateRes = await fetch(`${serverUrl}/api/tasks/${taskId}`, {
                method: 'PUT',
                headers: {
                    'Content-Type': 'application/json'
                },
                body: JSON.stringify({ status: args.status })
            });

            if (!updateRes.ok) {
                return `Error: Failed to update task status. Status: ${updateRes.status}`;
            }

            return `Task ${taskId} status successfully updated to '${args.status}'.`;
        } catch (e: any) {
            return `Error updating task status: ${e.message}`;
        }
    },
})
