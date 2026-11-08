package policy

// Skeletons for the derived policy files. The cilium headers are fmt format strings ending at
// `    - fromEndpoints:`; the generated sources, ciliumToPorts and the port block follow. The
// istio template's args are (name, ingressLabel, principalNamespace, principalName,
// inlinePortList). Every generated file starts with derivedMarker, which Locate keys on.

const derivedMarker = "# DERIVED:"

const ciliumContractHeader = `# DERIVED: resolved pod targetPorts from HTTPRoute backendRefs.
# Inputs: HTTPRoutes → Services (targetPort resolution) → EndpointSlices (named port resolution)
# Regenerate; do not hand-edit.
apiVersion: "cilium.io/v2"
kind: CiliumClusterwideNetworkPolicy
metadata:
  name: %s
spec:
  description: >-
    Contract: ingress gateway pods may reach backend application pods.
    Source is restricted to the discovered ingress-gateway pods.
    Destination pods must carry %s: "true".
  enableDefaultDeny:
    egress: false
    ingress: false
  endpointSelector:
    matchLabels:
      %s: "true"
  ingress:
    - fromEndpoints:
`

const ciliumToPorts = `      toPorts:
        - ports:
`

const ciliumHealthcheckHeader = `# DERIVED: resolved pod targetPorts from Gatus-annotated services and probe-labelled pods.
# Inputs: Gatus annotations → Services (targetPort resolution) → probe-labelled pod containerPorts
# Regenerate; do not hand-edit.
apiVersion: "cilium.io/v2"
kind: CiliumClusterwideNetworkPolicy
metadata:
  name: %s
spec:
  description: >-
    Allows the health prober to reach probe-labelled endpoints
    across the cluster on application and infrastructure ports.
  enableDefaultDeny:
    egress: false
    ingress: false
  endpointSelector:
    matchLabels:
      %s: "true"
  ingress:
    - fromEndpoints:
`

const istioTemplate = `# DERIVED: pod targetPorts from HTTPRoute backendRefs targeting this namespace.
# Inputs: HTTPRoutes → Services (targetPort resolution) → EndpointSlices (named port resolution)
# Key: (gateway parentRef -> discovered ingress-gateway class) + resolved targetPort
# Regenerate; do not hand-edit.
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: %s
spec:
  selector:
    matchLabels:
      %s: "true"
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
