with open("server/handlers.go", "r") as f:
    lines = f.readlines()

for i, line in enumerate(lines):
    if 'r.Post("/skills/import", s.importSkill)' in line:
        lines.insert(i+1, '\t\tr.Route("/providers", func(r chi.Router) {\n\t\t\tr.Get("/", s.listProviders)\n\t\t\tr.Post("/", s.createProvider)\n\t\t\tr.Post("/test", s.testProvider)\n\t\t})\n')
        break

with open("server/handlers.go", "w") as f:
    f.writelines(lines)
