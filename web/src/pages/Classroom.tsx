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

  const speak = useCallback((text: string, lang: 'en-US' | 'zh-CN') => {
    // R4-fe#2：REPEATING 阶段禁 TTS（回声抑制）；不可用降级纯视觉（tts_finished 照常由交互驱动）
    if (phaseRef.current === 'repeating' || !ttsAvail.current) return
    try {
      const u = new SpeechSynthesisUtterance(text)
      u.lang = lang
      u.rate = 0.85
      window.speechSynthesis.speak(u)
    } catch {
      /* 静默降级 */
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
          setSnapshot({ phase: f.phase, word: f.word, quiz_word: f.quiz_word, paused: f.paused })
          phaseRef.current = f.phase
          if (f.word && f.phase === 'show_word') speak(f.word, 'en-US')
          break
        case 'phase_changed':
          phaseRef.current = f.phase
          setSnapshot((s) => (s ? { ...s, phase: f.phase } : s))
          break
        case 'ui_action': {
          const a = f.action ?? {}
          switch (a.type) {
            case 'show_word':
              setSnapshot((s) => ({ ...(s ?? { phase: 'show_word' }), phase: 'show_word', word: a.word }))
              setJudge(null)
              phaseRef.current = 'show_word'
              break
            case 'play_tts':
              setTtsSeg(a.seg_idx || 1)
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

  // 心跳（30s）
  useEffect(() => {
    const hb = setInterval(() => wsRef.current?.send({ type: 'heartbeat' }), 30000)
    return () => clearInterval(hb)
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

  const sendVoice = (text: string) => {
    if (!wsRef.current?.sendVoiceText(text)) {
      api
        .post<{ command: string; frames: Frame[] }>(`/classroom/lessons/${lessonId}/voice`, { text })
        .then((r) => applyFrames(r.frames || []))
        .catch((e) => message.error((e as Error).message))
    }
  }

  const phase = snapshot?.phase || 'show_word'
  const bigWord = phase === 'repeating' || phase === 'tts_seg' ? snapshot?.word : snapshot?.quiz_word || snapshot?.word
  const zhShow = phase === 'reteach_show' || judge === 'bad'

  return (
    <div style={{ minHeight: '100vh', background: '#F7F9FA', display: 'flex', flexDirection: 'column' }}>
      {/* 顶栏 */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '12px 20px', background: '#fff' }}>
        <Button onClick={() => nav('/dashboard')}>退出（进度已保存）</Button>
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
              aria-label={phase === 'show_word' ? `${bigWord}，点击开始学习` : bigWord}
              onClick={() => phase === 'show_word' && sendEvent('word_clicked')}
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
            {zhShow && (
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
            <Input
              ref={inputRef}
              size="large"
              style={{ width: 320 }}
              placeholder="输入中文意思"
              aria-label="答案输入框"
              value={answer}
              onChange={(e) => setAnswer(e.target.value)}
              onPressEnter={() => sendEvent('answer_submitted', answer)}
            />
            <Button type="primary" size="large" onClick={() => sendEvent('answer_submitted', answer)}>
              回答
            </Button>
            <Button size="large" aria-label="语音回答" onClick={() => sendVoice('测试')}>
              语音回答
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
