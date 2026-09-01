# PolySieve

Derive Kubernetes network authorization policy from a GitOps repository's own manifests —
the *source of truth* — rather than from a live cluster.

<!-- sf:project:start -->
[![GitHub](https://img.shields.io/badge/GitHub-mirror-181717?logo=github)](https://github.com/PrPlanIT/PolySieve) [![GitLab](https://img.shields.io/badge/GitLab-source-FC6D26?logo=gitlab)](https://gitlab.prplanit.com/PrPlanIT/PolySieve) [![license](https://raw.githubusercontent.com/PrPlanIT/PolySieve/main/.stagefreight/scribe/license.svg)](https://github.com/PrPlanIT/PolySieve/blob/main/LICENSE) [![Open Issues](https://img.shields.io/github/issues/PrPlanIT/PolySieve)](https://github.com/PrPlanIT/PolySieve/issues) [![Open PRs](https://img.shields.io/github/issues-pr/PrPlanIT/PolySieve)](https://github.com/PrPlanIT/PolySieve/pulls) [![Contributors](https://img.shields.io/github/contributors/PrPlanIT/PolySieve)](https://github.com/PrPlanIT/PolySieve/graphs/contributors) [![donate](https://img.shields.io/badge/donate-FF5E5B?logo=ko-fi&logoColor=white)](https://ko-fi.com/T6T41IT163) [![sponsor](https://img.shields.io/badge/sponsor-EA4AAA?logo=githubsponsors&logoColor=white)](https://github.com/sponsors/PrPlanIT)
<!-- sf:project:end -->
<!-- sf:badges:start -->
[![release](https://raw.githubusercontent.com/PrPlanIT/PolySieve/main/.stagefreight/scribe/release.svg)](https://github.com/PrPlanIT/PolySieve/releases) [![build](https://raw.githubusercontent.com/PrPlanIT/PolySieve/main/.stagefreight/scribe/build.svg)](https://gitlab.prplanit.com/PrPlanIT/PolySieve/-/pipelines) [![Go Version](https://img.shields.io/github/go-mod/go-version/PrPlanIT/PolySieve?logo=go)](https://github.com/PrPlanIT/PolySieve) [![Go Reference](https://pkg.go.dev/badge/github.com/PrPlanIT/PolySieve.svg)](https://pkg.go.dev/github.com/PrPlanIT/PolySieve) [![Last Commit](https://img.shields.io/github/last-commit/PrPlanIT/PolySieve)](https://github.com/PrPlanIT/PolySieve/commits) [![StageFreight](https://img.shields.io/badge/StageFreight-0.10.0--dev+e3ee67e-310937?logo=readthedocs&logoColor=white)](https://stagefreight.prplanit.com)
<!-- sf:badges:end -->
<!-- sf:image:start -->
[![GHCR](https://img.shields.io/badge/GHCR-prplanit%2Fpolysieve-181717?logo=github&logoColor=white)](https://github.com/PrPlanIT/PolySieve/pkgs/container/polysieve) [![Docker](https://img.shields.io/badge/Docker-prplanit%2Fpolysieve-2496ED?logo=docker&logoColor=white)](https://hub.docker.com/r/prplanit/polysieve) [![pulls](https://raw.githubusercontent.com/PrPlanIT/PolySieve/main/.stagefreight/scribe/pulls.svg)](https://hub.docker.com/r/prplanit/polysieve) [![Harbor](https://img.shields.io/badge/Harbor-prplanit%2Fpolysieve-60b932)](https://cr.pcfae.com/harbor/projects)

[![latest](https://raw.githubusercontent.com/PrPlanIT/PolySieve/main/.stagefreight/scribe/release-latest.svg)](https://github.com/PrPlanIT/PolySieve/pkgs/container/polysieve) ![updated](https://raw.githubusercontent.com/PrPlanIT/PolySieve/main/.stagefreight/scribe/release-updated.svg) [![size](https://raw.githubusercontent.com/PrPlanIT/PolySieve/main/.stagefreight/scribe/release-size.svg)](https://github.com/PrPlanIT/PolySieve/pkgs/container/polysieve) [![latest-dev](https://raw.githubusercontent.com/PrPlanIT/PolySieve/main/.stagefreight/scribe/dev-latest.svg)](https://github.com/PrPlanIT/PolySieve/pkgs/container/polysieve) ![updated](https://raw.githubusercontent.com/PrPlanIT/PolySieve/main/.stagefreight/scribe/dev-updated.svg) [![size](https://raw.githubusercontent.com/PrPlanIT/PolySieve/main/.stagefreight/scribe/dev-size.svg)](https://github.com/PrPlanIT/PolySieve/pkgs/container/polysieve)
<!-- sf:image:end -->

Given a repo of Gateway API `HTTPRoute`s and `Service`s, PolySieve resolves the least-privilege
ingress each namespace actually needs and emits the corresponding policy objects (Cilium
`CiliumClusterwideNetworkPolicy`, Istio `AuthorizationPolicy`, …) in the shape a given cluster
expects, selected by a **profile**.

A **sieve** is a filter that decides what passes; a network policy is a sieve over traffic
(the allow-list is what gets through). "Poly" ⇒ *policy* and *poly-backend*.

## Why repo-driven

Deriving policy from the live cluster inverts GitOps layering — the committed policy becomes a
function of the running cluster. That is non-deterministic, needs cluster access, and can't
generate policy for an app that isn't deployed yet (a deploy-then-catch-up gap). Reading the
repo instead is deterministic, reproducible, CI-runnable with no cluster, and lets a new app
and its policy land in **one commit, no gap**.

## Usage

```
polysieve generate --repo <gitops-repo> --profile dungeon   # write derived policy files
polysieve check    --repo <gitops-repo> --profile dungeon   # diff vs committed (nonzero on drift)
```

`--root` overrides the profile's kustomize build directories; `--kustomize` sets the binary.

## Architecture

`repo → Discovery (generic route-graph) → Render (profile) → policy files`

Discovery renders the repo and walks the Gateway API graph
(`HTTPRoute.backendRefs → Service → targetPort`) into a typed `RouteGraph`. A profile maps that
graph to a cluster's concrete policy. See [docs/DESIGN.md](docs/DESIGN.md).

## License

AGPL-3.0-only.
