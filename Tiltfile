# Tilt for the contrib virtual workspaces: kcp itself comes from the kcp
# repo's own Tilt setup (included below, unchanged), and this repo's
# components run on top as live-reloading local processes against it.
#
#   hack/tilt/up.sh          create the kind cluster and start tilt
#   KCP_DIR=/path/to/kcp     where the kcp checkout lives (default ../kcp)
#   KCP_STATIC=false         build kcp from the checkout's sources instead
#                            of running the published, version-matched
#                            images (the default is static: this repo
#                            develops AGAINST kcp, not kcp itself; pin
#                            with KCP_IMAGE_REPO/KCP_IMAGE_TAG)
#   TENANCY_ENABLED=false    skip a component (also MARKETPLACE_ENABLED)
#
# What you get:
#   - everything kcp's contrib/tilt deploys (operator, shards, front-proxy
#     behind an envoy gateway on https://kcp.localhost:8443, observability,
#     optionally the dashboard),
#   - admin kubeconfigs extracted into .tilt/ (tilt-frontproxy.kubeconfig,
#     tilt-root.kubeconfig, tilt-theseus.kubeconfig),
#   - per component: a build resource (rebuilds on source changes), an
#     init resource (re-applies the exports — they are embedded in the
#     binary, so a rebuilt binary re-runs init), and operator + virtual
#     workspace processes that restart when the binary changes.

KCP_DIR = os.getenv('KCP_DIR', '../kcp')
if not os.path.exists(KCP_DIR + '/contrib/tilt/Tiltfile'):
    fail('no kcp checkout at %s — set KCP_DIR to your github.com/kcp-dev/kcp clone' % KCP_DIR)

# kcp's extract-kubeconfig.bash honors this: the admin kubeconfigs land in
# OUR .tilt/ instead of the kcp repo root.
TILT_DIR = os.getcwd() + '/.tilt'
os.putenv('KCP_KUBECONFIG_DIR', TILT_DIR)

# Static mode: the operator's default image tag (a release, v0.33.x) lags
# what these Tiltfiles and this repo are developed against. Track kcp main
# unless the caller pinned something else with KCP_IMAGE_REPO/KCP_IMAGE_TAG.
if os.getenv('KCP_IMAGE_TAG') == None:
    os.putenv('KCP_IMAGE_TAG', 'main')

# The dashboard lives in yet another sibling checkout; only keep kcp's
# default-on behavior when it is actually there.
if os.getenv('KCP_PORTAL_ENABLED') == None and not os.path.exists(KCP_DIR + '/../contrib-dashboard/Dockerfile'):
    os.putenv('KCP_PORTAL_ENABLED', 'false')

# Dex: an in-cluster OIDC issuer shared by kcp and the virtual workspaces,
# so `contribctl tenancy login` works against this environment. One issuer
# URL serves both sides: the host reaches it through the existing :8443
# ingress port-forward, the kcp pods through their gateway hostAliases
# (KCP_EXTRA_HOSTNAMES). Disable with DEX_ENABLED=false.
DEX_ENABLED = os.getenv('DEX_ENABLED', 'true').lower() not in ('false', '0', 'no', 'off')
DEX_ISSUER = 'https://dex.kcp.localhost:8443/dex'
if DEX_ENABLED:
    os.putenv('KCP_OIDC_ISSUER_URL', DEX_ISSUER)
    os.putenv('KCP_OIDC_CLIENT_ID', 'kcp')
    os.putenv('KCP_OIDC_CA_SECRET', 'dex-tls')
    os.putenv('KCP_OIDC_USERNAME_CLAIM', 'email')
    os.putenv('KCP_EXTRA_HOSTNAMES', 'dex.kcp.localhost')

if os.getenv('KCP_STATIC', 'true').lower() not in ('false', '0', 'no', 'off'):
    include(KCP_DIR + '/contrib/tilt/Tiltfile.static')
else:
    include(KCP_DIR + '/contrib/tilt/Tiltfile')

# ── this repo's components ───────────────────────────────────────────

# The components get their own credential rather than borrowing kcp's
# admin kubeconfig, because the group it carries matters:
#
# kcp's workspace admission records the creator as the workspace owner
# (LogicalCluster.spec.createdBy) only for identities that are NOT
# system-privileged, and the apibinder initializer impersonates that owner
# to bind a new workspace's defaultAPIBindings. A workspace created by a
# system:masters client therefore has no owner and sticks in Initializing
# on system:apibindings forever.
#
# kcp's own tilt mints its admin kubeconfigs with system:masters, and the
# front-proxy hides the problem by dropping that group on ingress — but
# our operators also talk to shard-direct APIExport URLs, where it is not
# dropped. So we mint system:kcp:admin without it: admin everywhere
# through RBAC, recorded as an owner, and not dependent on how the kcp
# checkout happens to be configured.
k8s_yaml(blob("""
apiVersion: operator.kcp.io/v1alpha1
kind: Kubeconfig
metadata:
  name: contrib
  namespace: default
spec:
  username: contrib-admin
  groups: ["system:kcp:admin"]
  validity: 8766h
  secretRef:
    name: kcp-contrib-kubeconfig
  target:
    frontProxyRef:
      name: frontproxy
"""))
local_resource('contrib-kubeconfig',
    cmd='kubectl wait kubeconfig/contrib --for=create --timeout=5m && ' +
        'kubectl wait secret/kcp-contrib-kubeconfig --for=create --timeout=5m && ' +
        "kubectl get secret/kcp-contrib-kubeconfig -o jsonpath='{.data.kubeconfig}' | base64 -d > " +
        TILT_DIR + '/tilt-contrib.kubeconfig',
    resource_deps=['kcp-admin extract'],
    labels=['contrib'],
)

KUBECONFIG = TILT_DIR + '/tilt-contrib.kubeconfig'
FRONT_PROXY_URL = os.getenv('KCP_FRONT_PROXY_URL', 'https://kcp.localhost:8443')

# A throwaway client CA + alice/bob, so the virtual workspaces can
# authenticate direct callers during development. With dex enabled the
# same resource also mints dex's serving cert, whose CA every consumer
# (kcp via the dex-tls secret, the VWs and login via the file) trusts.
PKI_CMD = 'hack/tilt/pki.sh ' + TILT_DIR + '/pki'
if DEX_ENABLED:
    PKI_CMD += ' && hack/tilt/dex-pki.sh ' + TILT_DIR + '/dex'
local_resource('dev-pki',
    cmd=PKI_CMD,
    labels=['contrib'],
    allow_parallel=True,
)

if DEX_ENABLED:
    # The one secret both dex (tls.crt/tls.key) and the kcp shards
    # (caFileRef -> ca.crt) mount.
    local_resource('dex-secret',
        cmd='kubectl create secret generic dex-tls' +
            ' --from-file=tls.crt=' + TILT_DIR + '/dex/tls.crt' +
            ' --from-file=tls.key=' + TILT_DIR + '/dex/tls.key' +
            ' --from-file=ca.crt=' + TILT_DIR + '/dex/ca.crt' +
            ' --dry-run=client -o yaml | kubectl apply -f -',
        deps=[TILT_DIR + '/dex/tls.crt'],
        resource_deps=['dev-pki'],
        labels=['auth'],
        allow_parallel=True,
    )
    k8s_yaml('hack/tilt/dex.yaml')
    k8s_resource('dex', resource_deps=['dex-secret'], labels=['auth'])

    # The issuer URL must resolve on the host too (the pods get it via
    # hostAliases). Warn early instead of failing a login later.
    if str(local('grep -qs dex.kcp.localhost /etc/hosts && echo yes || echo no', quiet=True)).strip() == 'no':
        print('WARNING: dex.kcp.localhost missing from /etc/hosts — add it to the 127.0.0.1 line for login to work')

    print('dex → %s (alice@team-a.example.com / bob@team-b.example.com / mallory@strangers.example.com, password "password")' % DEX_ISSUER)
    print('login: bin/contribctl tenancy login --issuer %s --certificate-authority %s/dex/ca.crt' % (DEX_ISSUER, TILT_DIR))
    print('walkthrough: hack/tilt/README.md')
    print('JWT usernames arrive as the plain email, e.g. alice@team-a.example.com')

# The flags every virtual workspace shares when dex is on, mirroring the
# shard-side mapping exactly (usernameClaim email, no prefix — the
# operator's documented "oidc:" default is not actually rendered) so a
# JWT names the same user at kcp and at a VW. The issuer's CA comes from
# the same dex-tls secret the shards mount, since these now run in the
# cluster rather than on the host.
OIDC_VW_ARGS_LIST = []
if DEX_ENABLED:
    OIDC_VW_ARGS_LIST = [
        '--oidc-issuer-url=' + DEX_ISSUER,
        '--oidc-client-id=kcp',
        '--oidc-username-claim=email',
        '--oidc-groups-claim=groups',
        '--oidc-ca-file=/etc/dex/ca.crt',
    ]

DEX_VW_VOLUMES = ''
if DEX_ENABLED:
    DEX_VW_VOLUMES = """  extraVolumes:
    - name: dex-ca
      secret:
        secretName: dex-tls
        items:
          - key: ca.crt
            path: ca.crt
  extraVolumeMounts:
    - name: dex-ca
      mountPath: /etc/dex
      readOnly: true"""

def enabled(name):
    return os.getenv(name + '_ENABLED', 'true').lower() not in ('false', '0', 'no', 'off')

# Every virtual workspace answers on the front-proxy, at the path it
# owns — so a client needs exactly one address for all of kcp. The
# operator does the wiring: it deploys each virtual workspace from a
# VirtualWorkspace object (Deployment + Service on :6443), issues its
# serving certificate from the shard CA and mounts the requestheader
# client CA, and the front-proxy mappings below forward the caller's
# identity to it in X-Remote-* headers over its own client certificate.
#
# Mappings are collected here and applied to the FrontProxy at the end,
# because one patch has to carry them all.
VW_PATH_MAPPINGS = []

# The kind cluster images are side-loaded into; matches hack/tilt/up.sh.
KIND_CLUSTER = os.getenv('CLUSTER_NAME', 'kcp-tilt')

def virtual_workspace(name, image_target, service, init_args, vw_args):
    """Deploy one component's virtual workspace through the operator."""

    # Built and side-loaded by hand rather than with docker_build: the
    # operator composes the image as "<repository>:<tag>", so there is no
    # single field for Tilt to inject a ref into. A fixed tag plus an
    # explicit rollout restart gets the new image running.
    local_resource('%s-image' % name,
        cmd=('docker build --target %s -t %s:dev . && ' % (image_target, image_target) +
             'kind load docker-image %s:dev --name %s && ' % (image_target, KIND_CLUSTER) +
             'kubectl rollout restart deploy/%s-virtual-workspace 2>/dev/null || true' % name),
        deps=['go.mod', 'go.sum', 'Dockerfile',
              name + '/main.go', name + '/cmd', name + '/pkg', name + '/config', name + '/sdk'],
        ignore=['**/*_test.go'],
        labels=[name],
        allow_parallel=True,
    )

    # Two credentials, because bootstrapping and serving want different
    # ones: init walks the workspace tree and installs exports (admin),
    # while the server only reads what its exports give it — init grants
    # that identity the server role with --server-user.
    k8s_yaml(blob("""
apiVersion: operator.kcp.io/v1alpha1
kind: Kubeconfig
metadata:
  name: %(name)s-vw-bootstrap
  namespace: default
spec:
  username: kcp-admin
  groups: ["system:kcp:admin"]
  validity: 8766h
  secretRef:
    name: %(name)s-vw-bootstrap-kubeconfig
  target:
    frontProxyRef:
      name: frontproxy
---
apiVersion: operator.kcp.io/v1alpha1
kind: Kubeconfig
metadata:
  name: %(name)s-vw-server
  namespace: default
spec:
  username: %(name)s-vw
  validity: 8766h
  targetWorkspace: root:%(name)s:controllers
  secretRef:
    name: %(name)s-vw-server-kubeconfig
  target:
    frontProxyRef:
      name: frontproxy
---
apiVersion: operator.kcp.io/v1alpha1
kind: VirtualWorkspace
metadata:
  name: %(name)s
  namespace: default
spec:
  target:
    rootShardRef:
      name: root
  external:
    hostname: kcp.localhost
    port: 8443
  image:
    repository: %(image)s
    tag: dev
  command: ["/%(image)s", "virtualworkspace"]
  kubeconfigSecretRef:
    name: %(name)s-vw-server-kubeconfig
  initContainers:
    - name: init
      kubeconfigSecretRef:
        name: %(name)s-vw-bootstrap-kubeconfig
      command: ["/%(image)s", "init"]
      args:
        - --kubeconfig=/etc/kcp/init-kubeconfig/kubeconfig
        - --server-user=%(name)s-vw
%(init_args)s
  extraArgs:
%(vw_args)s
%(dex_volumes)s
  deploymentTemplate:
    spec:
      template:
        spec:
          # The kubeconfigs address the front-proxy by its external
          # hostname, so it has to resolve in here too.
          hostAliases:
            - ip: 10.96.2.2
              hostnames: ["kcp.localhost", "root.kcp.localhost", "theseus.kcp.localhost", "dex.kcp.localhost"]
""" % {
        'name': name,
        'image': image_target,
        'init_args': init_args,
        'vw_args': vw_args,
        'dex_volumes': DEX_VW_VOLUMES,
    }))

    # The VirtualWorkspace is not a workload Tilt recognises — the
    # Deployment and Service behind it are the operator's doing — so it
    # gets no resource of its own unless one is assembled from the object.
    #
    # Deliberately NOT waiting on the two Kubeconfig resources: they have
    # no pods, so their runtime status never leaves "pending" and a
    # dependent would wait forever. The pod just stays in
    # ContainerCreating until the operator has minted their secrets.
    k8s_resource(
        new_name='%s-vw' % name,
        objects=['%s:VirtualWorkspace' % name],
        resource_deps=['%s-image' % name, 'contrib-kubeconfig'],
        labels=[name],
    )
    for kubeconfig in ['%s-vw-bootstrap' % name, '%s-vw-server' % name]:
        k8s_resource(kubeconfig, labels=[name])

    VW_PATH_MAPPINGS.append({
        'path': '/services/' + service,
        'backend': 'https://%s-virtual-workspace.default.svc.cluster.local:6443' % name,
        # The serving certificate chains up to the root CA the front-proxy
        # already mounts, and the requestheader client certificate is how
        # the virtual workspace knows to trust the forwarded identity.
        # This is the path the operator's own default mappings use; the
        # /etc/kcp-front-proxy/tls/ca/tls.crt in its docs does not exist
        # in the deployment, and the front-proxy exits on a CA it cannot
        # read rather than serving without it.
        'backend_server_ca': '/etc/kcp/tls/ca/tls.crt',
        'proxy_client_cert': '/etc/kcp-front-proxy/requestheader-client/tls.crt',
        'proxy_client_key': '/etc/kcp-front-proxy/requestheader-client/tls.key',
    })


def component(name, binary, operator_args):
    """The component's operator, which runs as a local process: it serves
    no path, so nothing about it needs the front-proxy's routing — only
    its client credential, which is the same one everything else uses.
    """
    bin_path = 'bin/' + binary

    local_resource('%s-build' % name,
        cmd='make --no-print-directory build-%s' % name,
        deps=[name + '/main.go', name + '/cmd', name + '/pkg', name + '/config', name + '/sdk'],
        ignore=['**/*_test.go'],
        labels=[name],
        allow_parallel=True,
    )

    local_resource('%s-operator' % name,
        serve_cmd='%s operator --kubeconfig %s %s --v=4' % (bin_path, KUBECONFIG, operator_args),
        deps=[bin_path],
        # The exports it reconciles are installed by the virtual
        # workspace's init container.
        resource_deps=['%s-build' % name, '%s-vw' % name, 'contrib-kubeconfig'],
        labels=[name],
    )

if enabled('TENANCY'):
    virtual_workspace('tenancy', 'tenancy-vw', 'tenancy',
        init_args='',
        # No --workspace-path: the server kubeconfig is already scoped to
        # root:tenancy:controllers by targetWorkspace, and retargeting it
        # would append a second /clusters/ segment.
        # Self-service tenants are off by default in the component; a dev
        # stack wants them on, so a fresh identity can get started without
        # an administrator.
        vw_args='    - --endpoint-base=%s/clusters/\n' % FRONT_PROXY_URL +
                '    - --self-service-tenants\n' +
                ''.join(['    - %s\n' % a for a in OIDC_VW_ARGS_LIST]),
    )
    component('tenancy', 'tenancy-vw',
        operator_args='--workspace-path root:tenancy:controllers ' +
                      '--tenants-workspace root:tenancy:tenants',
    )
    # Demo data, so a fresh environment answers whoami with something:
    # the component's own `tenancy seed` command converges on tenant acme
    # + project web, alice admin / bob view — each granted under both
    # identities (dex email and dev-cert CN). Idempotent; re-run anytime
    # with `tilt trigger tenancy-seed`. SEED_ENABLED=false skips it.
    if os.getenv('SEED_ENABLED', 'true').lower() not in ('false', '0', 'no', 'off'):
        local_resource('tenancy-seed',
            cmd='make --no-print-directory build-cli && ' +
                'bin/contribctl tenancy seed --kubeconfig ' + KUBECONFIG +
                ' --tenant acme --display-name "Acme Corp" --project web' +
                ' --admin alice@team-a.example.com --admin alice' +
                ' --view bob@team-b.example.com --view bob',
            resource_deps=['tenancy-operator'],
            labels=['tenancy'],
        )
    print('tenancy VW → %s/services/tenancy' % FRONT_PROXY_URL)

if enabled('MARKETPLACE'):
    virtual_workspace('marketplace', 'marketplace-vw', 'marketplace',
        init_args='',
        vw_args=''.join(['    - %s\n' % a for a in OIDC_VW_ARGS_LIST]),
    )
    component('marketplace', 'marketplace-vw',
        operator_args='--workspace-path root:marketplace:controllers ' +
                      '--catalog-workspace root:marketplace:catalog ' +
                      '--providers-workspace root:marketplace:providers',
    )
    # Example offerings, on demand: `tilt trigger marketplace-examples`.
    #
    # Ad-hoc rather than automatic because an empty catalog is a legitimate
    # state to develop against — and because publishing grants `bind` to
    # every authenticated user in this stack, which should be something
    # someone asked for. auto_init=False keeps it out of `tilt up`;
    # TRIGGER_MODE_MANUAL keeps editing the examples from re-running it
    # behind your back.
    local_resource('marketplace-examples',
        cmd='KUBECONFIG=%s KCP_FRONT_PROXY_URL=%s marketplace/config/examples/apply.sh'
            % (KUBECONFIG, FRONT_PROXY_URL),
        deps=['marketplace/config/examples'],
        resource_deps=['marketplace-operator'],
        trigger_mode=TRIGGER_MODE_MANUAL,
        auto_init=False,
        labels=['marketplace'],
    )
    print('marketplace VW → %s/services/marketplace' % FRONT_PROXY_URL)
    print('  example offerings: tilt trigger marketplace-examples')

# The web UI, served by vite in dev mode. It talks only to the
# front-proxy — the dev server proxies /services and /clusters there — so
# the browser stays on one origin and kcp needs no CORS configuration.
# UI_ENABLED=false skips it.
if enabled('UI'):
    # Dependencies are a resource of their own so the dev server is not
    # restarted by an install, and an install re-runs only when the
    # manifests change. `npm ci` when there is a lockfile, because a dev
    # environment that drifts from it is a class of bug nobody enjoys.
    local_resource('ui-deps',
        cmd='cd ui && if [ -f package-lock.json ]; then npm ci --silent; else npm install --silent; fi',
        deps=['ui/package.json', 'ui/package-lock.json'],
        labels=['ui'],
        allow_parallel=True,
    )

    # Vite watches the sources itself, so Tilt starts it and stays out of
    # the way: no deps here, or every keystroke would restart the server.
    local_resource('ui',
        serve_cmd='npm run dev --silent',
        serve_dir='ui',
        resource_deps=['ui-deps'] + (['dex'] if DEX_ENABLED else []),
        labels=['ui'],
        links=['http://localhost:5173'],
        readiness_probe=probe(
            period_secs=5,
            http_get=http_get_action(port=5173, path='/'),
        ),
        allow_parallel=True,
    )
    print('UI → http://localhost:5173 (sign in with the dex users above)')

# One patch carries every mapping: the front-proxy matches the longest
# prefix, so these win over the default /services/ route to the shards.
if VW_PATH_MAPPINGS:
    local_resource('frontproxy-path-mappings',
        cmd="kubectl patch frontproxy frontproxy --type=merge -p '%s'" % encode_json(
            {'spec': {'additionalPathMappings': VW_PATH_MAPPINGS}}),
        resource_deps=['frontproxy'],
        labels=['contrib'],
    )
    print('front-proxy routes: %s' % ', '.join([m['path'] for m in VW_PATH_MAPPINGS]))
