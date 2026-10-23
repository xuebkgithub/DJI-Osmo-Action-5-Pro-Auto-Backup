<script setup>
defineProps({ mode: Object })
</script>

<template>
  <div v-if="mode.mode !== 'production'" class="banner" :class="mode.mode">
    <span>运行模式：{{ mode.mode.toUpperCase() }}</span>
    <span class="tag" v-if="mode.faultinject_build">测试构建（faultinject）</span>
    <span class="tag">{{ mode.platform }} / {{ mode.storage_backend }}</span>
    <span class="tag" v-if="!mode.destination_write_allowed">零写入：目标不会被修改</span>
    <span class="tag" v-if="!mode.auth_enabled">未启用认证</span>
  </div>
  <div v-else-if="!mode.auth_enabled" class="banner dry-run">production 模式但未启用认证（配置校验应已拒绝）</div>
</template>
