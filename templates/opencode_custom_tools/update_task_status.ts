import { tool } from "@opencode-ai/plugin"
import fs from 'fs'
import path from 'path'
import os from 'os'

export default tool({
    description: "Update the status of the current task. Use this to mark progress.",
    args: {
        status: tool.schema.enum(['refinement', 'in-progress', 'blocked', 'in-review']).describe("The new status for the task."),
    },
    async execute(args, context) {
        try {
            let serverUrl = process.env.PAPERCLIP_SERVER_URL?.trim()

            if (!serverUrl) {
                const candidates = []
                
                if (process.env.E2E_PAPERCLIP_HOME) {
                    candidates.push(path.join(process.env.E2E_PAPERCLIP_HOME, '.paperclip2', 'server_url.txt'))
                }
                
                const homeDir = os.homedir()
                candidates.push(path.join(homeDir, '.paperclip2', 'server_url.txt'))
                
                if (process.env.HOME) {
                    candidates.push(path.join(process.env.HOME, '.paperclip2', 'server_url.txt'))
                }

                for (const candidate of candidates) {
                    if (fs.existsSync(candidate)) {
                        serverUrl = fs.readFileSync(candidate, 'utf8').trim()
                        break
                    }
                }
            }

            if (!serverUrl) {
                return "Error: Cannot find Orchestrator server URL. Please ensure the Paperclip server is running and server_url.txt exists in ~/.paperclip2/"
            }

            const sessionID = context.sessionID
            if (!sessionID) {
                return "Error: No session ID found in context."
            }

            // Detect if running in Docker and adjust URL accordingly
            function resolveServerUrl(url: string): string {
                // Check if we're in Docker (look for .dockerenv or cgroup)
                const isDocker = fs.existsSync('/.dockerenv') || 
                    (fs.existsSync('/proc/1/cgroup') && fs.readFileSync('/proc/1/cgroup', 'utf8').includes('docker'))
                
                if (isDocker) {
                    // Replace localhost/127.0.0.1 with host.docker.internal
                    return url.replace(/localhost|127\.0\.0\.1/g, 'host.docker.internal')
                }
                return url
            }

            const resolvedUrl = resolveServerUrl(serverUrl)

            // Try fetch first
            async function tryFetch(url: string, options: { method?: string, body?: string } = {}): Promise<string> {
                const fetchOpts: RequestInit = {
                    method: options.method || 'GET',
                    headers: { 'Content-Type': 'application/json' }
                }
                if (options.body) {
                    fetchOpts.body = options.body
                }
                
                const controller = new AbortController()
                const timeoutId = setTimeout(() => controller.abort(), 10000)
                fetchOpts.signal = controller.signal
                
                const response = await fetch(url, fetchOpts)
                clearTimeout(timeoutId)
                
                if (!response.ok) {
                    throw new Error(`HTTP ${response.status}: ${response.statusText}`)
                }
                return await response.text()
            }

            // Fallback to curl
            async function tryCurl(url: string, options: { method?: string, body?: string } = {}): Promise<string> {
                const { execSync } = await import('child_process')
                let cmd = `curl -s --connect-timeout 10 --max-time 30 "${url}"`
                if (options.method) {
                    cmd += ` -X ${options.method}`
                }
                if (options.body) {
                    const escapedBody = options.body.replace(/'/g, "'\\''")
                    cmd += ` -H "Content-Type: application/json" -d '${escapedBody}'`
                }
                
                return execSync(cmd, { encoding: 'utf8', timeout: 30000 })
            }

            // Fetch run by session ID (try fetch, then curl)
            let runResult: string
            try {
                runResult = await tryFetch(`${resolvedUrl}/api/runs/session/${sessionID}`)
            } catch (fetchErr: any) {
                try {
                    runResult = await tryCurl(`${resolvedUrl}/api/runs/session/${sessionID}`)
                } catch (curlErr: any) {
                    return `Error: Cannot reach server. Original URL: ${serverUrl}, Resolved: ${resolvedUrl}. Fetch error: ${fetchErr.message}. Curl error: ${curlErr.message}`
                }
            }
            
            let runData
            try {
                runData = JSON.parse(runResult)
            } catch {
                return `Error: Invalid response from server: ${runResult}`
            }
            
            if (runData.error) {
                return `Error: ${runData.error}`
            }
            
            const taskId = runData.task_id
            if (!taskId) {
                return `Error: No task found for session ${sessionID}.`
            }

            // Update task status
            const body = JSON.stringify({ status: args.status })
            let updateResult: string
            try {
                updateResult = await tryFetch(`${resolvedUrl}/api/tasks/${taskId}`, { method: 'PUT', body })
            } catch (fetchErr: any) {
                try {
                    updateResult = await tryCurl(`${resolvedUrl}/api/tasks/${taskId}`, { method: 'PUT', body })
                } catch (curlErr: any) {
                    return `Error: Failed to update task. Fetch error: ${fetchErr.message}. Curl error: ${curlErr.message}`
                }
            }
            
            let updateData
            try {
                updateData = JSON.parse(updateResult)
            } catch {
                return `Error: Invalid response from server: ${updateResult}`
            }
            
            if (updateData.error) {
                return `Error: ${updateData.error}`
            }

            return `Task ${taskId} status successfully updated to '${args.status}'.`
        } catch (e: any) {
            return `Error updating task status: ${e.message}`
        }
    },
})
