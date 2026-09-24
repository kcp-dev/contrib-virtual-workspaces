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

KUBECONFIG = TILT_DIR + '/tilt-frontproxy.kubeconfig'
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
    print('login: bin/contribctl tenancy login --issuer %s --ca-file %s/dex/ca.crt' % (DEX_ISSUER, TILT_DIR))
    print('JWT usernames arrive as the plain email, e.g. alice@team-a.example.com')

# The flags every virtual workspace shares when dex is on, mirroring the
# shard-side mapping exactly (usernameClaim email, no prefix — the
# operator's documented "oidc:" default is not actually rendered) so a
# JWT names the same user at kcp and at a VW.
OIDC_VW_ARGS = ''
if DEX_ENABLED:
    OIDC_VW_ARGS = ('--oidc-issuer-url %s --oidc-client-id kcp ' +
                    '--oidc-username-claim email --oidc-groups-claim groups ' +
                    '--oidc-ca-file %s/dex/ca.crt ') % (DEX_ISSUER, TILT_DIR)

def enabled(name):
    return os.getenv(name + '_ENABLED', 'true').lower() not in ('false', '0', 'no', 'off')

def component(name, binary, init_args, operator_args, vw_args):
    """One contrib component: build + init + operator + virtual workspace.

    Everything hangs off the built binary as a file dependency: a source
    change rebuilds it, which re-runs init (the kcp-side assets are
    embedded, so a changed asset must be re-applied) and restarts both
    long-running processes.
    """
    bin_path = 'bin/' + binary

    local_resource('%s-build' % name,
        cmd='make --no-print-directory build-%s' % name,
        deps=[name + '/main.go', name + '/cmd', name + '/pkg', name + '/config', name + '/sdk'],
        ignore=['**/*_test.go'],
        labels=[name],
        allow_parallel=True,
    )

    local_resource('%s-init' % name,
        cmd='%s init --kubeconfig %s %s' % (bin_path, KUBECONFIG, init_args),
        deps=[bin_path],
        resource_deps=['%s-build' % name, 'kcp-admin extract'],
        labels=[name],
    )

    local_resource('%s-operator' % name,
        serve_cmd='%s operator --kubeconfig %s %s --v=4' % (bin_path, KUBECONFIG, operator_args),
        deps=[bin_path],
        resource_deps=['%s-init' % name],
        labels=[name],
    )

    local_resource('%s-vw' % name,
        serve_cmd='%s virtualworkspace --kubeconfig %s --client-ca-file %s/pki/ca.crt %s%s --v=4' % (
            bin_path, KUBECONFIG, TILT_DIR, OIDC_VW_ARGS, vw_args),
        deps=[bin_path],
        resource_deps=['%s-init' % name, 'dev-pki'],
        labels=[name],
    )

if enabled('TENANCY'):
    component('tenancy', 'tenancy-vw',
        init_args='',
        operator_args='--workspace-path root:tenancy:controllers ' +
                      '--tenants-workspace root:tenancy:tenants',
        vw_args='--workspace-path root:tenancy:controllers ' +
                '--secure-port 9445 ' +
                '--endpoint-base %s/clusters/' % FRONT_PROXY_URL,
    )
    # Demo data, so a fresh environment answers whoami with something:
    # tenant acme + project web, alice admin / bob view — each granted
    # under both identities (dex email and dev-cert CN). Idempotent;
    # re-run anytime with `tilt trigger tenancy-seed`. SEED_ENABLED=false
    # skips it.
    if os.getenv('SEED_ENABLED', 'true').lower() not in ('false', '0', 'no', 'off'):
        local_resource('tenancy-seed',
            cmd='make --no-print-directory build-cli && hack/tilt/seed.sh ' + KUBECONFIG,
            resource_deps=['tenancy-operator'],
            labels=['tenancy'],
        )
    print('tenancy VW → https://localhost:9445/services/tenancy (alice/bob certs in .tilt/pki)')

if enabled('MARKETPLACE'):
    component('marketplace', 'marketplace-vw',
        init_args='',
        operator_args='--workspace-path root:marketplace:controllers ' +
                      '--catalog-workspace root:marketplace:catalog ' +
                      '--providers-workspace root:marketplace:providers',
        vw_args='--workspace-path root:marketplace:controllers ' +
                '--secure-port 9446',
    )
    print('marketplace VW → https://localhost:9446/services/marketplace (alice/bob certs in .tilt/pki)')
