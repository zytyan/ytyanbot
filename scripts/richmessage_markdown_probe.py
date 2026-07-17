#!/usr/bin/env python3
"""Probe Telegram RichMessage Markdown parsing with reproducible edge cases.

The script intentionally uses only the Python standard library. Credentials are
read from the environment and are never included in reports or error messages.

Example:
    TELEGRAM_BOT_TOKEN=... TELEGRAM_CHAT_ID=... \
      python3 scripts/richmessage_markdown_probe.py --suite all
"""

from __future__ import annotations

import argparse
import dataclasses
import datetime as dt
import json
import os
import pathlib
import sys
import time
import urllib.error
import urllib.request
from collections import Counter
from typing import Any, Iterable


DEFAULT_DELAY_SECONDS = 3.1
API_TIMEOUT_SECONDS = 45
LABEL_TEMPLATE = "[RM:{case_id}]"
# Cases are batched into RichMessages, but each passing batch is deleted before
# the next one so an interrupted run leaves at most the in-flight message.
DELETE_BATCH_SIZE = 1
DEFAULT_CASES_PER_MESSAGE = 12


@dataclasses.dataclass(frozen=True)
class ProbeCase:
    case_id: str
    markdown: str
    family: str
    expectation: str
    intent: str
    spec_expectation: str
    required_types: tuple[str, ...] = ()
    forbidden_types: tuple[str, ...] = ()
    skip_entity_detection: bool = False
    with_label: bool = True
    expected_block_count: int | None = None
    expected_table_columns: int | None = None
    expected_text_length: int | None = None

    def source(self) -> str:
        if not self.with_label:
            return self.markdown
        return f"{LABEL_TEMPLATE.format(case_id=self.case_id)}\n\n{self.markdown}"


class TelegramAPIError(Exception):
    def __init__(
        self,
        method: str,
        error_code: int | None,
        description: str,
        parameters: dict[str, Any] | None = None,
    ) -> None:
        super().__init__(f"{method}: {error_code or 'network'} {description}")
        self.method = method
        self.error_code = error_code
        self.description = description
        self.parameters = parameters or {}

    def as_report(self) -> dict[str, Any]:
        return {
            "method": self.method,
            "error_code": self.error_code,
            "description": self.description,
            "parameters": self.parameters,
        }


class TelegramClient:
    def __init__(self, token: str, delay_seconds: float) -> None:
        self._base_url = f"https://api.telegram.org/bot{token}/"
        self._delay_seconds = delay_seconds
        self._last_send_at = 0.0

    def call(
        self,
        method: str,
        payload: dict[str, Any],
        *,
        retry_429: int = 4,
    ) -> Any:
        if method in {"sendRichMessage", "sendMessage"}:
            remaining = self._delay_seconds - (time.monotonic() - self._last_send_at)
            if remaining > 0:
                time.sleep(remaining)

        encoded = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        for attempt in range(retry_429 + 1):
            request = urllib.request.Request(
                self._base_url + method,
                data=encoded,
                headers={"Content-Type": "application/json"},
                method="POST",
            )
            if method in {"sendRichMessage", "sendMessage"}:
                self._last_send_at = time.monotonic()
            try:
                with urllib.request.urlopen(request, timeout=API_TIMEOUT_SECONDS) as response:
                    body = json.load(response)
            except urllib.error.HTTPError as exc:
                try:
                    body = json.loads(exc.read().decode("utf-8", errors="replace"))
                except (json.JSONDecodeError, UnicodeDecodeError):
                    body = {
                        "ok": False,
                        "error_code": exc.code,
                        "description": "non-JSON Telegram HTTP error",
                    }
            except urllib.error.URLError as exc:
                reason = getattr(exc, "reason", None)
                raise TelegramAPIError(method, None, f"network error: {reason}") from None
            except TimeoutError:
                raise TelegramAPIError(method, None, "network timeout") from None

            if body.get("ok") is True:
                return body.get("result")

            error_code = body.get("error_code")
            description = str(body.get("description", "unknown Telegram API error"))
            parameters = body.get("parameters") or {}
            retry_after = parameters.get("retry_after")
            if error_code == 429 and retry_after is not None and attempt < retry_429:
                time.sleep(max(float(retry_after), 0.0) + 0.25)
                continue
            raise TelegramAPIError(method, error_code, description, parameters)

        raise TelegramAPIError(method, 429, "rate-limit retry budget exhausted")


def collect_types(value: Any) -> list[str]:
    found: list[str] = []
    if isinstance(value, dict):
        node_type = value.get("type")
        if isinstance(node_type, str):
            found.append(node_type)
        for child in value.values():
            found.extend(collect_types(child))
    elif isinstance(value, list):
        for child in value:
            found.extend(collect_types(child))
    return found


def flatten_text(value: Any) -> str:
    if isinstance(value, str):
        return value
    if isinstance(value, list):
        return "".join(flatten_text(child) for child in value)
    if not isinstance(value, dict):
        return ""

    if value.get("type") == "mathematical_expression":
        return str(value.get("expression", ""))
    pieces: list[str] = []
    for key in ("text", "summary", "credit", "caption", "blocks", "items", "cells"):
        if key in value:
            pieces.append(flatten_text(value[key]))
    return "".join(pieces)


def canonicalize(value: Any) -> Any:
    if isinstance(value, dict):
        return {key: canonicalize(value[key]) for key in sorted(value)}
    if isinstance(value, list):
        return [canonicalize(child) for child in value]
    return value


def body_blocks(case: ProbeCase, rich_message: dict[str, Any]) -> list[Any]:
    blocks = list(rich_message.get("blocks") or [])
    if not case.with_label or not blocks:
        return blocks
    expected_label = LABEL_TEMPLATE.format(case_id=case.case_id)
    if expected_label in flatten_text(blocks[0]):
        return blocks[1:]
    return blocks


def max_table_columns(value: Any) -> int:
    maximum = 0
    if isinstance(value, dict):
        if value.get("type") == "table":
            for row in value.get("cells") or []:
                if isinstance(row, list):
                    maximum = max(maximum, len(row))
        for child in value.values():
            maximum = max(maximum, max_table_columns(child))
    elif isinstance(value, list):
        for child in value:
            maximum = max(maximum, max_table_columns(child))
    return maximum


def make_case(
    case_id: str,
    markdown: str,
    family: str,
    expectation: str,
    intent: str,
    spec_expectation: str,
    *,
    required: Iterable[str] = (),
    forbidden: Iterable[str] = (),
    skip_entity_detection: bool = False,
) -> ProbeCase:
    return ProbeCase(
        case_id=case_id,
        markdown=markdown,
        family=family,
        expectation=expectation,
        intent=intent,
        spec_expectation=spec_expectation,
        required_types=tuple(required),
        forbidden_types=tuple(forbidden),
        skip_entity_detection=skip_entity_detection,
    )


def core_cases() -> list[ProbeCase]:
    cases: list[ProbeCase] = []
    delimiters = (
        ("star", "*", "italic"),
        ("strong_star", "**", "bold"),
        ("underscore", "_", "italic"),
        ("strong_underscore", "__", "bold"),
        ("strike", "~~", "strikethrough"),
        ("mark", "==", "marked"),
        ("spoiler", "||", "spoiler"),
    )
    paired_punctuation = (
        ("curly_double", "“", "”"),
        ("curly_single", "‘", "’"),
        ("corner", "「", "」"),
        ("white_corner", "『", "』"),
        ("full_paren", "（", "）"),
        ("lenticular", "【", "】"),
        ("book", "《", "》"),
        ("angle", "〈", "〉"),
        ("tortoise", "〔", "〕"),
    )

    for name, delimiter, node_type in delimiters:
        cases.append(
            make_case(
                f"baseline_{name}",
                f"前 {delimiter}内容{delimiter} 后",
                f"baseline-{name}",
                "format",
                "格式标记在普通空格之间应生效",
                "文档明确支持此行内格式",
                required=(node_type,),
            )
        )
        for punct_name, opener, closer in paired_punctuation:
            cases.append(
                make_case(
                    f"pair_out_{name}_{punct_name}",
                    f"前{opener}{delimiter}内容{delimiter}{closer}后",
                    f"pair-outside-{name}",
                    "format",
                    "标点位于格式标记外，内容应格式化",
                    "分隔符直接接内容，符合 GFM flanking 规则",
                    required=(node_type,),
                )
            )
            cases.append(
                make_case(
                    f"pair_in_{name}_{punct_name}",
                    f"前{delimiter}{opener}内容{closer}{delimiter}后",
                    f"pair-inside-{name}",
                    "hazard",
                    "LLM 通常意图将连同标点在内的片段格式化",
                    "标点紧邻分隔符且外侧是文字，可能不满足 GFM flanking 规则",
                    required=(node_type,),
                )
            )

    sentence_punctuation = (
        ("comma", "，"),
        ("period", "。"),
        ("exclamation", "！"),
        ("question", "？"),
        ("enumeration", "、"),
        ("semicolon", "；"),
        ("colon", "："),
        ("ellipsis", "……"),
        ("emdash", "—"),
    )
    for index, (punct_name, punct) in enumerate(sentence_punctuation):
        name, delimiter, node_type = delimiters[index % len(delimiters)]
        cases.append(
            make_case(
                f"sentence_in_{name}_{punct_name}",
                f"前{delimiter}内容{punct}{delimiter}后",
                f"sentence-inside-{name}",
                "hazard",
                "LLM 意图将句末标点一并格式化",
                "结束标点在分隔符内且后接文字，可能破坏右 flanking",
                required=(node_type,),
            )
        )
        cases.append(
            make_case(
                f"sentence_out_{name}_{punct_name}",
                f"前{delimiter}内容{delimiter}{punct}后",
                f"sentence-outside-{name}",
                "format",
                "句末标点位于格式标记外，内容应格式化",
                "符合 GFM flanking 规则",
                required=(node_type,),
            )
        )

    for punct_name, punct in (
        ("comma", ","),
        ("period", "."),
        ("exclamation", "!"),
        ("question", "?"),
        ("semicolon", ";"),
        ("colon", ":"),
    ):
        cases.append(
            make_case(
                f"ascii_in_bold_{punct_name}",
                f"before**text{punct}**after",
                "ascii-punctuation-inside",
                "hazard",
                "LLM 意图把 ASCII 标点包含在粗体中",
                "GFM 对 Unicode 与 ASCII 标点使用同类 flanking 规则",
                required=("bold",),
            )
        )
        cases.append(
            make_case(
                f"ascii_out_bold_{punct_name}",
                f"before**text**{punct}after",
                "ascii-punctuation-outside",
                "format",
                "ASCII 标点位于格式标记外",
                "符合 GFM flanking 规则",
                required=("bold",),
            )
        )

    cases.extend(
        (
            make_case(
                "fixture_paren_inside",
                "由**（内容）**开发。",
                "repository-fixture",
                "hazard",
                "括号和内容整体应为粗体",
                "仓库当前为此类 Goldmark 边缘输入做了预处理",
                required=("bold",),
            ),
            make_case(
                "fixture_quote_inside",
                "这是**“内容”**。",
                "repository-fixture",
                "hazard",
                "引号和内容整体应为粗体",
                "引号在分隔符内可能触发 flanking 限制",
                required=("bold",),
            ),
            make_case(
                "fixture_quote_outside",
                "这是“**内容**”。",
                "repository-fixture-valid",
                "format",
                "仅引号内文字为粗体",
                "符合 GFM flanking 规则",
                required=("bold",),
            ),
            make_case(
                "fixture_period_inside",
                "前**内容。**继续",
                "repository-fixture-close",
                "hazard",
                "句号和内容整体应为粗体",
                "结束标点在分隔符内且后接文字，可能破坏右 flanking",
                required=("bold",),
            ),
            make_case(
                "fixture_period_outside",
                "前**内容**。继续",
                "repository-fixture-valid",
                "format",
                "内容应为粗体，句号在外",
                "符合 GFM flanking 规则",
                required=("bold",),
            ),
            make_case(
                "space_ascii",
                "前 **内容** 后",
                "unicode-space",
                "format",
                "ASCII 空格边界",
                "标准空白边界",
                required=("bold",),
            ),
            make_case(
                "space_nbsp",
                "前\u00a0**内容**\u00a0后",
                "unicode-space",
                "format",
                "NBSP 应被视为空白边界",
                "GFM 将 Unicode 空白用于 flanking 判断",
                required=("bold",),
            ),
            make_case(
                "space_ideographic",
                "前\u3000**内容**\u3000后",
                "unicode-space",
                "format",
                "全角空格应作为空白边界",
                "GFM 将 Unicode 空白用于 flanking 判断",
                required=("bold",),
            ),
            make_case(
                "space_zero_width",
                "前\u200b**内容**\u200b后",
                "unicode-zero-width",
                "hazard",
                "零宽空格视觉上像空白，LLM 仍意图格式化",
                "U+200B 不一定被解析器视为 Unicode 空白",
                required=("bold",),
            ),
            make_case(
                "unicode_combining",
                "**e\u0301与中文**",
                "unicode-content",
                "format",
                "组合字符不应破坏粗体",
                "有效粗体内容",
                required=("bold",),
            ),
            make_case(
                "unicode_emoji_zwj",
                "**👩\u200d💻️与中文**",
                "unicode-content",
                "format",
                "ZWJ emoji 和变体选择符不应破坏粗体",
                "有效粗体内容",
                required=("bold",),
            ),
            make_case(
                "unicode_non_bmp",
                "**𠀀😄内容**",
                "unicode-content",
                "format",
                "非 BMP 字符不应破坏粗体",
                "有效粗体内容",
                required=("bold",),
            ),
            make_case(
                "multiline_bold",
                "**第一行\n第二行**",
                "multiline-inline",
                "format",
                "软换行两侧仍为同一个粗体范围",
                "CommonMark 允许强调跨软换行",
                required=("bold",),
            ),
            make_case(
                "nested_bold_italic",
                "***粗斜体***",
                "nested-inline",
                "format",
                "内容同时为粗体和斜体",
                "GFM 支持嵌套强调",
                required=("bold", "italic"),
            ),
            make_case(
                "rule_of_three",
                "***foo** bar*",
                "rule-of-three",
                "format",
                "外层斜体、局部粗体",
                "GFM rule-of-three 示例形态",
                required=("bold", "italic"),
            ),
            make_case(
                "intraword_underscore",
                "foo_bar_baz",
                "intraword",
                "literal",
                "词内下划线应保持文字",
                "GFM 禁止这种词内下划线强调",
                forbidden=("italic",),
            ),
            make_case(
                "intraword_star",
                "foo*bar*baz",
                "intraword",
                "format",
                "星号允许词内强调",
                "GFM 允许星号词内强调",
                required=("italic",),
            ),
            make_case(
                "escaped_delimiters",
                r"\*内容\* 和 \*\*内容\*\*",
                "escaping",
                "literal",
                "转义后的星号应按原文显示",
                "GFM 反斜杠转义",
                forbidden=("italic", "bold"),
            ),
            make_case(
                "unclosed_bold",
                "**未闭合内容",
                "malformed-inline",
                "literal",
                "未闭合分隔符应安全降级为文字",
                "CommonMark 文档始终可作为普通文本解析",
                forbidden=("bold",),
            ),
            make_case(
                "code_span_literal_markdown",
                "`**内容** ==高亮==`",
                "code-literal",
                "format",
                "代码 span 内的 Markdown 不再解析",
                "代码 span 优先于强调",
                required=("code",),
                forbidden=("bold", "marked"),
            ),
            make_case(
                "code_fence_literal_markdown",
                "```text\n**内容** ==高亮==\n```",
                "code-literal",
                "format",
                "代码块内的 Markdown 不再解析",
                "围栏代码块是独立 block",
                required=("pre",),
                forbidden=("bold", "marked"),
            ),
            make_case(
                "link_nested_parentheses",
                "[中文（测试）](https://example.com/a_(b)?q=%E4%B8%AD)",
                "links",
                "format",
                "括号 URL 与中文链接文字应形成链接",
                "GFM 支持平衡括号链接目标",
                required=("url",),
            ),
            make_case(
                "link_cjk_punctuation",
                "请看（[链接](https://example.com)）。",
                "links",
                "format",
                "中文标点包围的链接应正常解析",
                "链接边界明确",
                required=("url",),
            ),
            make_case(
                "link_unclosed",
                "[链接](https://example.com",
                "malformed-link",
                "format",
                "未闭合 Markdown 链接降级后，裸 URL 仍应被自动识别",
                "CommonMark 链接无效，但 Telegram 自动实体检测仍然生效",
                required=("url",),
            ),
            make_case(
                "link_unclosed_skip",
                "[链接](https://example.com",
                "malformed-link",
                "literal",
                "关闭自动实体检测后，未闭合链接应完整降级为文字",
                "无有效 Markdown 链接，且 skip_entity_detection 为真",
                forbidden=("url",),
                skip_entity_detection=True,
            ),
            make_case(
                "heading",
                "### 中文标题",
                "rich-block",
                "manual",
                "三级标题应形成 heading block",
                "Telegram Rich Markdown 明确支持六级标题",
                required=("heading",),
            ),
            make_case(
                "unordered_list",
                "- 第一项\n- 第二项",
                "rich-block",
                "manual",
                "应形成无序列表",
                "Telegram Rich Markdown 明确支持列表",
                required=("list",),
            ),
            make_case(
                "task_list",
                "- [ ] 待办\n- [x] 完成",
                "rich-block",
                "manual",
                "应形成带复选框的列表",
                "Telegram Rich Markdown 明确支持任务列表",
                required=("list",),
            ),
            make_case(
                "blockquote",
                "> 引用中的 **粗体**",
                "rich-block",
                "manual",
                "应形成包含粗体的引用块",
                "Telegram Rich Markdown 明确支持引用",
                required=("blockquote", "bold"),
            ),
            make_case(
                "table",
                "| 指标 | 值 |\n|:---|---:|\n| 状态 | **正常** |",
                "rich-block",
                "manual",
                "应形成表格并保留单元格粗体",
                "Telegram Rich Markdown 明确支持 GFM 表格",
                required=("table", "bold"),
            ),
            make_case(
                "footnote",
                "正文引用[^注]。\n\n[^注]: 中文脚注。",
                "rich-footnote",
                "manual",
                "应形成脚注引用和定义",
                "Telegram Rich Markdown 明确支持脚注",
                required=("reference_link", "reference"),
            ),
            make_case(
                "inline_math",
                "公式 $x^2 + y^2$。",
                "rich-math",
                "manual",
                "行内公式应成为数学表达式",
                "Telegram Rich Markdown 明确支持行内 LaTeX",
                required=("mathematical_expression",),
            ),
            make_case(
                "block_math",
                "$$E = mc^2$$",
                "rich-math",
                "manual",
                "块公式应成为数学表达式 block",
                "Telegram Rich Markdown 明确支持块 LaTeX",
                required=("mathematical_expression",),
            ),
            make_case(
                "inline_html_markdown",
                "<u>下划线中的 **粗体**</u>",
                "html-mixed",
                "format",
                "行内 HTML 内继续解析 Markdown",
                "Telegram 文档明确说明 inline tag 内解析 Markdown",
                required=("underline", "bold"),
            ),
            make_case(
                "block_html_no_markdown",
                "<blockquote>**保持星号**</blockquote>",
                "html-mixed",
                "literal",
                "block HTML 内的 Markdown 标记保持文字",
                "除 details/collage/slideshow 外，block HTML 内不解析 Markdown",
                required=("blockquote",),
                forbidden=("bold",),
            ),
            make_case(
                "details_markdown",
                "<details open><summary>摘要</summary>\n\n**粗体详情**\n\n</details>",
                "html-details",
                "manual",
                "details 内应继续解析 Markdown",
                "Telegram 文档将 details 列为 block HTML 例外",
                required=("details", "bold"),
            ),
            make_case(
                "unsupported_html",
                "<blink>不支持的标签</blink>",
                "malformed-html",
                "literal",
                "不支持的 HTML 标签应被安全忽略并保留内部文字",
                "Telegram 只渲染文档列出的 HTML 标签",
            ),
        )
    )

    entities = (
        ("url", "地址：https://example.com，结束", "url"),
        ("email", "邮箱：probe@example.com，结束", "email_address"),
        ("mention", "用户：@telegram，结束", "mention"),
        ("hashtag", "话题：#中文标签，结束", "hashtag"),
        ("cashtag", "资产：$USD，结束", "cashtag"),
        ("command", "命令：/start，结束", "bot_command"),
        ("phone", "电话：+12345678901，结束", "phone_number"),
        ("bank_card", "卡号：4242 4242 4242 4242，结束", "bank_card_number"),
    )
    for name, markdown, node_type in entities:
        cases.append(
            make_case(
                f"entity_on_{name}",
                markdown,
                f"entity-{name}",
                "format",
                "自动实体检测应识别中文标点边界",
                "Telegram 文档列出该自动实体类型",
                required=(node_type,),
            )
        )
        cases.append(
            make_case(
                f"entity_off_{name}",
                markdown,
                f"entity-skip-{name}",
                "literal",
                "skip_entity_detection 应阻止自动实体生成",
                "Telegram 文档定义 skip_entity_detection",
                forbidden=(node_type,),
                skip_entity_detection=True,
            )
        )
    cases.extend(
        (
            make_case(
                "entity_control_email_ascii",
                "email probe@example.com end",
                "entity-control-email",
                "format",
                "ASCII 空格边界中的邮箱应被自动识别",
                "Telegram 文档声明自动检测邮箱",
                required=("email_address",),
            ),
            make_case(
                "entity_control_bank_card_ascii",
                "card 4242 4242 4242 4242 end",
                "entity-control-bank-card",
                "format",
                "ASCII 空格边界中的银行卡号应被自动识别",
                "Telegram 文档声明自动检测银行卡号",
                required=("bank_card_number",),
            ),
        )
    )
    return cases


def exact_length_source(case_id: str, length: int, fill: str = "x") -> str:
    if len(fill) != 1:
        raise ValueError("length boundary fill must be one Unicode code point")
    prefix = f"{LABEL_TEMPLATE.format(case_id=case_id)}\n\n"
    visible_prefix_length = len(LABEL_TEMPLATE.format(case_id=case_id))
    if visible_prefix_length >= length:
        raise ValueError("length boundary is smaller than probe label")
    # Telegram's limit is applied to parsed rich-message text. The blank line
    # separates blocks but doesn't appear in RichText, so it isn't counted.
    return prefix + (fill * (length - visible_prefix_length))


def exact_source_bytes_source(case_id: str, byte_length: int) -> str:
    prefix = f"{LABEL_TEMPLATE.format(case_id=case_id)}\n\n"
    remaining = byte_length - len(prefix.encode("utf-8"))
    if remaining < 1:
        raise ValueError("source byte boundary is smaller than probe label")
    cjk_count, ascii_remainder = divmod(remaining, len("文".encode("utf-8")))
    source = prefix + ("文" * cjk_count) + ("x" * ascii_remainder)
    if len(source.encode("utf-8")) != byte_length:
        raise ValueError("failed to construct exact Markdown UTF-8 byte boundary")
    return source


def plain_boundary_text_length(source: str) -> int:
    return len(source.replace("\n\n", "", 1))


def exact_block_source(case_id: str, count: int) -> str:
    if count < 1:
        raise ValueError("block count must be positive")
    paragraphs = [LABEL_TEMPLATE.format(case_id=case_id)]
    paragraphs.extend(f"段落 {index}" for index in range(1, count))
    return "\n\n".join(paragraphs)


def nested_quote_source(case_id: str, depth: int) -> str:
    return f"{LABEL_TEMPLATE.format(case_id=case_id)}\n\n" + ("> " * depth) + "嵌套"


def table_source(case_id: str, columns: int) -> str:
    headers = "| " + " | ".join(f"H{i}" for i in range(columns)) + " |"
    separators = "| " + " | ".join("---" for _ in range(columns)) + " |"
    values = "| " + " | ".join(f"V{i}" for i in range(columns)) + " |"
    return f"{LABEL_TEMPLATE.format(case_id=case_id)}\n\n{headers}\n{separators}\n{values}"


def limit_cases() -> list[ProbeCase]:
    return [
        ProbeCase(
            case_id="limit_chars_32768",
            markdown=exact_length_source("limit_chars_32768", 32768),
            family="limit-characters",
            expectation="format",
            intent="文档声明的最大字符数应被接受",
            spec_expectation="32768 字符上限",
            with_label=False,
            expected_text_length=32768,
        ),
        ProbeCase(
            case_id="limit_chars_32769",
            markdown=exact_length_source("limit_chars_32769", 32769),
            family="limit-characters",
            expectation="reject",
            intent="超过字符上限应被拒绝",
            spec_expectation="超过 32768 字符",
            with_label=False,
        ),
        ProbeCase(
            case_id="limit_cjk_chars_32768",
            markdown=exact_length_source("limit_cjk_chars_32768", 32768, "文"),
            family="limit-characters-cjk",
            expectation="hazard",
            intent="32768 个中文字符应完整保留，不应按 UTF-8 字节静默截断",
            spec_expectation="文档声明 32768 UTF-8 characters，而非 35000 bytes",
            with_label=False,
            expected_text_length=32768,
        ),
        *[
            ProbeCase(
                case_id=f"limit_source_bytes_{byte_length}",
                markdown=exact_source_bytes_source(
                    f"limit_source_bytes_{byte_length}", byte_length
                ),
                family="limit-source-bytes",
                expectation="boundary",
                intent="源 Markdown 必须完整保留或被明确拒绝，不能静默截断",
                spec_expectation="探索云端解析器接近 35000 UTF-8 bytes 的实际阈值",
                with_label=False,
                expected_text_length=plain_boundary_text_length(
                    exact_source_bytes_source(
                        f"limit_source_bytes_{byte_length}", byte_length
                    )
                ),
            )
            for byte_length in range(34995, 35001)
        ],
        ProbeCase(
            case_id="limit_blocks_500",
            markdown=exact_block_source("limit_blocks_500", 500),
            family="limit-blocks",
            expectation="format",
            intent="文档声明的最大 block 数应被接受",
            spec_expectation="500 blocks 上限",
            with_label=False,
            expected_block_count=500,
        ),
        ProbeCase(
            case_id="limit_blocks_501",
            markdown=exact_block_source("limit_blocks_501", 501),
            family="limit-blocks",
            expectation="reject",
            intent="超过 block 上限应被拒绝",
            spec_expectation="超过 500 blocks",
            with_label=False,
        ),
        ProbeCase(
            case_id="limit_nesting_16",
            markdown=nested_quote_source("limit_nesting_16", 16),
            family="limit-nesting",
            expectation="format",
            intent="文档声明的最大嵌套层数应被接受",
            spec_expectation="16 层嵌套上限",
            required_types=("blockquote",),
            with_label=False,
        ),
        ProbeCase(
            case_id="limit_nesting_17",
            markdown=nested_quote_source("limit_nesting_17", 17),
            family="limit-nesting",
            expectation="reject",
            intent="超过嵌套上限应被拒绝",
            spec_expectation="超过 16 层嵌套",
            with_label=False,
        ),
        ProbeCase(
            case_id="limit_table_20",
            markdown=table_source("limit_table_20", 20),
            family="limit-table-columns",
            expectation="format",
            intent="文档声明的最大表格列数应被接受",
            spec_expectation="20 列上限",
            required_types=("table",),
            with_label=False,
            expected_table_columns=20,
        ),
        ProbeCase(
            case_id="limit_table_21",
            markdown=table_source("limit_table_21", 21),
            family="limit-table-columns",
            expectation="reject",
            intent="超过表格列数上限应被拒绝",
            spec_expectation="超过 20 列",
            with_label=False,
        ),
    ]


def evaluate_success(
    case: ProbeCase,
    rich_message: dict[str, Any],
) -> tuple[str, list[str], list[str], list[str]]:
    blocks = body_blocks(case, rich_message)
    observed_types = collect_types(blocks)
    observed_set = set(observed_types)
    missing = [node_type for node_type in case.required_types if node_type not in observed_set]
    forbidden = [node_type for node_type in case.forbidden_types if node_type in observed_set]
    structural_errors: list[str] = []

    if case.expected_block_count is not None:
        actual = len(rich_message.get("blocks") or [])
        if actual != case.expected_block_count:
            structural_errors.append(
                f"expected {case.expected_block_count} blocks, observed {actual}"
            )
    if case.expected_table_columns is not None:
        actual = max_table_columns(rich_message)
        if actual != case.expected_table_columns:
            structural_errors.append(
                f"expected {case.expected_table_columns} table columns, observed {actual}"
            )
    if case.expected_text_length is not None:
        actual = len(flatten_text(rich_message.get("blocks") or []))
        if actual != case.expected_text_length:
            structural_errors.append(
                f"expected {case.expected_text_length} text characters, observed {actual}"
            )

    if case.expectation == "reject":
        return "FAIL", missing, forbidden, ["input was accepted but rejection was expected"]
    if missing or forbidden or structural_errors:
        if case.expectation in {"hazard", "boundary"} and not forbidden:
            return "HAZARD", missing, forbidden, structural_errors
        return "FAIL", missing, forbidden, structural_errors
    if case.expectation == "literal":
        return "EXPECTED_LITERAL", missing, forbidden, structural_errors
    if case.expectation == "manual":
        return "MANUAL", missing, forbidden, structural_errors
    return "PASS", missing, forbidden, structural_errors


def evaluate_error(case: ProbeCase, error: TelegramAPIError) -> str:
    if case.expectation in {"reject", "boundary"} and error.error_code == 400:
        return "EXPECTED_REJECT"
    return "FAIL"


def retention_signature(result: dict[str, Any]) -> tuple[Any, ...]:
    return (
        result["classification"],
        result["family"],
        tuple(result.get("missing_types") or ()),
        tuple(result.get("forbidden_types_seen") or ()),
        (result.get("error") or {}).get("description"),
    )


def write_report(path: pathlib.Path, report: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(
        json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    os.replace(temporary, path)


def make_summary(report: dict[str, Any]) -> str:
    counts = Counter(result["classification"] for result in report["results"])
    lines = [
        "RichMessage Markdown probe complete",
        f"run: {report['run_id']}",
        f"cases: {len(report['results'])}/{report['case_count']}",
    ]
    for name in (
        "PASS",
        "EXPECTED_LITERAL",
        "EXPECTED_REJECT",
        "HAZARD",
        "MANUAL",
        "FAIL",
    ):
        if counts[name]:
            lines.append(f"{name}: {counts[name]}")

    notable = [
        result["case_id"]
        for result in report["results"]
        if result["classification"] in {"HAZARD", "MANUAL", "FAIL"}
    ]
    if notable:
        lines.append("notable: " + ", ".join(notable[:40]))
        if len(notable) > 40:
            lines.append(f"notable list truncated; {len(notable) - 40} more in JSON report")
    if report.get("stopped_reason"):
        lines.append("stopped: " + str(report["stopped_reason"]))
    return "\n".join(lines)[:3900]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Probe Telegram RichMessage Markdown parsing edge cases."
    )
    parser.add_argument(
        "--chat-id",
        default=os.environ.get("TELEGRAM_CHAT_ID"),
        help="target chat ID (default: TELEGRAM_CHAT_ID)",
    )
    parser.add_argument(
        "--suite",
        choices=("core", "limits", "all"),
        default="core",
        help="case suite to run (default: core)",
    )
    parser.add_argument(
        "--report",
        type=pathlib.Path,
        help="JSON report path (default: timestamped file under /tmp)",
    )
    parser.add_argument(
        "--keep-all",
        action="store_true",
        help="keep every successfully sent probe message",
    )
    parser.add_argument(
        "--no-summary",
        action="store_true",
        help="do not send the final plain-text summary message",
    )
    parser.add_argument(
        "--delay",
        type=float,
        default=DEFAULT_DELAY_SECONDS,
        help=f"minimum seconds between sends (default: {DEFAULT_DELAY_SECONDS})",
    )
    parser.add_argument(
        "--batch-size",
        type=int,
        default=DEFAULT_CASES_PER_MESSAGE,
        help=(
            "maximum compatible cases per RichMessage "
            f"(default: {DEFAULT_CASES_PER_MESSAGE})"
        ),
    )
    parser.add_argument(
        "--list-cases",
        action="store_true",
        help="print selected case IDs without contacting Telegram",
    )
    parser.add_argument(
        "--case",
        dest="case_ids",
        action="append",
        default=[],
        help="run only this exact case ID; may be specified more than once",
    )
    return parser.parse_args()


def selected_cases(suite: str) -> list[ProbeCase]:
    if suite == "core":
        return core_cases()
    if suite == "limits":
        return limit_cases()
    return core_cases() + limit_cases()


def case_batch_group(case: ProbeCase) -> str:
    isolated_ids = {
        "unclosed_bold",
        "link_unclosed",
        "footnote",
        "unsupported_html",
    }
    if not case.with_label or case.expectation == "reject" or case.case_id in isolated_ids:
        return f"isolated:{case.case_id}"
    if case.expectation == "hazard":
        return f"hazard:{case.skip_entity_detection}"
    if case.expectation == "manual":
        return f"manual:{case.skip_entity_detection}"
    return f"safe:{case.skip_entity_detection}"


def make_case_batches(cases: list[ProbeCase], batch_size: int) -> list[list[ProbeCase]]:
    if batch_size < 1:
        raise ValueError("--batch-size must be at least 1")
    groups: dict[str, list[ProbeCase]] = {}
    for case in cases:
        groups.setdefault(case_batch_group(case), []).append(case)

    batches: list[list[ProbeCase]] = []
    for group_name, grouped_cases in groups.items():
        if group_name.startswith("isolated:"):
            batches.extend([[case] for case in grouped_cases])
            continue
        for offset in range(0, len(grouped_cases), batch_size):
            batches.append(grouped_cases[offset : offset + batch_size])
    return batches


def batch_source(cases: list[ProbeCase]) -> str:
    return "\n\n".join(case.source() for case in cases)


def split_batch_blocks(
    cases: list[ProbeCase], rich_message: dict[str, Any]
) -> tuple[dict[str, list[Any]], list[str]]:
    labels = {
        LABEL_TEMPLATE.format(case_id=case.case_id): case.case_id
        for case in cases
        if case.with_label
    }
    if not labels:
        case = cases[0]
        return {case.case_id: list(rich_message.get("blocks") or [])}, []

    segments = {case.case_id: [] for case in cases}
    found: set[str] = set()
    current_case_id: str | None = None
    for block in rich_message.get("blocks") or []:
        label = labels.get(flatten_text(block).strip())
        if label is not None:
            current_case_id = label
            found.add(label)
            continue
        if current_case_id is not None:
            segments[current_case_id].append(block)
    missing_labels = [case.case_id for case in cases if case.case_id not in found]
    return segments, missing_labels


def validate_cases(cases: list[ProbeCase]) -> None:
    seen: set[str] = set()
    valid_expectations = {
        "format",
        "literal",
        "hazard",
        "manual",
        "reject",
        "boundary",
    }
    for case in cases:
        if case.case_id in seen:
            raise ValueError(f"duplicate case ID: {case.case_id}")
        seen.add(case.case_id)
        if case.expectation not in valid_expectations:
            raise ValueError(f"invalid expectation for {case.case_id}: {case.expectation}")
        if not case.case_id.isascii() or len(case.case_id) > 80:
            raise ValueError(f"case ID must be short ASCII: {case.case_id}")
        if not case.source():
            raise ValueError(f"empty source for {case.case_id}")


def run() -> int:
    args = parse_args()
    cases = selected_cases(args.suite)
    if args.case_ids:
        requested = set(args.case_ids)
        available = {case.case_id for case in cases}
        unknown = sorted(requested - available)
        if unknown:
            print("error: unknown case ID(s): " + ", ".join(unknown), file=sys.stderr)
            return 2
        cases = [case for case in cases if case.case_id in requested]
    validate_cases(cases)

    if args.list_cases:
        for case in cases:
            print(case.case_id)
        print(f"total: {len(cases)}")
        return 0

    token = os.environ.get("TELEGRAM_BOT_TOKEN")
    if not token:
        print("error: TELEGRAM_BOT_TOKEN is required", file=sys.stderr)
        return 2
    if not args.chat_id:
        print("error: --chat-id or TELEGRAM_CHAT_ID is required", file=sys.stderr)
        return 2
    if args.delay < 0:
        print("error: --delay must be non-negative", file=sys.stderr)
        return 2

    try:
        chat_id = int(args.chat_id)
    except ValueError:
        chat_id = args.chat_id

    now = dt.datetime.now(dt.timezone.utc)
    run_id = now.strftime("%Y%m%dT%H%M%SZ")
    report_path = args.report or pathlib.Path(
        f"/tmp/richmessage-markdown-probe-{run_id}.json"
    )
    client = TelegramClient(token, args.delay)
    report: dict[str, Any] = {
        "schema_version": 2,
        "run_id": run_id,
        "started_at": now.isoformat(),
        "suite": args.suite,
        "chat_id": str(chat_id),
        "case_count": len(cases),
        "configuration": {
            "delay_seconds": args.delay,
            "batch_size": args.batch_size,
            "keep_all": args.keep_all,
            "summary_enabled": not args.no_summary,
        },
        "preflight": {},
        "batches": [],
        "results": [],
        "stopped_reason": None,
    }

    try:
        bot = client.call("getMe", {})
        chat = client.call("getChat", {"chat_id": chat_id})
        membership = client.call(
            "getChatMember", {"chat_id": chat_id, "user_id": bot["id"]}
        )
    except TelegramAPIError as error:
        report["preflight"] = {"ok": False, "error": error.as_report()}
        report["finished_at"] = dt.datetime.now(dt.timezone.utc).isoformat()
        write_report(report_path, report)
        print(f"preflight failed: {error}", file=sys.stderr)
        print(f"report: {report_path}")
        return 2

    status = membership.get("status")
    restricted_without_send = status == "restricted" and not membership.get(
        "can_send_messages", False
    )
    if status in {"left", "kicked"} or restricted_without_send:
        report["preflight"] = {
            "ok": False,
            "error": {"description": f"bot chat membership cannot send: {status}"},
        }
        report["finished_at"] = dt.datetime.now(dt.timezone.utc).isoformat()
        write_report(report_path, report)
        print(f"preflight failed: bot cannot send to chat ({status})", file=sys.stderr)
        print(f"report: {report_path}")
        return 2

    report["preflight"] = {
        "ok": True,
        "bot": {"id": bot.get("id"), "username": bot.get("username")},
        "chat": {
            "id": chat.get("id"),
            "type": chat.get("type"),
            "title": chat.get("title"),
            "is_forum": chat.get("is_forum", False),
        },
        "membership_status": status,
    }

    batches = make_case_batches(cases, args.batch_size)
    report["planned_batch_count"] = len(batches)
    retained_signatures: set[tuple[Any, ...]] = set()
    cleanup_failed = False
    pending_deletions: list[tuple[int, list[dict[str, Any]]]] = []
    processed_count = 0

    def flush_deletions() -> bool:
        nonlocal cleanup_failed
        if not pending_deletions:
            return True

        queued = list(pending_deletions)
        pending_deletions.clear()
        message_ids = [message_id for message_id, _ in queued]
        try:
            client.call(
                "deleteMessages",
                {"chat_id": chat_id, "message_ids": message_ids},
            )
            for _, queued_results in queued:
                for queued_result in queued_results:
                    queued_result["deleted"] = True
            return True
        except TelegramAPIError as bulk_error:
            # A local Bot API server may lag behind the cloud API. Fall back to
            # the single-message method so already-sent passing probes do not
            # remain in the group merely because bulk deletion is unavailable.
            for message_id, queued_results in queued:
                try:
                    client.call(
                        "deleteMessage",
                        {"chat_id": chat_id, "message_id": message_id},
                    )
                    for queued_result in queued_results:
                        queued_result["deleted"] = True
                except TelegramAPIError as error:
                    for queued_result in queued_results:
                        queued_result["classification"] = "FAIL"
                        queued_result["cleanup_error"] = error.as_report()
                        queued_result["bulk_cleanup_error"] = bulk_error.as_report()
                    report["stopped_reason"] = (
                        f"cleanup failed for batch {queued_results[0]['batch_id']}; "
                        "stopped to avoid group noise"
                    )
                    cleanup_failed = True
                    return False
            return True

    def new_result(
        case: ProbeCase,
        batch_id: str,
        message_id: int | None,
        latency_ms: float,
    ) -> dict[str, Any]:
        return {
            "case_id": case.case_id,
            "batch_id": batch_id,
            "family": case.family,
            "intent": case.intent,
            "spec_expectation": case.spec_expectation,
            "expectation": case.expectation,
            "skip_entity_detection": case.skip_entity_detection,
            "markdown": case.source(),
            "source_characters": len(case.source()),
            "source_utf8_bytes": len(case.source().encode("utf-8")),
            "required_types": list(case.required_types),
            "forbidden_types": list(case.forbidden_types),
            "message_id": message_id,
            "classification": None,
            "observed_types": [],
            "missing_types": [],
            "forbidden_types_seen": [],
            "structural_errors": [],
            "rich_message": None,
            "error": None,
            "deleted": False,
            "retained": False,
            "latency_ms": latency_ms,
        }

    def process_batch(batch_cases: list[ProbeCase], batch_id: str) -> None:
        nonlocal cleanup_failed, processed_count
        if cleanup_failed:
            return
        source = batch_source(batch_cases)
        skip_values = {case.skip_entity_detection for case in batch_cases}
        if len(skip_values) != 1:
            raise ValueError(f"batch {batch_id} mixes skip_entity_detection values")
        skip_entity_detection = next(iter(skip_values))
        started = time.monotonic()
        batch_record: dict[str, Any] = {
            "batch_id": batch_id,
            "case_ids": [case.case_id for case in batch_cases],
            "case_count": len(batch_cases),
            "source_length": len(source),
            "skip_entity_detection": skip_entity_detection,
            "message_id": None,
            "error": None,
            "split_after_error": False,
        }
        try:
            message = client.call(
                "sendRichMessage",
                {
                    "chat_id": chat_id,
                    "rich_message": {
                        "markdown": source,
                        "skip_entity_detection": skip_entity_detection,
                    },
                    "disable_notification": True,
                },
            )
        except TelegramAPIError as error:
            batch_record["error"] = error.as_report()
            if len(batch_cases) > 1:
                batch_record["split_after_error"] = True
                report["batches"].append(batch_record)
                write_report(report_path, report)
                midpoint = len(batch_cases) // 2
                process_batch(batch_cases[:midpoint], batch_id + "a")
                process_batch(batch_cases[midpoint:], batch_id + "b")
                return

            case = batch_cases[0]
            latency_ms = round((time.monotonic() - started) * 1000, 1)
            result = new_result(case, batch_id, None, latency_ms)
            result["classification"] = evaluate_error(case, error)
            result["error"] = error.as_report()
            report["batches"].append(batch_record)
            report["results"].append(result)
            processed_count += 1
            print(
                f"[{processed_count:03d}/{len(cases):03d}] "
                f"{case.case_id}: {result['classification']}"
            )
            write_report(report_path, report)
            return

        message_id = message.get("message_id")
        batch_record["message_id"] = message_id
        rich_message = message.get("rich_message") or {}
        segments, missing_labels = split_batch_blocks(batch_cases, rich_message)
        latency_ms = round((time.monotonic() - started) * 1000, 1)
        batch_results: list[dict[str, Any]] = []
        for case in batch_cases:
            result = new_result(case, batch_id, message_id, latency_ms)
            segment = segments.get(case.case_id, [])
            segment_message = {"blocks": segment}
            if rich_message.get("is_rtl"):
                segment_message["is_rtl"] = True
            result["rich_message"] = canonicalize(segment_message)
            evaluation_case = dataclasses.replace(case, with_label=False)
            classification, missing, forbidden, structural_errors = evaluate_success(
                evaluation_case, segment_message
            )
            if case.case_id in missing_labels:
                classification = "FAIL"
                structural_errors.append("case label was not found in returned blocks")
            result["classification"] = classification
            result["observed_types"] = sorted(set(collect_types(segment)))
            result["missing_types"] = missing
            result["forbidden_types_seen"] = forbidden
            result["structural_errors"] = structural_errors
            batch_results.append(result)

        report["batches"].append(batch_record)
        report["results"].extend(batch_results)
        notable_results = [
            result
            for result in batch_results
            if result["classification"] in {"HAZARD", "MANUAL", "FAIL"}
        ]
        should_retain = args.keep_all
        if not should_retain and notable_results:
            signatures = {retention_signature(result) for result in notable_results}
            new_signatures = signatures - retained_signatures
            if new_signatures:
                should_retain = True
                retained_signatures.update(signatures)

        if any(case.family.startswith("limit-") for case in batch_cases):
            should_retain = False

        if should_retain:
            for result in batch_results:
                result["retained"] = True
        else:
            pending_deletions.append((message_id, batch_results))

        for result in batch_results:
            processed_count += 1
            print(
                f"[{processed_count:03d}/{len(cases):03d}] "
                f"{result['case_id']}: {result['classification']}"
            )

        flush_now = len(pending_deletions) >= DELETE_BATCH_SIZE
        if any(case.family.startswith("limit-") for case in batch_cases):
            flush_now = True
        if flush_now:
            flush_deletions()
        write_report(report_path, report)

    for batch_index, batch_cases in enumerate(batches, start=1):
        process_batch(batch_cases, f"batch-{batch_index:03d}")
        if cleanup_failed:
            break

    if not cleanup_failed:
        flush_deletions()
    report["finished_at"] = dt.datetime.now(dt.timezone.utc).isoformat()
    report["completed"] = len(report["results"]) == len(cases) and not cleanup_failed

    if not args.no_summary and not cleanup_failed:
        try:
            summary_message = client.call(
                "sendMessage",
                {
                    "chat_id": chat_id,
                    "text": make_summary(report),
                    "disable_notification": True,
                },
            )
            report["summary_message_id"] = summary_message.get("message_id")
        except TelegramAPIError as error:
            report["summary_error"] = error.as_report()

    write_report(report_path, report)
    counts = Counter(result["classification"] for result in report["results"])
    print("counts: " + ", ".join(f"{key}={value}" for key, value in sorted(counts.items())))
    print(f"report: {report_path}")

    if cleanup_failed or counts["FAIL"] or report.get("summary_error"):
        return 1
    return 0


def main() -> None:
    try:
        raise SystemExit(run())
    except KeyboardInterrupt:
        print("interrupted", file=sys.stderr)
        raise SystemExit(130) from None
    except (OSError, ValueError) as error:
        print(f"error: {error}", file=sys.stderr)
        raise SystemExit(2) from None


if __name__ == "__main__":
    main()
