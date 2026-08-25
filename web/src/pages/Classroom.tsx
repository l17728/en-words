import { useEffect, useRef, useState, useCallback } from 'react'
import { Button, Typography, Spin, Modal, Input, message, Tag } from 'antd'
import { CheckCircleFilled, CloseCircleFilled } from '@ant-design/icons'
import type { InputRef } from 'antd'
import { useParams, useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import { ClassroomWS, type Frame } from '../ws/client'

// 协同课堂页（docs/07 §6.3）：WS 驱动（R4-fe#1），REST 仅 enter 取 ticket。
// REPEATING 禁 TTS（回声抑制 FE-02/R4-fe#2）；语音不可用降级视觉动画。

interface EnterResult {
  ticket: string
  snapshot: Snapshot
  status: string
}
interface Snapshot {
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
  judging: '叮当老师在听…',
  reteach_show: '再认识一遍这个词',
  lesson_end: '下课啦！',
}

export default function ClassroomPage() {
  const { lessonId } = useParams()
  const nav = useNavigate()
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const [bubble, setBubble] = useState('')
  const [judge, setJudge] = useState<'ok' | 'bad' | null>(null)
  const [answer, setAnswer] = useState('')
  const [ended, setEnded] = useState(false)
  const [summary, setSummary] = useState<Record<string, any> | null>(null)
  const [ttsSeg, setTtsSeg] = useState(0)
  const [repeatN, setRepeatN] = useState(0)
  const [wsOk, setWsOk] = useState(false)
  const inputRef = useRef<InputRef | null>(null)
  const wsRef = useRef<ClassroomWS | null>(null)
  const phaseRef = useRef<string>('show_word')
  const ttsAvail = useRef<boolean>(typeof window !== 'undefined' && 'speechSynthesis' in window)
  // voices 预热：Chrome 的语音列表异步加载，未就绪时 speak() 静默失败（曾致"头几个词哑火"）。
  // 页面加载即触发 getVoices + voiceschanged 监听重取，确保第一词就有声。
  useEffect(() => {
    if (!('speechSynthesis' in window)) return
    const warm = () => { void window.speechSynthesis.getVoices() }
    warm()
    window.speechSynthesis.onvoiceschanged = warm
    return () => { window.speechSynthesis.onvoiceschanged = null }
  }, [])
  // TTS 完成回执通过 WS 上报（声明在前供 speak 闭包引用）
  const sendEventRef = useRef<((ev: string, text?: string) => void) | null>(null)
  const sendVoiceRef = useRef<((text: string) => void) | null>(null)

  // ===== TTS 串行播放器（修复：连续段被吞/哑火——cancel+speak 快速连发会杀掉未入队段）=====
  // 每段排队逐个播（onend 驱动出队），看门狗兜底 onend 失联；tts_finished 每段上报一次。
  const ttsQueueRef = useRef<Array<{ text: string; lang: 'en-US' | 'zh-CN' }>>([])
  const ttsBusyRef = useRef(false)
  const ttsTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const segDur = useCallback((text: string, lang: string): number => {
    const per = lang === 'zh-CN' ? 250 : 300
    return Math.max(800, text.length * per) + 400
  }, [])

  // pumpQueue 取队首播放；REPEATING 阶段静默跳过（回声抑制 FE-02）
  const pumpTts = useCallback(() => {
    if (ttsBusyRef.current) return
    const next = ttsQueueRef.current.shift()
    if (!next) return
    // 回声抑制：跟读阶段不发声——静默段仍链式出队+回执（R5-P0：防队列卡死）
    if (phaseRef.current === 'repeating' || !ttsAvail.current) {
      ttsBusyRef.current = true
      ttsTimerRef.current = setTimeout(() => {
        if (ttsTimerRef.current) clearTimeout(ttsTimerRef.current)
        ttsTimerRef.current = null
        ttsBusyRef.current = false
        sendEventRef.current?.('tts_finished')
        pumpTts() // 链式出队（修复：静默后队列卡死）
      }, 200) // 静默段统一 200ms（模拟段间隔，保持节奏）
      return
    }
    ttsBusyRef.current = true
    let fired = false
    const once = () => {
      if (fired) return
      fired = true
      if (ttsTimerRef.current) clearTimeout(ttsTimerRef.current)
      ttsTimerRef.current = null
      ttsBusyRef.current = false
      sendEventRef.current?.('tts_finished')
      pumpTts() // 链式出队（间隔由后端 ttsIntervalMs 配置，此处紧接）
    }
    try {
      const u = new SpeechSynthesisUtterance(next.text)
      u.lang = next.lang
      u.rate = next.lang === 'en-US' ? 0.8 : 0.95
      u.onend = once
      u.onerror = once
      window.speechSynthesis.speak(u)
      ttsTimerRef.current = setTimeout(once, segDur(next.text, next.lang) * 2.5) // 看门狗
    } catch {
      once()
    }
  }, [segDur])

  // speak = 入队（保持外部签名；word 参数已内聚在 text）
  const speak = useCallback(
    (text: string, lang: 'en-US' | 'zh-CN') => {
      // R6-fe#1：REPEATING 相位不入队（回声抑制入口守卫——防相位切换竞态时旧段发声）
      if (phaseRef.current === 'repeating') return
      ttsQueueRef.current.push({ text, lang })
      pumpTts()
    },
    [pumpTts],
  )
  // 阶段切换清队列（防旧词语音溢到新词）；R5：cancel 中断正在播的段+清 timer
  useEffect(() => {
    return () => {
      ttsQueueRef.current = []
      ttsBusyRef.current = false
      if (ttsTimerRef.current) clearTimeout(ttsTimerRef.current)
      ttsTimerRef.current = null
      try { window.speechSynthesis?.cancel() } catch { /* noop */ }
    }
  }, [])

  const applyFrames = useCallback(
    (frames: Frame[]) => {
      for (const f of frames) {
        handleFrame(f)
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [speak],
  )

  const handleFrame = useCallback(
    (f: Frame) => {
      switch (f.type) {
        case 'state_snapshot':
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
            case 'show_word':
              setSnapshot((s) => ({ ...(s ?? { phase: 'show_word' }), phase: 'show_word',
                word: a.word, zhMeaning: Array.isArray(a.meanings) && a.meanings.length > 0 ? a.meanings[0] : s?.zhMeaning })) // R5：meanings 防空保旧
              setJudge(null)
              phaseRef.current = 'show_word'
              break
            case 'play_tts':
              setTtsSeg(a.seg_idx || 1)
              // Meanings：en 首段即携带中文释义（点击即显示中英对照——原始需求）
              {
                const zh = a.lang === 'zh' && a.word ? a.word
                  : Array.isArray(a.meanings) && a.meanings.length > 0 ? a.meanings[0] : ''
                if (zh) {
                  setSnapshot((s) => ({ ...(s ?? { phase: 'tts_seg' }), phase: 'tts_seg',
                    word: s?.word, zhMeaning: zh }))
                }
              }
              if (a.word) speak(a.word, a.lang === 'zh' ? 'zh-CN' : 'en-US')
              break
            case 'start_repeat':
              setSnapshot((s) => ({ ...(s ?? { phase: 'repeating' }), phase: 'repeating' }))
              phaseRef.current = 'repeating' // 禁后续 TTS（回声抑制）
              setRepeatN(0)
              break
            case 'show_quiz_item':
              setSnapshot((s) => ({ ...(s ?? { phase: 'wait_answer' }), phase: 'wait_answer', quiz_word: a.word }))
              phaseRef.current = 'wait_answer'
              setJudge(null)
              setAnswer('')
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
            case 'nudge_answer':
              setBubble(a.text ?? '再想想～')
              break
            case 'start_quiz':
              setBubble(a.kind === 'final' ? '最后一个大关：完整测试！' : a.kind === 'cumulative' ? '综合测试开始！' : '小测时间，别紧张～')
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
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [speak],
  )

  useEffect(() => {
    let ws: ClassroomWS | null = null
    api
      .post<EnterResult>(`/classroom/lessons/${lessonId}/enter`)
      .then((r) => {
        setSnapshot(r.snapshot)
        phaseRef.current = r.snapshot.phase
        if (r.snapshot.word && r.snapshot.phase === 'show_word') speak(r.snapshot.word, 'en-US')
        // R4-fe#1：真 WebSocket 连接（事件/指令全部走 WS）
        ws = new ClassroomWS(Number(lessonId), r.ticket, {
          onFrame: handleFrame,
          onOpen: () => setWsOk(true),
          onClose: () => setWsOk(false),
        })
        wsRef.current = ws
      })
      .catch((e) => {
        message.error((e as Error).message)
        nav('/dashboard')
      })
    return () => ws?.close()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lessonId])

  // ===== 默认语音监听模式（用户定案：语音是默认作答方式，无需点击按钮）=====
  // wait_answer 相位自动启动 SpeechRecognition；识别结果→sendVoice（voice_text 管线）。
  // E2E/不支持环境：window.__TEST_VOICE__ 注入 mock 或降级文本框。
  const recogRef = useRef<any>(null)
  const listenActiveRef = useRef(false)
  const [listening, setListening] = useState(false)

  const stopListening = useCallback(() => {
    listenActiveRef.current = false
    setListening(false)
    try {
      recogRef.current?.stop()
    } catch {
      /* noop */
    }
    recogRef.current = null
  }, [])

  const startListening = useCallback(() => {
    if (listenActiveRef.current) return
    // E2E 测试模式：URL 带 ?e2e=1 时禁自动监听（避免 TTS 朗读被拾音→自动提交
    // 与测试显式提交叠加造成"回答两次"；真实拾音路径由 omni 回环测试覆盖）
    if (new URLSearchParams(window.location.search).get('e2e') === '1') return
    const W = window as any
    const SR = W.SpeechRecognition || W.webkitSpeechRecognition
    if (!SR) return // 不支持：文本框兜底（输入框已自动聚焦）
    listenActiveRef.current = true
    setListening(true)
    const recog = new SR()
    recogRef.current = recog
    recog.lang = 'zh-CN'
    recog.interimResults = false
    recog.continuous = false
    recog.onresult = (ev: any) => {
      const text = ev.results?.[0]?.[0]?.transcript || ''
      if (text) sendVoiceRef.current?.(text.trim()) // 语音作答/指令统一管线
    }
    recog.onend = () => {
      // 相位仍在答题且未停止 → 持续监听（用户可以说多次）
      if (listenActiveRef.current && phaseRef.current === 'wait_answer') {
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

  const sendEvent = (event: string, text?: string) => {
    if (!wsRef.current?.sendEvent(event, text)) {
      // WS 未连接降级 REST（双通道保障）
      api
        .post<Frame[]>(`/classroom/lessons/${lessonId}/event`, { event, text })
        .then(applyFrames)
        .catch((e) => message.error((e as Error).message))
    }
  }
  sendEventRef.current = sendEvent // TTS onend 回执上报通道

  const sendVoice = (text: string) => {
    if (!wsRef.current?.sendVoiceText(text)) {
      api
        .post<{ command: string; frames: Frame[] }>(`/classroom/lessons/${lessonId}/voice`, { text })
        .then((r) => applyFrames(r.frames || []))
        .catch((e) => message.error((e as Error).message))
    }
  }
  sendVoiceRef.current = sendVoice // 语音识别结果上报通道

  const phase = snapshot?.phase || 'show_word'

  // 相位驱动：进答题自动开麦；离场停麦（phase 声明之后）
  useEffect(() => {
    if (phase === 'wait_answer') {
      const t = setTimeout(() => startListening(), 300) // 等 UI 稳定
      return () => clearTimeout(t)
    }
    stopListening()
    return undefined
  }, [phase, startListening, stopListening])
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
            content: '进度已经存好啦，随时可以回来继续。',
            okText: '退出', cancelText: '继续学习',
            onOk: () => nav('/dashboard'),
          })
        }}>退出</Button>
        <span style={{ color: '#5C6B73' }}>{PHASE_TIP[phase] || phase}</span>
        <Tag color={wsOk ? 'success' : 'warning'}>{wsOk ? '已连接' : '重连中…'}</Tag>
      </div>

      {/* 主角卡 */}
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
            {phase === 'tts_seg' && (
              <div style={{ color: '#5C6B73' }} aria-live="polite">
                🔊 第 {ttsSeg} 遍
              </div>
            )}
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
          <Button
            type="primary"
            size="large"
            aria-label={`我读完了，第 ${Math.min(repeatN % 3 + 1, 3)} 遍`}
            onClick={() => {
              setRepeatN((n) => n + 1)
              sendEvent('repeat_done')
            }}
          >
            我读完了 ({Math.min(repeatN % 3 + 1, 3)}/3)
          </Button>
        )}
        {phase === 'wait_answer' && (
          <>
            {/* 默认语音监听指示（无需点击；识别结果自动提交） */}
            <Tag color={listening ? 'processing' : 'default'} aria-live="polite">
              {listening ? '🎙 正在听你说…（说出中文意思即可）' : '语音就绪'}
            </Tag>
            <Input
              ref={inputRef}
              size="large"
              style={{ width: 320 }}
              placeholder={listening ? '或直接打字（语音优先）' : '输入中文意思'}
              aria-label="答案输入框"
              value={answer}
              onChange={(e) => setAnswer(e.target.value)}
              onPressEnter={() => sendEvent('answer_submitted', answer)}
            />
            <Button type="primary" size="large" onClick={() => sendEvent('answer_submitted', answer)}>
              回答
            </Button>
          </>
        )}
        {phase === 'judging' && <Spin />}
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
