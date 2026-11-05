# The architecture: pack, pack-operator, pack-ui

Three processes, each with one job:

- **pack** — the deploy engine. A CLI that knows how to plan a release and apply it (the saga, the journal,
  the state store), how to render values against a pack, how to talk to the targets. It holds no users and
  no API: everything it says is a `--json` document on stdout (`pkg/api`, К1), everything it needs is the
  release's directory and the environment.
- **pack-operator** — the fleet's head. Watches the git repositories of the projects, queues the deploy
  requests, has them approved, runs them as jobs on its workers (each job is a `pack` process), and keeps
  the rights (casbin), the sessions, the secrets and the audit. Its API is REST + SSE (К4); a model
  reaches it through its MCP server.
- **pack-ui** — the person's window. A single binary: the SPA (embedded) and its backend, which edits the
  repositories as the user (go-git, no git binary), proxies the operator's API under the session's
  identity, and serves the values, environment, authoring and diagnosis work. It holds no rights of its
  own — the operator's policy decides every call.

## The whole picture

```mermaid
flowchart LR
    subgraph person["the person"]
        browser["browser — the SPA"]
        model["a model<br/>(MCP client)"]
    end

    subgraph ui["pack-ui — one binary"]
        spa["web/dist<br/>(embedded SPA)"]
        backend["backend: edit via go-git,<br/>values, author, diagnose"]
        uimcp["MCP server<br/>questions · answer · diagnose · author_*"]
    end

    subgraph op["pack-operator"]
        api["API: REST + SSE<br/>authn · authz casbin"]
        syncer["sync: fetch + read the repos"]
        policy["policy: requests,<br/>approvals, changes"]
        bus[("Bus + DB:<br/>postgres or sqlite")]
        secrets[("secrets:<br/>OpenBao or the DB")]
        worker["workers: native · docker · k8s"]
        opmcp["MCP server<br/>next · plan · request · job …"]
    end

    packbin["pack — the CLI<br/>plan · apply · saga · journal"]
    git[("git repositories:<br/>releases, packs, change requests")]
    targets["targets: docker, ansible, k8s<br/>— what the releases install on"]
    registry[("OCI registry:<br/>public packs")]

    browser -->|"HTTPS + session cookie,<br/>CSRF token"| backend
    backend --> spa
    backend -->|"commits as the user,<br/>go-git"| git
    backend -->|"REST, service token<br/>+ X-Pack-User"| api
    model --> uimcp --> backend
    model --> opmcp --> api

    syncer <-->|"fetch, working copies"| git
    api --> policy --> bus
    policy -->|"a job per request"| worker
    worker -->|"runs"| packbin
    packbin --> targets
    packbin -->|"pull packs"| registry
    api --> secrets
    syncer --> secrets
```

## Who holds what

|                              | pack                         | pack-operator                                   | pack-ui                                   |
|------------------------------|------------------------------|-------------------------------------------------|-------------------------------------------|
| the deploy itself            | plan, apply, saga, journal   | —                                               | —                                         |
| the state of an installation | the state store (file/DB)    | reads it (К2)                                   | —                                         |
| the repositories             | —                            | fetches, locks, merges (go-git)                 | edits and commits as the user (go-git)    |
| the queue                    | —                            | requests → approvals → jobs (Bus)               | asks for them through the API             |
| the rights                   | —                            | casbin RBAC with domains (К7)                   | none — every call goes to the operator    |
| the users                    | —                            | local + OIDC, sessions, API tokens              | own sessions; the user forwarded by token |
| the secrets                  | reads via the environment    | OpenBao or the encrypted DB store               | names and keys only — never a value       |
| the models                   | —                            | MCP: run a release (no approval tool)           | MCP: values and authoring (no approval)   |

## A deploy, end to end

The operator's flow (К3): a request is idempotent, a plan waits for a person's approval where the policy
says so, a job is one `pack` process on a worker, the events stream back into the Bus.

```mermaid
sequenceDiagram
    actor u as the user
    participant ui as pack-ui
    participant op as pack-operator
    participant w as a worker
    participant p as pack
    participant t as a target

    u->>ui: values saved / apply
    ui->>op: POST /api/sync (the commit landed)
    op->>op: fetch — the release's head moves
    u->>op: POST /api/releases/{rel}/requests {action: apply, sha}
    op->>op: plan — the request waits (run.approve)
    u->>op: POST /api/requests/{id}/approve {digest, sha}
    op->>bus: a job queued
    bus->>w: the job (labels match)
    w->>p: pack -C <release> apply --approve <digest> --stream
    p->>t: install / upgrade the keys
    p-->>w: events (pkg/api.Event)
    w-->>bus: the journal, the result
    u->>op: GET /api/jobs/{id}/events (SSE)
    op-->>u: the saga as it runs, then applied
```

## Authentication (stage 3, К7)

```mermaid
flowchart TD
    subgraph login["who are you"]
        local["auth: local<br/>bcrypt password"] --> session
        oidc["auth: oidc<br/>the provider's code flow;<br/>groups → roles"] --> session
        session["an httpOnly cookie<br/>+ X-CSRF-Token on mutations"]
        token["CI: a bearer token,<br/>no cookie, no CSRF"]
    end

    subgraph uiflow["pack-ui → the operator"]
        session --> fwd["the user as X-Pack-User<br/>+ pack-ui's service token"]
        token --> fwd
        fwd --> imp{"the token's user<br/>an admin of the domain?"}
        imp -->|"yes"| act["the operator acts as the user"]
        imp -->|"no"| self["the operator acts as the token's user"]
    end

    act --> casbin["casbin: a role of the user's in the domain,<br/>object + action"]
    self --> casbin
    casbin --> decide{"allow?"}
```

What is a person's stays a person's: neither MCP server has an approve or a merge tool; a secret's value
is never read over any API; with `auth: none` the dev user is an admin everywhere — a mode that is only
ever switched on explicitly and is announced in the log at start.
