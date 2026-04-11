with open("e2e/tests/app.spec.ts", "r") as f:
    content = f.read()

content = content.replace("await expect(page.getByText('Dashboard')).toBeVisible({ timeout: 10000 });", "await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible({ timeout: 10000 });")

with open("e2e/tests/app.spec.ts", "w") as f:
    f.write(content)
