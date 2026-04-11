import re

with open("frontend/src/components/Layout.tsx", "r") as f:
    content = f.read()

# Add logic to redirect from onboarding to / if companies exist
new_content = content.replace("""
  if (location.pathname === '/onboarding') {
      return <Onboarding />;
  }""", """
  if (location.pathname === '/onboarding') {
      if (companies.length > 0) {
          window.location.href = '/';
          return null;
      }
      return <Onboarding />;
  }""")

with open("frontend/src/components/Layout.tsx", "w") as f:
    f.write(new_content)
