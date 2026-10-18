#!/usr/bin/env python3
"""滚动摘要式审查：把任意长度的输入切成块，每轮只发「累计摘要 + 本次片段」。

上游单请求上限实测 ≈19,500 字符（ascii 与中文在同一点断，见 README），所以输入总长
不受上下文限制——被限制的只有「单轮正文」，靠累计摘要把历史压成有界的一段。

  python3 scripts/context_review.py --input README.md --out review.md
  python3 scripts/context_review.py --self-test          # 不联网，验分块与预算

环境变量：BASE_URL（网关地址）、AUTH_KEY（网关配了 Authorization 时填）。
"""
import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request

MAX_CHARS = 19000  # 上游按字符计（实测 19,500 通过 / 19,750 拒），留余量
SUMMARY_CHARS = 2500  # 累计摘要预算；片段的剩余空间全给它

INSTRUCTION = """你在做代码/文档审查。你会拿到「已有累计摘要」和「本次新增片段」。

只输出下面两段，不要寒暄、不要复述指令：

[摘要]
把已有摘要与本次片段合并成一份不超过 {limit} 字的累计摘要，保留对后续审查有用的事实、
结论、待查项；无关内容丢掉。

[新发现]
本次片段里发现的问题，每条一行，格式：位置 — 问题 — 依据。没有就写「无」。
"""


def split_chunks(text, budget):
    """按字符切，尽量切在段落边界。每块 <= budget，拼回去与原串完全一致。"""
    if budget < 200:
        raise ValueError("预算太小")
    out, start = [], 0
    while start < len(text):
        end = min(start + budget, len(text))
        if end < len(text):
            cut = text.rfind("\n\n", start, end)
            if cut > start + budget // 2:  # 别切太碎
                end = cut
        out.append(text[start:end])
        start = end
    return [c for c in out if c.strip()]


def build_prompt(summary, piece):
    instr = INSTRUCTION.format(limit=SUMMARY_CHARS)
    prior = summary or "（这是第一块，还没有摘要）"
    return f"{instr}\n[已有累计摘要]\n{prior}\n\n[本次新增片段]\n{piece}\n"


def parse(out):
    """取出 [摘要] 与 [新发现] 两段；模型偶尔加壳也不至于丢内容。"""
    summary, findings = "", ""
    if "[摘要]" in out:
        summary = out.split("[摘要]", 1)[1]
    if "[新发现]" in summary:
        summary, findings = summary.split("[新发现]", 1)
    return summary.strip(), findings.strip()


def call(base, model, key, prompt, timeout=180):
    body = json.dumps({"model": model, "stream": False, "reasoning_effort": "low",
                       "messages": [{"role": "user", "content": prompt}]}).encode()
    headers = {"Content-Type": "application/json"}
    if key:
        headers["Authorization"] = "Bearer " + key
    req = urllib.request.Request(base.rstrip("/") + "/chat/completions", data=body, headers=headers)
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.load(r)["choices"][0]["message"].get("content") or ""


def self_test():
    # 分块：任一块都不超预算，且拼回去逐字一致（不丢、不重）
    text = "段落甲。\n\n" + ("很长的一段" * 900) + "\n\n段落乙。"
    chunks = split_chunks(text, 3000)
    assert len(chunks) > 1, chunks
    assert all(len(c) <= 3000 for c in chunks), [len(c) for c in chunks]
    assert "".join(chunks) == text, "拼接后与原文不一致"
    assert "段落甲。" in chunks[0] and "段落乙。" in chunks[-1]
    # 预算算得住：指令 + 摘要 + 片段 <= MAX_CHARS
    prompt = build_prompt("摘" * SUMMARY_CHARS, "片" * (MAX_CHARS - 6000))
    assert len(prompt) <= MAX_CHARS, len(prompt)
    # 解析
    s, f = parse("[摘要]\n已看完前半。\n[新发现]\nREADME.md:12 — 上限写错 — 实测 19.5k")
    assert s == "已看完前半。" and f.startswith("README.md:12"), (s, f)
    print("self-test ok")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--input", required=False)
    ap.add_argument("--out", default="review.md")
    ap.add_argument("--model", default=os.getenv("MODEL", "gpt-5.6-luna"))
    ap.add_argument("--self-test", action="store_true")
    args = ap.parse_args()

    if args.self_test:
        self_test()
        return 0
    if not args.input:
        ap.error("--input 必填")

    base = os.getenv("BASE_URL", "").rstrip("/")
    if not base:
        sys.exit("BASE_URL 未设置（例：https://xxx.koyeb.app/v1）")
    key = os.getenv("AUTH_KEY", "")

    text = open(args.input, encoding="utf-8").read()
    # 每轮能放多少正文：总预算 - 指令 - 摘要预留
    piece_budget = MAX_CHARS - len(INSTRUCTION.format(limit=SUMMARY_CHARS)) - SUMMARY_CHARS - 40
    chunks = split_chunks(text, piece_budget)
    print(f"输入 {len(text)} 字符 → {len(chunks)} 块，每块 ≤{piece_budget} 字符", flush=True)

    summary, findings_all = "", []
    with open(args.out, "w", encoding="utf-8") as rep:
        rep.write(f"# 审查报告：{args.input}\n\n模型 `{args.model}`，{len(chunks)} 块，滚动摘要。\n\n")
        for i, piece in enumerate(chunks, 1):
            prompt = build_prompt(summary, piece)
            for attempt in (1, 2):  # 上游抖动重试一次，仍失败就让 CI 红
                try:
                    out = call(base, args.model, key, prompt)
                    break
                except (urllib.error.URLError, urllib.error.HTTPError, OSError, KeyError) as e:
                    if attempt == 2:
                        rep.write(f"\n## 第 {i} 块\n\n**失败**：{e}\n")
                        rep.flush()
                        sys.exit(f"第 {i} 块调用失败：{e}")
                    time.sleep(20)
            summary, findings = parse(out)
            summary = summary[:SUMMARY_CHARS]  # 摘要不能自己滚大
            findings_all.append(findings)
            print(f"  [{i}/{len(chunks)}] 摘要 {len(summary)} 字，本块发现 {len(findings)} 字", flush=True)
            rep.write(f"\n## 第 {i} 块\n\n{findings or '无'}\n")
            rep.flush()
            time.sleep(2)
        rep.write(f"\n---\n\n## 最终累计摘要\n\n{summary}\n")
    print(f"完成 → {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
