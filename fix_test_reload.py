import re

with open("frontend/src/pages/Onboarding.tsx", "r") as f:
    content = f.read()

# Make sure the redirect handles the state correctly or reloads completely
content = content.replace("window.location.href = '/';", "window.location.href = '/';\n            setTimeout(() => window.location.reload(), 100);")

with open("frontend/src/pages/Onboarding.tsx", "w") as f:
    f.write(content)
