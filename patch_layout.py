import re

with open("frontend/src/components/Layout.tsx", "r") as f:
    content = f.read()

# remove logic to return <Onboarding /> directly because the router handles /onboarding
new_content = content.replace("""
  if (companies.length === 0 || location.pathname === '/onboarding') {
    return <Onboarding />;
  }""", """
  if (companies.length === 0 && location.pathname !== '/onboarding') {
      window.location.href = '/onboarding';
      return null;
  }

  if (location.pathname === '/onboarding') {
      return <Onboarding />;
  }
""")

with open("frontend/src/components/Layout.tsx", "w") as f:
    f.write(new_content)
