import re

with open("frontend/src/pages/Onboarding.tsx", "r") as f:
    content = f.read()

content = content.replace("import { useStore } from '../store';", "")
content = content.replace("const res = await axios.post('/api/providers', {", "await axios.post('/api/providers', {")

with open("frontend/src/pages/Onboarding.tsx", "w") as f:
    f.write(content)

with open("frontend/src/pages/ProjectBoard.tsx", "r") as f:
    content = f.read()

content = content.replace("import { DragDropContext, Droppable, Draggable, DropResult } from '@hello-pangea/dnd';", "import { DragDropContext, Droppable, Draggable } from '@hello-pangea/dnd';\nimport type { DropResult } from '@hello-pangea/dnd';")

with open("frontend/src/pages/ProjectBoard.tsx", "w") as f:
    f.write(content)
