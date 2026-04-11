import re

with open("server/handlers.go", "r") as f:
    content = f.read()

# Add registerSkillRoutes in handlers.go instead of having duplicate Mounts
content = content.replace('registerActivityRoutes(r, s.db)', 'registerActivityRoutes(r, s.db)\n\tregisterSkillRoutes(r, s.db)')

with open("server/handlers.go", "w") as f:
    f.write(content)
