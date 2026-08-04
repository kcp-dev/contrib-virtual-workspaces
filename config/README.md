# Deploying the Access Virtual Workspace

The repo ships three layers of manifest. Apply in this order against the kcp instance hosting the access VW.

## 1. System APIExport (apply once, in a system workspace)

Prefer `make init`, which applies the assets below in the order kcp requires and verifies them. To do it by hand:

```sh
kubectl --context system kcp ws use root:access:controllers   # or your own --workspace-prefix
kubectl apply -f config/apiexport/apiresourceschema.yaml
kubectl apply -f config/apiexport/apiexport.yaml
kubectl apply -f config/apiexport/rbac-bind.yaml
kubectl apply -f config/apiexport/apiexportendpointslice.yaml
```

Order matters. An APIExport naming a schema that does not exist yet is rejected, and kcp gates `APIExportEndpointSlice` (and every consumer `APIBinding`) on a `bind` verb against the APIExport evaluated in the export's own workspace — a separate admission check that cluster-admin does not satisfy, which is what `rbac-bind.yaml` grants.

kcp does **not** create the `APIExportEndpointSlice` for you. Without it `--apiexport-endpointslice` names an object that does not exist, no logical clusters are ever engaged, and the server reports itself ready while serving an empty graph.

Both `init` and the server need a kcp credential, mounted from the `access-vw-kubeconfig` Secret in the deployment's namespace. In a kcp-operator installation that Secret is produced by a `Kubeconfig` resource; otherwise create it by hand:

```sh
kubectl create secret generic access-vw-kubeconfig --from-file=kubeconfig=./access-vw.kubeconfig
```

It may point at any workspace — both containers compute their own target from `--workspace-prefix` / `--controllers-workspace`. `init` needs rights to create workspaces from the root down and to install in the target workspace; the server needs to read the `APIExportEndpointSlice` there and reach `apiexports/content`.

## 2. Controller deployment (apply once)

```sh
kubectl apply -f config/deployment/deployment.yaml
```

The deployment runs the `cmd/access-vw` binary in multi-shard mode. It does NOT need to be deployed in a kcp workspace — the controller is a normal Kubernetes deployment that talks to kcp via the kubeconfig in the secret. It can live anywhere reachable from kcp (its sidecar host, a management cluster, etc.).

## 3. Consumer opt-in (per workspace that wants discovery)

```sh
kubectl --context user kcp ws use my-workspace
kubectl apply -f config/examples/apibinding-consumer.yaml
```

The example references the default exports workspace, `root:access:controllers`. Edit `spec.reference.export.path` if the install used a different `--workspace-prefix` or `--controllers-workspace`; `init` logs the workspace it installed into and its logical cluster ID, either of which kcp accepts.

Until a workspace applies this APIBinding **and** accepts the permission claims, it stays invisible to the indexer. That's the opt-in mechanism the design calls for.

## 4. FrontProxy routing (deployment-specific)

The SCAR endpoint path is `/services/access/apis/access.contrib.kcp.io/v1alpha1/selfclusteraccessreviews`. FrontProxy needs to forward the `/services/access` prefix to the controller's Service, using a backend of `https://<service>:9443` with `backend_server_ca` plus `proxy_client_cert` / `proxy_client_key` (the requestheader client certificate). The exact mechanism is deployment-dependent:

- kcp-operator deployments configure backends through the operator's CR.
- Bare kcp deployments configure FrontProxy via its config file (`pathMappings:` section in some versions).

In all cases, FrontProxy is responsible for authenticating the caller (bearer / cert) and injecting `X-Remote-User` / `X-Remote-Group` before forwarding. The controller trusts those headers only when FrontProxy presents its requestheader client certificate (verified against `--requestheader-client-ca-file`), the same pattern as the Kubernetes API server aggregation layer.

## Verifying

Once all four layers are in place:

```sh
curl -k -X POST \
  -H "Authorization: Bearer $KCP_TOKEN" \
  -H "Content-Type: application/json" -d '{}' \
  https://kcp.example.com/services/access/apis/access.contrib.kcp.io/v1alpha1/selfclusteraccessreviews
```

Expected response shape:

```json
{
  "kind": "SelfClusterAccessReview",
  "apiVersion": "access.contrib.kcp.io/v1alpha1",
  "status": {
    "clusters": [
      {"clusterName": "abc123", "endpoint": "https://kcp.example.com/clusters/abc123"}
    ]
  }
}
```
