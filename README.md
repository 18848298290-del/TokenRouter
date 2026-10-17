<div align="center">
  <img src="assets/logo.svg" alt="TokenRouter" width="112" />

  <h1>TokenRouter</h1>

  <p>An open source unified LLM gateway</p>

  <p>
    <a href="https://github.com/TokenFlux/TokenRouter/actions/workflows/ci.yml"><img src="https://github.com/TokenFlux/TokenRouter/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
    <a href="https://github.com/TokenFlux/TokenRouter/releases"><img src="https://img.shields.io/github/v/release/TokenFlux/TokenRouter?display_name=tag" alt="Release" /></a>
    <a href="https://github.com/TokenFlux/TokenRouter/pkgs/container/tokenrouter"><img src="https://img.shields.io/badge/container-ghcr.io%2Ftokenflux%2Ftokenrouter-2496ED?logo=docker&logoColor=white" alt="Container" /></a>
    <a href="LICENSE"><img src="https://img.shields.io/badge/license-LGPL--3.0--or--later-4c1.svg" alt="License: LGPL-3.0-or-later" /></a>
  </p>

  <p><a href="README_ZH.md">简体中文</a> | <strong>English</strong></p>
  <p><a href="https://discord.gg/4tPj7uk4">Join our Discord community</a></p>
</div>

## Overview

TokenRouter is an open source unified LLM gateway. It puts model services from Anthropic, OpenAI, Gemini, and other vendors behind one endpoint, so a client can call models from different vendors with a single API key.

> [!WARNING]
> Commercial API gateway services are permitted under the applicable open-source licenses. You must not impersonate the official TokenFlux service or use names, logos, domains, interfaces, or promotional materials that misleadingly suggest official operation, authorization, partnership, or endorsement by TokenFlux. Commercial operators are encouraged to customize the frontend and clearly identify the actual operator. The relevant rights holders reserve the right to seek corrective action under applicable law for infringement or such confusion. See the [Commercial Use and Branding Notice](BRANDING_EN.md).

## Screenshots

<details open>
<summary>Light mode</summary>

### Dashboard

<img src="assets/screenshots/dashboard-light.jpg" alt="TokenRouter Dashboard · Light mode" width="1600" />

### Model Marketplace

<img src="assets/screenshots/models-light.jpg" alt="TokenRouter Model Marketplace · Light mode" width="1600" />

</details>

<details>
<summary>Dark mode</summary>

### Dashboard

<img src="assets/screenshots/dashboard-dark.jpg" alt="TokenRouter Dashboard · Dark mode" width="1600" />

### Model Marketplace

<img src="assets/screenshots/models-dark.jpg" alt="TokenRouter Model Marketplace · Dark mode" width="1600" />

</details>

Visit the service officially operated by TokenFlux at [tokenflux.dev](https://tokenflux.dev).

## Capabilities

| Area | What you can do |
| --- | --- |
| Model access | Accept Anthropic Messages, OpenAI Responses, Chat Completions, and Gemini requests, and convert between the protocols a group allows |
| Upstream platforms | Connect Anthropic, OpenAI, Gemini, Antigravity, Grok, Qoder, Kimi, Zhipu, and DeepSeek through OAuth accounts, API keys, AWS Bedrock, or Vertex AI |
| Routing and retries | Pick an upstream by model, protocol support, rate limit state, and sticky sessions, and move to the next one when an upstream fails |
| Access control | Manage users, teams, groups, and API key quotas, concurrency, and RPM limits. Sign in with email and password, passkeys, two-factor authentication, GitHub, Google, LinuxDo, OIDC, WeChat, or DingTalk |
| Billing and payments | Charge by usage, with balance, subscription plans, quota packs, redeem codes, and referral rebates. Built-in payments support EasyPay, Alipay, WeChat Pay, Stripe, and Airwallex |
| Content moderation | Block requests with keyword and hash rules, and ban offending users automatically |
| Admin dashboard | Browse usage logs and statistics, set up alerts and email reports, and upgrade or roll back online. The creative studio generates and edits images in the browser. The UI is available in Simplified Chinese and English |

### Protocols and Endpoints

| Type | Common endpoints |
| --- | --- |
| Anthropic Messages | `POST /v1/messages` |
| OpenAI Responses / Chat | `POST /v1/responses`, `POST /v1/chat/completions` |
| Gemini | `POST /v1beta/models/{model}:generateContent`, `POST /v1beta/models/{model}:streamGenerateContent` |
| WebSocket | `GET /v1/responses`, `GET /v1/realtime` |
| Embeddings | `POST /v1/embeddings` |
| Images | `/v1/images/generations`, `/v1/images/edits`, `/v1/images/batches` |
| Video | `/v1/videos/generations`, `/v1/videos/edits`, `/v1/videos/extensions` |
| Voice | `/v1/tts`, `/v1/stt`, `/v1/custom-voices` |
| Search | `/v1/web_search`, `/v1/x_search`, `/v1/alpha/search` |
| Models and usage | `GET /v1/models`, `GET /v1/usage` |

The [upstream provider capability matrix (Chinese)](docs/interfaces/upstream_provider_matrix.md) lists which upstream platforms serve each endpoint.

## Quick Start

Deploy with Docker Compose:

```bash
mkdir -p tokenrouter-deploy && cd tokenrouter-deploy

# Download the Compose file and .env, and generate the database password and secrets
curl -sSL https://raw.githubusercontent.com/TokenFlux/TokenRouter/main/deploy/docker-deploy.sh | bash

docker compose up -d
```

Then open `http://localhost:8080`. You can set the admin password with `ADMIN_PASSWORD` in `.env`. If it is not set, TokenRouter generates a random password and prints it to the log:

```bash
docker compose logs tokenrouter | grep "admin password"
```

The [deployment guide (Chinese)](docs/guides/deployment/index.md) covers other options such as the install script and standalone containers.

## Development

The backend is written in Go and the frontend in Vue 3. To run the full stack from source:

```bash
docker compose -f deploy/docker-compose.dev.yml up --build
```

[Development workflow (Chinese)](docs/operations/development_workflow.md) lists the required toolchain versions, how to run the backend and frontend separately, and the test commands.

## Documentation

- [Usage and operations guides (Chinese)](docs/guides/index.md): deployment, payment setup, and external integrations.
- [Engineering documentation (Chinese)](docs/index.md): architecture, business rules, interfaces, and operational constraints. Start here before changing code.
- [Upgrade notes (Chinese)](docs/operations/upgrade_notes.md): changes to watch for in each release.

## License

This project is distributed under the [GNU Lesser General Public License v3.0 or later](LICENSE).

Copyright (c) 2026 Wesley Liddick & TokenFlux

## Star History

<a href="https://star-history.com/#TokenFlux/TokenRouter&Date">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=TokenFlux/TokenRouter&type=Date&theme=dark" />
    <img src="https://api.star-history.com/svg?repos=TokenFlux/TokenRouter&type=Date" alt="Star History Chart" />
  </picture>
</a>
