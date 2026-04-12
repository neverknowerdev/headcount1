import re

with open("server/handlers.go", "r") as f:
    content = f.read()

content = content.replace('logMsg := "Request URL: " + url + "\nStatus: " + resp.Status + "\nResponse: " + string(respBody)', 'logMsg := "Request URL: " + url + "\\nStatus: " + resp.Status + "\\nResponse: " + string(respBody)')

with open("server/handlers.go", "w") as f:
    f.write(content)
