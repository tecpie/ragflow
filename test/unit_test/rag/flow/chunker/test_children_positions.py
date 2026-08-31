#
#  Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
#
import unittest

from rag.flow.chunker.children_positions import (
    _child_line_indices,
    _non_empty_lines,
    split_chunk_by_children,
)


class TestChildrenPositions(unittest.TestCase):
    def test_line_aligned_positions(self):
        parent = {
            "text": "第一行\n第二行\n第三行",
            "_pdf_positions": [[1, 10, 100, 20, 30], [1, 10, 100, 40, 50], [1, 10, 100, 60, 70]],
        }
        parts = ["第一行", "第二行", "第三行"]
        children = split_chunk_by_children(parent, parts, lambda t: t)
        self.assertEqual(3, len(children))
        self.assertEqual([[1, 10, 100, 20, 30]], children[0]["_pdf_positions"])
        self.assertEqual([[1, 10, 100, 40, 50]], children[1]["_pdf_positions"])

    def test_child_line_indices(self):
        parent_text = "a\nb\nc"
        parts = ["a", "b", "c"]
        positions = [[1, 0, 1, 0, 1], [1, 0, 1, 1, 2], [1, 0, 1, 2, 3]]
        idx = _child_line_indices(parent_text, parts, positions)
        self.assertEqual([0, 1, 2], idx)

    def test_non_empty_lines(self):
        self.assertEqual(["a", "b"], _non_empty_lines("a\n\n b \r\nc")[:2])


if __name__ == "__main__":
    unittest.main()
