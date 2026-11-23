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
//   wake       stand-up one-shot. Required bridge out of sleep — never
//              cut from the snore loop straight into a jump or a groom.
//   walk       slow pace only. Working turns and idle strolls use this.
//   run        the running GIF. Replaces every fast return / fast walk.
//   stand      short connective beat, or a longer idle linger.
//   groom      lick / wash / full groom, in place, from a stand.
//   look       head-tilt GIF: glance side to side, feet planted.
//   jump       playful hops (jumping GIF). Short, low, a mood — not travel.
//   bound      continuous travel bounces. The clip loops from cushion
//              straight into the next crouch; code moves it sideways.
//   stroll     idle slow walk to a random spot.
//   fallAsleep stand-to-sleep one-shot. Starts from a stand, wherever
//              the cat already is — a nap does not have to be at home.
// While waiting for the next message the cat does not follow a fixed
// script. Each finished beat rolls the next one (groom, look, hop,
// stroll, linger, or sleep). Clips only chain from a pose they can
// start in: sleep → wake → stand, then any awake beat; a hop run keeps
// one facing until it lands, then may plant its feet and hop back.
// Getting tired is one of those rolls. Sometimes it lies down on the
// spot; sometimes it runs home, or covers the distance in travel leaps,
// stands, then plays the stand-to-sleep clip.
// A new turn during groom/jump/stand/fallAsleep waits for the current
// one-shot to finish, then walks. A look, stroll, or trip home is
// already a soft pose, so the cat joins the working walk at once.
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

const props = withDefaults(defineProps<{ working?: boolean; tip?: boolean }>(), {
  // tip=true: the compact walker that lives inside the todo tip bubble —
  // a simple walk-a-segment / groom / walk-again loop with no sleep,
  // wake, jump or run states (the tip only exists while a turn runs,
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
  bound: '/app/cat/cat-jump-registered.gif',
  run: '/app/cat/cat-running.gif',
  tilt: '/app/cat/cat-head-tilt.gif',
  fallAsleep: '/app/cat/cat-stand-to-sleep.gif',
} as const
type SrcKey = keyof typeof SRC

// One-shot / loop durations (ms), from the source frames.json timings.
const WAKE_MS = 1490 // cat-wake-to-stand, 16-frame stand-up (loop:1)
const GROOM_MS: Record<string, number> = { lick: 3100, face: 3480, groomAll: 4280 }
const JUMP_MS = 1380 // playful hop, one low bounce
const BOUND_MS = 580 // continuous bounce; loops straight into the next crouch
// Pose clip has no baked travel. Takeoff is 120ms in, front paws touch at 470ms.
const BOUND_TAKEOFF = 120 / 580
const BOUND_LANDING = 470 / 580
const TILT_MS = 3070 // cat-head-tilt, one left-then-right glance
const FALL_ASLEEP_MS = 1440 // cat-stand-to-sleep, 16-frame lie-down (loop:1)
const GROOM_KEYS: SrcKey[] = ['lick', 'face', 'groomAll']

const CAT_W = 18
const SPEED = 26 // px/s while working — slow walk, matches the walk GIF
// Running replaces the old fast walk home. Tuned to the 560ms stride so
// the feet read as running rather than sliding.
const RUN_SPEED = 78
const RUN_CYCLE_MS = 560
const HOME_X = CAT_W / 2 // resting spot: left edge

// Idle nap length. Waking is on a loose timer so the cat is not on a clock.
const SLEEP_WAKE_MIN_MS = 7000
const SLEEP_WAKE_RAND_MS = 16000
// Playful hops stay short. A continuous travel bounce covers about two
// body lengths, mostly while the paws are in the air.
const PLAY_STEP = 7
const BOUND_STEP = 32
const PLAY_AIR_START = 580
const PLAY_AIR_END = 880
const STROLL_MIN_SPEED = 18
const STROLL_SPEED_SPAN = 16

type State =
  | 'sleep'
  | 'wake'
  | 'walk'
  | 'stand'
  | 'groom'
  | 'look'
  | 'jump'
  | 'stroll'
  | 'run'
  | 'fallAsleep'
  // tip mode only: mid-segment walk inside the tip bubble.
  | 'seg'

// What the idle cat does once it is standing. 'linger' is just standing
// a while — a valid "do nothing" beat, not a missing animation.
// 'jump-back' is not rolled; it continues a hop run in the other direction.
type IdleNext = 'groom' | 'jump' | 'bound' | 'look' | 'stroll' | 'run' | 'linger' | 'sleep'
type JumpKind = 'play' | 'bound'
type Held = IdleNext | 'jump-back' | 'roll' | 'nap'

let raf = 0
let state: State = 'sleep'
let current: SrcKey | '' = ''
let dir = 1 // 1 = facing right, -1 = facing left
let center = HOME_X
let until = 0 // timestamp the current one-shot / timed state ends
let walkMin = HOME_X
let walkMax = HOME_X
let wakeAt = 0 // idle: when the sleeping cat next wakes on its own
let awakeBeats = 0 // idle decisions since the last nap; biases toward sleep
let repeatsLeft = 0 // remaining grooms in the current beat
// Stand beat: 'roll' picks a fresh idle action when it ends; anything
// else is the action already chosen, waiting out the connective stand.
let held: Held | null = null
// jump: one hop's travel, plus the rest of the run in this direction.
let jumpFrom = HOME_X
let jumpTo = HOME_X
let jumpStart = 0
let jumpKind: JumpKind = 'play'
let jumpStep = PLAY_STEP
let jumpHopsLeft = 0
let jumpReturn = false
// Fast hop-home: travel leaps toward the left corner, then nap.
let jumpHome = false
let strollTo = HOME_X
let strollSpeed = SPEED
let runTo = HOME_X
let runSpeed = RUN_SPEED
let runHome = false
let runCycleStart = 0
let runSettleUntil = 0
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

// Playful hops only drift while airborne. A travel bounce follows the
// clip: a short crouch shove, the flight, then a brief cushion after the
// front paws touch. It does not keep sliding once the hop is over.
function boundTravel(p: number): number {
  const flight = BOUND_LANDING - BOUND_TAKEOFF
  const vx = 2 / (1 + flight)
  if (p <= BOUND_TAKEOFF) return (vx * p * p) / (2 * BOUND_TAKEOFF)
  if (p < BOUND_LANDING) return (vx * BOUND_TAKEOFF) / 2 + vx * (p - BOUND_TAKEOFF)
  const t = p - BOUND_LANDING
  const d = 1 - BOUND_LANDING
  return 1 - (vx * d) / 2 + vx * t - (vx * t * t) / (2 * d)
}

function jumpTravel(now: number): number {
  const dur = Math.max(1, until - jumpStart)
  const t = Math.min(1, Math.max(0, (now - jumpStart) / dur))
  if (jumpKind === 'bound') return boundTravel(t)
  const start = PLAY_AIR_START / JUMP_MS
  const end = PLAY_AIR_END / JUMP_MS
  if (t <= start) return 0
  if (t >= end) return 1
  const u = (t - start) / (end - start)
  return u * u * (3 - 2 * u)
}

function placeJump(now: number) {
  center = jumpFrom + (jumpTo - jumpFrom) * jumpTravel(now)
  place(center)
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

function nextGroom(avoid?: SrcKey): SrcKey {
  const pool = avoid ? GROOM_KEYS.filter(key => key !== avoid) : GROOM_KEYS
  const keys = pool.length > 0 ? pool : GROOM_KEYS
  return keys[Math.floor(Math.random() * keys.length)]
}

function pickWeighted(items: { key: IdleNext; w: number }[]): IdleNext {
  const total = items.reduce((sum, item) => sum + item.w, 0)
  let roll = Math.random() * total
  for (const item of items) {
    roll -= item.w
    if (roll <= 0) return item.key
  }
  return items[items.length - 1].key
}

// Roll the next idle beat. The first one after waking is never "go back
// to sleep" — the cat just stood up. Later beats slowly favour a nap so
// it does settle, without a fixed groom-then-jump script.
function chooseIdle(): IdleNext {
  const justWoke = awakeBeats === 0
  awakeBeats += 1
  const sleepW = justWoke ? 0 : Math.min(6, 1.1 + (awakeBeats - 1) * 0.85)
  return pickWeighted([
    { key: 'groom', w: 2.6 },
    { key: 'look', w: 2.2 },
    { key: 'jump', w: 2.4 },
    { key: 'bound', w: 2.2 },
    { key: 'stroll', w: 1.8 },
    { key: 'run', w: 2.0 },
    { key: 'linger', w: 1.2 },
    { key: 'sleep', w: sleepW },
  ])
}

function enterSleep(now: number) {
  state = 'sleep'
  awakeBeats = 0
  held = null
  jumpHome = false
  // Stay where the stand-to-sleep clip finished. Snapping back to the
  // corner would undo an in-place nap.
  dir = 1
  face(dir)
  place(center)
  setSrc('sleep')
  wakeAt = now + SLEEP_WAKE_MIN_MS + Math.random() * SLEEP_WAKE_RAND_MS
}

function enterWake(now: number) {
  // Sleep pose → sleepy stand-up. Callers decide walk vs idle afterwards.
  state = 'wake'
  held = null
  until = now + WAKE_MS
  setSrc('wake')
}

function enterStandBeat(now: number, ms: number, next: Held) {
  state = 'stand'
  held = next
  setSrc('stand')
  until = now + ms
}

// A short stand between two different kinds of motion, so the GIF swap
// reads as a pose change instead of a cut.
function bridgeThen(now: number, next: IdleNext) {
  enterStandBeat(now, 260 + Math.random() * 420, next)
}

function enterWalk(now: number) {
  state = 'walk'
  jumpHome = false
  runHome = false
  measure()
  // Walk toward the far edge from wherever we are.
  dir = center <= (walkMin + walkMax) / 2 ? 1 : -1
  face(dir)
  setSrc('walk')
}

function enterGroom(now: number) {
  const g = nextGroom(current && current in GROOM_MS ? current : undefined)
  state = 'groom'
  held = null
  setSrc(g)
  until = now + GROOM_MS[g]
}

function hopsAhead(d: number): number {
  const room = d > 0 ? walkMax - center : center - walkMin
  return Math.max(0, Math.floor(room / Math.max(8, jumpStep)))
}

function beginJumpRun(now: number) {
  measure()
  jumpHome = false
  jumpKind = 'play'
  // Little bounces. The playful clip is the low one; keep the travel short.
  jumpStep = PLAY_STEP * (0.75 + Math.random() * 0.5)
  const right = hopsAhead(1)
  const left = hopsAhead(-1)
  if (right < 1 && left < 1) {
    beginIdle(now, Math.random() < 0.5 ? 'look' : 'groom')
    return
  }
  let go = dir
  const ahead = go > 0 ? right : left
  if (ahead < 2 || Math.random() < 0.35) go = right >= left ? 1 : -1
  if ((go > 0 ? right : left) < 1) go = right >= left ? 1 : -1
  dir = go
  face(dir)
  const room = go > 0 ? right : left
  const want = 2 + Math.floor(Math.random() * 3) // 2–4 little hops
  jumpHopsLeft = Math.max(1, Math.min(want, room))
  jumpReturn = Math.random() < 0.55
  enterJump(now, true)
}

function beginBoundRun(now: number) {
  measure()
  jumpHome = false
  jumpKind = 'bound'
  jumpStep = BOUND_STEP * (0.85 + Math.random() * 0.35)
  const right = hopsAhead(1)
  const left = hopsAhead(-1)
  if (right < 1 && left < 1) {
    beginJumpRun(now)
    return
  }
  let go = dir
  if ((go > 0 ? right : left) < 1) go = right >= left ? 1 : -1
  else if (Math.random() < 0.4) go = right >= left ? 1 : -1
  dir = go
  face(dir)
  const room = go > 0 ? right : left
  const want = 1 + Math.floor(Math.random() * 2) // 1–2 long leaps
  jumpHopsLeft = Math.max(1, Math.min(want, room))
  jumpReturn = Math.random() < 0.4
  enterJump(now, true)
}

function enterJump(now: number, restart: boolean) {
  measure()
  face(dir)
  jumpFrom = center
  let target = center + dir * jumpStep
  const pastEdge = dir > 0 ? target > walkMax : target < walkMin
  if (pastEdge) {
    target = dir > 0 ? walkMax : walkMin
    if (Math.abs(target - center) < 6) {
      jumpHopsLeft = 0
      finishJumpRun(now)
      return
    }
  }
  jumpTo = Math.min(walkMax, Math.max(walkMin, target))
  state = 'jump'
  held = null
  const clip: SrcKey = jumpKind === 'bound' ? 'bound' : 'jump'
  // Keep the GIF in phase across hops of one run. Restart when a run begins.
  if (restart || current !== clip) setSrc(clip)
  jumpStart = now
  until = now + (jumpKind === 'bound' ? BOUND_MS : JUMP_MS)
  place(center)
}

function finishJumpRun(now: number) {
  if (jumpHome) {
    measure()
    const dist = Math.abs(center - HOME_X)
    if (dist > Math.max(12, jumpStep * 0.65)) {
      dir = center > HOME_X ? -1 : 1
      face(dir)
      const room = hopsAhead(dir)
      if (room >= 1) {
        jumpHopsLeft = room
        enterJump(now, false)
        return
      }
    }
    center = Math.min(walkMax, Math.max(walkMin, HOME_X))
    place(center)
    jumpHome = false
    // Hop GIF ends standing. Hold that pose, then sink into sleep.
    enterStandBeat(now, 160 + Math.random() * 220, 'nap')
    return
  }
  if (!jumpReturn) {
    afterIdleAction(now)
    return
  }
  jumpReturn = false
  measure()
  dir *= -1
  face(dir)
  const room = hopsAhead(dir)
  if (room < 1) {
    afterIdleAction(now)
    return
  }
  const want = 2 + Math.floor(Math.random() * 4)
  jumpHopsLeft = Math.max(1, Math.min(want, room))
  // Feet down, then the same kind of run back the other way.
  enterStandBeat(now, 120 + Math.random() * 260, 'jump-back')
}

function onJumpLanded(now: number) {
  center = jumpTo
  place(center)
  if (jumpHopsLeft > 1) {
    jumpHopsLeft -= 1
    enterJump(now, false)
    return
  }
  finishJumpRun(now)
}

function enterLook(now: number) {
  state = 'look'
  held = null
  // One glance, sometimes a second, so it lingers like it's watching.
  const cycles = Math.random() < 0.28 ? 2 : 1
  setSrc('tilt')
  until = now + TILT_MS * cycles
}

function enterStroll(now: number) {
  measure()
  const span = walkMax - walkMin
  if (span < 30) {
    // Nowhere to walk. Pick an in-place beat instead of rolling again,
    // which might land on another stroll.
    beginIdle(now, Math.random() < 0.5 ? 'look' : 'groom')
    return
  }
  let target = walkMin + Math.random() * span
  if (Math.abs(target - center) < Math.min(36, span * 0.35)) {
    target = center < (walkMin + walkMax) / 2 ? walkMax - span * 0.12 : walkMin + span * 0.12
  }
  strollTo = Math.min(walkMax, Math.max(walkMin, target))
  strollSpeed = STROLL_MIN_SPEED + Math.random() * STROLL_SPEED_SPAN
  dir = strollTo >= center ? 1 : -1
  face(dir)
  state = 'stroll'
  held = null
  setSrc('walk')
}

function beginIdle(now: number, next: IdleNext) {
  if (next === 'groom') {
    repeatsLeft = 1 + Math.floor(Math.random() * 3)
    enterGroom(now)
  } else if (next === 'jump') {
    beginJumpRun(now)
  } else if (next === 'bound') {
    beginBoundRun(now)
  } else if (next === 'run') {
    beginRun(now)
  } else if (next === 'look') {
    enterLook(now)
  } else if (next === 'stroll') {
    enterStroll(now)
  } else if (next === 'linger') {
    enterStandBeat(now, 1200 + Math.random() * 2400, 'roll')
  } else {
    beginSleepTrip(now)
  }
}

// Finished an awake beat. Linger is already a stand, so it starts
// immediately; every other choice gets a short standing bridge first.
function afterIdleAction(now: number) {
  const next = chooseIdle()
  if (next === 'linger') beginIdle(now, next)
  else bridgeThen(now, next)
}

function onStandFinished(now: number) {
  const next = held
  held = null
  if (next === 'jump-back') {
    enterJump(now, true)
    return
  }
  if (next === 'nap') {
    enterFallAsleep(now)
    return
  }
  if (next && next !== 'roll') beginIdle(now, next)
  else afterIdleAction(now)
}

function beginJumpHome(now: number) {
  measure()
  jumpHome = true
  jumpReturn = false
  jumpKind = 'bound'
  jumpStep = BOUND_STEP * (0.9 + Math.random() * 0.25)
  dir = center > HOME_X ? -1 : 1
  face(dir)
  const room = hopsAhead(dir)
  if (room < 1) {
    jumpHome = false
    enterFallAsleep(now)
    return
  }
  jumpHopsLeft = room
  enterJump(now, true)
}

function finishRun(now: number) {
  runSettleUntil = 0
  if (runHome) {
    runHome = false
    center = Math.min(walkMax, Math.max(walkMin, HOME_X))
    place(center)
    enterStandBeat(now, 160 + Math.random() * 200, 'nap')
    return
  }
  afterIdleAction(now)
}

function enterRunTo(now: number, target: number, home: boolean) {
  measure()
  runTo = Math.min(walkMax, Math.max(walkMin, target))
  if (Math.abs(center - runTo) <= 4) {
    if (home) {
      runHome = false
      enterFallAsleep(now)
    } else {
      afterIdleAction(now)
    }
    return
  }
  runHome = home
  runSpeed = RUN_SPEED * (0.9 + Math.random() * 0.25)
  runSettleUntil = 0
  runCycleStart = now
  dir = runTo >= center ? 1 : -1
  face(dir)
  state = 'run'
  held = null
  setSrc('run')
}

function beginRun(now: number) {
  measure()
  const span = walkMax - walkMin
  if (span < 48) {
    beginIdle(now, Math.random() < 0.5 ? 'stroll' : 'bound')
    return
  }
  const far = span * (0.45 + Math.random() * 0.4)
  let target = center + (Math.random() < 0.5 ? far : -far)
  if (Math.abs(target - center) < span * 0.3) {
    target = center < (walkMin + walkMax) / 2 ? walkMax : walkMin
  }
  enterRunTo(now, target, false)
}

function beginSleepTrip(now: number) {
  measure()
  const away = Math.abs(center - HOME_X) > 18
  // Tired in place is a real outcome, not a fallback for "already home".
  if (!away || Math.random() < 0.45) {
    enterFallAsleep(now)
    return
  }
  // Still away: run home, or cover it in travel leaps. Both end in a nap.
  if (Math.random() < 0.5) enterRunTo(now, HOME_X, true)
  else beginJumpHome(now)
}

function enterFallAsleep(now: number) {
  state = 'fallAsleep'
  dir = 1
  face(dir)
  setSrc('fallAsleep')
  until = now + FALL_ASLEEP_MS
}

function finishGroom(now: number) {
  if (repeatsLeft > 1) {
    repeatsLeft -= 1
    enterGroom(now)
    return
  }
  afterIdleAction(now)
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
    } else if (state === 'run' || state === 'stroll' || state === 'look') {
      // Already mid-motion, or just glancing — join the working walk now.
      enterWalk(now)
    } else if (state === 'jump') {
      // Land the hop that's already in the air, then walk.
      placeJump(now)
      if (now >= until) {
        center = jumpTo
        place(center)
        enterWalk(now)
      }
    } else {
      // stand / groom / fallAsleep: finish the current one-shot
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
      // Stood up. The turn is over, so roll an idle beat instead of walking.
      if (now >= until) afterIdleAction(now)
    } else if (state === 'walk') {
      // Turn ended mid-walk → stop, then pick an idle beat from a stand.
      afterIdleAction(now)
    } else if (state === 'stand') {
      if (now >= until) onStandFinished(now)
    } else if (state === 'groom') {
      if (now >= until) finishGroom(now)
    } else if (state === 'look') {
      if (now >= until) afterIdleAction(now)
    } else if (state === 'jump') {
      // Travel sideways across the hop, landing at jumpTo when it ends.
      placeJump(now)
      if (now >= until) onJumpLanded(now)
    } else if (state === 'sleep') {
      if (now >= wakeAt) {
        awakeBeats = 0
        enterWake(now)
      }
    } else if (state === 'stroll') {
      center += (dir * strollSpeed * 16.7) / 1000
      const arrived = dir > 0 ? center >= strollTo : center <= strollTo
      if (arrived) {
        center = strollTo
        place(center)
        afterIdleAction(now)
      } else {
        place(center)
      }
    } else if (state === 'run') {
      if (runSettleUntil > 0) {
        // Finish the stride in place so the loop doesn't cut mid-air.
        if (now >= runSettleUntil) finishRun(now)
      } else {
        center += (dir * runSpeed * 16.7) / 1000
        const arrived = dir > 0 ? center >= runTo : center <= runTo
        if (arrived) {
          center = runTo
          place(center)
          const into = (now - runCycleStart) % RUN_CYCLE_MS
          const remain = into < 50 ? 0 : RUN_CYCLE_MS - into
          if (remain > 40) runSettleUntil = now + remain
          else finishRun(now)
        } else {
          place(center)
        }
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
