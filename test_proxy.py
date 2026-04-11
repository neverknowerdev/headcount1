import requests

try:
    resp = requests.post("http://localhost:8080/api/providers/test", json={
        "base_url": "e2e-mock",
        "api_key": "test",
        "model": "test"
    })
    print(resp.status_code)
    print(resp.text)
except Exception as e:
    print(e)
