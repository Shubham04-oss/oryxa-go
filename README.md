# Oryxa

Oryxa is an AWS-backed Go monorepo. It starts with a single Go module and will grow as independently deployable services are introduced.

## Service layout

Each service owns a lowercase, hyphen-free directory at the repository root. Known examples are `/auth` and `/email`; create additional service directories only when their domain and deployment boundary are clear.

```text
/
├── auth/                  # Identity and access service
├── email/                 # Email delivery service
├── integrations/          # Third-party connection and automation control plane
└── <service>/             # One directory per future service
```

Every service follows the same internal shape once it is created:

```text
/<service>
├── cmd/<service>/         # Executable entrypoint
├── internal/              # Service-private Go packages
├── infra/                 # AWS resources owned by this service
├── migrations/            # Optional, service-owned data migrations
├── README.md              # Service contract and local run instructions
└── go.mod                 # Only if the service needs an independent module
```

Keep service boundaries explicit: a service owns its API, data, AWS resources, and deployment configuration. Avoid a generic `common` directory. Shared Go packages belong in `/pkg` only after they have a stable, genuinely cross-service contract.

`/auth` owns Cognito-based Google and email/password sign-in, PostgreSQL-backed verification/reset actions and users/organizations/memberships, and transactional EventBridge publishing. `/email` remains a delivery boundary: it consumes auth delivery-requested events but never performs auth transitions. `/integrations` contains the Go control plane, webhook worker, and lightweight TypeScript connector runtime built from approved Activepieces pieces. Successful connections, trigger registrations, execution claims, and domain outbox events are durable PostgreSQL records; other services receive only the credential-free connection catalog. The full Activepieces platform is not deployed.
