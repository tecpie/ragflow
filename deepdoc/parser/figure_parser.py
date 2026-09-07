#
#  Copyright 2025 The InfiniFlow Authors. All Rights Reserved.
#
#  Licensed under the Apache License, Version 2.0 (the "License");
#  you may not use this file except in compliance with the License.
#  You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
#  Unless required by applicable law or agreed to in writing, software
#  distributed under the License is distributed on an "AS IS" BASIS,
#  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#  See the License for the specific language governing permissions and
#  limitations under the License.
#
import logging
import re
from concurrent.futures import FIRST_COMPLETED, ThreadPoolExecutor, wait

from PIL import Image

from api.db.joint_services.tenant_model_service import get_tenant_default_model_by_type
from api.db.services.llm_service import LLMBundle
from common.connection_utils import timeout
from common.constants import LLMType
from common.exceptions import TaskCanceledException
from rag.app.picture import vision_llm_chunk as picture_vision_llm_chunk
from rag.nlp import append_context2table_image4pdf
from rag.prompts.generator import (
    vision_llm_figure_describe_prompt,
    vision_llm_figure_describe_prompt_with_context,
)
from rag.prompts.vision_media_prompts import (
    vision_llm_signature_describe_prompt,
    vision_llm_table_describe_prompt,
)
from rag.utils.lazy_image import ensure_pil_image, is_image_like, open_image_for_processing

# Labels that usually sit to the left of handwritten signatures / seals.
_SIGNATURE_LABEL_RE = re.compile(r"(批准|审核|校核|编制|签字|签名|签章|盖章)")
_CJK_RE = re.compile(r"[\u4e00-\u9fff]")


def vision_figure_parser_figure_data_wrapper(figures_data_without_positions):
    if not figures_data_without_positions:
        return []
    res = []
    for figure_data in figures_data_without_positions:
        img = ensure_pil_image(figure_data[1])
        if not isinstance(img, Image.Image):
            continue
        res.append(
            (
                (img, [figure_data[0]]),
                [(0, 0, 0, 0, 0)],
            )
        )
    return res


def _normalize_vision_language(lang):
    return lang or "English"


def _is_figure_item(item):
    try:
        return is_image_like(item[0][0]) and isinstance(item[0][1], list)
    except (TypeError, IndexError, KeyError):
        return False


def _is_table_item(item):
    """DeepDOC tables are (img, html_str) paired with positions."""
    try:
        return is_image_like(item[0][0]) and isinstance(item[0][1], str)
    except (TypeError, IndexError, KeyError):
        return False


def _table_vision_enhance_enabled(parser_config):
    """Default on; set parser_config.table_vision_enhance=false to disable."""
    if not isinstance(parser_config, dict):
        return True
    raw = parser_config.get("table_vision_enhance", True)
    if isinstance(raw, bool):
        return raw
    if isinstance(raw, (int, float)):
        return bool(raw)
    return str(raw).strip().lower() not in {"0", "false", "no", "off"}


def _signature_vision_enhance_enabled(parser_config):
    """Default on; set parser_config.signature_vision_enhance=false to disable."""
    if not isinstance(parser_config, dict):
        return True
    raw = parser_config.get("signature_vision_enhance", True)
    if isinstance(raw, bool):
        return raw
    if isinstance(raw, (int, float)):
        return bool(raw)
    return str(raw).strip().lower() not in {"0", "false", "no", "off"}


def _already_has_signature_name(text: str) -> bool:
    """Skip Vision when OCR already captured a plausible name after the label."""
    cleaned = _SIGNATURE_LABEL_RE.sub("", text or "")
    cleaned = re.sub(r"[:：\s\-_/|]+", "", cleaned)
    return len(_CJK_RE.findall(cleaned)) >= 2


def _section_text_and_tag(section):
    if isinstance(section, (list, tuple)) and len(section) >= 2:
        return str(section[0] or ""), str(section[1] or "")
    if isinstance(section, dict):
        return str(section.get("text") or ""), str(section.get("position_tag") or section.get("tag") or "")
    return str(section or ""), ""


def _crop_signature_band(pdf_parser, position_tag: str, zoomin: int = 3):
    """Crop label row extended to the right so handwritten signatures are included."""
    from deepdoc.parser.pdf_parser import RAGFlowPdfParser

    page_images = getattr(pdf_parser, "page_images", None) or []
    if not page_images:
        return None

    poss = RAGFlowPdfParser.extract_positions(position_tag)
    if not poss:
        return None

    pns, left, right, top, bottom = poss[0]
    if not pns:
        return None
    page_idx = pns[0]
    if not (0 <= page_idx < len(page_images)):
        return None

    page = page_images[page_idx]
    page_w = page.size[0] / zoomin
    page_h = page.size[1] / zoomin
    height = max(bottom - top, 8.0)
    # Extend right across the signature area; keep a bit of vertical padding.
    crop_left = max(0.0, left)
    crop_right = min(page_w, max(right + max(120.0, height * 8.0), page_w * 0.72))
    crop_top = max(0.0, top - height * 0.35)
    crop_bottom = min(page_h, bottom + height * 0.55)
    if crop_right - crop_left < 8 or crop_bottom - crop_top < 8:
        return None

    box = (crop_left * zoomin, crop_top * zoomin, crop_right * zoomin, crop_bottom * zoomin)
    try:
        return page.crop(box)
    except Exception:
        logging.exception("Failed to crop signature band page=%s box=%s", page_idx, box)
        return None


def enhance_signature_sections_with_vision(pdf_parser, sections, callback=None, lang="English", **kwargs):
    """Find signature-label lines, crop rightward bands, and append Vision transcriptions."""
    lang = _normalize_vision_language(lang)
    callback = callback or (lambda prog, msg: None)
    parser_config = kwargs.get("parser_config", {})
    if not sections or not _signature_vision_enhance_enabled(parser_config):
        return sections
    if not getattr(pdf_parser, "page_images", None):
        return sections

    tenant_id = kwargs.get("tenant_id")
    if not tenant_id:
        return sections

    try:
        vision_model_config = get_tenant_default_model_by_type(tenant_id, LLMType.VISION)
        vision_model = LLMBundle(tenant_id, vision_model_config, lang=lang)
    except Exception:
        return sections

    candidates = []
    for idx, section in enumerate(sections):
        text, tag = _section_text_and_tag(section)
        if not tag or not _SIGNATURE_LABEL_RE.search(text):
            continue
        if _already_has_signature_name(text):
            continue
        candidates.append(idx)

    if not candidates:
        return sections

    zoomin = int(kwargs.get("zoomin", 3) or 3)
    callback(0.74, f"Enhancing {len(candidates)} signature field(s) with vision model...")

    @timeout(30, 3)
    def process(section_idx):
        text, tag = _section_text_and_tag(sections[section_idx])
        img = _crop_signature_band(pdf_parser, tag, zoomin=zoomin)
        if img is None:
            return section_idx, ""
        prompt = vision_llm_signature_describe_prompt(language=lang)
        desc = picture_vision_llm_chunk(
            binary=img,
            vision_model=vision_model,
            prompt=prompt,
            callback=callback,
        )
        return section_idx, (desc or "").strip()

    executor = ThreadPoolExecutor(max_workers=10)
    pending = {executor.submit(process, idx) for idx in candidates}
    results = {}
    try:
        while pending:
            done, pending = wait(pending, timeout=1.0, return_when=FIRST_COMPLETED)
            for future in done:
                section_idx, desc = future.result()
                results[section_idx] = desc
    except TaskCanceledException:
        for f in pending:
            f.cancel()
        executor.shutdown(wait=False, cancel_futures=True)
        raise
    except Exception:
        for f in pending:
            f.cancel()
        executor.shutdown(wait=False, cancel_futures=True)
        raise
    else:
        executor.shutdown(wait=True)

    out = list(sections)
    for idx, desc in results.items():
        if not desc:
            continue
        text, tag = _section_text_and_tag(out[idx])
        # Candidates are label-only OCR lines; Vision already emits "批准: 姓名".
        # Replace instead of appending to avoid "批准:\n批准: 李明".
        merged = desc if not _already_has_signature_name(text) else f"{text}\n{desc}"
        if isinstance(out[idx], (list, tuple)) and len(out[idx]) >= 2:
            out[idx] = (merged, out[idx][1])
        elif isinstance(out[idx], dict):
            item = dict(out[idx])
            item["text"] = merged
            out[idx] = item
        else:
            out[idx] = (merged, tag)
    return out


def _enhance_pdf_tables_with_vision(tbls, vision_model, lang, callback=None):
    """Append Vision descriptions to DeepDOC table crops (seals/signatures in cells)."""
    callback = callback or (lambda prog, msg: None)
    table_idxs = [i for i, item in enumerate(tbls) if _is_table_item(item)]
    if not table_idxs:
        return tbls

    callback(0.72, f"Enhancing {len(table_idxs)} table image(s) with vision model...")

    @timeout(30, 3)
    def process(table_idx):
        item = tbls[table_idx]
        img = ensure_pil_image(item[0][0])
        if img is None:
            return table_idx, ""
        prompt = vision_llm_table_describe_prompt(language=lang)
        desc = picture_vision_llm_chunk(
            binary=img,
            vision_model=vision_model,
            prompt=prompt,
            callback=callback,
        )
        return table_idx, (desc or "").strip()

    executor = ThreadPoolExecutor(max_workers=10)
    pending = {executor.submit(process, idx) for idx in table_idxs}
    results = {}
    try:
        while pending:
            done, pending = wait(pending, timeout=1.0, return_when=FIRST_COMPLETED)
            for future in done:
                table_idx, desc = future.result()
                results[table_idx] = desc
    except TaskCanceledException:
        for f in pending:
            f.cancel()
        executor.shutdown(wait=False, cancel_futures=True)
        raise
    except Exception:
        for f in pending:
            f.cancel()
        executor.shutdown(wait=False, cancel_futures=True)
        raise
    else:
        executor.shutdown(wait=True)

    enhanced = []
    for i, item in enumerate(tbls):
        if i not in results or not results[i]:
            enhanced.append(item)
            continue
        html = item[0][1]
        vision_txt = results[i]
        merged = f"{html}\n{vision_txt}" if html else vision_txt
        if len(item) >= 2:
            enhanced.append(((item[0][0], merged), item[1]))
        else:
            enhanced.append(((item[0][0], merged), []))
    return enhanced


def vision_figure_parser_docx_wrapper(sections, tbls, callback=None, lang="English", **kwargs):
    lang = _normalize_vision_language(lang)
    if not sections:
        return tbls
    try:
        vision_model_config = get_tenant_default_model_by_type(kwargs["tenant_id"], LLMType.VISION)
        vision_model = LLMBundle(kwargs["tenant_id"], vision_model_config, lang=lang)
        callback(0.7, "Visual model detected. Attempting to enhance figure extraction...")
    except Exception:
        vision_model = None
    if vision_model:
        figures_data = vision_figure_parser_figure_data_wrapper(sections)
        try:
            docx_vision_parser = VisionFigureParser(
                vision_model=vision_model,
                figures_data=figures_data,
                lang=lang,
                **kwargs,
            )
            boosted_figures = docx_vision_parser(callback=callback)
            tbls.extend(boosted_figures)
        except TaskCanceledException:
            raise
        except Exception as e:
            callback(0.8, f"Visual model error: {e}. Skipping figure parsing enhancement.")
    return tbls


def vision_figure_parser_figure_xlsx_wrapper(images, callback=None, lang="English", **kwargs):
    lang = _normalize_vision_language(lang)
    tbls = []
    if not images:
        return []
    try:
        vision_model_config = get_tenant_default_model_by_type(kwargs["tenant_id"], LLMType.VISION)
        vision_model = LLMBundle(kwargs["tenant_id"], vision_model_config, lang=lang)
        callback(0.2, "Visual model detected. Attempting to enhance Excel image extraction...")
    except Exception:
        vision_model = None
    if vision_model:
        figures_data = [
            (
                (
                    img["image"],  # Image.Image or LazyImage (converted by ensure_pil_image)
                    [img["image_description"]],  # description list (must be list)
                ),
                [
                    (0, 0, 0, 0, 0)  # dummy position
                ],
            )
            for img in images
        ]
        try:
            parser = VisionFigureParser(
                vision_model=vision_model,
                figures_data=figures_data,
                lang=lang,
                **kwargs,
            )
            callback(0.22, "Parsing images...")
            boosted_figures = parser(callback=callback)
            tbls.extend(boosted_figures)
        except TaskCanceledException:
            raise
        except Exception as e:
            callback(0.25, f"Excel visual model error: {e}. Skipping vision enhancement.")
    return tbls


def vision_figure_parser_pdf_wrapper(tbls, callback=None, lang="English", **kwargs):
    lang = _normalize_vision_language(lang)
    if not tbls:
        return []
    sections = kwargs.get("sections")
    parser_config = kwargs.get("parser_config", {})
    context_size = max(0, int(parser_config.get("image_context_size", 0) or 0))
    try:
        vision_model_config = get_tenant_default_model_by_type(kwargs["tenant_id"], LLMType.VISION)
        vision_model = LLMBundle(kwargs["tenant_id"], vision_model_config, lang=lang)
        callback(0.7, "Visual model detected. Attempting to enhance figure extraction...")
    except Exception:
        vision_model = None
    if vision_model:
        figures_data = [item for item in tbls if _is_figure_item(item)]
        figure_contexts = []
        if sections and figures_data and context_size > 0:
            figure_contexts = append_context2table_image4pdf(
                sections,
                figures_data,
                context_size,
                return_context=True,
            )
        try:
            docx_vision_parser = VisionFigureParser(
                vision_model=vision_model,
                figures_data=figures_data,
                figure_contexts=figure_contexts,
                context_size=context_size,
                lang=lang,
                **kwargs,
            )
            boosted_figures = docx_vision_parser(callback=callback)
            tbls = [item for item in tbls if not _is_figure_item(item)]
            tbls.extend(boosted_figures)
        except TaskCanceledException:
            raise
        except Exception as e:
            callback(0.8, f"Visual model error: {e}. Skipping figure parsing enhancement.")

        if _table_vision_enhance_enabled(parser_config):
            try:
                tbls = _enhance_pdf_tables_with_vision(tbls, vision_model, lang, callback=callback)
            except TaskCanceledException:
                raise
            except Exception as e:
                callback(0.8, f"Table visual model error: {e}. Skipping table vision enhancement.")
    return tbls


def vision_figure_parser_docx_wrapper_naive(chunks, idx_lst, callback=None, lang="English", **kwargs):
    lang = _normalize_vision_language(lang)
    if not chunks:
        return []
    try:
        vision_model_config = get_tenant_default_model_by_type(kwargs["tenant_id"], LLMType.VISION)
        vision_model = LLMBundle(kwargs["tenant_id"], vision_model_config, lang=lang)
        callback(0.7, "Visual model detected. Attempting to enhance figure extraction...")
    except Exception:
        vision_model = None
    if vision_model:

        @timeout(30, 3)
        def worker(idx, ck):
            img, close_after = open_image_for_processing(ck.get("image"), allow_bytes=True)
            if not isinstance(img, Image.Image):
                return idx, ""
            context_above = ck.get("context_above", "")
            context_below = ck.get("context_below", "")
            if context_above or context_below:
                prompt = vision_llm_figure_describe_prompt_with_context(
                    # context_above + caption if any
                    context_above=ck.get("context_above") + ck.get("text", ""),
                    context_below=ck.get("context_below"),
                    language=lang,
                )
                logging.info(f"[VisionFigureParser] figure={idx} context_above_len={len(context_above)} context_below_len={len(context_below)} prompt=with_context")
            else:
                prompt = vision_llm_figure_describe_prompt(language=lang)
                logging.info(f"[VisionFigureParser] figure={idx} context_len=0 prompt=default")

            try:
                description_text = picture_vision_llm_chunk(
                    binary=img,
                    vision_model=vision_model,
                    prompt=prompt,
                    callback=callback,
                )
                return idx, description_text
            finally:
                if close_after and isinstance(img, Image.Image):
                    try:
                        img.close()
                    except Exception:
                        pass

        executor = ThreadPoolExecutor(max_workers=10)
        pending = {executor.submit(worker, idx, chunks[idx]) for idx in idx_lst}
        try:
            while pending:
                done, pending = wait(pending, timeout=1.0, return_when=FIRST_COMPLETED)
                for future in done:
                    idx, description = future.result()
                    chunks[idx]["text"] += description
                if callback:
                    callback(0.75, "")
        except Exception:
            for f in pending:
                f.cancel()
            executor.shutdown(wait=False, cancel_futures=True)
            raise
        else:
            executor.shutdown(wait=True)


shared_executor = ThreadPoolExecutor(max_workers=10)


class VisionFigureParser:
    def __init__(self, vision_model, figures_data, *args, **kwargs):
        self.vision_model = vision_model
        self.language = kwargs.get("lang") or "English"
        self.figure_contexts = kwargs.get("figure_contexts") or []
        self.context_size = max(0, int(kwargs.get("context_size", 0) or 0))
        self._extract_figures_info(figures_data)
        assert len(self.figures) == len(self.descriptions)
        assert not self.positions or (len(self.figures) == len(self.positions))

    def _extract_figures_info(self, figures_data):
        self.figures = []
        self.descriptions = []
        self.positions = []

        for item in figures_data:
            # position
            if len(item) == 2 and isinstance(item[0], tuple) and len(item[0]) == 2 and isinstance(item[1], list) and isinstance(item[1][0], tuple) and len(item[1][0]) == 5:
                img_desc = item[0]
                img = ensure_pil_image(img_desc[0])
                if img is None:
                    continue
                assert len(img_desc) == 2 and isinstance(img_desc[1], list), "Should be (figure, [description])"
                self.figures.append(img)
                self.descriptions.append(img_desc[1])
                self.positions.append(item[1])
            else:
                img = ensure_pil_image(item[0])
                if img is None:
                    continue
                assert len(item) == 2 and isinstance(item[1], list), f"Unexpected form of figure data: get {len(item)=}, {item=}"
                self.figures.append(img)
                self.descriptions.append(item[1])

    def _assemble(self):
        self.assembled = []
        self.has_positions = len(self.positions) != 0
        for i in range(len(self.figures)):
            figure = self.figures[i]
            desc = self.descriptions[i]
            pos = self.positions[i] if self.has_positions else None

            figure_desc = (figure, desc)

            if pos is not None:
                self.assembled.append((figure_desc, pos))
            else:
                self.assembled.append((figure_desc,))

        return self.assembled

    def __call__(self, **kwargs):
        callback = kwargs.get("callback") or (lambda prog, msg: None)

        @timeout(30, 3)
        def process(figure_idx, figure_binary):
            context_above = ""
            context_below = ""
            if figure_idx < len(self.figure_contexts):
                context_above, context_below = self.figure_contexts[figure_idx]
            if context_above or context_below:
                prompt = vision_llm_figure_describe_prompt_with_context(
                    context_above=context_above,
                    context_below=context_below,
                    language=self.language,
                )
                logging.info(
                    f"[VisionFigureParser] figure={figure_idx} context_size={self.context_size} context_above_len={len(context_above)} context_below_len={len(context_below)} prompt=with_context"
                )
            else:
                prompt = vision_llm_figure_describe_prompt(language=self.language)
                logging.info(f"[VisionFigureParser] figure={figure_idx} context_size={self.context_size} context_len=0 prompt=default")
            description_text = picture_vision_llm_chunk(
                binary=figure_binary,
                vision_model=self.vision_model,
                prompt=prompt,
                callback=callback,
            )
            return figure_idx, description_text

        pending = {shared_executor.submit(process, idx, img_binary) for idx, img_binary in enumerate(self.figures or [])}
        try:
            while pending:
                done, pending = wait(pending, timeout=1.0, return_when=FIRST_COMPLETED)
                for future in done:
                    figure_num, txt = future.result()
                    if txt:
                        self.descriptions[figure_num] = txt + "\n".join(self.descriptions[figure_num])
                callback(0.75, "")
        except Exception:
            for f in pending:
                f.cancel()
            raise

        self._assemble()

        return self.assembled
