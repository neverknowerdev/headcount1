with open("server/handlers.go", "r") as f:
    content = f.read()

content = content.replace('LogContent: "Mock execution started...\nMock execution completed successfully.",', 'LogContent: "Mock execution started...\\nMock execution completed successfully.",')

with open("server/handlers.go", "w") as f:
    f.write(content)
