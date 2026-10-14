import json
from pathlib import Path

texts = ["苹果", "老师", "图书", "安静", "我不知道", "我会了", "请再说一遍", "我想跳过", "每天两次", "每课五十个词", "我想减少单词", "课程改到明天"]
samples = []
for index, text in enumerate(texts, 1):
    samples.append(dict(case_id=f"ASR-ZH-{index:02}", text=text, language="zh",
                        category="word" if index <= 4 else "classroom" if index <= 8 else "feedback",
                        source="Piper zh_CN-huayan-medium, independent from platform sherpa zh-ll",
                        source_class="independent_synthetic", source_license="dataset license Unknown; local evaluation only; model weights not distributed",
                        file=f"zh/{index:02}.wav", human_listening="pending",
                        critical_slots={5: ["negation"], 9: ["number:2"], 10: ["number:50"], 12: ["date:tomorrow"]}.get(index, [])))
data = dict(schema_version=1, status="pending_independent_review", samples=samples,
            generator="Piper CPU onnx; default length_scale/noise_scale/noise_w; PCM16 mono 22050Hz; fixed text labels; no resampling",
            source_model_revision="c10ece1aade47bb51c153c893d14e5bf8e5b7117",
            source_model_sha256="9929917bf8cabb26fd528ea44d3a6699c11e87317a14765312420be230be0f3d")
with Path(__file__).with_name("audio-fixtures").joinpath("manifest-zh.json").open("x", encoding="utf-8") as file:
    json.dump(data, file, ensure_ascii=False, indent=2)
    file.write("\n")
