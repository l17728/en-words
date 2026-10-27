import { clientId } from '../interaction/client-id'
import { AnswerFlight } from '../interaction/answer-flight'
import { speechCache } from '../audio/speech-cache'
import { useEffect, useRef, useState, useCallback } from 'react'
import { Alert, Button, Typography, Spin, Modal, Input, message, Tag } from 'antd'
import { CheckCircleFilled, CloseCircleFilled } from '@ant-design/icons'
import type { InputRef } from 'antd'
import { useParams, useNavigate } from 'react-router-dom'
import { api, ApiError, type FeedbackRecord } from '../api/client'
import { ClassroomWS, type Frame, type TurnContext } from '../ws/client'
import { PlaybackCaption, shouldCancelPlaybackPhase } from '../audio/caption'
import { capabilities, recordLocal, playLocalChinese, type Capabilities } from '../audio/local'
import { useAuthStore } from '../stores/auth'
import { useFeedback } from '../feedback/useFeedback'
import { FeedbackResults } from '../feedback/FeedbackResults'
import { markFeedbackPending } from '../feedback/pending'
import { InputReadiness, sameContext, dispatchClassroom, type InputTurn } from '../ws/input-readiness'

// 协同课堂页（docs/07 §6.3）：WS 驱动（R4-fe#1），REST 仅 enter 取 ticket。
// REPEATING 禁 TTS（回声抑制 FE-02/R4-fe#2）；语音不可用降级视觉动画。

interface EnterResult {
  ticket: string
  snapshot: Snapshot
  status: string
}
interface Snapshot {
  teacher_text?: string
  context?: TurnContext
  phase: string
  word?: string
  quiz_word?: string
  zhMeaning?: string // 教学阶段中英对照（来自 play_tts zh 段文本）
  paused?: boolean
}

const PHASE_TIP: Record<string, string> = {
  show_word: '点一下这个单词，开始学习',
  tts_seg: '叮当老师在读，注意听～',
  repeating: '轮到你读了！大声一点没关系',
  wait_answer: '这个单词是什么意思？',
  judging: '叮当老师正在判分…',
  reteach_show: '再认识一遍这个词',
  lesson_end: '下课啦！',
}

// TTS 可观测探针（铁律 3 定位日志）：window.__ttsDebug 暴露每段 voice 选择与 onstart 实况。
// 探针证明浏览器生命周期；扬声器实际可听性由独立真实媒体验收证明。
interface TtsDebugEntry {
  textLength: number
  lang: string
  voice: string | null
  local: boolean | null
  status: 'pending' | 'started' | 'completed' | 'failed'
  failure?: string
  startMs: number | null // null=onstart 从未触发（看门狗/静默路径）
}

export default function ClassroomPage() {
  const { lessonId } = useParams()
  const nav = useNavigate()
  useEffect(() => () => speechCache.clear(), [])
  const userId = useAuthStore(s => s.userId)
  const sessionToken = useAuthStore(s => s.token)
  const feedback = useFeedback(userId)
  const feedbackRef = useRef(feedback)
  feedbackRef.current = feedback
  const voiceFeedbackRef = useRef(new Map<string, { id: string; text: string; timer?: ReturnType<typeof setTimeout> }>())
  const feedbackLifetime = useRef(0)
  const [undeliveredFeedback, setUndeliveredFeedback] = useState<{ id: string; original: string; text: string } | null>(null)
  const [resendingFeedback, setResendingFeedback] = useState(false)
  const queryVoiceFeedback = useCallback(async (id: string, text: string) => {
    const lifetime = feedbackLifetime.current
    try { await feedbackRef.current.query(id) }
    catch (error) {
      if (lifetime !== feedbackLifetime.current) return
      if (error instanceof ApiError && error.code === 404) setUndeliveredFeedback({ id, original: text, text })
      else setSpeechError('反馈处理状态暂时未知，可到课程表查询反馈历史。')
    }
  }, [])
  useEffect(() => {
    const pendingVoiceFeedback = voiceFeedbackRef.current // This Map instance is never replaced.
    feedbackLifetime.current++
    setUndeliveredFeedback(null)
    setResendingFeedback(false)
    setSpeechError('')
    return () => {
      // eslint-disable-next-line react-hooks/exhaustive-deps -- Invalidate the latest feedback generation before any late query can apply.
      feedbackLifetime.current++
      for (const entry of pendingVoiceFeedback.values()) if (entry.timer) clearTimeout(entry.timer)
      pendingVoiceFeedback.clear()
    }
  }, [lessonId, userId, sessionToken])
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const [providers, setProviders] = useState<Capabilities | null>(null)
  const providersRef = useRef<Capabilities | null>(null)
  const localAudioRef = useRef<AbortController | null>(null)
  const localRecordingRef = useRef<AbortController | null>(null)
  const [speechError, setSpeechError] = useState('')
  const [captionText, setCaptionText] = useState('')
  const captionRef = useRef<PlaybackCaption | null>(null)
  if (!captionRef.current) captionRef.current = new PlaybackCaption(setCaptionText)
  const [connectionError, setConnectionError] = useState<{ message: string; retryable: boolean } | null>(null)
  useEffect(() => {
    let disposed = false
    capabilities().then(p => { if (!disposed) { providersRef.current = p; setProviders(p) } }).catch(() => {})
    return () => { disposed = true; localAudioRef.current?.abort() }
  }, [])
  const [bubble, setBubble] = useState('')
  const [judge, setJudge] = useState<'ok' | 'bad' | null>(null)
  const [answer, setAnswer] = useState('')
  const [ended, setEnded] = useState(false)
  const [summary, setSummary] = useState<Record<string, any> | null>(null)
  const [wsOk, setWsOk] = useState(false)
  const onlineRef = useRef(false)
  const inputGate = useRef(new InputReadiness())
  const answerFlight = useRef(new AnswerFlight())
  const [answerPending, setAnswerPending] = useState(false)
  const [inputTurn, setInputTurn] = useState<InputTurn | null>(null)
  const revokeInput = () => { answerFlight.current.reset(); setAnswerPending(false); inputGate.current.revoke(); setInputTurn(null); stopListening() }
  const grantInput = (context: TurnContext, phase: InputTurn['phase']) => {
    if (inputGate.current.grant(context, phase)) setAnswer('')
    setInputTurn(inputGate.current.ready)
  }
  const [resuming, setResuming] = useState(false)
  const [resumeError, setResumeError] = useState('')
  const resumeRequestRef = useRef(0)
  const lastFrameRef = useRef({ epoch: '', seq: 0 })
  const inputRef = useRef<InputRef | null>(null)
  const wsRef = useRef<ClassroomWS | null>(null)
  const phaseRef = useRef<string>('show_word')
  const ttsAvail = useRef<boolean>(typeof window !== 'undefined' && 'speechSynthesis' in window)
  // voices 预热+缓存：Chrome 的语音列表异步加载，未就绪时 speak() 静默失败（曾致"头几个词哑火"）。
  // 页面加载即触发 getVoices + voiceschanged 监听重取，确保第一词就有声。
  // voice 显式选择（2026-08-26 无声 BUG 实证修复）：真实 Chrome 默认会选中 Google 在线语音
  // （国内网络不可达→speak 静默挂起，看门狗推进=课堂正常但无声）。严格本地优先，绝不主动选网络语音：
  // 精确lang本地 → 同语言族本地；无匹配本地voice明确失败，禁止静默默认网络语音。
  const voicesRef = useRef<SpeechSynthesisVoice[]>([])
  useEffect(() => {
    if (!('speechSynthesis' in window)) return
    const grab = () => { voicesRef.current = window.speechSynthesis.getVoices() }
    grab()
    window.speechSynthesis.onvoiceschanged = grab
    return () => { window.speechSynthesis.onvoiceschanged = null }
  }, [])
  const pickVoice = useCallback((lang: string): SpeechSynthesisVoice | null => {
    const vs = voicesRef.current
    if (!vs.length) return null
    const base = lang.split('-')[0]
    return (
      vs.find((v) => v.localService && v.lang === lang) ||
      vs.find((v) => v.localService && v.lang.startsWith(base)) ||
      // 不再 fallback 到任意本地 voice（zh-CN voice 读英文=用户听到中文腔→根因）
      // 返回null走明确失败与文字提示，不假报已播放。
      null
    )
  }, [])
  // TTS 完成回执通过 WS 上报（声明在前供 speak 闭包引用）
  const contextRef = useRef<TurnContext>({ epoch: "", turn: "" })
  const eventCounterRef = useRef(0)
  const eventPrefixRef = useRef(clientId())
  const sendEventRef = useRef<((ev: string, text?: string, context?: TurnContext) => void) | null>(null)
  const sendVoiceRef = useRef<((text: string, context?: TurnContext) => void) | null>(null)

  // ===== TTS 串行播放器（修复：连续段被吞/哑火——cancel+speak 快速连发会杀掉未入队段）=====
  // 每段排队逐个播；仅onend成功回执，看门狗/error失败不回执，由服务端显式超时跳段。
  const ttsQueueRef = useRef<Array<{ text: string; lang: 'en-US' | 'zh-CN'; context: TurnContext; delayMs: number; segment: number }>>([])
  const ttsBusyRef = useRef(false)
  const ttsEpochRef = useRef(0)
  const ttsTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const ttsDelayTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const cancelTts = useCallback(() => {
    ttsEpochRef.current++
    captionRef.current?.reset()
    localAudioRef.current?.abort()
    ttsQueueRef.current = []
    ttsBusyRef.current = false
    if (ttsTimerRef.current) clearTimeout(ttsTimerRef.current)
    if (ttsDelayTimerRef.current) clearTimeout(ttsDelayTimerRef.current)
    ttsTimerRef.current = null
    ttsDelayTimerRef.current = null
    try { window.speechSynthesis?.cancel() } catch { /* unavailable */ }
  }, [])
  const segDur = useCallback((text: string, lang: string): number => {
    const per = lang === 'zh-CN' ? 250 : 300
    return Math.max(800, text.length * per) + 400
  }, [])

  // pumpQueue 取队首播放；REPEATING 阶段静默跳过（回声抑制 FE-02）
  const pumpTts = useCallback(() => {
    if (ttsBusyRef.current) return
    const next = ttsQueueRef.current.shift()
    if (!next) return
    const epoch = ttsEpochRef.current
    ttsBusyRef.current = true
    const startPlayback = () => {
    ttsDelayTimerRef.current = null
    if (epoch !== ttsEpochRef.current) return
    if (next.context.epoch !== contextRef.current.epoch || next.context.turn !== contextRef.current.turn) {
      ttsBusyRef.current = false
      return
    }
    let fired = false
    let captionLease = captionRef.current!.begin()
    const started = () => {
      if (!fired && epoch === ttsEpochRef.current) captionRef.current?.show(captionLease, next.text)
    }
    const dbg: TtsDebugEntry = { textLength: next.text.length, lang: next.lang, voice: null,
      local: null, startMs: null, status: 'pending' }
    const w = window as typeof window & { __ttsDebug?: TtsDebugEntry[] }
    ;(w.__ttsDebug ??= []).push(dbg)
    const complete = (failure?: string) => {
      if (fired || epoch !== ttsEpochRef.current) return
      fired = true
      captionRef.current?.clear(captionLease)
      if (ttsTimerRef.current) clearTimeout(ttsTimerRef.current)
      ttsTimerRef.current = null
      ttsBusyRef.current = false
      dbg.status = failure ? 'failed' : 'completed'
      if (failure) {
        dbg.failure = failure
        localAudioRef.current?.abort()
        try { window.speechSynthesis?.cancel() } catch { /* unavailable */ }
        setSpeechError('语音播放失败，请先看屏幕文字；老师会稍后继续。')
      }
      console.info(failure ? '[playback_failed]' : '[playback_completed]', {
        lesson_id: lessonId, epoch: next.context.epoch, turn: next.context.turn,
        segment: next.segment, language: next.lang, text_length: next.text.length, failure })
      if (!failure) sendEventRef.current?.('tts_finished', undefined, next.context)
      pumpTts()
    }
    const once = () => complete()
    if (phaseRef.current === 'repeating') { ttsBusyRef.current = false; return }
    if (!ttsAvail.current && !(next.lang === 'zh-CN' && providersRef.current?.tts_zh)) {
      complete('unavailable')
      return
    }
    const browserSpeak = () => { if (epoch !== ttsEpochRef.current) return; try {
      const u = new SpeechSynthesisUtterance(next.text)
      // 显式选 voice：防"浏览器默认选中不可用 voice→静默挂起"（2026-08-26 无声 BUG 根因）
      const v = pickVoice(next.lang)
      if (v) {
        u.voice = v
        u.lang = v.lang // 与选中 voice 对齐（中文 voice 兜底读英文时以 voice 引擎为准）
      } else {
        complete('no_local_voice')
        return
      }
      u.rate = next.lang === 'en-US' ? 0.8 : 0.95
      console.log(`[TTS-pump] SPEAK voice=${v?.name ?? 'null'} local=${v?.localService ?? 'null'} u.lang=${u.lang} u.rate=${u.rate}`)
      // 可观测探针：记录本段 voice 决策（onstart 后补记实际发声时刻）
      dbg.voice = v.name; dbg.local = v.localService
      const t0 = performance.now()
      u.onstart = () => { if (!fired && epoch === ttsEpochRef.current) { dbg.startMs = Math.round(performance.now() - t0); dbg.status = 'started'; started() } }
      u.onend = once
      u.onerror = () => complete('engine_error')
      window.speechSynthesis.speak(u)
      // 看门狗：onend 失联=引擎挂起（如 Google 网络语音不可达）→ cancel 清挂起段，防阻塞后续所有段
      ttsTimerRef.current = setTimeout(() => {
        complete('timeout')
      }, segDur(next.text, next.lang) * 2.5)
    } catch {
      complete('engine_exception')
    } }
    if (next.lang === 'zh-CN' && providersRef.current?.tts_zh) {
      const controller = new AbortController()
      localAudioRef.current = controller
      playLocalChinese(next.text, controller.signal, lessonId, started).then(once).catch(() => {
        if (controller.signal.aborted || fired || epoch !== ttsEpochRef.current) return
        captionRef.current?.clear(captionLease)
        captionLease = captionRef.current!.begin()
        setSpeechError('本机中文朗读暂不可用，已尝试浏览器语音。')
        browserSpeak()
      })
    } else browserSpeak()
    }
    console.info('[tts_segment_scheduled]', { lesson_id: lessonId, epoch: next.context.epoch,
      turn: next.context.turn, segment: next.segment, language: next.lang, delay_before_ms: next.delayMs })
    if (next.delayMs > 0) ttsDelayTimerRef.current = setTimeout(startPlayback, next.delayMs)
    else startPlayback()
  }, [segDur, pickVoice, lessonId])

  // speak = 入队（保持外部签名；word 参数已内聚在 text）
  const speak = useCallback(
    (text: string, lang: 'en-US' | 'zh-CN', delayBeforeMs: unknown = 0, segment = 0) => {
      console.debug('[classroom_media]', { lesson_id: lessonId, phase: phaseRef.current })
      // R6-fe#1：REPEATING 相位不入队（回声抑制入口守卫——防相位切换竞态时旧段发声）
      if (phaseRef.current === 'repeating') { console.log('[TTS-speak] BLOCKED by repeating phase'); return }
      const delayMs = typeof delayBeforeMs === 'number' && Number.isFinite(delayBeforeMs)
        ? Math.min(10_000, Math.max(0, Math.floor(delayBeforeMs))) : 0
      ttsQueueRef.current.push({ text, lang, context: { ...contextRef.current }, delayMs, segment })
      pumpTts()
    },
    [pumpTts, lessonId],
  )
  // 阶段切换清队列（防旧词语音溢到新词）；R5：cancel 中断正在播的段+清 timer
  useEffect(() => {
    return () => {
      cancelTts()
    }
  }, [cancelTts, lessonId])

  const applyFrames = useCallback(
    (frames: Frame[]) => {
      for (const f of frames) {
        handleFrame(f)
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [speak, userId, sessionToken],
  )

  const handleFrame = useCallback(
    (f: Frame) => {
      if (useAuthStore.getState().userId !== userId || localStorage.getItem('token') !== sessionToken) return
      // Feedback has its own durable identity/version, independent of classroom
      // playback seq and turn. A late task result must not move the lesson.
      if (f.type === 'feedback_reply') {
        const record = f.feedback as FeedbackRecord | undefined
        if (record) {
          feedbackRef.current.accept(record)
          for (const [event, entry] of voiceFeedbackRef.current) if (entry.id === record.submission_id) {
            if (entry.timer) clearTimeout(entry.timer)
            voiceFeedbackRef.current.delete(event)
          }
        } else if (f.reply) feedbackRef.current.accept({ id: 0, status: f.status || 'processing', reply: f.reply, text: '', created_at: '' })
        else if (f.error_code) setSpeechError(f.msg || '反馈未完成，请查询反馈历史。')
        return
      }
      if ((f.type === 'event_rejected' || f.type === 'event_processed') && f.event_id) {
        if ((f.type === 'event_rejected' || f.error_code || f.command === 'feedback') && answerFlight.current.reject(f.event_id)) setAnswerPending(false)
        const pending = voiceFeedbackRef.current.get(f.event_id)
        if (pending && f.type === 'event_rejected') void queryVoiceFeedback(pending.id, pending.text)
        if (pending && f.command === 'feedback' && typeof f.normalized_text === 'string') pending.text = f.normalized_text
        if (!pending && f.command === 'feedback' && f.submission_id && typeof f.normalized_text === 'string') {
          markFeedbackPending(userId, f.submission_id, true)
          const entry = { id: f.submission_id as string, text: f.normalized_text as string,
            timer: setTimeout(() => { void queryVoiceFeedback(f.submission_id, f.normalized_text) }, 5000) }
          voiceFeedbackRef.current.set(f.event_id, entry)
        }
      }
      if (f.context?.epoch && typeof f.seq === 'number') {
        // One server action can arrive over WS and its REST response. Old frames
        // must not reset context or restart speech after a newer segment arrived.
        if (contextRef.current.epoch && f.context.epoch !== contextRef.current.epoch) return
        const last = lastFrameRef.current
        if (last.epoch === f.context.epoch && f.seq <= last.seq) return
        lastFrameRef.current = { epoch: f.context.epoch, seq: f.seq }
      }
      if (f.context) {
        if (inputGate.current.ready && !sameContext(inputGate.current.ready, f.context)) revokeInput()
        contextRef.current = f.context
      }
      if ((f.type === 'state_snapshot' && (f.paused || (inputGate.current.ready && f.phase !== inputGate.current.ready.phase))) ||
        (f.type === 'phase_changed' && inputGate.current.ready && f.phase !== inputGate.current.ready.phase) ||
        ['session_timeout', 'classroom_error', 'lesson_summary'].includes(f.type) ||
        (f.type === 'ui_action' && ['show_word', 'play_tts', 'lesson_paused', 'lesson_summary'].includes(f.action?.type))) revokeInput()
      if ((f.type === 'state_snapshot' && shouldCancelPlaybackPhase(f.phase, !!f.paused)) || f.type === 'session_timeout' ||
        f.type === 'lesson_summary' ||
        (f.type === 'phase_changed' && shouldCancelPlaybackPhase(f.phase)) ||
        (f.type === 'ui_action' && ['show_word', 'start_repeat', 'show_quiz_item', 'lesson_summary', 'lesson_paused'].includes(f.action?.type))) {
        cancelTts()
      }
      if (f.type === 'classroom_error') {
        cancelTts(); stopListening()
        const terminal = f.retryable === false || f.code === 'transaction_outcome_uncertain'
        if (terminal) {
          setConnectionError({ message: '课堂状态无法确认，为避免重复操作已停止本课。请返回课程表查看进度。', retryable: false })
          return
        }
        setConnectionError({ message: '课堂动作暂未完成，请重新连接继续当前进度。', retryable: true })
        return
      }
      switch (f.type) {
        case 'state_snapshot':
          if (typeof f.teacher_text === 'string') setBubble(f.teacher_text)
          console.debug('[classroom_media]', { lesson_id: lessonId, phase: phaseRef.current })
          setSnapshot({ phase: f.phase, word: f.word, quiz_word: f.quiz_word,
            zhMeaning: f.zhMeaning, paused: f.paused })
          phaseRef.current = f.phase
          // 朗读唯一驱动源=play_tts 帧（此快照朗读曾是"英文读两遍"根因——删除）
          break
        case 'phase_changed':
          phaseRef.current = f.phase
          setSnapshot((s) => (s ? { ...s, phase: f.phase } : s))
          break
        case 'ui_action': {
          const a = f.action ?? {}
          switch (a.type) {
            case 'lesson_paused':
              setSnapshot(s => s ? { ...s, paused: true } : s)
              break
            case 'show_word':
              console.debug('[classroom_media]', { lesson_id: lessonId, phase: phaseRef.current })
              setSnapshot((s) => ({ ...(s ?? { phase: 'show_word' }), phase: 'show_word', paused: false,
                word: a.word, zhMeaning: Array.isArray(a.meanings) && a.meanings.length > 0 ? a.meanings[0] : s?.zhMeaning })) // R5：meanings 防空保旧
              setJudge(null)
              phaseRef.current = 'show_word'
              break
            case 'play_tts':
              // A new server segment supersedes any timed-out previous segment.
              cancelTts()
              setSnapshot(s => s ? { ...s, paused: false } : s)
              phaseRef.current = 'tts_seg'
              console.debug('[classroom_media]', { lesson_id: lessonId, phase: phaseRef.current })
              // Meanings：en 首段即携带中文释义（点击即显示中英对照——原始需求）
              {
                const zh = a.lang === 'zh' && a.word ? a.word
                  : Array.isArray(a.meanings) && a.meanings.length > 0 ? a.meanings[0] : ''
                if (zh) {
                  setSnapshot((s) => ({ ...(s ?? { phase: 'tts_seg' }), phase: 'tts_seg',
                    word: s?.word, zhMeaning: zh }))
                }
              }
              if (a.word) {
                console.debug('[classroom_media]', { lesson_id: lessonId, phase: phaseRef.current })
                speak(a.word, a.lang === 'zh' ? 'zh-CN' : 'en-US', a.delay_before_ms, a.seg_idx)
              }
              break
            case 'start_repeat':
              console.log(`[TTS-recv] start_repeat phase=${phaseRef.current} ttsQueueLen=${ttsQueueRef.current.length}`)
              setSnapshot((s) => ({ ...(s ?? { phase: 'repeating' }), phase: 'repeating', paused: false }))
              if (f.context) grantInput(f.context, 'repeating')
              phaseRef.current = 'repeating' // 禁后续 TTS（回声抑制）
              break
            case 'show_quiz_item':
              setSnapshot((s) => ({ ...(s ?? { phase: 'wait_answer' }), phase: 'wait_answer', quiz_word: a.word, paused: false }))
              phaseRef.current = 'wait_answer'
              setJudge(null)
              if (f.context) grantInput(f.context, 'wait_answer')
              setTimeout(() => inputRef.current?.focus(), 100)
              break
            case 'show_check':
              setJudge('ok')
              break
            case 'show_cross':
              setJudge('bad')
              break
            case 'say_hint': // 两次机会用尽：老师安抚"没关系，记住它，我们继续"
              setBubble(a.reason === 'give_up_next' ? '没关系，这个词先记住样子，我们继续下一个！' : (a.text ?? ''))
              setJudge('bad')
              setTimeout(() => setJudge(null), 1200)
              break
            case 'say':
              setBubble(a.text ?? '')
              break
            case 'nudge_repeat':
              setBubble(a.text ?? '再试一次，大声读出来～')
              break
            case 'nudge_answer':
              setBubble(a.text ?? '想起来了吗？说出来试试，说错也没事～')
              break
            case 'start_quiz': // final 唯一测试（D-21：教学中零测试，教完才测）
              setBubble('词都学完了！来做个完整测试，正常发挥就好～')
              break
            case 'lesson_summary':
              setEnded(true)
              break
          }
          break
        }
        case 'judge_result':
          setJudge(f.judgment === 'correct' || f.judgment === 'near' ? 'ok' : 'bad')
          break
        case 'lesson_summary':
          setEnded(true)
          setSummary(f.report ?? null)
          break
        case 'settlement_failed':
          setSpeechError('课报暂未保存，正在重试。请暂时留在课堂。')
          break
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [speak, cancelTts, userId, sessionToken],
  )

  useEffect(() => {
    let ws: ClassroomWS | null = null
    let disposed = false
    const current = () => !disposed && useAuthStore.getState().userId === userId && localStorage.getItem('token') === sessionToken
    cancelTts()
    stopListening()
    onlineRef.current = false; inputGate.current.reset(); setInputTurn(null); setAnswer('')
    setSnapshot(null); setBubble(''); setEnded(false); setSummary(null); setWsOk(false)
    resumeRequestRef.current++
    lastFrameRef.current = { epoch: '', seq: 0 }
    setResuming(false)
    setResumeError('')
    setConnectionError(null)
    ws = new ClassroomWS(Number(lessonId), async () => {
      onlineRef.current = false; revokeInput()
      const r = await api.post<EnterResult>(`/classroom/lessons/${lessonId}/enter`)
      if (!current()) throw new ApiError('Session changed', 401)
      setSnapshot(r.snapshot)
      if (typeof r.snapshot.teacher_text === 'string') setBubble(r.snapshot.teacher_text)
      if (r.snapshot.context) {
        if (contextRef.current.epoch !== r.snapshot.context.epoch) lastFrameRef.current = { epoch: '', seq: 0 }
        contextRef.current = r.snapshot.context
      }
      phaseRef.current = r.snapshot.phase
      return r.ticket
    }, {
      onFrame: frame => { if (current()) handleFrame(frame) },
      onOpen: () => { if (current()) { onlineRef.current = true; setWsOk(true); setConnectionError(null) } },
      onClose: () => { if (current()) { onlineRef.current = false; revokeInput(); setWsOk(false); cancelTts() } },
      onError: (message, retryable) => { if (current()) { onlineRef.current = false; revokeInput(); setWsOk(false); setConnectionError({ message, retryable }) } },
    })
    wsRef.current = ws
    // eslint-disable-next-line react-hooks/exhaustive-deps -- Increment the latest resume generation; capturing an old scalar would not invalidate all requests.
    return () => { disposed = true; onlineRef.current = false; inputGate.current.reset(); resumeRequestRef.current++; ws?.close(); wsRef.current = null; cancelTts(); stopListening() }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lessonId, userId, sessionToken])

  // ===== 默认语音监听模式（用户定案：语音是默认作答方式，无需点击按钮）=====
  // wait_answer 相位自动启动 SpeechRecognition；识别结果→sendVoice（voice_text 管线）。
  // E2E/不支持环境：window.__TEST_VOICE__ 注入 mock 或降级文本框。
  const recogRef = useRef<any>(null)
  const listenActiveRef = useRef(false)
  const [listening, setListening] = useState(false)
  const [localStage, setLocalStage] = useState<'idle' | 'preparing' | 'recording' | 'transcribing'>('idle')

  const stopListening = useCallback(() => {
    localRecordingRef.current?.abort()
    listenActiveRef.current = false
    setListening(false)
    setLocalStage('idle')
    try {
      recogRef.current?.stop()
    } catch {
      /* noop */
    }
    recogRef.current = null
  }, [])

  const startListening = useCallback(() => {
    if (providersRef.current?.local) return // Local mode never silently invokes a browser cloud recognizer.
    if (listenActiveRef.current) return
    const capture = inputGate.current.ready
    if (!capture || !inputGate.current.accepts(contextRef.current, phaseRef.current, onlineRef.current, capture)) return
    // E2E 测试模式：URL 带 ?e2e=1 时禁自动监听（避免 TTS 朗读被拾音→自动提交
    // 与测试显式提交叠加造成"回答两次"；真实拾音路径由 omni 回环测试覆盖）
    if (new URLSearchParams(window.location.search).get('e2e') === '1') return
    const W = window as any
    const SR = W.SpeechRecognition || W.webkitSpeechRecognition
    if (!SR) return // 不支持：文本框兜底（输入框已自动聚焦）
    listenActiveRef.current = true
    setListening(true)
    const recog = new SR()
    const captureToken = localStorage.getItem('token')
    recogRef.current = recog
    // 跟读判读听英文（学生读单词）；答题听中文（说释义）——按当前相位选语言
    recog.lang = phaseRef.current === 'repeating' ? 'en-US' : 'zh-CN'
    recog.interimResults = false
    recog.continuous = false
    const captureContext = { epoch: capture.epoch, turn: capture.turn }
    const captureCurrent = () => inputGate.current.accepts(contextRef.current, phaseRef.current, onlineRef.current, capture)
    recog.onstart = () => { if (!captureCurrent()) { try { recog.stop() } catch { /* stale capture */ } } }
    recog.onresult = (ev: any) => {
      if (!listenActiveRef.current || recogRef.current !== recog || localStorage.getItem('token') !== captureToken || !captureCurrent()) return
      const text = ev.results?.[0]?.[0]?.transcript || ''
      if (text) sendVoiceRef.current?.(text.trim(), captureContext) // 语音作答/判读/指令统一管线
    }
    recog.onend = () => {
      if (recogRef.current !== recog || localStorage.getItem('token') !== captureToken || !captureCurrent()) return
      // 相位仍在跟读/答题且未停止 → 持续监听（用户可以说多次）
      if (listenActiveRef.current &&
          (phaseRef.current === 'wait_answer' || phaseRef.current === 'repeating')) {
        try {
          recog.start()
        } catch {
          /* restart 竞态忽略 */
        }
      } else {
        listenActiveRef.current = false
        setListening(false)
      }
    }
    recog.onerror = () => {
      /* onend 会接手重启/停止 */
    }
    try {
      recog.start()
    } catch {
      listenActiveRef.current = false
      setListening(false)
    }
  }, [])

  const sendEvent = (event: string, text?: string, context = contextRef.current) => {
    if (event === 'answer_submitted' && (localStorage.getItem('token') !== sessionToken ||
      phaseRef.current !== 'wait_answer' || !inputGate.current.answer(context, onlineRef.current, text || ''))) return
    const envelope = { context, event_id: `${eventPrefixRef.current}-${++eventCounterRef.current}` }
    if (event === 'answer_submitted') {
      if (answerFlight.current.send(context, envelope.event_id, () => wsRef.current?.sendEvent(event, text, envelope) ?? false)) { setAnswerPending(true); stopListening() }
      return
    }
    dispatchClassroom(() => wsRef.current?.sendEvent(event, text, envelope) ?? false, () => {
      // WS 未连接降级 REST（双通道保障）
      api
        .post<Frame[]>(`/classroom/lessons/${lessonId}/event`, { event, text, ...envelope })
        .then(applyFrames)
        .catch((e) => message.error((e as Error).message))
    }, event === 'answer_submitted')
  }
  sendEventRef.current = sendEvent // TTS onend 回执上报通道

  const sendVoice = (text: string, capturedContext?: TurnContext) => {
    const context = capturedContext ?? contextRef.current
    if (!text.trim() || localStorage.getItem('token') !== sessionToken) return
    if ((capturedContext || ['wait_answer', 'repeating'].includes(phaseRef.current)) &&
      !inputGate.current.accepts(context, phaseRef.current, onlineRef.current)) return
    const lifetime = feedbackLifetime.current
    const envelope = { context, event_id: `${eventPrefixRef.current}-${++eventCounterRef.current}` }
    const submissionId = clientId()
    // This conservative prefix check only exposes a recovery UI; the server is
    // authoritative for classification and returns its exact normalized body.
    const match = text.trim().match(/^(?:我想反馈|我要反馈|我反馈|提个意见|反馈|建议)\s*(.+)$/s)
    if (match) {
      markFeedbackPending(userId, submissionId, true)
      const pending = { id: submissionId, text: match[1], timer: undefined as ReturnType<typeof setTimeout> | undefined }
      pending.timer = setTimeout(() => { void queryVoiceFeedback(pending.id, pending.text) }, 5000)
      voiceFeedbackRef.current.set(envelope.event_id, pending)
      while (voiceFeedbackRef.current.size > 128) {
        const oldest = voiceFeedbackRef.current.keys().next().value!
        clearTimeout(voiceFeedbackRef.current.get(oldest)?.timer)
        voiceFeedbackRef.current.delete(oldest)
      }
    }
    if (phaseRef.current === 'wait_answer' && !match) {
      if (answerFlight.current.send(context, envelope.event_id, () => wsRef.current?.sendVoiceText(text, envelope, submissionId) ?? false)) { setAnswerPending(true); stopListening() }
      return
    }
    dispatchClassroom(() => wsRef.current?.sendVoiceText(text, envelope, submissionId) ?? false, () => {
      api
        .post<{ command: string; frames: Frame[]; feedback?: FeedbackRecord }>(`/classroom/lessons/${lessonId}/voice`, { text, ...envelope, submission_id: submissionId })
        .then((r) => { if (lifetime !== feedbackLifetime.current || localStorage.getItem('token') !== sessionToken) return; applyFrames(r.frames || []); if (r.feedback) handleFrame({ type: 'feedback_reply', feedback: r.feedback }) })
        .catch((e) => { if (lifetime === feedbackLifetime.current) message.error((e as Error).message) })
    }, !!capturedContext || ['wait_answer', 'repeating'].includes(phaseRef.current))
  }
  sendVoiceRef.current = sendVoice // 语音识别结果上报通道

  const phase = snapshot?.phase || 'show_word'
  const resumeClassroom = async () => {
    if (resuming) return
    const request = ++resumeRequestRef.current
    const before = { ...lastFrameRef.current }
    setResuming(true)
    setResumeError('')
    try {
      const frames = await api.post<Frame[]>(`/classroom/lessons/${lessonId}/resume`)
      if (request !== resumeRequestRef.current) return
      applyFrames(frames)
      // Nonempty recovery is governed by accepted business frames. An empty
      // successful response means already resumed, but cannot override a newer pause.
      if (frames.length === 0 && lastFrameRef.current.epoch === before.epoch && lastFrameRef.current.seq === before.seq) {
        setSnapshot(s => s ? { ...s, paused: false } : s)
      }
    } catch (error) {
      if (request === resumeRequestRef.current) setResumeError((error as Error).message)
    } finally {
      if (request === resumeRequestRef.current) setResuming(false)
    }
  }
  useEffect(() => {
    if (snapshot?.paused || phase === 'repeating' || phase === 'lesson_end') cancelTts()
  }, [phase, snapshot?.paused, cancelTts])

  useEffect(() => {
    if (!inputTurn || !inputGate.current.accepts(contextRef.current, phase, onlineRef.current, inputTurn)) return
    if (!wsOk || !providers?.asr || !lessonId || ended || snapshot?.paused || !['repeating', 'wait_answer'].includes(phase)) return
    if (new URLSearchParams(location.search).get('e2e') === '1') return
    const controller = new AbortController()
    localRecordingRef.current = controller
    const listen = async () => {
      try {
        while (!controller.signal.aborted) {
          setLocalStage('preparing')
          if (!inputGate.current.accepts(contextRef.current, phase, onlineRef.current, inputTurn)) return
          const captureContext = { epoch: inputTurn.epoch, turn: inputTurn.turn }
          const text = await recordLocal(lessonId, phase === 'repeating' ? 'en' : 'zh', controller.signal, stage => {
            if (controller.signal.aborted || localRecordingRef.current !== controller || localStorage.getItem('token') !== sessionToken ||
              !inputGate.current.accepts(contextRef.current, phase, onlineRef.current, inputTurn)) return
            setLocalStage(stage); setListening(stage === 'recording')
          })
          if (controller.signal.aborted || localStorage.getItem('token') !== sessionToken ||
            !inputGate.current.accepts(contextRef.current, phase, onlineRef.current, inputTurn)) return
          sendVoiceRef.current?.(text, captureContext)
          // One transcript per turn; a changed question/phase starts a fresh capture.
          break
        }
      } catch (e) {
        if (!controller.signal.aborted) setSpeechError((e as Error).message)
      } finally { if (!controller.signal.aborted && localRecordingRef.current === controller) { setListening(false); setLocalStage('idle') } }
    }
    void listen()
    return () => { controller.abort(); setListening(false); setLocalStage('idle') }
  }, [inputTurn, wsOk, providers?.asr, lessonId, phase, snapshot?.word, snapshot?.quiz_word, snapshot?.paused, ended, userId, sessionToken])

  // 相位驱动：进跟读/答题自动开麦；离场停麦（phase 声明之后）
  useEffect(() => {
    if (inputTurn && inputGate.current.accepts(contextRef.current, phase, onlineRef.current, inputTurn) && wsOk && !snapshot?.paused && (phase === 'wait_answer' || phase === 'repeating')) {
      const t = setTimeout(() => startListening(), 300) // 等 UI 稳定
      return () => { clearTimeout(t); stopListening() }
    }
    stopListening()
    return undefined
  }, [inputTurn, wsOk, snapshot?.paused, phase, startListening, stopListening])
  useEffect(() => () => stopListening(), [stopListening])

  const bigWord = phase === 'repeating' || phase === 'tts_seg' ? snapshot?.word
    : phase === 'reteach_show' ? (snapshot?.quiz_word || snapshot?.word) // R5-P2：RETEACH 显示错词
    : snapshot?.quiz_word || snapshot?.word
  // 教学相位（show_word 起即中英对照——用户定案：不要第一屏，直接显示英文和汉语）
  const teachingZh = phase === 'show_word' || phase === 'tts_seg' || phase === 'repeating' || phase === 'reteach_show'
  const zhShow = phase === 'reteach_show' || judge === 'bad'

  return (
    <div style={{ minHeight: '100vh', background: '#F7F9FA', display: 'flex', flexDirection: 'column' }}>
      {/* 顶栏 */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '12px 20px', background: '#fff' }}>
        <Button onClick={() => {
          Modal.confirm({
            title: '确定退出课堂吗？',
            content: '确定返回课程表吗？开启持久化后可恢复已保存的进度。',
            okText: '退出', cancelText: '继续学习',
            onOk: () => nav('/dashboard'),
          })
        }}>退出</Button>
        <span style={{ color: '#5C6B73' }}>{PHASE_TIP[phase] || phase}</span>
        <Tag color={wsOk ? 'success' : 'warning'}>{wsOk ? '已连接' : connectionError && !connectionError.retryable ? '连接已停止' : '重连中…'}</Tag>
      </div>

      {/* 主角卡 */}
      {!!feedback.records.length && <div aria-label="课堂反馈结果" style={{ padding: '0 20px' }}>
        {feedback.records.map(record => <FeedbackResults key={record.submission_id || record.id} record={record}
          busy={feedback.busy.includes(record.submission_id || '')}
          onClarify={(id, text, parent) => feedback.submit(id, text, 'page', parent)} onQuery={() => void feedback.recover(record.submission_id!)} onRetry={() => void feedback.recover(record.submission_id!, true)} />)}
        <Button onClick={() => nav('/dashboard')}>查看反馈历史</Button>
      </div>}
      {feedback.error && <Alert type="error" title={feedback.error} />}
      <Modal title="反馈未送达" open={!!undeliveredFeedback} okText="确认提交反馈" confirmLoading={resendingFeedback}
        onCancel={() => setUndeliveredFeedback(null)} onOk={async () => {
          if (!undeliveredFeedback || resendingFeedback) return
          const lifetime = feedbackLifetime.current
          setResendingFeedback(true)
          try {
            const id = undeliveredFeedback.text === undeliveredFeedback.original ? undeliveredFeedback.id : clientId()
            await feedbackRef.current.submit(id, undeliveredFeedback.text, 'voice')
            if (lifetime === feedbackLifetime.current) setUndeliveredFeedback(null)
          } catch (error) { if (lifetime === feedbackLifetime.current) message.error((error as Error).message) }
          finally { if (lifetime === feedbackLifetime.current) setResendingFeedback(false) }
        }}>
        <p>未查询到这次反馈。请确认正文后提交；此操作不会重放课堂语音事件。</p>
        <Input.TextArea aria-label="待确认反馈正文" value={undeliveredFeedback?.text || ''}
          onChange={e => setUndeliveredFeedback(value => value ? { ...value, text: e.target.value } : value)} />
      </Modal>
      {snapshot?.paused && <Alert type="info" title="课堂已暂停" description={resumeError || '休息好了就继续当前学习进度。'}
        action={<Button loading={resuming} disabled={resuming} onClick={() => void resumeClassroom()}>继续课堂</Button>} />}
      {connectionError && <Alert type="error" title={connectionError.message} action={<>
        <Button style={{ display: connectionError.retryable ? undefined : 'none' }} onClick={() => window.location.reload()}>重新连接课堂</Button>
        <Button onClick={() => nav('/dashboard')}>返回课程表</Button>
      </>} />}
      {localStage !== 'idle' && <div role="status">{localStage === 'preparing' ? '正在准备麦克风' : localStage === 'recording' ? '正在录音' : '正在识别，请稍候'}</div>}
      {speechError && <Alert type="warning" title={speechError} closable onClose={() => setSpeechError('')} />}
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 18, padding: 24 }}>
        {!snapshot ? (
          <Spin size="large" />
        ) : (
          <>
            <div
              role="button"
              tabIndex={0}
              aria-label={phase === 'show_word' ? `${bigWord}，点击开始学习` : bigWord}
              onClick={() => phase === 'show_word' && sendEvent('word_clicked')}
              onKeyDown={(e) => {
                if (phase === 'show_word' && (e.key === 'Enter' || e.key === ' ')) sendEvent('word_clicked')
              }}
              style={{
                fontSize: 84,
                fontWeight: 700,
                fontFamily: 'Georgia, serif',
                cursor: phase === 'show_word' ? 'pointer' : 'default',
                padding: '30px 60px',
                background: '#fff',
                borderRadius: 16,
                boxShadow: '0 2px 12px rgba(28,43,51,.06)',
                border: judge === 'ok' ? '3px solid #22C55E' : judge === 'bad' ? '3px solid #F87171' : 'none',
                animation: judge === 'ok' ? 'pop .3s' : judge === 'bad' ? 'shake .2s' : 'none',
                display: 'flex',
                alignItems: 'center',
                gap: 16,
              }}
            >
              {/* R4-fe#3：色盲安全——✓✗ 图标形状差异 + aria */}
              {judge === 'ok' && <CheckCircleFilled style={{ fontSize: 48, color: '#22C55E' }} aria-label="回答正确" />}
              {judge === 'bad' && <CloseCircleFilled style={{ fontSize: 48, color: '#F87171' }} aria-label="回答错误" />}
              {bigWord || '…'}
            </div>
            {/* 第几遍显示已移除：无实际用途且首次不显示/后续显示不统一 */}
            {/* 教学阶段中英对照（原始需求：学习时显示中英文对照） */}
            {teachingZh && snapshot?.zhMeaning && (
              <Typography.Text strong style={{ fontSize: 40, color: '#0D9488' }}>
                {snapshot.zhMeaning}
              </Typography.Text>
            )}
            {zhShow && !teachingZh && (
              <Typography.Text type="secondary" style={{ fontSize: 24 }}>
                看中文意思回忆一下
              </Typography.Text>
            )}

            <div role="status" aria-label="当前朗读字幕" aria-live="polite" aria-atomic="true"
              style={{ minHeight: 56, width: '100%', maxWidth: 640, boxSizing: 'border-box',
                padding: '8px 16px', textAlign: 'center', overflowWrap: 'anywhere', whiteSpace: 'pre-wrap',
                color: '#334155', fontSize: 22, lineHeight: 1.5 }}>
              {captionText && <><span style={{ display: 'block', fontSize: 13, color: '#64748B' }}>正在朗读</span><span>{captionText}</span></>}
            </div>

            {/* 老师气泡（内容来自 ui_action.say，无硬编码前缀；R4-fe#4） */}
            {bubble && (
              <div
                style={{
                  background: '#F5F3FF',
                  border: '1.5px solid #8B5CF6',
                  borderRadius: '14px 14px 14px 2px',
                  padding: '10px 18px',
                  maxWidth: 560,
                  color: '#1C2B33',
                }}
                aria-live="polite"
              >
                {bubble}
              </div>
            )}
          </>
        )}
      </div>

      {/* 操作区（TTS 播放期 pointer-events:none 防误触；R4-fe docs/07 §7.7） */}
      <div
        style={{
          padding: 20,
          background: '#fff',
          display: 'flex',
          gap: 12,
          justifyContent: 'center',
          flexWrap: 'wrap',
          pointerEvents: phase === 'tts_seg' || phase === 'judging' ? 'none' : 'auto',
          opacity: phase === 'tts_seg' || phase === 'judging' ? 0.6 : 1,
        }}
      >
        {phase === 'repeating' && (
          // 用户定案：不设"我读完了"按钮——老师（语音判读）听学生读完自动进下一词
          <Tag color={listening ? 'processing' : 'default'} aria-live="polite">
            {listening ? '🎙 跟读：大声读出这个词～' : '准备跟读…'}
          </Tag>
        )}
        {phase === 'wait_answer' && (
          <>
            {/* 默认语音监听指示（无需点击；识别结果自动提交） */}
            <Tag color={listening ? 'processing' : 'default'} aria-live="polite">
              {!inputTurn ? '正在恢复题目，请稍候' : listening ? '🎙 正在听你说…（说出中文意思即可）' : '语音就绪'}
            </Tag>
            <Input
              ref={inputRef}
              size="large"
              style={{ width: 320 }}
              placeholder={listening ? '或直接打字（语音优先）' : '输入中文意思'}
              aria-label="答案输入框"
              disabled={answerPending || !wsOk || !inputTurn || inputTurn.phase !== 'wait_answer'}
              value={answer}
              onChange={(e) => setAnswer(e.target.value)}
              onPressEnter={() => sendEvent('answer_submitted', answer)}
            />
            <Button type="primary" size="large" disabled={answerPending || !wsOk || !inputTurn || inputTurn.phase !== 'wait_answer' || !answer.trim()} onClick={() => sendEvent('answer_submitted', answer)}>
              回答
            </Button>
          </>
        )}
        {(answerPending || phase === 'judging') && <div role="status">正在判分，请稍候 <Spin /></div>}
        {(phase === 'show_word' || phase === 'tts_seg') && (
          <Button size="large" onClick={() => sendVoice('下一个')} aria-label="语音指令：下一个">
            语音：下一个
          </Button>
        )}
      </div>

      {/* 下课总结（字段映射后端 LessonReport json tag；R4-fe#5） */}
      <Modal
        open={ended}
        title="下课啦！叮当老师的小结"
        okText="回课程表"
        cancelButtonProps={{ style: { display: 'none' } }}
        onOk={() => nav('/dashboard')}
      >
        {summary ? (
          <div>
            <p>
              今天 {String(summary.total_words)} 个词，首答对了 {String(summary.correct_count)} 个！
            </p>
            <p>
              首答率 {String(Math.round(Number(summary.first_try_rate) * 100))}% · 最长连对 {String(summary.streak_best)} ·
              补学 {String(summary.reteach_count)} 轮
            </p>
            <p>
              整体状态：{String(summary.qual_solid)}（节奏{String(summary.qual_pace)}）
            </p>
          </div>
        ) : (
          <p>今天辛苦啦，明天见！</p>
        )}
      </Modal>

      <style>{`
        @keyframes pop { 0%{transform:scale(.85)} 60%{transform:scale(1.08)} 100%{transform:scale(1)} }
        @keyframes shake { 0%,100%{transform:translateX(0)} 25%{transform:translateX(-8px)} 75%{transform:translateX(8px)} }
      `}</style>
    </div>
  )
}
