// The session is who you are and where you are working — the UI's
// equivalent of the CLI's token cache plus `use`.

import { defineStore } from 'pinia'
import * as oidc from '@/lib/oidc'
import { ApiError } from '@/lib/api'
import {
  bindings,
  boundKinds,
  canI,
  clusterPath,
  review,
  type TenantClaim,
  type ProjectClaim,
} from '@/lib/api'

interface State {
  claims: oidc.Claims | null
  tenants: TenantClaim[]
  tenant: string
  project: string
  loading: boolean
  error: string
  /**
   * unauthorized means the servers did not accept our token. Kept apart
   * from error because it has an action — sign in again — and two very
   * different causes: a token that expired, or a server that cannot
   * validate it at all. Neither is fixed by retrying the same request.
   */
  unauthorized: boolean
  admin: boolean
  adminChecked: boolean
  /** enabled is what the bindings in the selected project actually serve. */
  enabled: { binding: string; group: string; resource: string }[]
}

const SELECTION = 'contrib.selection'

export const useSession = defineStore('session', {
  state: (): State => ({
    claims: oidc.claims(),
    tenants: [],
    tenant: '',
    project: '',
    loading: false,
    error: '',
    unauthorized: false,
    admin: false,
    adminChecked: false,
    enabled: [],
  }),

  getters: {
    authenticated: (): boolean => oidc.token() !== null,

    username(state): string {
      // The username kcp derives is the email claim, which is what a
      // Membership has to name — so that, not a display name, is the
      // identity worth showing.
      return state.claims?.email || state.claims?.sub || 'unknown'
    },

    currentTenant(state): TenantClaim | undefined {
      return state.tenants.find((t) => t.name === state.tenant)
    },

    currentProject(): ProjectClaim | undefined {
      return this.currentTenant?.projects?.find((p) => p.name === this.project)
    },

    /** workspace is the cluster path of the selected project. */
    workspace(): string {
      const endpoint = this.currentProject?.endpoint
      return endpoint ? clusterPath(endpoint) : ''
    },

    projects(state): { tenant: string; project: ProjectClaim }[] {
      return state.tenants.flatMap((t) => (t.projects ?? []).map((p) => ({ tenant: t.name, project: p })))
    },
  },

  actions: {
    async load() {
      this.loading = true
      this.error = ''
      this.unauthorized = false
      try {
        const answer = await review()
        this.tenants = answer.status?.tenants ?? []
        this.restore()
        await this.loadEnabled()
      } catch (e) {
        if (e instanceof ApiError && e.unauthorized) {
          // Deliberately not an automatic redirect to login: when the
          // cause is server-side — a validator that cannot reach the
          // issuer, say — signing in again returns a fresh token that is
          // refused just the same, and the redirect becomes a loop. Say
          // what happened and let the person choose.
          this.unauthorized = true
          this.tenants = []
        } else {
          this.error = e instanceof Error ? e.message : String(e)
        }
      } finally {
        this.loading = false
      }
    },

    /** restore re-selects what was chosen last, if it is still reachable. */
    restore() {
      const saved = localStorage.getItem(SELECTION)
      if (saved) {
        const [tenant, project] = saved.split('/')
        if (this.tenants.some((t) => t.name === tenant)) {
          this.tenant = tenant
          this.project = project ?? ''
          if (this.currentProject || !project) return
        }
      }
      // Nothing valid remembered: land on the first project, which is
      // where work actually happens.
      const first = this.projects[0]
      if (first) this.select(first.tenant, first.project.name)
      else if (this.tenants[0]) this.select(this.tenants[0].name, '')
    },

    select(tenant: string, project: string) {
      this.tenant = tenant
      this.project = project
      localStorage.setItem(SELECTION, project ? `${tenant}/${project}` : tenant)
      this.adminChecked = false
      this.loadEnabled()
    },

    /**
     * loadEnabled asks the workspace what its bindings put there, so an
     * enabled offering gets its own place in the nav instead of being
     * something you have to go looking for in discovery.
     */
    async loadEnabled() {
      const cluster = this.workspace
      if (!cluster) {
        this.enabled = []
        return
      }
      try {
        const body = await bindings(cluster)
        this.enabled = boundKinds(body.items ?? [])
      } catch {
        // Not being allowed to read bindings just means no shortcuts.
        this.enabled = []
      }
    },

    /**
     * checkAdmin asks kcp whether this identity may write where the
     * platform's own records live. Nothing in the token says "admin" —
     * being able to do the thing is what makes someone one.
     */
    async checkAdmin() {
      if (this.adminChecked) return
      this.adminChecked = true
      this.admin = await canI('/clusters/root:tenancy:store', {
        group: 'tenancy.platform.kcp.io',
        resource: 'tenants',
        verb: 'create',
      })
    },

    logout() {
      oidc.forget()
      localStorage.removeItem(SELECTION)
      this.$reset()
    },
  },
})
