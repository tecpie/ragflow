import unittest

from rag.nlp import split_with_pattern


class TestSplitWithPatternChildPositions(unittest.TestCase):
    def test_line_aligned_child_positions(self):
        parent = {
            "docnm_kwd": "doc.pdf",
            "position_int": [
                (1, 10, 100, 20, 30),
                (1, 10, 100, 40, 50),
                (1, 10, 100, 60, 70),
            ],
            "page_num_int": [1, 1, 1],
            "top_int": [20, 40, 60],
        }
        content = "第一行\n第二行\n第三行"
        children = split_with_pattern(parent, r"\n", content, eng=False, language="Chinese")
        self.assertEqual(3, len(children))
        self.assertEqual([(1, 10, 100, 20, 30)], children[0]["position_int"])
        self.assertEqual([(1, 10, 100, 40, 50)], children[1]["position_int"])
        self.assertEqual([(1, 10, 100, 60, 70)], children[2]["position_int"])
        self.assertEqual([20], children[0]["top_int"])
        self.assertEqual([40], children[1]["top_int"])

    def test_ratio_fallback_when_lines_mismatch(self):
        parent = {
            "docnm_kwd": "doc.pdf",
            "position_int": [(1, 0, 100, 0, 100)],
            "page_num_int": [1],
            "top_int": [0],
        }
        content = "aaaa\nbbbb"
        children = split_with_pattern(parent, r"\n", content, eng=True, language="English")
        self.assertEqual(2, len(children))
        self.assertEqual(1, len(children[0]["position_int"]))
        self.assertEqual(1, len(children[1]["position_int"]))
        self.assertNotEqual(children[0]["position_int"], children[1]["position_int"])
        self.assertLess(children[0]["position_int"][0][4], children[1]["position_int"][0][4])


if __name__ == "__main__":
    unittest.main()
