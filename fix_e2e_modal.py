with open("e2e/tests/app.spec.ts", "r") as f:
    content = f.read()

# Since the modal blocks pointer events, let's use a specific locator for the submit button in the modal
content = content.replace("await page.click('button[type=\"submit\"]');", "await page.locator('.fixed.inset-0').locator('button[type=\"submit\"]').click();")
content = content.replace("await page.click('button:has-text(\"\")'); // Actually the X button, let's just press Esc", "")

with open("e2e/tests/app.spec.ts", "w") as f:
    f.write(content)
