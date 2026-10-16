# Dujiao-Next

Dujiao-Next is a digital goods e-commerce platform. This repository contains the complete
application: the Go backend, the customer storefront, and the admin panel.

## External Upstream Integrations (Custom Build)

This checkout adds `yichiyuan` and `16688` protocols under **Upstream / Site Connections**.
It is based on upstream commit `41b86260dc9a60269526057eed8d915844ceb3a3`.
Build from this source; official prebuilt binaries do not contain these adapters.
Keep custom builds on `BuildType=source` so an official self-update cannot replace the changes.

### Configuration

- Run Redis and the background worker (`-mode all`, or separate API and worker processes).
  Procurement and delivery polling require the queue; API-only mode without a worker is insufficient.
- Select a protocol, use the site **root URL** (no `/openApi` or documentation path),
  put `app_id` in Application ID, and put `app_key` (Yichiyuan) or `secret` (16688) in Application Secret.
  Production URLs should use HTTPS. Secrets retain the existing encrypted-at-rest behavior.
- Test the connection, then import goods through the existing product-mapping screen.
  Configure markup and exchange rate before publishing, and verify a single low-value purchase first.
  Connection tests and catalog queries do not purchase goods.
- 16688 requires already-agented **supply** goods and a funded operating wallet. Local merchant
  goods are not procurement sources. Imported and synchronized prices include the quoted procurement
  fee (per-unit cost rounded up to a cent); quantity-based fees and future price changes can still
  affect the actual charge. Set a sufficient margin. Categories are not exposed because the goods
  response does not associate supply goods with the merchant's local category identifiers.
- Yichiyuan supports basic automatic cards only. Category/SKU variants, custom widgets, typed
  contacts and order passwords are rejected, rather than purchasing an unspecified default variant.
  Preselection and manual-delivery goods are not supported. Minimum/maximum quantities are imported
  and checked again immediately before procurement; changed upstream limits may require a re-import.
  This adapter requires CNY; older responses omit currency, so confirm the upstream shop currency
  before enabling. If a response explicitly reports another currency, import/purchase is rejected.
- External connection identity (protocol, root URL and application ID) cannot be reassigned.
  Create another connection for a different account/site. Secret rotation remains supported.

### Yichiyuan Delivery-Status Requirement

The documented `/shared/commodity/query` returns **payment** `status` and `secret`, but no
reliable delivery state. The upstream source can place a review/waiting message in `secret`,
including for automatic goods. Treating that as a card would incorrectly complete the local order.

For this reason, Yichiyuan procurement and product import are disabled by default. The upstream
operator must add the real order `delivery_status` to the authenticated query response (without
removing existing fields), then verify it and enable **Upstream delivery status extension verified**
on the connection. This is an explicit extension, not a field promised by the linked API document.
If the upstream cannot add it, do not enable the switch; catalog inspection remains available,
but automatic Yichiyuan fulfillment is not ready for production.

For the upstream controller shown in the current Yichiyuan source, the response addition is:

```php
return $this->json(200, 'success', [
    'secret' => $secret,
    'widget' => $widget,
    'status' => $order->status,
    'delivery_status' => (int)$order->delivery_status, // 0=pending, 1=delivered
]);
```

Only payment `status=1`, `delivery_status=1` and non-empty content complete local fulfillment.
Missing/invalid delivery status fails the query and leaves the order pending for investigation.

### Purchase Safety and Recovery

String product codes and order numbers are preserved in `upstream_remote_references`; local
numeric IDs never truncate or hash the actual identifiers. `upstream_purchase_attempts` provides
a durable unique send barrier per connection and procurement trace. Both new tables and the
`site_connections.ycy_delivery_status_confirmed` column are added by normal startup migrations,
with SQLite and PostgreSQL-compatible GORM operations. No existing rows are deleted.

After a purchase has been sent, retries reuse a saved successful receipt and never purchase again.
Timeouts, malformed responses, upstream errors and receipt-save failures keep the barrier. Even
manual retries cannot resend an uncertain purchase. A failed *preflight* can be retried because no
purchase request was sent. Yichiyuan also receives a stable `request_no`; 16688 documents no
equivalent idempotency key. This conservative approach can require manual intervention after a
crash, even if the first request never reached the upstream.

For an uncertain purchase, stop retries and check the upstream purchase history, account balance,
local order/trace and (16688) `dujiao:<request hash>` remark. Fulfill an existing purchase manually,
or refund/resolve locally after reconciling. There is intentionally no UI action that blindly clears
the barrier. Do not delete attempt/reference rows, recreate the same purchase under a new connection,
or remove these tables while orders are outstanding; those actions can cause duplicate charges.
Neither upstream documents a purchase-cancellation endpoint, so accepted purchases cannot be
canceled through this integration. 16688 full-refund status is mapped from its documented order
state; the actual refunded amount is not inferred from the original charge (fees may be retained).
Complaint workflows and partial-refund accounting are not implemented, and upstream refunds do
not automatically refund the local customer's payment; reconcile and handle that separately.

Back up the database and `app.secret_key` before upgrading or rolling back. A rollback binary
will not understand the new protocols: disable their mapped products and resolve outstanding
orders first, while retaining the extra tables and encrypted configuration. Deployment should use
this source's Dockerfile or a custom fullstack binary, not the official one-click download.

API references: [Yichiyuan](https://faka.wiki/zh-cn/dev/api.html),
[16688](https://www.16688.com.cn/api/purchase/docs/).

## ❤️ Brand Partners (Sponsors)

<table>

<tr>
<td width="180"><a href="https://www.vmrack.net/?ref_code=5iXmGUMf5f5"><img src="assets/partners/vmrack.jpeg" alt="CCTK.AI" width="150"></a></td>
<td><a href="https://www.vmrack.net/?ref_code=5iXmGUMf5f5">Vmrack.com</a> 全球自动化云基础设施服务商 提供先进的云服务器、裸金属、CDN、媒体处理、对象存储和网络解决方案，助力企业轻松上云。
⚡️官方合作闪购款，仅需76刀/年，三网优化线路，助力您业务起飞，<a href="https://www.vmrack.net/vps/flash-deals/2082383856451452928?ref_code=5iXmGUMf5f5">👉点我直达</a>
</td>
</tr>

<tr>
<td width="180"><a href="https://www.99cdn.com/"><img src="assets/partners/99cdn.jpg" alt="openmodel" width="150"></a></td>
<td>99CDN 自建 CDN 平台，自主管理节点 · 智能流量调度 · 多级缓存加速。 <a href="https://www.99cdn.com/">99CDN</a> 是 EasyLink 旗下的商业化自建 CDN 与 DNS 智能调度平台，支持边缘缓存、分片缓存、多级回源、GTM 调度与边缘计算能力。</td>
</tr>

<tr>
<td width="180"><a href="https://niub.me"><img src="assets/partners/niub.png" alt="openmodel" width="150"></a></td>
<td> <a href="https://niub.me">NIUB — 数字服务，一站直达(DujiaoNext自营旗舰店)</a> 正在寻找更便捷的 AI 服务、社交账号或数字礼品卡？NIUB（niub.me）专注提供多种虚拟商品与数字服务，让不同类型的数字需求都能在一个站点完成选购。
我们重视清晰的商品信息、明确的交付方式和负责任的售后支持。每件商品的账号类型、适用地区、有效期限、使用条件与售后范围，均以对应商品页面说明为准。
访问 NIUB，探索更多数字服务与虚拟商品。</td>
</tr>



</table>

## Tech Stack

| Layer | Stack |
| --- | --- |
| Backend | Go 1.26 · Gin · GORM · SQLite / PostgreSQL |
| Auth | JWT (separate admin / user realms) · Casbin RBAC · TOTP 2FA |
| Async | asynq on Redis (optional — the server runs without it) |
| Config | Viper (`config.yml`) |
| Frontend | Vue 3 · Vite · TypeScript · Tailwind CSS v4 · pnpm 10 |
| Admin UI | shadcn-vue / reka-ui |

## Repository Layout

```
.
├── cmd/server/               # entry point; also hosts the `admin` operator subcommands
├── internal/
│   ├── app/                  # composition root
│   │   ├── container/        # dependency-injection container
│   │   ├── httpserver/       # Gin router, route groups, middleware
│   │   └── jobs/             # asynq worker service and consumers
│   ├── bootstrap/            # per-module wiring (adapters.go + wiring.go)
│   ├── modules/              # 35 business modules — one vertical slice per domain
│   ├── workflows/            # use cases that span several modules
│   ├── platform/             # framework-facing infrastructure
│   │   ├── database/gormdb/  # connection, auto-migration
│   │   └── http/             # response envelope, Gin helpers
│   ├── shared/               # dependency-free primitives (money, jsonmap, serial …)
│   ├── authz/                # Casbin RBAC: policy model, built-in role seeds
│   ├── web/                  # SPA embedding and mounting (build-tag gated)
│   ├── architecture/         # architecture guard tests — no production code
│   ├── cache/ config/ constants/ crypto/ i18n/ logger/ queue/ version/
│   └── admincmd/ htmltext/ persistence/ telegramidentity/ testkit/ upstream/
├── frontend/
│   ├── admin/                # admin panel SPA        (dev :5174)
│   └── user/                 # customer storefront SPA (dev :5173)
├── config.yml.example
├── Dockerfile                # single full-stack image
└── .goreleaser.yaml
```

Runtime directories created on first start: `db/` (SQLite), `uploads/`, `logs/`.

## Architecture

A modular monolith. Each domain under `internal/modules/<name>/` is a vertical slice with its
own layers:

| Layer | Holds | May import |
| --- | --- | --- |
| `domain/` | entities, value objects, business invariants | nothing from the other layers |
| `application/` | use cases, port interfaces | `domain`, `contract` |
| `infrastructure/` | GORM stores, gateways, queue adapters | `domain`, `application` ports |
| `transport/` | HTTP handlers, presenters | `application` contracts |
| `contract/` | port interfaces the application layer depends on, and the module's public surface for other modules | — |

**These rules are enforced by tests, not convention.** `internal/architecture/` parses every
import in the tree and fails the build on violations. The main ones:

- `domain` must not reach into `application`, `infrastructure`, or `transport`
- `application` must not import Gin or asynq — no transport libraries in use cases
- only a module's `infrastructure/gormstore` adapter may import GORM
- `transport` depends on application contracts, never on concrete stores
- `internal/shared` stays free of modules, GORM, Gin, and asynq
- `internal/platform` must not depend on business modules

Run them with the rest of the suite: `go test ./internal/architecture/...`

Modules never import each other's internals — they talk through `contract/`, and the wiring
lives in `internal/bootstrap/<module>/`.

### RBAC

Every `/api/v1/admin/...` route passes through Casbin. The permission catalog is generated
from the live route table, but the **built-in roles are hand-maintained** in
`internal/authz/bootstrap.go`. Adding an admin route without adding it to a role seed leaves
that route reachable only by the super admin. `internal/app/httpserver/rbac_coverage_test.go`
checks that every registered route is covered.

## Build Tags

| Tag | Effect |
| --- | --- |
| *(none)* | API only. No SPAs mounted — the default for local development. |
| `fullstack` | Embeds `internal/web/dist/{admin,user}` into the binary via `go:embed`. |
| `release` | Production behavior for outbound URL building. |

`go:embed all:dist/admin all:dist/user` requires **both** directories to exist, so a
`fullstack` build fails outright if the frontends were not built first. A plain `go build`
does not compile `embed_fullstack.go` — after touching `internal/web/`, verify with
`go build -tags release,fullstack ./cmd/server`.

## Run Modes

```bash
./dujiao-next                 # all    — HTTP server + background worker (default)
./dujiao-next -mode api       # HTTP server only
./dujiao-next -mode worker    # background worker only
```

Operator subcommands ship in the same binary, so a container needs no extra tooling:

```bash
./dujiao-next admin list-admins
./dujiao-next admin reset-password
./dujiao-next admin reset-2fa
```

## Frontend Notes

Two independent SPAs, both built with Vite and embedded at release time.

**Mount points.** The storefront is served at `/`; the admin panel at `web.admin_path`
(default `/admin`). `/api`, `/uploads`, and `/health` are reserved prefixes — an unmatched
path under them returns 404 instead of falling through to the SPA shell. Adding a new
top-level backend prefix means updating `reservedPaths` in `internal/web/handler.go`.

**The admin base path is resolved at runtime, not at build time.** Since `web.admin_path` is
configurable, `pnpm run build:fullstack` only injects a `<base href="__DJ_ADMIN_BASE__/">`
placeholder, which the server rewrites on startup. Consequences for admin code:

- native `<a href>` and `window.location` navigation must go through `adminUrl()` in
  `src/utils/adminBase.ts`
- `<router-link :to>` and `router.push()` must **not** — vue-router already carries the base,
  and prefixing again yields `/admin/admin/...`

**Storefront templates.** The customer frontend ships more than one look, selected by the
`storefront_template` site setting (`classic`, `vault`). Template pages live in
`src/templates/<name>/` and fall back to `src/views/` when a page has no template-specific
version; see `src/templates/registry.ts`. Append `?template=vault` to preview one locally.

**i18n.** Both frontends and all API responses are localized — Simplified Chinese, Traditional
Chinese, and English. Do not hard-code user-facing strings on either side.

## Quick Start (Deploy)

### Official one-click installer (Ubuntu / Debian)

On a fresh Ubuntu 22.04+ or Debian 12+ server, download and run the official
interactive installer:

```bash
curl -fsSL https://raw.githubusercontent.com/dujiao-next/dujiao-next/main/scripts/dujiao-next-manager.sh \
  -o /tmp/dujiao-next-manager.sh
sudo bash /tmp/dujiao-next-manager.sh install
```

The installer deploys the release binary with systemd, an isolated local Redis,
Nginx, SQLite, and a Let's Encrypt certificate. After installation, reopen the
management menu with:

```bash
sudo dujiao-next-manager
```

Common automation-friendly commands are also available:

```bash
sudo dujiao-next-manager status
sudo dujiao-next-manager logs app
sudo dujiao-next-manager restart
sudo dujiao-next-manager configure-domain
sudo dujiao-next-manager configure-admin-path
sudo dujiao-next-manager renew-cert
sudo dujiao-next-manager admin-reset-password
sudo dujiao-next-manager admin-reset-2fa
sudo dujiao-next-manager uninstall
```

The first release supports a single non-wildcard domain on Ubuntu/Debian only.
It does not adopt an existing manual installation. If SMTP is skipped, configure
it in the admin panel before enabling email-verification registration. Application
data lives in `/opt/dujiao-next`; installer state is stored in
`/etc/dujiao-next/install-state.json`. TLS failures leave only the ACME challenge
endpoint enabled, and `install` can be rerun after DNS or firewall repair. Safe
uninstall creates and verifies a `0600` recovery archive under
`/var/backups/dujiao-next` before deleting managed data.

### Manual binary installation

Download the latest `dujiao-next_*.tar.gz` from [Releases](https://github.com/dujiao-next/dujiao-next/releases):

```bash
tar -xzf dujiao-next_*.tar.gz
cp config.yml.example config.yml
# edit config.yml: set jwt.secret, user_jwt.secret, and web.admin_path
./dujiao-next
```

Full instructions: https://dujiao-next.com/deploy/

Or with Docker:

```bash
docker run -d -p 8080:8080 -v $PWD/config.yml:/app/config.yml:ro dujiaonext/dujiao-next:latest
```

## Quick Start (Develop)

Run the backend and the two frontends separately for hot reload:

```bash
go mod tidy && go run ./cmd/server   # :8080 — API only, no SPAs mounted

cd frontend/user  && pnpm install && pnpm run dev   # :5173
cd frontend/admin && pnpm install && pnpm run dev   # :5174
```

Both dev servers proxy `/api`, `/uploads`, `/sitemap.xml`, and `/robots.txt` to
`localhost:8080`. In production everything is same-origin, so these proxies are a
development-only concern.

> Use `pnpm` via corepack. `pnpm --dir X` does not read the `packageManager` field of the
> target directory and will pick the wrong version — `cd` into the package first.

## Building the Full-Stack Binary

```bash
goreleaser build --snapshot --single-target --clean
```

This builds both frontends, embeds them, and compiles with `-tags fullstack` — the same path
CI uses for releases. The manual equivalent:

```bash
(cd frontend/admin && pnpm run build:fullstack)   # injects the <base> placeholder
(cd frontend/user  && pnpm run build)
rm -rf internal/web/dist && mkdir -p internal/web/dist
cp -r frontend/admin/dist internal/web/dist/admin
cp -r frontend/user/dist  internal/web/dist/user
go build -tags release,fullstack -o dujiao-next ./cmd/server
```

Note that admin uses `build:fullstack`, not `build`. Plain `build` produces a bundle pinned to
`/`, which silently breaks a custom `web.admin_path`.

## Testing

```bash
go test ./...                              # full suite
go test ./internal/architecture/...        # dependency and layering guards
go test ./internal/modules/order/...       # one module

cd frontend/user  && pnpm run build        # includes vue-tsc type checking
cd frontend/admin && pnpm run build
```

Health check endpoint: `GET /health`

## Notes on Data Access

SQLite runs with `MaxOpenConns=1`. A store opens a transaction through
`WithinTransaction(func(tx contract.Transaction) error)`, and every query inside the closure
must go through that `tx` handle or a store bound to it via `WithTx(tx)`. Reaching for the
global DB handle instead asks for a second connection that will never be granted, deadlocking
the process — including indirectly, by calling a service that queries on its own. Read any
settings you need *before* opening the transaction, and keep outbound HTTP calls (payment
gateways and the like) outside it.

## Online Documentation

- https://dujiao-next.com
