#!/usr/bin/env python3
"""Rolling-summary review: split input into chunks, sending the cumulative summary and next chunk each turn.

Prior upstream testing suggested an approximately 19,500-character request limit (see README). The full input can exceed this limit because each turn only sends bounded context.

  python3 scripts/context_review.py --input README.md --out review.md
  python3 scripts/context_review.py --self-test          # Offline: validate chunking and budgets

Environment variables: BASE_URL (gateway address), AUTH_KEY (when Authorization is enabled).
"""
import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request

MAX_CHARS = 19000  # Allow headroom beneath the previously observed upstream character limit.
SUMMARY_CHARS = 2500  # Cumulative summary budget; allocate the rest to the chunk.

INSTRUCTION = """You are reviewing code or documentation. You will receive an existing summary and the next input chunk.

Output only the following two sections. Do not add greetings or repeat instructions:

[Summary]
Combine the existing summary and new chunk into a summary of at most {limit} characters. Preserve useful facts, conclusions and open questions; discard irrelevant details.

[New Findings]
List issues found in this chunk, one per line: location — issue — evidence. If there are none, write 'None'.
"""


def split_chunks(text, budget):
    """Split by characters, preferably at paragraph boundaries; reconstruct the original exactly."""
    if budget < 200:
        raise ValueError("Budget too small")
    out, start = [], 0
    while start < len(text):
        end = min(start + budget, len(text))
        if end < len(text):
            cut = text.rfind("\n\n", start, end)
            if cut > start + budget // 2:  # Avoid tiny chunks.
                end = cut
        out.append(text[start:end])
        start = end
    return [c for c in out if c.strip()]


def build_prompt(summary, piece):
    instr = INSTRUCTION.format(limit=SUMMARY_CHARS)
    prior = summary or "(First chunk, no existing summary)"
    return f"{instr}\n[Existing Summary]\n{prior}\n\n[New Chunk]\n{piece}\n"


def parse(out):
    """Extract Summary and New Findings sections, tolerating extra output formatting."""
    summary, findings = "", ""
    if "[Summary]" in out:
        summary = out.split("[Summary]", 1)[1]
    if "[New Findings]" in summary:
        summary, findings = summary.split("[New Findings]", 1)
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
    # Chunks stay within the budget and reconstruct the input exactly.
    text = "Paragraph A.\n\n" + ("Long paragraph" * 900) + "\n\nParagraph B."
    chunks = split_chunks(text, 3000)
    assert len(chunks) > 1, chunks
    assert all(len(c) <= 3000 for c in chunks), [len(c) for c in chunks]
    assert "".join(chunks) == text, "Reassembled text differs from the original"
    assert "Paragraph A." in chunks[0] and "Paragraph B." in chunks[-1]
    # Instructions + summary + chunk must fit within MAX_CHARS.
    prompt = build_prompt("S" * SUMMARY_CHARS, "P" * (MAX_CHARS - 6000))
    assert len(prompt) <= MAX_CHARS, len(prompt)
    # Parsing
    s, f = parse("[Summary]\nReviewed the first half.\n[New Findings]\nREADME.md:12 — Incorrect limit — Observed 19.5k")
    assert s == "Reviewed the first half." and f.startswith("README.md:12"), (s, f)
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
        ap.error("--input is required")

    base = os.getenv("BASE_URL", "").rstrip("/")
    if not base:
        sys.exit("BASE_URL is not set (e.g. https://xxx.koyeb.app/v1)")
    key = os.getenv("AUTH_KEY", "")

    text = open(args.input, encoding="utf-8").read()
    # Calculate remaining chunk budget after instructions and summary reserve.
    piece_budget = MAX_CHARS - len(INSTRUCTION.format(limit=SUMMARY_CHARS)) - SUMMARY_CHARS - 40
    chunks = split_chunks(text, piece_budget)
    print(f"Input {len(text)} chars → {len(chunks)} chunks, maximum {piece_budget} chars each", flush=True)

    summary, findings_all = "", []
    with open(args.out, "w", encoding="utf-8") as rep:
        rep.write(f"# Review Report: {args.input}\n\nModel `{args.model}`, {len(chunks)} chunks, rolling summary.\n\n")
        for i, piece in enumerate(chunks, 1):
            prompt = build_prompt(summary, piece)
            for attempt in (1, 2):  # Retry transient upstream failures once, then fail CI.
                try:
                    out = call(base, args.model, key, prompt)
                    break
                except (urllib.error.URLError, urllib.error.HTTPError, OSError, KeyError) as e:
                    if attempt == 2:
                        rep.write(f"\n## Chunk {i}\n\n**Failed**：{e}\n")
                        rep.flush()
                        sys.exit(f"Chunk {i} request failed：{e}")
                    time.sleep(20)
            summary, findings = parse(out)
            summary = summary[:SUMMARY_CHARS]  # Keep the summary within its size limit.
            findings_all.append(findings)
            print(f"  [{i}/{len(chunks)}] summary {len(summary)} chars, findings {len(findings)} chars", flush=True)
            rep.write(f"\n## Chunk {i}\n\n{findings or 'None'}\n")
            rep.flush()
            time.sleep(2)
        rep.write(f"\n---\n\n## Final Cumulative Summary\n\n{summary}\n")
    print(f"Completed → {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
