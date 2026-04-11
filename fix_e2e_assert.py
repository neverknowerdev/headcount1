with open("e2e/tests/app.spec.ts", "r") as f:
    content = f.read()

# Since we trigger a reload in UI on submit, let's wait for that to happen
content = content.replace("await page.goto('/');", "await page.waitForTimeout(2000);\n        await page.goto('/');")

with open("e2e/tests/app.spec.ts", "w") as f:
    f.write(content)
