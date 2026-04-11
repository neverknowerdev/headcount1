import os, glob

files = [
    "frontend/src/components/TaskModal.tsx",
    "frontend/src/pages/AgentManager.tsx",
    "frontend/src/pages/CompanyView.tsx",
    "frontend/src/pages/Dashboard.tsx",
    "frontend/src/pages/Onboarding.tsx",
    "frontend/src/pages/ProjectBoard.tsx",
    "frontend/src/pages/SkillsManager.tsx",
]

for file in files:
    with open(file, "r") as f:
        content = f.read()

    # Fix any typing
    content = content.replace("any[]", "Record<string, unknown>[]")
    content = content.replace("useState<any>", "useState<Record<string, unknown>>")
    content = content.replace("useState<any[]>", "useState<Record<string, unknown>[]>")
    content = content.replace("catch (_error)", "catch")
    content = content.replace("} catch {}", "} catch (e) { /* ignore */ }")

    with open(file, "w") as f:
        f.write(content)
