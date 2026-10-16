<template>
  <!-- fork：一行一个、完整显示，不截断也不限数量（6647ec5e8）；上游 GroupBadge 默认截断，这里显式关闭。 -->
  <div v-if="groups && groups.length > 0" class="flex flex-col gap-1">
    <GroupBadge
      v-for="group in groups"
      :key="group.id"
      :name="group.name"
      :platform="group.platform"
      :subscription-type="group.subscription_type"
      :rate-multiplier="group.rate_multiplier"
      :show-rate="false"
      :truncate="false"
      class="w-fit max-w-full"
    />
  </div>
  <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
</template>

<script setup lang="ts">
import GroupBadge from '@/components/common/GroupBadge.vue'
import type { Group } from '@/types'

defineProps<{
  groups: Group[] | null | undefined
}>()
</script>
