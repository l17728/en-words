import { useEffect, useRef, useState, useCallback } from 'react'
import { Button, Typography, Spin, Modal, Input, message } from 'antd'
import type { InputRef } from 'antd'
import { LeftOutlined, ReloadOutlined, SoundOutlined } from '@ant-design/icons'
import { useParams, useNavigate } from 'react-router-dom'
import { api } from '../api/client'

// 协同课堂页（docs/07 §6.3：单词卡主角/遍数指示/跟读面板/答题/老师气泡/✓✗动画）
// Mock 语音：Web Speech 朗读；跟读=按钮；答题=文本框（语音走 REST voice 通道）

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
interface Frame {
  type: string
  seq?: number
  action?: { type: string; lang?: string; word?: string; seg_idx?: number; with_zh?: boolean; kind?: string; reason?: string }
  judgment?: string
  asr_text?: string
  quiz_item?: { word_id: number; kind: string; round: number }
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
  const [repeatN, setRepeatN] = useState(0)
  const [judge, setJudge] = useState<'ok' | 'bad' | null>(null)
  const [answer, setAnswer] = useState('')
  const [ended, setEnded] = useState(false)
  const [summary, setSummary] = useState<Record<string, unknown> | null>(null)
  const [ttsSeg, setTtsSeg] = useState(0)
  const inputRef = useRef<InputRef | null>(null)

  const speak = useCallback((text: string, lang: 'en-US' | 'zh-CN') => {
    try {
      const u = new SpeechSynthesisUtterance(text)
      u.lang = lang
      u.rate = 0.85
      window.speechSynthesis.speak(u)
    } catch {
      /* 无语音环境静默 */
    }
  }, [])

  const applyFrames = useCallback(
    (frames: Frame[]) => {
      for (const f of frames) {
        if (f.type === 'state_snapshot') setSnapshot(f as unknown as Snapshot)
        if (f.type === 'ui_action' && f.action) {
          const a = f.action
          switch (a.type) {
            case 'show_word':
              setSnapshot((s) => ({ ...s!, phase: 'show_word', word: a.word }))
              setJudge(null)
              break
            case 'play_tts':
              setTtsSeg(a.seg_idx || 1)
              if (a.word) speak(a.word, a.lang === 'zh' ? 'zh-CN' : 'en-US')
              break
            case 'start_repeat':
              setSnapshot((s) => ({ ...s!, phase: 'repeating' }))
              setRepeatN(0)
              break
            case 'show_quiz_item':
              setSnapshot((s) => ({ ...s!, phase: 'wait_answer', quiz_word: a.word }))
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
            case 'start_quiz':
              setBubble(a.kind === 'final' ? '最后一个大关：完整测试！' : a.kind === 'cumulative' ? '综合测试开始！' : '小测时间，别紧张～')
              break
            case 'lesson_summary':
              setEnded(true)
              break
            case 'nudge_repeat':
              setBubble('该你读了，大声一点没关系～')
              break
            case 'nudge_answer':
              setBubble('想起来了吗？说出来试试，说错也没事')
              break
          }
        }
        if (f.type === 'judge_result') {
          setJudge(f.judgment === 'correct' || f.judgment === 'near' ? 'ok' : 'bad')
        }
        if (f.type === 'lesson_summary') {
          setEnded(true)
          setSummary((f as unknown as { report: Record<string, unknown> }).report ?? null)
        }
      }
    },
    [speak],
  )

  useEffect(() => {
    api
      .post<EnterResult>(`/classroom/lessons/${lessonId}/enter`)
      .then(async (r) => {
        setSnapshot(r.snapshot)
        if (r.snapshot.word) speak(r.snapshot.word, 'en-US')
      })
      .catch((e) => {
        message.error((e as Error).message)
        nav('/dashboard')
      })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lessonId])

  const sendEvent = (event: string, text?: string) => {
    api
      .post<Frame[]>(`/classroom/lessons/${lessonId}/event`, { event, text })
      .then(applyFrames)
      .catch((e) => message.error((e as Error).message))
  }

  const sendVoice = (text: string) => {
    api
      .post<{ command: string; frames: Frame[] }>(`/classroom/lessons/${lessonId}/voice`, { text })
      .then((r) => {
        if (r.command) setBubble(`（语音指令：${r.command}）`)
        applyFrames(r.frames || [])
      })
      .catch((e) => message.error((e as Error).message))
  }

  const phase = snapshot?.phase || 'show_word'
  const bigWord = phase === 'repeating' || phase === 'tts_seg' ? snapshot?.word : snapshot?.quiz_word || snapshot?.word
  const zhShow = phase === 'reteach_show' || judge === 'bad'

  return (
    <div style={{ minHeight: '100vh', background: '#F7F9FA', display: 'flex', flexDirection: 'column' }}>
      {/* 顶栏 */}
      <div style={{ display: 'flex', justifyContent: 'space-between', padding: '12px 20px', background: '#fff' }}>
        <Button icon={<LeftOutlined />} onClick={() => nav('/dashboard')}>退出（进度已保存）</Button>
        <span style={{ color: '#5C6B73' }}>{PHASE_TIP[phase] || phase}</span>
        <Button icon={<ReloadOutlined />} onClick={() => window.location.reload()} />
      </div>

      {/* 主角卡 */}
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 18, padding: 24 }}>
        {!snapshot ? (
          <Spin size="large" />
        ) : (
          <>
            <div
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
              }}
            >
              {bigWord || '…'}
            </div>
            {phase === 'tts_seg' && (
              <div style={{ color: '#5C6B73' }}>
                <SoundOutlined /> 第 {ttsSeg} 遍
              </div>
            )}
            {zhShow && (
              <Typography.Text type="secondary" style={{ fontSize: 24 }}>
                看中文意思回忆一下
              </Typography.Text>
            )}

            {/* 老师气泡 */}
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
              >
                叮当老师：{bubble}
              </div>
            )}
          </>
        )}
      </div>

      {/* 操作区（随相位切换） */}
      <div style={{ padding: 20, background: '#fff', display: 'flex', gap: 12, justifyContent: 'center', flexWrap: 'wrap' }}>
        {phase === 'repeating' && (
          <Button
            type="primary"
            size="large"
            onClick={() => {
              setRepeatN((n) => n + 1)
              sendEvent('repeat_done')
            }}
          >
            我读完了 ({repeatN % 3}/3)
          </Button>
        )}
        {phase === 'wait_answer' && (
          <>
            <Input
              ref={inputRef}
              size="large"
              style={{ width: 320 }}
              placeholder="输入中文意思（或点麦克风说话）"
              value={answer}
              onChange={(e) => setAnswer(e.target.value)}
              onPressEnter={() => sendEvent('answer_submitted', answer)}
            />
            <Button type="primary" size="large" onClick={() => sendEvent('answer_submitted', answer)}>
              回答
            </Button>
            <Button
              size="large"
              onClick={() => {
                // 浏览器语音识别（存在时）；失败降级文本
                const SR = (window as unknown as { webkitSpeechRecognition?: new () => never }).webkitSpeechRecognition
                if (!SR) {
                  message.info('此浏览器不支持语音，请打字回答')
                  return
                }
                sendVoice('测试') // Mock：真实版接入 Omni 中继
              }}
            >
              语音回答
            </Button>
          </>
        )}
        {phase === 'judging' && <Spin />}
        {(phase === 'show_word' || phase === 'tts_seg') && (
          <Button size="large" onClick={() => sendVoice('下一个')}>语音：下一个</Button>
        )}
      </div>

      {/* 下课总结 */}
      <Modal
        open={ended}
        title="下课啦！叮当老师的小结"
        okText="回课程表"
        cancelText={null}
        cancelButtonProps={{ style: { display: 'none' } }}
        onOk={() => nav('/dashboard')}
      >
        {summary ? (
          <div>
            <p>今天 {String(summary.total_words)} 个词，首答对了 {String(summary.correct_count)} 个！</p>
            <p>正确率 {String(Math.round(Number(summary.first_try_rate) * 100))}% · 最长连对 {String(summary.streak_best)} · 补学 {String(summary.reteach_count)} 轮</p>
            <p>整体状态：{String(summary.qual_solid)}（节奏{String(summary.qual_pace)}）</p>
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
