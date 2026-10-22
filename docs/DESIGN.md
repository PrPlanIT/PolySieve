# PolySieve — Design

## What it is

PolySieve derives Kubernetes **network authorization policy** from a GitOps repository's
own manifests — the *source of truth* — rather than from the live cluster. Given a repo of
Gateway API `HTTPRoute`s and `Service`s, it computes the least-privilege ingress each
namespace actually needs and emits the corresponding policy objects (Cilium
`CiliumClusterwideNetworkPolicy`, Istio `AuthorizationPolicy`, …) in the shapes a given
cluster expects.

The name: a **sieve** is a filter that decides what passes. A network policy *is* a sieve
over traffic (the allow-list is what gets through), and PolySieve sieves the repo's declared
routes down to the minimal set of allowed paths. "Poly" ⇒ *policy* and *poly-backend*.

## Why repo-driven, not cluster-driven

The tool this replaces reads the **live cluster** (`kubectl get httproutes -A`). That inverts
GitOps layering — the committed policy becomes a function of the running cluster instead of
the repo. Costs of that model:

- **Non-deterministic** — same repo, different cluster states → different output. Not reproducible.
- **Needs a live cluster + kubeconfig** to regenerate; can't run in CI or offline.
- **Two-phase gap** — you can't generate policy for an app that isn't deployed yet, so every
  new ingress app has a deploy-then-catch-up window where its traffic is denied.
- **Bidirectional drift** — anything present in the cluster but not the repo silently tracks
  the cluster.

Repo-driven fixes all four: deterministic, reproducible, CI-runnable with no cluster, and a
new app + its policy land in **one commit, no gap**. It also makes the pre-commit "regenerate
policy when apps change" guard coherent — the source *is* the apps.

## Architecture — two layers, one seam

```
repo ──▶ Discovery ──▶ RouteGraph ──▶ Render(profile) ──▶ policy files
        (generic)      (typed artifact)  (project-specific)
```

- **Discovery (generic).** `kustomize build` the overlays, walk the Gateway API graph:
  `HTTPRoute.backendRefs → Service → targetPort` (resolving named ports via the Service /
  container / EndpointSlice defs already in the repo), grouped by namespace and by the
  gateway each route's `parentRefs` targets. Output is a typed **RouteGraph** — no policy
  shape, no cluster labels. This layer is reusable across any Gateway-API GitOps repo.

- **Render (project-specific, profile-driven).** A **profile** maps the RouteGraph to a
  cluster's concrete policy: which CRDs to emit, the label/identity model (e.g. the dungeon's
  `policy.prplanit.com/*` Universal Capability Model), the gateway → SA-principal mapping,
  and the on-disk file layout. The dungeon cluster is **profile #1**.

The seam is deliberate: the same discipline as separating *discovery* from *presentation*.
If a second GitOps repo ever needs this, the Discovery layer is the promotable kernel (it
could live in StageFreight, which already renders the repo for gitops validation); Render
stays a per-project concern. We do **not** pre-build that generality — one consumer today.

## Honesty gate — never emit a policy smaller than reality

Repo-driven derivation has a bounded blind spot: Helm-rendered and operator-rendered pods do
not exist in `kustomize build` output. A policy derived from what the repo cannot see would
silently drop ports that real traffic uses. The core rule: **under partial coverage, preserve;
never prune against what cannot be verified.**

Discovery carries two coverage signals:

- **Workload visibility** — `ServiceHasVisibleWorkload`: does any repo workload's pod template
  satisfy the Service's selector? A backend with no visible workload is **blind**: its
  non-routed ports (e.g. health-check ports) are unknown.
- **Port resolution** — each backend carries a resolved-vs-fallback bit. A Service absent from
  the repo, or a named `targetPort` that resolves neither through an `EndpointSlice` nor through
  the backing workload's named container port, yields a guessed port. Such a backend is
  **route-blind**. Named ports on raw (non-Helm) workloads resolve from the pod template, so
  they need no runtime `EndpointSlice`.

When any backend a policy file depends on is blind or route-blind, the profile folds the
**committed** file's existing ports into its output — additive only — and does not contribute
the guessed port. Every run **reports** the blind and route-blind backends by name on stderr.
When nothing is blind, the derived set is authoritative and pruning stale ports is safe.

Because `kustomize build` is the only reader, apps not referenced by an active kustomization
are invisible by construction and need no special-casing.

## Source layering — repo, then live cluster, then Helm render

The repo is always read. Two optional, best-effort layers can resolve blind backends:

```
repo (always) ──▶ --cluster (live state) ──▶ --helm (rendered charts) ──▶ Discovery
```

- **`--cluster`** (`--kubectl` selects the binary) runs `kubectl get` for Deployments,
  StatefulSets, DaemonSets, Services and EndpointSlices across all namespaces. HTTPRoutes are
  deliberately **not** fetched: routing intent stays repo-authoritative; the cluster only
  resolves ports and the pods behind them.
- **`--helm`** (`--helm-bin` selects the binary) resolves each Flux `HelmRelease`'s chart from
  its pinned source (`HelmRepository` classic or OCI, or an `OCIRepository` via
  `chartRef`/`sourceRef`), runs `helm template` with the inline `spec.values`, and feeds the
  rendered workloads into discovery. `valuesFrom` is ignored. `GitRepository` chart sources
  are unsupported. This tier reaches a chart registry, so it trades hermeticity for coverage.

Precedence is by append order under discovery's first-occurrence-wins indexing: repo objects
are parsed first, then cluster objects, then rendered charts. Repo objects always win; live
state wins over a render; later layers only fill gaps.

Each layer degrades safely. An unreachable cluster is a warning and the run falls back to the
repo alone. A HelmRelease that will not render is skipped and reported per release, never a
hard error. Whatever stays unresolved remains blind, and the honesty gate preserves it. Absence
of a layer can make output less precise; it never makes it smaller than reality.

Operator-managed workloads that are not charts stay blind in every layer except `--cluster`,
and are preserved and reported when it is off.

The container image bundles `kustomize`, `kubectl` and `helm` so all three layers run from it.

## MVP — reproduce the dungeon generators, byte-for-byte

Scope of v0: replace `dungeon/hack/gen-cilium-backend-ports.sh` and
`gen-gateway-ingress-policies.sh`.

1. **Discovery** over the dungeon `fluxcd/` overlays → RouteGraph.
2. **Profile: dungeon** → emit:
   - the Cilium `ccnp-contract-ingress-backend.yaml` backend-port contract, and
   - the per-namespace `allow-gateway-ingress-<gateway>.yaml` Istio `AuthorizationPolicy` set.
3. **Acceptance gate:** golden diff — PolySieve's output must be **byte-identical** to the
   files currently committed in the dungeon for the current app set. This proves two things
   at once: the tool is correct, *and* the profile/config model is expressive enough to be
   real (if the dungeon's shape can't be expressed cleanly as a profile, that's the signal
   the generalization was premature).

## Non-goals (for now)

Multi-cluster profile library; additional CRD backends (plain `NetworkPolicy`, Gateway-API
`*Policy`); egress derivation; live-cluster reconciliation/diffing. All additive once the
byte-identical MVP lands.

## Conventions

Go, standalone. AGPL-3.0. Structure and tooling salvaged from the sibling repos (StageFreight
/ HASteward): `cmd/` + `internal/`, multi-stage `Dockerfile`, StageFreight-managed CI, mkdocs
docs. Commits signed as SoFMeRight; no third-party attribution.
