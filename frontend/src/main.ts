import { createApp } from "vue";
import { createPinia } from "pinia";
import App from "./App.vue";
import router from "./router";
import TDesign from "tdesign-vue-next";
// 引入组件库的少量全局样式变量
import "tdesign-vue-next/dist/tdesign.css";
import "@/assets/theme/theme.css";
import "@/assets/theme/tdesign-overrides.less";
import "@/assets/dropdown-menu.less";
import "@/components/css/chat-hljs-dark.less";
// vue-virtual-scroller ships its own tiny stylesheet — required for
// RecycleScroller/DynamicScroller to size their viewport correctly.
// Without it the scroller computes 0 height and renders no items.
import "vue-virtual-scroller/dist/vue-virtual-scroller.css";
import i18n from "./i18n";
import { initTheme } from "@/composables/useTheme";
import { initFont } from "@/composables/useFont";
import { installTDesignIconOfflineGuard } from "@/utils/tdesign-icon-offline";
import { installAutofillGuard } from "@/utils/disable-autofill";
import { useAuthStore } from "@/stores/auth";

// 必须在 Vue 组件挂载之前执行，避免 tdesign-icons 运行时请求 tdesign.gtimg.com
installTDesignIconOfflineGuard();

initTheme();
initFont();

/**
 * 外部平台（如 LoLR）通过 iframe 嵌入本应用时，既无法设置 Authorization
 * 请求头，也写不了本域的 localStorage，只能把 token 放在 URL 上传递。
 * 这里在启动最早期把它落盘为正常登录态，随后从地址栏移除，避免 token
 * 残留在浏览器历史中；用户 / 租户由路由守卫的 /auth/me 校准补齐。
 *
 * 参数名固定为 `weknora_token`：不能叫 `token`，那是 /login 邀请链接
 * （Login.vue 的 route.query.token）在用的名字，否则会误吞邀请令牌。
 */
function consumeTokenFromUrl() {
  const url = new URL(window.location.href);
  const token = (url.searchParams.get("weknora_token") || "").trim();
  if (!token) return;

  localStorage.setItem("weknora_token", token);
  // 外部签发的换发令牌可能不属于同一账号，先清掉避免串号。
  localStorage.removeItem("weknora_refresh_token");

  url.searchParams.delete("weknora_token");
  const query = url.searchParams.toString();
  window.history.replaceState(
    window.history.state,
    "",
    `${url.pathname}${query ? `?${query}` : ""}${url.hash}`,
  );
}

async function bootstrap() {
  // 必须早于下面读取 localStorage 会话的 refreshFromAuthMe
  consumeTokenFromUrl();

  const app = createApp(App);

  // 全局错误处理：捕获未处理的组件错误，防止白屏
  app.config.errorHandler = (err, instance, info) => {
    console.error("[WeKnora] Unhandled Vue error:", err, "\nComponent:", instance, "\nInfo:", info);
  };

  app.use(TDesign);
  const pinia = createPinia();
  app.use(pinia);

  // Capabilities (can_create_tenant, auto_accept_invitation) are not cached
  // in localStorage — reconcile once before first paint when a session exists.
  const authStore = useAuthStore();
  if (localStorage.getItem("weknora_token")) {
    try {
      await authStore.refreshFromAuthMe();
    } catch {
      // best-effort; capabilities stay at defaults until the next refresh
    }
  }

  app.use(router);
  app.use(i18n);

  // 等首屏路由（含导航守卫、Lite 自动登录）完成后再挂载，避免先闪默认页再跳转
  await router.isReady();
  app.mount("#app");
  installAutofillGuard();
}

bootstrap();
