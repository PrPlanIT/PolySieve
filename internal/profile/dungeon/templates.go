package dungeon

// Skeletons for the derived policy files. The cilium skeletons end exactly at their first
// `        - ports:` line; the generated port block is appended after. The istio template is a
// fmt format string whose args are (gatewayClass, principalNamespace, principalName,
// inlinePortList). The contract-ingress-backend policy's fromEndpoints is generated from the
// discovered ingress gateways (see ciliumContractSources), so its skeleton is split into a
// header (up to `    - fromEndpoints:`) and a toPorts tail.

const ciliumContractHeader = `# DERIVED: resolved pod targetPorts from HTTPRoute backendRefs.
# Inputs: HTTPRoutes → Services (targetPort resolution) → EndpointSlices (named port resolution)
# Source of truth: hack/gen-cilium-backend-ports.sh
# Regenerate; do not hand-edit.
apiVersion: "cilium.io/v2"
kind: CiliumClusterwideNetworkPolicy
metadata:
  name: contract-ingress-backend
spec:
  description: >-
    Contract: ingress gateway pods may reach backend application pods.
    Source is restricted to the discovered ingress-gateway namespaces AND
    must carry the ingress-gateway client-class label. Destination pods must
    carry policy.prplanit.com/ingress: "true".
  enableDefaultDeny:
    egress: false
    ingress: false
  endpointSelector:
    matchLabels:
      policy.prplanit.com/ingress: "true"
  ingress:
    - fromEndpoints:
`

const ciliumContractToPorts = `      toPorts:
        - ports:
`

const ciliumGatusSkeleton = `# DERIVED: resolved pod targetPorts from Gatus-annotated services and probe-labelled pods.
# Inputs: Gatus annotations → Services (targetPort resolution) → probe-labelled pod containerPorts
# Source of truth: hack/gen-cilium-backend-ports.sh
# Regenerate; do not hand-edit.
apiVersion: "cilium.io/v2"
kind: CiliumClusterwideNetworkPolicy
metadata:
  name: allow-gatus-healthcheck
spec:
  description: >-
    Allows Gatus health checker to reach probe-labelled endpoints.
    Gatus runs in gossip-stone namespace and probes service health
    across the cluster on application and infrastructure ports.
  enableDefaultDeny:
    egress: false
    ingress: false
  endpointSelector:
    matchLabels:
      policy.prplanit.com/probe: "true"
  ingress:
    - fromEndpoints:
        - matchLabels:
            k8s:io.kubernetes.pod.namespace: gossip-stone
            app.kubernetes.io/name: gatus
      toPorts:
        - ports:
`

const istioTemplate = `# DERIVED: pod targetPorts from HTTPRoute backendRefs targeting this namespace.
# Inputs: HTTPRoutes → Services (targetPort resolution) → EndpointSlices (named port resolution)
# Key: (gateway parentRef -> discovered ingress-gateway class) + resolved targetPort
# Regenerate; do not hand-edit.
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: allow-gateway-ingress-%s
spec:
  selector:
    matchLabels:
      policy.prplanit.com/ingress: "true"
  action: ALLOW
  rules:
    - from:
        - source:
            principals:
              - cluster.local/ns/%s/sa/%s
      to:
        - operation:
            ports: [%s]
`
