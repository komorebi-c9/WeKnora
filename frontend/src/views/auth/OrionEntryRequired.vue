<template>
  <main class="orion-entry">
    <div class="orion-entry__glow" aria-hidden="true"></div>

    <section class="orion-entry__card">
      <div class="orion-entry__brand">
        <BrandMark :size="26" />
      </div>

      <h1 class="orion-entry__title">{{ t('orionEntry.title') }}</h1>
      <p class="orion-entry__description">{{ t('orionEntry.description') }}</p>

      <button type="button" class="orion-entry__button" :class="{ 'is-loading': loading }"
        :disabled="loading" :aria-busy="loading" @click="retry">
        <span v-if="loading" class="orion-entry__spinner" aria-hidden="true"></span>
        {{ loading ? t('orionEntry.retrying') : t('orionEntry.retry') }}
      </button>

      <p class="orion-entry__status" :class="{ 'is-error': failed }" role="status" aria-live="polite">
        <span v-if="failed">{{ t('orionEntry.failed') }}</span>
      </p>

      <p class="orion-entry__help">{{ t('orionEntry.help') }}</p>
    </section>
  </main>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { exchangeOrionToken } from '@/api/auth'
import BrandMark from '@/components/BrandMark.vue'
import { persistLoginResponse } from '@/utils/authSession'
import { consumeOrionUrlCredential } from '@/utils/orionAssertion'
import { useAuthStore } from '@/stores/auth'

const { t } = useI18n()
const router = useRouter()
const authStore = useAuthStore()

const loading = ref(false)
const failed = ref(false)

/**
 * 重新尝试一次凭据换取。守卫进入本页前已经试过一次，这里只在用户主动
 * 点击时再试，避免失败后无限轮询 Orion。
 */
async function retry() {
  if (loading.value) return
  loading.value = true
  failed.value = false
  try {
    // 复用本次页面加载期间从地址栏取到并已抹除的凭据（NS-TOKEN 或 License SK，若有）。
    const response = await exchangeOrionToken(consumeOrionUrlCredential())
    if (response?.success && response.token) {
      persistLoginResponse(authStore, response)
      router.replace(
        authStore.hasValidTenant ? '/platform/knowledge-bases' : '/onboarding/workspace',
      )
      return
    }
    failed.value = true
  } catch {
    failed.value = true
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.orion-entry {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  padding: 24px;
  background: var(--td-bg-color-page);
  overflow: hidden;
}

/* 极淡的主色渐晕：单卡片也需要一点层次，但不能盖过内容。 */
.orion-entry__glow {
  position: absolute;
  inset: -20% -10% auto -10%;
  height: 70vh;
  background: radial-gradient(
    60% 60% at 50% 0%,
    color-mix(in srgb, var(--td-brand-color) 12%, transparent),
    transparent 70%
  );
  pointer-events: none;
}

.orion-entry__card {
  position: relative;
  width: 100%;
  max-width: 480px;
  padding: 40px 36px 28px;
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--app-radius-xl);
  background: var(--td-bg-color-container);
  box-shadow: 0 18px 48px rgba(31, 35, 41, 0.08);
  text-align: center;
  animation: orion-entry-in 240ms ease-out both;
}

.orion-entry__brand {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  margin-bottom: 28px;
  color: var(--td-brand-color);
}

.orion-entry__title {
  margin: 0 0 12px;
  font-size: var(--app-text-3xl);
  line-height: 1.4;
  font-weight: 600;
  color: var(--td-text-color-primary);
}

.orion-entry__description {
  margin: 0 0 28px;
  font-size: var(--app-text-base);
  line-height: 1.7;
  color: var(--td-text-color-secondary);
}

.orion-entry__button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  width: 100%;
  height: 44px;
  border: none;
  border-radius: var(--app-radius-md);
  background: var(--td-brand-color);
  color: var(--td-text-color-anti);
  font-size: var(--app-text-base);
  line-height: 1;
  font-weight: 500;
  cursor: pointer;
  transition: background-color var(--app-motion-fast) ease, transform var(--app-motion-fast) ease,
    box-shadow var(--app-motion-fast) ease;
}

.orion-entry__button:hover:not(:disabled) {
  background: var(--td-brand-color-hover);
  transform: translateY(-1px);
  box-shadow: 0 8px 20px rgba(31, 35, 41, 0.16);
}

.orion-entry__button:active:not(:disabled) {
  transform: translateY(0);
  box-shadow: none;
}

.orion-entry__button:focus-visible {
  outline: 2px solid var(--td-brand-color-focus);
  outline-offset: 2px;
}

.orion-entry__button:disabled {
  cursor: not-allowed;
  opacity: 0.72;
}

.orion-entry__spinner {
  width: 14px;
  height: 14px;
  border: 2px solid color-mix(in srgb, var(--td-text-color-anti) 45%, transparent);
  border-top-color: var(--td-text-color-anti);
  border-radius: var(--app-radius-pill);
  animation: orion-entry-spin 700ms linear infinite;
}

.orion-entry__status {
  min-height: 20px;
  margin: 12px 0 0;
  font-size: var(--app-text-md);
  line-height: 1.5;
  color: var(--td-text-color-secondary);
}

.orion-entry__status.is-error {
  color: var(--td-error-color);
}

.orion-entry__help {
  margin: 20px 0 0;
  padding-top: 16px;
  border-top: 1px solid var(--td-component-stroke);
  font-size: var(--app-text-sm);
  line-height: 1.6;
  color: var(--td-text-color-placeholder);
}

@keyframes orion-entry-in {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

@keyframes orion-entry-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (max-width: 639px) {
  .orion-entry__card {
    padding: 32px 24px 24px;
  }

  .orion-entry__title {
    font-size: var(--app-text-2xl);
  }
}

@media (prefers-reduced-motion: reduce) {
  .orion-entry__card,
  .orion-entry__spinner {
    animation: none;
  }
}
</style>
