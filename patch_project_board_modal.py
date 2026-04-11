import re

with open("frontend/src/pages/ProjectBoard.tsx", "r") as f:
    content = f.read()

# Add TaskModal import
content = content.replace("import { Plus } from 'lucide-react';", "import { Plus } from 'lucide-react';\nimport { TaskModal } from '../components/TaskModal';")

# Add state for selected task
content = content.replace('const [newTaskTitle, setNewTaskTitle] = useState(\'\');', 'const [newTaskTitle, setNewTaskTitle] = useState(\'\');\n  const [viewingTaskId, setViewingTaskId] = useState<number | null>(null);')

# Update Draggable item rendering to click to open modal
old_draggable = """<div
                                        ref={provided.innerRef}
                                        {...provided.draggableProps}
                                        {...provided.dragHandleProps}
                                        className={`bg-white p-4 rounded-md border shadow-sm ${snapshot.isDragging ? 'shadow-lg ring-2 ring-indigo-500 border-transparent' : 'hover:border-indigo-300'} transition-shadow cursor-grab`}
                                    >"""

new_draggable = """<div
                                        ref={provided.innerRef}
                                        {...provided.draggableProps}
                                        {...provided.dragHandleProps}
                                        onClick={() => setViewingTaskId(task.id)}
                                        className={`bg-white p-4 rounded-md border shadow-sm ${snapshot.isDragging ? 'shadow-lg ring-2 ring-indigo-500 border-transparent' : 'hover:border-indigo-300'} transition-shadow cursor-grab`}
                                    >"""

content = content.replace(old_draggable, new_draggable)

# Add TaskModal rendering at the end of the return statement
content = content.replace('    </div>\n  );\n};', '    </div>\n      {viewingTaskId && <TaskModal taskId={viewingTaskId} onClose={() => setViewingTaskId(null)} />}\n  );\n};')

with open("frontend/src/pages/ProjectBoard.tsx", "w") as f:
    f.write(content)
