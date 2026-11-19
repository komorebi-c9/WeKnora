import { userInfoFromApi } from '@/api/auth'
import type { useAuthStore } from '@/stores/auth'

type AuthStore = ReturnType<typeof useAuthStore>

/**
 * 把登录类接口（/auth/login、/auth/register-by-invite、/auth/orion/exchange）
 * 返回的会话写进 store 与 localStorage。
 *
 * 单独抽出来是因为 Orion 换取与本地登录必须用同一套落库逻辑，
 * 否则两处字段处理稍有差异就会出现“登录成功但没租户/没角色”的错位。
 */
export function persistLoginResponse(authStore: AuthStore, response: any) {
  const activeTenant = response.active_tenant || response.tenant
  if (response.user && response.token) {
    const homeTenantId = response.user.tenant_id ?? activeTenant?.id ?? ''
    authStore.setUser(userInfoFromApi(response.user, homeTenantId))
    authStore.setToken(response.token)
    if (response.refresh_token) {
      authStore.setRefreshToken(response.refresh_token)
    }
    if (activeTenant) {
      authStore.setTenant({
        id: String(activeTenant.id) || '',
        name: activeTenant.name || '',
        owner_id: response.user.id || '',
        created_at: activeTenant.created_at || new Date().toISOString(),
        updated_at: activeTenant.updated_at || new Date().toISOString(),
      })
    } else {
      authStore.setTenant(null)
    }
    if (Array.isArray(response.memberships)) {
      authStore.setMemberships(response.memberships)
    }
  }
}
