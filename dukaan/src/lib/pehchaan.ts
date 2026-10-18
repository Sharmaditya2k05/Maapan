/** Session helpers on top of pehchaan's /v1/me and /v1/stores. */
import { get } from './api'
import { session } from './session'
import type { ListResponse, MeResponse, SessionUser, Store } from './types'

/** Build the app's SessionUser: pehchaan /v1/me (user, tenant, role) + /v1/stores (store objects the user may access). */
export async function loadSessionUser(): Promise<SessionUser> {
  const [me, stores] = await Promise.all([
    get<MeResponse>('/pehchaan/v1/me'),
    get<ListResponse<Store>>('/pehchaan/v1/stores'),
  ])
  return {
    id: me.user.id,
    name: me.user.name,
    phone: me.user.phone,
    role: me.role,
    tenant_id: me.tenant.id,
    business_name: me.tenant.business_name,
    permissions: me.permissions,
    stores: (stores.items ?? []).filter((s) => s.active !== false),
    default_store_id: me.store_id || undefined,
  }
}

/** Load the user into the session (after login, onboarding or a role change). */
export async function refreshSessionUser(): Promise<SessionUser> {
  const u = await loadSessionUser()
  session.setUser(u)
  return u
}
