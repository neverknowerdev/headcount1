with open("frontend/src/pages/ProjectBoard.tsx", "r") as f:
    lines = f.readlines()

for i, line in enumerate(lines):
    if "{viewingTaskId && <TaskModal" in line:
        # Move this line up before the last closing div
        lines.pop(i)
        for j in range(len(lines)-1, -1, -1):
            if "</div>" in lines[j] and j > len(lines) - 5:
                lines.insert(j, "      {viewingTaskId && <TaskModal taskId={viewingTaskId} onClose={() => setViewingTaskId(null)} />}\n")
                break
        break

with open("frontend/src/pages/ProjectBoard.tsx", "w") as f:
    f.writelines(lines)
