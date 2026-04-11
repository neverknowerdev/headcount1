with open("e2e/tests/app.spec.ts", "r") as f:
    content = f.read()

content = content.replace("await expect(page.getByText('Projects', { exact: true })).toBeVisible();", "await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible();")

with open("e2e/tests/app.spec.ts", "w") as f:
    f.write(content)
