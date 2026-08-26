#!/usr/bin/env python3

import unittest

from architecture_check import DOMAIN_PACKAGE, architecture_layer_inversions


class ArchitectureCheckTest(unittest.TestCase):
    def test_allows_domain_primitives(self) -> None:
        imports = [
            "encoding/json",
            "github.com/chiga0/marshal-harness/internal/canonical",
            "github.com/chiga0/marshal-harness/internal/evidencebinding",
        ]
        self.assertEqual(architecture_layer_inversions(DOMAIN_PACKAGE, imports), [])

    def test_rejects_profile_and_runtime_implementations(self) -> None:
        imports = [
            "github.com/chiga0/marshal-harness/internal/selfidentity",
            "github.com/chiga0/marshal-harness/internal/execution",
            "github.com/chiga0/marshal-harness/internal/adapter/qoder",
        ]
        self.assertEqual(
            architecture_layer_inversions(DOMAIN_PACKAGE, imports),
            sorted(imports),
        )

    def test_rejects_unknown_package_policy(self) -> None:
        with self.assertRaises(ValueError):
            architecture_layer_inversions(
                "github.com/chiga0/marshal-harness/internal/other", []
            )


if __name__ == "__main__":
    unittest.main()
