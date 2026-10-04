# Releasing

Every change that reaches a deployment gets a version number and a line in
[CHANGELOG.md](../CHANGELOG.md). The number is how someone running Syslogc
tells whether the fix they were waiting for is in the build in front of them;
the changelog is how they find out what else came with it.

## Which number to bump

`MAJOR.MINOR.PATCH`.

| Bump      | When                                                                 |
| --------- | -------------------------------------------------------------------- |
| **Patch** | A fix. Nothing new, nothing to do on upgrade.                        |
| **Minor** | A feature, a new endpoint, a new configuration key with a default.   |
| **Major** | An upgrade needs someone to act: a key renamed or removed, an endpoint withdrawn, a migration that is not automatic. |

A configuration key with a sensible default is a minor bump, not a major one
— nobody has to do anything. A key whose default changes behaviour on an
existing deployment is a major bump even if the key itself is old.

## Cutting a release

```sh
./scripts/release.sh 1.1.0
```

It refuses to run on a dirty tree or without a `## 1.1.0` section in the
changelog, then tags `v1.1.0` and pushes the tag. Pushing the tag is what
starts the release: `.github/workflows/release.yml` builds `linux/amd64` and
`linux/arm64` images, signs them keylessly with cosign, attaches an SBOM and
provenance, and creates the GitHub release using the changelog section as its
notes.

Write the changelog section first, as part of the work, not afterwards from
the commit log. The commits say what was changed; the changelog has to say
what is different for whoever is running it.

## What the version is stamped into

- The image's `VERSION` and `COMMIT` build arguments, which become
  `main.version` and `main.commit` in the binary.
- `syslogc version`.
- The **Version** field on the System page, and the `version` label on
  `/api/v1/system/health` and the Prometheus build-info metric.

`deploy.sh` stamps a local build with `git describe`, so a deployment built
from a checkout reports something like `v1.1.0-3-gab12cd3` — the release it
is past, how far past, and the commit. `--pull` runs the published `latest`
instead.

## Upgrading a deployment

```sh
./deploy.sh --upgrade
```

Read the changelog's sections between the running version (System page) and
the new one first. A major bump means there is something in there to do
before restarting.
