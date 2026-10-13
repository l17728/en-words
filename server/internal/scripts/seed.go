// 内置话术模板（04 §5 全量 + §5.9 RB 课报系列；按表精编，每类 ≥6 条）。
package scripts

// SeedAll 播种全部内置模板（04 §5.1~5.9）。
func (b *Bank) SeedAll() {
	b.Add([]Template{
		// 5.1 open 开场（6，叮当老师人设自称）
		{ID: "OP-1", Category: "open", Tone: "热情", Weight: 3, Template: "我是叮当老师！{name}，今天我们搞定 {n} 个新单词，准备好了吗？"},
		{ID: "OP-2", Category: "open", Tone: "轻快", Weight: 2, Template: "来啦？坐好，咱们直接开始，今天的词都不难！"},
		{ID: "OP-3", Category: "open", Tone: "温和", Weight: 2, Template: "Hi {name}！先深呼吸，接下来交给叮当老师，你跟着读就行。"},
		{ID: "OP-4", Category: "open", Tone: "鼓励", Weight: 2, Template: "又见面啦 {name}，上次你表现很棒，今天继续保持！"},
		{ID: "OP-5", Category: "open", Tone: "关怀", Weight: 1, Template: "今天状态怎么样？不管怎么样，五分钟热身后就进入状态了。"},
		{ID: "OP-6", Category: "open", Tone: "激将", Weight: 1, Template: "上次你错了 {n} 个词，今天它们还会回来找你，这次干掉它们！"},
	}...)
	b.Add([]Template{
		// 5.2 transition 转场（6；2026-08-25 课堂流程定案：组测/综测/期末三分废弃→唯一 final）
		{ID: "TR-1", Category: "transition", Tone: "中性", Weight: 4, Template: "好，下一个："},
		{ID: "TR-2", Category: "transition", Tone: "轻快", Weight: 2, Template: "挺顺的嘛，继续。"},
		{ID: "TR-3", Category: "transition", Tone: "中性", Weight: 2, Template: "这个词记住感觉了吗？来，下一个。"},
		{ID: "TR-4", Category: "transition", Tone: "提示", Weight: 3, Template: "词都学完了！来做个小验收，就刚才学的那些，别紧张。"},
		{ID: "TR-6", Category: "transition", Tone: "温和", Weight: 3, Template: "前面错的词我们再过一遍，这次你会记得更牢。"},
		{ID: "TR-8", Category: "transition", Tone: "关怀", Weight: 2, Template: "快到终点了，坚持一下，{name}。"},
	}...)
	b.Add([]Template{
		// 5.3 encourage 鼓励（8）
		{ID: "EN-1", Category: "encourage", Tone: "轻快", Weight: 3, Template: "不错不错，答对了！"},
		{ID: "EN-2", Category: "encourage", Tone: "热情", Weight: 2, Template: "反应很快啊 {name}！"},
		{ID: "EN-3", Category: "encourage", Tone: "肯定", Weight: 2, Template: "对！这个 {word} 你已经拿下了。"},
		{ID: "EN-4", Category: "encourage", Tone: "数据", Weight: 2, Template: "连着对 {n} 个了，今天状态在线！"},
		{ID: "EN-5", Category: "encourage", Tone: "对比", Weight: 2, Template: "这波连着全对，比刚才还稳，进步看得见。"},
		{ID: "EN-6", Category: "encourage", Tone: "肯定", Weight: 2, Template: "好样的，这个词第二次见到你就认识了。"},
		{ID: "EN-7", Category: "encourage", Tone: "庆祝", Weight: 1, Template: "{name}，正确率越来越高了，给自己一点掌声。"},
		{ID: "EN-8", Category: "encourage", Tone: "庆祝", Weight: 1, Template: "这波完整测试零失误，太强了！"},
	}...)
	b.Add([]Template{
		// 5.4 correct 纠错/安抚（7）
		{ID: "CO-1", Category: "correct", Tone: "引导", Weight: 3, Template: "差一点点，{word} 的意思是“{meaning}”，再看我读一遍。"},
		{ID: "CO-2", Category: "correct", Tone: "安抚", Weight: 2, Template: "没事，错的原因就是还没见过面几次，多见两次就熟了。"},
		{ID: "CO-3", Category: "correct", Tone: "安抚", Weight: 2, Template: "这个词有点难，很多人都会错，再来一次。"},
		{ID: "CO-4", Category: "correct", Tone: "鼓励", Weight: 2, Template: "别灰心，错了才说明学得到东西，我们再过一遍。"},
		{ID: "CO-5", Category: "correct", Tone: "引导", Weight: 2, Template: "差不多了！标准一点说，“{meaning}”，跟我念。"},
		{ID: "CO-6", Category: "correct", Tone: "安抚", Weight: 2, Template: "没关系，忘了就再看一眼，这不丢人，忘了再看才是学习。"},
		{ID: "CO-7", Category: "correct", Tone: "激将", Weight: 1, Template: "这个 {word} 已经坑你 {n} 次了，今天必须拿下它！"},
	}...)
	b.Add([]Template{
		// 5.5 closing 下课（6）
		{ID: "CL-1", Category: "closing", Tone: "总结", Weight: 3, Template: "今天搞定 {n} 个词，错了 {m} 个但都补回来了，{name} 辛苦啦！"},
		{ID: "CL-2", Category: "closing", Tone: "数据", Weight: 2, Template: "下课！今天正确率 {p}%，比上次进步了，保持这个节奏！"},
		{ID: "CL-3", Category: "closing", Tone: "预告", Weight: 2, Template: "今天的词都过关了，明天老时间，我们复习加新词，不见不散。"},
		{ID: "CL-4", Category: "closing", Tone: "关怀", Weight: 2, Template: "表现很稳，{name}。休息一下眼睛，明天见！"},
		{ID: "CL-5", Category: "closing", Tone: "庆祝", Weight: 1, Template: "错词刚才都在回顾里过了一遍，今天的账清了，完美。"},
		{ID: "CL-6", Category: "closing", Tone: "安抚", Weight: 1, Template: "今天有点难熬，但你坚持完了，这本身就是本事。明天轻松点。"},
	}...)
	b.Add([]Template{
		// 5.6 nudge 提醒（7）
		{ID: "NU-1", Category: "nudge", Tone: "温和", Weight: 3, Template: "在吗 {name}？点一下这个词咱们继续。"},
		{ID: "NU-2", Category: "nudge", Tone: "轻快", Weight: 2, Template: "我等你点单词哦～"},
		{ID: "NU-3", Category: "nudge", Tone: "鼓励", Weight: 3, Template: "该你读了，大声一点没关系。"},
		{ID: "NU-4", Category: "nudge", Tone: "鼓励", Weight: 2, Template: "别害羞，跟我读：{word}。"},
		{ID: "NU-5", Category: "nudge", Tone: "温和", Weight: 3, Template: "想起来了吗？说出来试试，说错也没事。"},
		{ID: "NU-6", Category: "nudge", Tone: "提示", Weight: 2, Template: "再给你 20 秒，想不出来我们就再看一遍释义。"},
		{ID: "NU-7", Category: "nudge", Tone: "安抚", Weight: 3, Template: "网络好像开小差了，我还在，别慌。"},
	}...)
	b.Add([]Template{
		// 5.7 feedback_reply 反馈回复（11，含 FR-11 多意图）
		{ID: "FR-1", Category: "feedback_reply", Tone: "确认", Weight: 3, Template: "收到！下次课的词量已调整为 {n} 个，先试试这个节奏。"},
		{ID: "FR-2", Category: "feedback_reply", Tone: "确认", Weight: 3, Template: "好嘞，{date} 的课已经挪到 {newDate}，到时候提醒你。"},
		{ID: "FR-3", Category: "feedback_reply", Tone: "确认", Weight: 3, Template: "“{word}”已标记为重点关照，下次课优先安排+多测几次。"},
		{ID: "FR-4", Category: "feedback_reply", Tone: "确认", Weight: 3, Template: "想把词量调到 {n}？这个数偏低哦，确认的话我马上改。"},
		{ID: "FR-5", Category: "feedback_reply", Tone: "婉拒", Weight: 2, Template: "一天 {n} 次已经是上限啦，贪多嚼不烂，我们先这样？"},
		{ID: "FR-6", Category: "feedback_reply", Tone: "安抚", Weight: 3, Template: "这个问题我记下来转给管理员了，会尽快处理，谢谢你反馈！"},
		{ID: "FR-7", Category: "feedback_reply", Tone: "澄清", Weight: 3, Template: "没太明白你的意思，是想少学点、改时间，还是某个词太难？"},
		{ID: "FR-8", Category: "feedback_reply", Tone: "观察", Weight: 2, Template: "收到你的建议！最近正确率 {p}%，我保持现在的节奏，继续观察几天。"},
		{ID: "FR-9", Category: "feedback_reply", Tone: "关怀", Weight: 3, Template: "好像最近缺了几次课，是时间太紧吗？要不要把节奏放慢一点？"},
		{ID: "FR-10", Category: "feedback_reply", Tone: "确认", Weight: 2, Template: "已经帮你把计划暂停了，想回来的时候随时说一声。"},
		{ID: "FR-11", Category: "feedback_reply", Tone: "合并", Weight: 3, Template: "收到！两件事都办好了：词量改为 {n} 个，{date} 的课挪到 {newDate}。"},
	}...)
	b.Add([]Template{
		// 5.9 report 课报（8，D-13：量化+定性）
		{ID: "RB-1", Category: "report", Tone: "评价", Weight: 3, Template: "【评价】{name}，今天 {n} 个词，首答对了 {c} 个，正确率 {p}%！{streak}"},
		{ID: "RB-2", Category: "report", Tone: "评价", Weight: 2, Template: "【评价】最难的是 {weak}，我们完整过了 {r} 遍，现在稳多了。"},
		{ID: "RB-3", Category: "report", Tone: "定性", Weight: 2, Template: "【评价】今天节奏{pace}，平均 {t} 秒一个词，{pace_comment}"},
		{ID: "RB-4", Category: "report", Tone: "建议", Weight: 3, Template: "【建议】{wrongwords} 这几个先混个脸熟就行，明天它们会先来找你。"},
		{ID: "RB-5", Category: "report", Tone: "建议", Weight: 2, Template: "【建议】到期的复习词今天清掉了 {done}/{plan}，{mem_comment}"},
		{ID: "RB-6", Category: "report", Tone: "总结", Weight: 3, Template: "【总结】到这里你已经拿下 {cum} 个词，毕业 {grad} 个，正确率比五课前 {trend}。"},
		{ID: "RB-7", Category: "report", Tone: "定性", Weight: 3, Template: "【定性】今天的整体状态：{solid}。{solid_comment}"},
		{ID: "RB-8", Category: "report", Tone: "数据", Weight: 2, Template: "【数据】首答率 {p}%（比上课 {delta_p}），最长连对 {streak}，平均 {t}s，补学 {r} 轮。"},
	}...)
	b.Add([]Template{
		// 5.8 system_notify 系统通知（6）
		{ID: "SN-1", Category: "system_notify", Tone: "通知", Weight: 3, Template: "下一课已安排：{date} {time}，词库《{book}》，共 {n} 词。"},
		{ID: "SN-2", Category: "system_notify", Tone: "通知", Weight: 3, Template: "{date} 的课改期到 {newDate}（你的请假申请）。"},
		{ID: "SN-3", Category: "system_notify", Tone: "提醒", Weight: 3, Template: "你缺席了 {date} 的课，已为你改到 {newDate}，别再错过啦。"},
		{ID: "SN-4", Category: "system_notify", Tone: "周报", Weight: 2, Template: "学习周报：本周学 {n} 词，毕业 {m} 词，正确率 {p}%。"},
		{ID: "SN-5", Category: "system_notify", Tone: "通知", Weight: 2, Template: "词库《{book}》有更新，新增 {n} 词。"},
		{ID: "SN-6", Category: "system_notify", Tone: "通知", Weight: 3, Template: "你的反馈已处理：{summary}。"},
	}...)
}
