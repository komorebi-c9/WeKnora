<template>
  <span class="brand-mark">
    <svg class="brand-mark__icon" :width="size" :height="size" viewBox="0 0 24 24" fill="none"
      stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M12 2 2 7l10 5 10-5-10-5z" />
      <path d="M2 17l10 5 10-5" />
      <path d="M2 12l10 5 10-5" />
    </svg>
    <span v-if="showText" class="brand-mark__text">{{ brandName }}</span>
  </span>
</template>

<script setup lang="ts">
import { BRAND_NAME } from '@/constants/brand'

withDefaults(
  defineProps<{
    /** 图标边长（px）。文字大小由主题令牌控制。 */
    size?: number
    /** 是否在图标右侧显示品牌名。 */
    showText?: boolean
  }>(),
  { size: 26, showText: true },
)

const brandName = BRAND_NAME
</script>

<style scoped>
.brand-mark {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: var(--td-brand-color);
}

.brand-mark__icon {
  flex: none;
}

.brand-mark__text {
  font-size: var(--app-text-lg);
  font-weight: 600;
  letter-spacing: 0.2px;
  color: var(--td-text-color-primary);
  white-space: nowrap;
}
</style>
