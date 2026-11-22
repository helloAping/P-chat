<script setup lang="ts">
// HeaderCat — the little mascot that rides the top edge of the composer.
// A single rAF loop owns BOTH the GIF swap and the translateX, so frame
// changes and movement can never race (past iterations that split them
// into two loops twitched). Assets are plain <img> swaps of pre-rendered
// GIFs — no sprite math, no canvas, no scaling on our side, so the
// outline stays as crisp as the source files. Horizontal movement is
// rounded to whole pixels to avoid sub-pixel smearing.
//
// Behaviour (driven by the `working` prop = a turn is running):
//   sleep      idle home on the left, snoring loop. The resting state.
//   wake       a turn started while asleep → play the "sleepy stand up"
//              one-shot (wake-to-stand), then walk.
//   walk       turn running → walk back and forth across the composer.
//   stand      turn ended mid-wake → just stand where it is, then groom.
//   groom      turn ended mid-walk (or after standing) → wash face /
//              lick fur in place a few times.
//   jump       after grooming with still no turn → hop left/right.
//   goHome     still no turn → walk back to the left home spot.
//   fallAsleep arrived home → stand-to-sleep one-shot, then sleep.
// A new turn during groom/jump/stand waits for the current one-shot to
// finish (per UX choice) then wakes into walk; during goHome it turns
// around immediately and walks back into the working loop.
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

const props = withDefaults(defineProps<{ working?: boolean; tip?: boolean }>(), {
  // tip=true: the compact walker that lives inside the todo tip bubble —
  // a simple walk-a-segment / groom / walk-again loop with no sleep,
  // wake, jump or goHome states (the tip only exists while a turn runs,
  // so the full idle cycle would never fit). Shares the same GIF-swap +
  // single-rAF approach, so the outline stays just as crisp.
  working: false,
  tip: false,
})

const shiftEl = ref<HTMLElement | null>(null)
const imgEl = ref<HTMLImageElement | null>(null)

// Static files are served under /app/ by pchat-server (see vite.config
// base). Absolute paths so the src resolves regardless of the route.
const SRC = {
  sleep: '/app/cat/cat-sleep-snore.gif',
  wake: '/app/cat/cat-wake-to-stand.gif',
  sit: '/app/cat/cat-sit.png',
  stand: '/app/cat/cat-stand.png',
  walk: '/app/cat/cat-walk-loop.gif',
  lick: '/app/cat/cat-lick-fur.gif',
  face: '/app/cat/cat-wash-face.gif',
  groomAll: '/app/cat/cat-grooming.gif',
  jump: '/app/cat/cat-jumping.gif',
  fallAsleep: '/app/cat/cat-stand-to-sleep.gif',
} as const
type SrcKey = keyof typeof SRC

// One-shot / loop durations (ms), from the source frames.json timings.
const WAKE_MS = 4420 // cat-wake-to-stand (loop:1)
const GROOM_MS: Record<string, number> = { lick: 3100, face: 3480, groomAll: 4280 }
const JUMP_MS = 1380 // cat-jumping single hop cycle
const FALL_ASLEEP_MS = 8200 // cat-stand-to-sleep (loop:1)
const GROOM_KEYS: SrcKey[] = ['lick', 'face', 'groomAll']

const CAT_W = 18
const SPEED = 26 // px/s while working — a touch brisker than the old bar walk
// Heading home to sleep is purposeful, not a stroll — much faster so the
// cat doesn't take forever crossing a wide composer back to its corner.
const GO_HOME_SPEED = 140 // px/s
const HOME_X = CAT_W / 2 // resting spot: left edge

// How many grooms before hopping, hops before heading home.
const GROOM_ROUNDS = 2
const JUMP_ROUNDS = 2
// Idle sleep: after this long asleep the cat wakes on its own, stands,
// grooms and hops a little, then walks home and goes back to sleep.
const SLEEP_WAKE_MIN_MS = 12000
const SLEEP_WAKE_RAND_MS = 8000
// Horizontal distance each hop covers (px). Jump frames already carry
// the vertical leap; this moves the cat sideways so it actually travels.
const JUMP_STEP = 26

type State =
  | 'sleep'
  | 'wake'
  | 'walk'
  | 'stand'
  | 'groom'
  | 'jump'
  | 'goHome'
  | 'fallAsleep'
  // tip mode only: mid-segment walk inside the tip bubble.
  | 'seg'

let raf = 0
let state: State = 'sleep'
let current: SrcKey | '' = ''
let dir = 1 // 1 = facing right, -1 = facing left
let center = HOME_X
let until = 0 // timestamp the current one-shot / timed state ends
let walkMin = HOME_X
let walkMax = HOME_X
let groomLeft = 0
let jumpLeft = 0
let wakeAt = 0 // idle: when the sleeping cat next wakes on its own
// jump: sideways target for the current hop, so the leap has real travel.
let jumpFrom = HOME_X
let jumpTo = HOME_X
let jumpStart = 0
// tip-mode segment walker state.
let tipWalkFrom = HOME_X
let tipWalkTo = HOME_X
let tipWalkStart = 0
let tipWalkEnd = 0
let tipFirst = true
const TIP_SPEED = 16 // px/s — the sample tip bubble's unhurried amble
// Stadium-pill tips curve inward near the top/bottom edges. The cat walks
// on the lower baseline, so a rectangular [CAT_W/2, w-CAT_W/2] lane lets
// its feet poke through the rounded caps. Inset the tip lane enough that
// the sprite stays inside the straight middle of the pill.
const TIP_EDGE_PAD = 10
let tipRo: ResizeObserver | null = null

function setSrc(key: SrcKey) {
  if (current === key || !imgEl.value) return
  current = key
  imgEl.value.src = SRC[key]
  // The jump GIF's frames use a smaller visible region than the other
  // clips, so at the same 18×14 box it reads as shrunken. Scale it up to
  // match the others' apparent size (anchored at the feet).
  const fit = imgEl.value.parentElement
  if (fit) fit.classList.toggle('jump-scale', key === 'jump')
}

function place(x: number) {
  if (shiftEl.value) shiftEl.value.style.transform = `translateX(${Math.round(x)}px)`
}

function face(d: number) {
  if (shiftEl.value) shiftEl.value.classList.toggle('face-left', d < 0)
}

function laneWidth(): number {
  return shiftEl.value?.parentElement?.clientWidth ?? 0
}

function measure() {
  const w = laneWidth()
  const pad = props.tip ? TIP_EDGE_PAD : 0
  walkMin = HOME_X + pad
  walkMax = Math.max(walkMin, w - CAT_W / 2 - pad)
}

function nextGroom(): SrcKey {
  return GROOM_KEYS[Math.floor(Math.random() * GROOM_KEYS.length)]
}

function enterSleep(now: number) {
  state = 'sleep'
  center = HOME_X
  dir = 1
  face(dir)
  place(center)
  setSrc('sleep')
  // After a while asleep the cat wakes on its own for a groom + hop
  // before settling back down — keeps the idle mascot feeling alive.
  wakeAt = now + SLEEP_WAKE_MIN_MS + Math.random() * SLEEP_WAKE_RAND_MS
}

function enterWake(now: number) {
  // From sleep (or any settled state) → sleepy stand-up one-shot → walk.
  state = 'wake'
  until = now + WAKE_MS
  setSrc('wake')
}

function enterWalk(now: number) {
  state = 'walk'
  measure()
  // Walk toward the far edge from wherever we are.
  dir = center <= (walkMin + walkMax) / 2 ? 1 : -1
  face(dir)
  setSrc('walk')
}

function enterStand(now: number) {
  state = 'stand'
  setSrc('stand')
  groomLeft = GROOM_ROUNDS
  jumpLeft = JUMP_ROUNDS
  // Brief beat standing before the first groom.
  until = now + 700
}

function enterGroom(now: number) {
  const g = nextGroom()
  state = 'groom'
  setSrc(g)
  until = now + GROOM_MS[g]
}

function enterJump(now: number) {
  measure()
  // Flip direction each hop and actually travel sideways (the leap is in
  // the frames; this moves the cat). Clamp the landing spot to the lane
  // so the hop never leaves the input box.
  dir *= -1
  face(dir)
  jumpFrom = center
  let target = center + dir * JUMP_STEP
  if (target > walkMax || target < walkMin) {
    // No room that way — hop back the other direction instead.
    dir *= -1
    face(dir)
    target = center + dir * JUMP_STEP
  }
  jumpTo = Math.min(walkMax, Math.max(walkMin, target))
  state = 'jump'
  setSrc('jump')
  jumpStart = now
  until = now + JUMP_MS
  place(center)
}

function enterGoHome(now: number) {
  measure()
  // Already (almost) home? Skip the walk and go straight to sleep —
  // otherwise the cat would pace away from the doorstep forever.
  if (Math.abs(center - HOME_X) <= 2) {
    center = HOME_X
    place(center)
    enterFallAsleep(now)
    return
  }
  state = 'goHome'
  dir = center > HOME_X ? -1 : 1
  face(dir)
  setSrc('walk')
}

function enterFallAsleep(now: number) {
  state = 'fallAsleep'
  dir = 1
  face(dir)
  setSrc('fallAsleep')
  until = now + FALL_ASLEEP_MS
}

// Advance the idle (non-working) side of the machine after a one-shot
// ends: stand → groom ×N → jump ×N → goHome → fallAsleep → sleep.
function advanceIdle(now: number) {
  if (state === 'stand') {
    if (groomLeft > 0) {
      groomLeft -= 1
      enterGroom(now)
    } else {
      enterJump(now)
    }
  } else if (state === 'groom') {
    if (groomLeft > 0) {
      groomLeft -= 1
      enterGroom(now)
    } else {
      jumpLeft = JUMP_ROUNDS
      enterJump(now)
    }
  } else if (state === 'jump') {
    if (jumpLeft > 1) {
      jumpLeft -= 1
      enterJump(now)
    } else {
      enterGoHome(now)
    }
  }
}

// --- tip mode -------------------------------------------------------------
// Compact walker for the todo tip bubble: walk a short segment along the
// text's baseline, sit for a groom, then pick another segment. Ported from
// the sample page's tip cat, which has always rendered cleanly.

function tipClamp(x: number): number {
  return Math.min(walkMax, Math.max(walkMin, x))
}

function tipPlanSeg(now: number) {
  measure()
  center = tipClamp(center)
  // Narrow tips (< ~2 cat widths of travel) just sit, then groom in place —
  // a forced long segment would overshoot the padded lane.
  const span = walkMax - walkMin
  if (span < 20) {
    tipWalkFrom = center
    tipWalkTo = center
    tipWalkStart = now + (tipFirst ? 520 : 800)
    tipFirst = false
    tipWalkEnd = tipWalkStart // zero-length walk → groom after the sit pause
    setSrc('sit')
    return
  }
  let room = dir > 0 ? walkMax - center : center - walkMin
  if (room < 24) {
    dir *= -1
    face(dir)
    room = dir > 0 ? walkMax - center : center - walkMin
  }
  const travel = Math.min(room, Math.max(36, Math.min(72, span * 0.55)))
  tipWalkFrom = center
  tipWalkTo = tipClamp(center + dir * travel)
  tipWalkStart = now + (tipFirst ? 520 : 160)
  tipFirst = false
  const dist = Math.abs(tipWalkTo - tipWalkFrom)
  tipWalkEnd = tipWalkStart + (dist / TIP_SPEED) * 1000
  setSrc('sit')
}

function tipTick(now: number) {
  measure()
  if (state === 'seg') {
    if (now < tipWalkStart) {
      center = tipClamp(tipWalkFrom)
    } else if (now >= tipWalkEnd) {
      center = tipClamp(tipWalkTo)
      const g = nextGroom()
      setSrc(g)
      state = 'groom'
      until = now + GROOM_MS[g]
    } else {
      setSrc('walk')
      const t = (now - tipWalkStart) / Math.max(1, tipWalkEnd - tipWalkStart)
      center = tipClamp(tipWalkFrom + (tipWalkTo - tipWalkFrom) * t)
    }
    place(center)
  } else if (state === 'groom') {
    center = tipClamp(center)
    place(center)
    if (now >= until) {
      state = 'seg'
      tipPlanSeg(now)
    }
  } else {
    state = 'seg'
    tipPlanSeg(now)
  }
}

function tick(now: number) {
  if (props.tip) {
    tipTick(now)
    raf = requestAnimationFrame(tick)
    return
  }
  if (props.working) {
    if (state === 'sleep') {
      enterWake(now)
    } else if (state === 'wake') {
      if (now >= until) enterWalk(now)
    } else if (state === 'walk') {
      // Pace back and forth across the composer, flipping at the edges.
      center += (dir * SPEED * 16.7) / 1000
      if (center >= walkMax) {
        center = walkMax
        dir = -1
        face(dir)
      } else if (center <= walkMin) {
        center = walkMin
        dir = 1
        face(dir)
      }
      place(center)
    } else if (state === 'goHome') {
      // A turn started on the way home → turn around and work again.
      enterWalk(now)
    } else {
      // stand / groom / jump / fallAsleep: finish the current one-shot
      // first (the "finish the clip before reacting" rule), then work.
      if (now >= until) {
        if (state === 'fallAsleep') {
          enterSleep(now)
          enterWake(now)
        } else {
          enterWalk(now)
        }
      }
    }
  } else {
    // Not working.
    if (state === 'wake') {
      // Turn ended before the wake one-shot finished → just stand there.
      if (now >= until) enterStand(now)
    } else if (state === 'walk') {
      // Turn ended mid-walk → stop where it is and settle into grooming.
      enterStand(now)
    } else if (state === 'stand' || state === 'groom') {
      if (now >= until) advanceIdle(now)
    } else if (state === 'jump') {
      // Travel sideways across the hop, landing at jumpTo when it ends.
      const t = until === jumpStart ? 1 : Math.min(1, (now - jumpStart) / (until - jumpStart))
      center = jumpFrom + (jumpTo - jumpFrom) * t
      place(center)
      if (now >= until) {
        center = jumpTo
        place(center)
        advanceIdle(now)
      }
    } else if (state === 'sleep') {
      // Slept long enough → wake on its own for a groom + hop cycle.
      if (now >= wakeAt) {
        groomLeft = GROOM_ROUNDS
        jumpLeft = JUMP_ROUNDS
        enterStand(now)
      }
    } else if (state === 'goHome') {
      center += (dir * GO_HOME_SPEED * 16.7) / 1000
      // Reached (or overshot) home in either direction → settle to sleep.
      const arrived = dir < 0 ? center <= HOME_X : center >= HOME_X
      if (arrived) {
        center = HOME_X
        place(center)
        enterFallAsleep(now)
      } else {
        place(center)
      }
    } else if (state === 'fallAsleep') {
      if (now >= until) enterSleep(now)
    }
  }
  raf = requestAnimationFrame(tick)
}

onMounted(() => {
  // Preload every asset so the first swap never flashes a broken frame.
  for (const key of Object.keys(SRC) as SrcKey[]) {
    const pre = new Image()
    pre.src = SRC[key]
  }
  measure()
  if (props.tip) {
    center = walkMin
    dir = 1
    face(dir)
    place(center)
    setSrc('sit')
    state = 'seg'
    tipPlanSeg(performance.now())
    // Tip width changes when the active task text updates; keep the lane
    // bounds in sync so a mid-walk cat can't outrun a shrinking pill.
    const lane = shiftEl.value?.parentElement
    if (lane && typeof ResizeObserver !== 'undefined') {
      tipRo = new ResizeObserver(() => {
        measure()
        center = tipClamp(center)
        place(center)
        if (state === 'seg') {
          tipWalkFrom = tipClamp(tipWalkFrom)
          tipWalkTo = tipClamp(tipWalkTo)
        }
      })
      tipRo.observe(lane)
    }
  } else {
    enterSleep(performance.now())
  }
  raf = requestAnimationFrame(tick)
})

watch(
  () => props.working,
  () => {
    if (props.tip) return
    // Mode changes are picked up on the next tick; nothing to force here,
    // because the tick reads props.working directly and one-shots honour
    // the "finish current clip first" rule. Re-measure the lane in case
    // the composer resized while we were away.
    measure()
  },
)

onBeforeUnmount(() => {
  cancelAnimationFrame(raf)
  tipRo?.disconnect()
  tipRo = null
})
</script>

<template>
  <span class="header-cat" :class="{ 'header-cat--tip': props.tip }" aria-hidden="true">
    <span ref="shiftEl" class="header-cat-shift">
      <span class="header-cat-fit">
        <img ref="imgEl" :src="props.tip ? SRC.sit : SRC.sleep" alt="" width="18" height="14" />
      </span>
    </span>
  </span>
</template>

<style scoped>
/* Lane stretched across the composer's top edge; the cat walks inside
 * it without affecting layout. */
.header-cat {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  height: 14px;
  pointer-events: none;
  z-index: 1;
  /* Composer lane stays overflow-visible so jump-scale can breathe; tip
   * mode clips hard so the walker never paints past the pill. */
  overflow: visible;
}
.header-cat--tip {
  overflow: hidden;
}
.header-cat-shift {
  position: absolute;
  left: 0;
  bottom: 0;
  width: 0;
  height: 0;
}
.header-cat-fit {
  position: absolute;
  left: calc(-1 * 18px / 2);
  bottom: 0;
  width: 18px;
  height: 14px;
}
.header-cat-fit.face-left,
.header-cat-shift.face-left .header-cat-fit {
  transform: scaleX(-1);
  transform-origin: center bottom;
}
.header-cat img {
  display: block;
  width: 18px;
  height: 14px;
}
/* The jump frames' visible region is ~25% smaller than the other clips',
 * so at 18×14 the hopping cat looks shrunken. Enlarge it to match, feet
 * anchored to the lane bottom. Keep the horizontal flip working by
 * scaling the img, not the fit box that face-left also transforms. */
.header-cat-fit.jump-scale img {
  position: absolute;
  bottom: 0; /* keep the feet on the lane bottom as the sprite grows up */
  left: 50%;
  transform: translateX(-50%); /* keep the enlarged sprite centred */
  width: 24px;
  height: 19px;
}
</style>
