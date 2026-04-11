import re

with open("frontend/src/pages/Onboarding.tsx", "r") as f:
    content = f.read()

content = re.sub(r'const \[createdProviderId, setCreatedProviderId\] = useState<number \| null>\(null\);', '', content)
content = content.replace('setCreatedProviderId(res.data.id);', '')
content = re.sub(r'const \{ fetchCompanies \} = useStore\(state => \(\{.*?\}\)\);', '', content, flags=re.DOTALL)
content = content.replace('catch (error)', 'catch (_error)')

with open("frontend/src/pages/Onboarding.tsx", "w") as f:
    f.write(content)
