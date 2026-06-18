<script setup lang="ts">
import type { Train as TrainType } from '@/lib/db'
import { getBlobURL, videoFileName } from '@/lib/paths'
import RelativeTime from '@/components/RelativeTime.vue'
import FavoriteIcon from '@/components/FavoriteIcon.vue'

defineProps<{
  trains: TrainType[]
}>()
</script>

<template>
  <v-container fluid>
    <v-row dense>
      <v-col v-for="train in trains" v-bind:key="train.id" cols="6" sm="3" md="2" xl="1">
        <router-link
          :to="{ name: 'trainDetail', params: { id: train.id } }"
          style="text-decoration: none; color: inherit"
        >
          <v-card>
            <div class="grid-media">
              <video
                :src="getBlobURL(videoFileName(train.start_ts))"
                autoplay
                loop
                muted
                playsinline
              ></video>
              <v-card-title class="grid-title text-white">
                <RelativeTime :ts="train.start_ts" />
                <FavoriteIcon :id="train.id" />
              </v-card-title>
            </div>
          </v-card>
        </router-link>
      </v-col>
    </v-row>
  </v-container>
</template>

<style scoped>
.pointer {
  cursor: pointer;
}
.grid-media {
  position: relative;
  height: 200px;
}
.grid-media video {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.grid-title {
  position: absolute;
  bottom: 0;
  left: 0;
  right: 0;
  background: linear-gradient(to bottom, rgba(0, 0, 0, 0.1), rgba(0, 0, 0, 0.5));
}
</style>
