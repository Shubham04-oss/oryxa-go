# Auth service

The auth service uses Amazon Cognito as its OIDC issuer and currently enables Google as its only interactive identity provider. It validates Cognito access tokens locally and publishes versioned domain events to Amazon EventBridge.

## Provision Cognito

Copy `.env.example` to an ignored `.env`, provide a globally unique Cognito domain prefix, Google OAuth credentials, and the application's exact callback/logout URLs. Load those values into the shell, authenticate the AWS SDK default credential chain, and run:

```sh
go run ./auth/infra/cognito/cmd/provision
```

The command is repeatable. It creates or updates an Essentials-tier user pool in `ap-south-1`, enables deletion protection, registers Google, creates a public authorization-code app client with refresh-token rotation, and activates Cognito's managed-login defaults.

In Google Cloud Console, the authorized redirect URI must be the `google_redirect_uri` printed by the provisioner:

```text
https://<prefix>.auth.ap-south-1.amazoncognito.com/oauth2/idpresponse
```

The Google OAuth client ID and secret are provisioning inputs only. Do not pass them to the running auth container.

## Run auth

Set the runtime values printed by the provisioner, then build from the repository root:

```sh
docker build -f auth/Dockerfile -t oryxa-auth:dev .
docker run --rm -p 127.0.0.1:8080:8080 --env-file auth/.env oryxa-auth:dev
```

The service exposes `GET /healthz`, public OIDC discovery metadata at `GET /v1/auth/config`, and authenticated claims at `GET /v1/auth/me`.

## EventBridge contract

Auth publishes to `EVENT_BUS_NAME` with source `oryxa.auth`. The first email-facing contract is `auth.user.registered.v1`; consumers should match EventBridge metadata and deserialize `detail.data` using `contracts/events/auth/v1.UserRegistered`.

```json
{
  "source": ["oryxa.auth"],
  "detail-type": ["auth.user.registered.v1"]
}
```

No access token, refresh token, provider secret, or AWS credential is included in an event.

