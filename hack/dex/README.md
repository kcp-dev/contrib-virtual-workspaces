# A throwaway identity provider

[Dex](https://dexidp.io) in a container, plus the
`AuthenticationConfiguration` that kcp and every virtual workspace in this
repository consume. It lives at the repository root because all three
virtual workspaces authenticate callers the same way — by being pointed at
the same configuration file kcp uses — so an identity provider belongs to
none of them in particular.

```sh
hack/dex/dex.sh up            # start it, write the config into .dex/
hack/dex/dex.sh token alice   # print an ID token
hack/dex/dex.sh env           # the variables the e2e harnesses read
hack/dex/dex.sh down
```

## What it gives you

Three users, whose identities are their email addresses:

| Dex user | Email | Becomes |
|---|---|---|
| `alice` | `alice@team-a.example.com` | username `alice`, group `team-a` |
| `bob` | `bob@team-b.example.com` | username `bob`, group `team-b` |
| `mallory` | `mallory@strangers.example.com` | username `mallory`, group `strangers` |

They are deliberately the same users and groups the certificate-based
harnesses mint, so a test can swap one credential for the other without
touching a single `Membership` or RoleBinding.

## Two things worth knowing before you copy this

**Dex's password database carries no groups.** That is a property of the
connector, not a misconfiguration: `staticPasswords` has no groups field,
and asking for `scope=groups` changes nothing. Groups here are derived from
the email domain by a CEL claim mapping in the
`AuthenticationConfiguration`:

```yaml
claimMappings:
  username:
    expression: "claims.email.split('@')[0]"
  groups:
    expression: "[claims.email.split('@')[1].split('.')[0]]"
```

Deriving identity attributes is what structured authentication is for, so
this is a demonstration of the real mechanism rather than a workaround —
but if you need groups that come from the provider itself, you need a
connector that has them (LDAP, GitHub, OIDC upstream).

**Kubernetes will not derive a username from an unverified email.** Using
`claims.email` without also asserting `claims.email_verified` is rejected
at startup, not at request time:

> claims.email_verified must be used in claimMappings.username.expression
> or claimValidationRules[*].expression when claims.email is used

which is why the generated config carries a `claimValidationRules` entry.

## Using it from a component

Give the same file to both sides. That is the whole point: a username the
virtual workspace computes and a username kcp computes have to be the same
string, or a `Membership` written against one means nothing to the other.

```sh
eval "$(hack/dex/dex.sh env)"

kcp start --authentication-config "$DEX_AUTHENTICATION_CONFIG" ...
tenancy-vw virtualworkspace --authentication-config "$DEX_AUTHENTICATION_CONFIG" ...
```

A virtual workspace can hold this alongside `--client-ca-file`; they are
separate authenticators and both apply, which is how the tenancy e2e proves
the same identity twice.

Tests have no browser, so tokens come from the resource-owner password
grant — `oauth2.passwordConnector: local` with a public client. Do not copy
that part into a deployment.
