// OIDC authorization code flow with PKCE, against the same issuer kcp and
// the virtual workspaces validate tokens from.
//
// The UI is a public client: it has no secret to keep, which is why PKCE
// exists. The ID token it ends up with is the credential every request
// carries, and it is the same token the CLI caches — one identity, one
// mapping to a username, whichever tool the person reaches for.

export interface OIDCConfig {
  issuer: string
  clientId: string
  scopes: string
}

export interface Claims {
  sub?: string
  email?: string
  name?: string
  groups?: string[]
  exp?: number
  iss?: string
  [key: string]: unknown
}

const STORAGE_TOKEN = 'contrib.idToken'
const STORAGE_VERIFIER = 'contrib.pkceVerifier'
const STORAGE_STATE = 'contrib.oauthState'
const STORAGE_RETURN = 'contrib.returnTo'

export function config(): OIDCConfig {
  return {
    issuer: (import.meta.env.VITE_OIDC_ISSUER as string) || '',
    clientId: (import.meta.env.VITE_OIDC_CLIENT_ID as string) || 'kcp',
    scopes: (import.meta.env.VITE_OIDC_SCOPES as string) || 'openid email groups',
  }
}

export function redirectUri(): string {
  return `${window.location.origin}/callback`
}

/** discovery reads the issuer's metadata; nothing about endpoints is hardcoded. */
async function discovery(issuer: string): Promise<{ authorization_endpoint: string; token_endpoint: string; end_session_endpoint?: string }> {
  const url = `${issuer.replace(/\/$/, '')}/.well-known/openid-configuration`
  const resp = await fetch(url)
  if (!resp.ok) throw new Error(`the identity provider at ${issuer} answered ${resp.status}`)
  return resp.json()
}

function randomString(bytes = 32): string {
  const buf = new Uint8Array(bytes)
  crypto.getRandomValues(buf)
  return base64url(buf)
}

function base64url(buf: Uint8Array | ArrayBuffer): string {
  const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf)
  let s = ''
  for (const b of bytes) s += String.fromCharCode(b)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

async function challengeFor(verifier: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier))
  return base64url(digest)
}

/** begin sends the browser to the provider. */
export async function begin(returnTo = '/'): Promise<void> {
  const cfg = config()
  if (!cfg.issuer) throw new Error('no issuer configured: set VITE_OIDC_ISSUER')

  const { authorization_endpoint } = await discovery(cfg.issuer)
  const verifier = randomString()
  const state = randomString(16)

  sessionStorage.setItem(STORAGE_VERIFIER, verifier)
  sessionStorage.setItem(STORAGE_STATE, state)
  sessionStorage.setItem(STORAGE_RETURN, returnTo)

  const params = new URLSearchParams({
    response_type: 'code',
    client_id: cfg.clientId,
    redirect_uri: redirectUri(),
    scope: cfg.scopes,
    state,
    code_challenge: await challengeFor(verifier),
    code_challenge_method: 'S256',
  })
  window.location.assign(`${authorization_endpoint}?${params}`)
}

/** complete exchanges the code the provider redirected back with. */
export async function complete(search: string): Promise<string> {
  const cfg = config()
  const params = new URLSearchParams(search)

  const error = params.get('error')
  if (error) throw new Error(params.get('error_description') || error)

  const code = params.get('code')
  if (!code) throw new Error('the provider redirected back without a code')

  // The state check is what stops someone walking a victim's browser
  // through a login they did not start.
  const expected = sessionStorage.getItem(STORAGE_STATE)
  if (!expected || params.get('state') !== expected) {
    throw new Error('state mismatch: this redirect did not come from a login started here')
  }
  const verifier = sessionStorage.getItem(STORAGE_VERIFIER)
  if (!verifier) throw new Error('no PKCE verifier in this browser; start the login again')

  const { token_endpoint } = await discovery(cfg.issuer)
  const resp = await fetch(token_endpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({
      grant_type: 'authorization_code',
      code,
      redirect_uri: redirectUri(),
      client_id: cfg.clientId,
      code_verifier: verifier,
    }),
  })
  const body = await resp.json().catch(() => ({}))
  if (!resp.ok) throw new Error(body.error_description || body.error || `token endpoint answered ${resp.status}`)
  if (!body.id_token) throw new Error('the provider returned no id_token; ask for the openid scope')

  sessionStorage.removeItem(STORAGE_VERIFIER)
  sessionStorage.removeItem(STORAGE_STATE)
  localStorage.setItem(STORAGE_TOKEN, body.id_token)
  return body.id_token
}

export function returnTo(): string {
  return sessionStorage.getItem(STORAGE_RETURN) || '/'
}

export function token(): string | null {
  const t = localStorage.getItem(STORAGE_TOKEN)
  if (!t) return null
  const c = claims(t)
  // An expired token produces errors that read like permission problems,
  // so it is dropped rather than presented.
  if (c?.exp && Date.now() / 1000 > c.exp - 30) {
    localStorage.removeItem(STORAGE_TOKEN)
    return null
  }
  return t
}

export function forget(): void {
  localStorage.removeItem(STORAGE_TOKEN)
}

/**
 * claims decodes the payload for display only. Nothing is trusted on this
 * side: the servers verify the signature, and the UI showing a name it
 * read out of a token grants nobody anything.
 */
export function claims(raw?: string | null): Claims | null {
  const t = raw ?? localStorage.getItem(STORAGE_TOKEN)
  if (!t) return null
  const part = t.split('.')[1]
  if (!part) return null
  try {
    const json = atob(part.replace(/-/g, '+').replace(/_/g, '/'))
    return JSON.parse(json) as Claims
  } catch {
    return null
  }
}
