#!/usr/bin/env python3

import unittest

from architecture_check import DOMAIN_PACKAGE, MODULE, architecture_layer_inversions


def package(imports: list[str], dependencies: list[str] | None = None) -> dict[str, object]:
    return {
        "ImportPath": DOMAIN_PACKAGE,
        "Module": {"Path": MODULE},
        "Imports": imports,
        "Deps": imports if dependencies is None else dependencies,
    }


class ArchitectureCheckTest(unittest.TestCase):
    def test_allows_domain_primitives(self) -> None:
        imports = [
            "encoding/json",
            "github.com/chiga0/marshal-harness/internal/canonical",
            "github.com/chiga0/marshal-harness/internal/evidencebinding",
        ]
        self.assertEqual(architecture_layer_inversions(package(imports)), [])

    def test_rejects_profile_and_runtime_implementations(self) -> None:
        imports = [
            "github.com/chiga0/marshal-harness/internal/selfidentity",
            "github.com/chiga0/marshal-harness/internal/execution",
            "github.com/chiga0/marshal-harness/internal/adapter/qoder",
        ]
        self.assertEqual(
            architecture_layer_inversions(package(imports)),
            sorted(imports),
        )

    def test_rejects_transitive_implementation_dependency(self) -> None:
        imports = ["github.com/chiga0/marshal-harness/internal/evidencebinding"]
        dependencies = imports + [
            "github.com/chiga0/marshal-harness/internal/selfidentity"
        ]
        self.assertEqual(
            architecture_layer_inversions(package(imports, dependencies)),
            ["github.com/chiga0/marshal-harness/internal/selfidentity"],
        )

    def test_rejects_wrong_module_or_package_identity(self) -> None:
        wrong_module = package([])
        wrong_module["Module"] = {"Path": "example.invalid/not-marshal"}
        with self.assertRaises(ValueError):
            architecture_layer_inversions(wrong_module)
        wrong_package = package([])
        wrong_package["ImportPath"] = f"{MODULE}/internal/not-domain"
        with self.assertRaises(ValueError):
            architecture_layer_inversions(wrong_package)

    def test_does_not_match_external_module_prefix_lookalike(self) -> None:
        lookalike = "github.com/chiga0/marshal-harness-extra/internal/selfidentity"
        self.assertEqual(architecture_layer_inversions(package([lookalike])), [])


if __name__ == "__main__":
    unittest.main()
