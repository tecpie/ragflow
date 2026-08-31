#
#  Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
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
import copy


def _non_empty_lines(text):
    normalized = (text or "").replace("\r\n", "\n").replace("\r", "\n")
    lines = []
    for line in normalized.split("\n"):
        trimmed = line.strip()
        if trimmed:
            lines.append(trimmed)
    return lines


def _position_matrix(positions):
    if not positions:
        return []
    if isinstance(positions, list):
        return positions
    return []


def _position_matrix_row(positions, index):
    matrix = _position_matrix(positions)
    if index < 0 or index >= len(matrix):
        return None
    return [matrix[index]]


def _slice_positions_by_text_ratio(positions, start_ratio, end_ratio):
    matrix = _position_matrix(positions)
    if not matrix:
        return None
    if start_ratio < 0:
        start_ratio = 0
    if end_ratio > 1:
        end_ratio = 1
    if start_ratio >= end_ratio:
        return None

    heights = []
    total = 0.0
    for row in matrix:
        if not row or len(row) < 5:
            return None
        top, bottom = float(row[3]), float(row[4])
        if bottom <= top:
            return None
        h = bottom - top
        heights.append(h)
        total += h
    if total <= 0:
        return None

    target_start = start_ratio * total
    target_end = end_ratio * total
    out = []
    cum = 0.0
    for row, h in zip(matrix, heights):
        seg_start = cum
        seg_end = cum + h
        cum += h
        inter_start = max(seg_start, target_start)
        inter_end = min(seg_end, target_end)
        if inter_start >= inter_end:
            continue
        local_start = (inter_start - seg_start) / h
        local_end = (inter_end - seg_start) / h
        old_top, old_bottom = float(row[3]), float(row[4])
        old_h = old_bottom - old_top
        new_row = list(row)
        new_row[3] = old_top + local_start * old_h
        new_row[4] = old_top + local_end * old_h
        out.append(new_row)
    return out or None


def _child_line_indices(parent_text, parts, positions):
    lines = _non_empty_lines(parent_text)
    row_count = len(_position_matrix(positions))
    if row_count <= 1 or len(lines) != row_count or len(lines) != len(parts):
        return None
    indices = []
    used = [-1] * len(lines)
    for part in parts:
        want = part.strip()
        idx = -1
        for i, line in enumerate(lines):
            if used[i] >= 0:
                continue
            if line.strip() == want:
                idx = i
                break
        if idx < 0:
            dup_skip = 0
            for i, line in enumerate(lines):
                if line.strip() != want:
                    continue
                count = sum(1 for j in range(i) if lines[j].strip() == want)
                if count == dup_skip:
                    idx = i
                    break
                dup_skip += 1
        if idx < 0:
            return None
        used[idx] = len(indices)
        indices.append(idx)
    return indices


def assign_child_positions(child, parent, part_text, parts, part_index, remove_tag_fn):
    pos_key = "_pdf_positions"
    alt_key = "positions"
    parent_positions = parent.get(pos_key) or parent.get(alt_key)
    if not parent_positions:
        return

    line_idx = _child_line_indices(parent.get("text", ""), parts, parent_positions)
    if line_idx is not None and line_idx[part_index] >= 0:
        row = _position_matrix_row(parent_positions, line_idx[part_index])
        if row:
            child[pos_key] = copy.deepcopy(row)
            child[alt_key] = copy.deepcopy(row)
        return

    visible = remove_tag_fn(parent.get("text", ""))
    run_count = len(visible) or 1
    cum = 0
    for i, p in enumerate(parts):
        if i == part_index:
            part_visible = remove_tag_fn(part_text)
            part_len = len(part_visible)
            start_ratio = cum / run_count
            end_ratio = (cum + part_len) / run_count
            sliced = _slice_positions_by_text_ratio(parent_positions, start_ratio, end_ratio)
            if sliced:
                child[pos_key] = copy.deepcopy(sliced)
                child[alt_key] = copy.deepcopy(sliced)
            else:
                child[pos_key] = copy.deepcopy(parent.get(pos_key))
                child[alt_key] = copy.deepcopy(parent.get(alt_key))
            return
        cum += len(remove_tag_fn(p))


def split_chunk_by_children(parent, parts, remove_tag_fn):
    if not parts:
        return []
    mom = (parent.get("text") or "").removeprefix("\n")
    if len(parts) == 1:
        child = copy.deepcopy(parent)
        child["text"] = parts[0]
        child["mom"] = mom
        assign_child_positions(child, parent, parts[0], parts, 0, remove_tag_fn)
        return [child]

    out = []
    for i, text in enumerate(parts):
        child = copy.deepcopy(parent)
        child["text"] = text
        child["mom"] = mom
        assign_child_positions(child, parent, text, parts, i, remove_tag_fn)
        out.append(child)
    return out
