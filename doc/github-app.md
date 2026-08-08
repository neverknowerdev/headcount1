# GitHub App: production and staging

Headcount1 uses one GitHub App for OAuth, repository access, pull requests,
and webhook events. Users add named GitHub identities from **MCP Servers →
GitHub**; OAuth tokens are encrypted as MCP accounts, not entered as PATs.

## GitHub App settings

In the GitHub App's **User authorization callback URL** list, add both:

```text
https://app.headcount1.ai/api/github/callback
https://stagingapp.headcount1.ai/api/github/callback
```

GitHub Apps support multiple callback URLs. Each Headcount1 deployment sends
its own `DEPLOY_URL` as the OAuth `redirect_uri`, so the user returns to the
same environment in which they began authorization.

GitHub Apps have a single webhook URL. Set it to production:

```text
https://app.headcount1.ai/api/github/webhook
```

Enable the events Headcount1 reacts to:

- **Issue comment** (PR review comments)
- **Workflow run** (failed GitHub Actions checks)

## Deployment environment values

Set these shared values in both production and staging. Keep secrets in the
deployment secret store.

```text
HEADCOUNT1_GITHUB_APP_ID=<same App ID>
HEADCOUNT1_GITHUB_APP_CLIENT_ID=<same Client ID>
HEADCOUNT1_GITHUB_APP_CLIENT_SECRET=<same Client secret>
HEADCOUNT1_GITHUB_APP_PRIVATE_KEY=<same private key>
HEADCOUNT1_GITHUB_APP_SLUG=<GitHub App slug>
HEADCOUNT1_GITHUB_APP_WEBHOOK_SECRET=<GitHub webhook secret>
HEADCOUNT1_GITHUB_WEBHOOK_FORWARD_SECRET=<new random shared secret>
```

Set the environment-specific values as follows:

| Environment | `DEPLOY_URL` | `HEADCOUNT1_GITHUB_WEBHOOK_FORWARD_URL` |
| --- | --- | --- |
| Production | `https://app.headcount1.ai` | `https://stagingapp.headcount1.ai/api/github/webhook` |
| Staging | `https://stagingapp.headcount1.ai` | Leave unset |

The production endpoint verifies GitHub's webhook signature first, then
forwards the original delivery to staging with an HMAC signature using
`HEADCOUNT1_GITHUB_WEBHOOK_FORWARD_SECRET`. Staging accepts only a correctly
signed forwarded delivery and does not forward it again. Each environment
ignores deliveries for repositories or pull requests it does not know.

After saving the values, redeploy production and staging. Then authorize a
GitHub account in each environment from **MCP Servers → GitHub** and choose
repository access through the GitHub App installation screen.
