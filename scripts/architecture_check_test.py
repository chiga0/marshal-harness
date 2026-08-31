#!/usr/bin/env python3

from pathlib import Path
import tempfile
import unittest

from architecture_check import (
    APPLICATION_PACKAGE,
    DOMAIN_PACKAGE,
    INPUT_ADAPTER_PACKAGES,
    MODULE,
    PRODUCTION_RUNTIME_PACKAGE,
    architecture_layer_inversions,
    production_dependency_inversions,
    production_source_inversions,
    provisional_owner_ast_inversions,
)


def package(imports: list[str], dependencies: list[str] | None = None) -> dict[str, object]:
    return {
        "ImportPath": DOMAIN_PACKAGE,
        "Module": {"Path": MODULE},
        "Imports": imports,
        "Deps": imports if dependencies is None else dependencies,
    }


def production_graph(imports: dict[str, list[str]]) -> list[dict[str, object]]:
    required = INPUT_ADAPTER_PACKAGES | {APPLICATION_PACKAGE, PRODUCTION_RUNTIME_PACKAGE}
    paths = required | set(imports)
    return [
        {
            "ImportPath": import_path,
            "Module": {"Path": MODULE},
            "Imports": imports.get(import_path, []),
        }
        for import_path in paths
    ]


class ArchitectureCheckTest(unittest.TestCase):

    def owner_provisional_fixture(self, root: Path, mutation: str = "") -> None:
        source = root / "internal/productionruntime/owner_lock_darwin.go"
        source.parent.mkdir(parents=True)
        body = (
            "package productionruntime\n"
            "type darwinProvisionalOwnerVerifier struct{}\n"
            "func (lock *darwinRepositoryOwnerScopeLock) acquireOwner() {\n"
            "  physical.withHeld(ctx, false, func() error {\n"
            "    verifier := &darwinProvisionalOwnerVerifier{candidate: candidate}\n"
            "    " + mutation + "\n"
            "    result, acquireErr = store.AcquireOwner(ctx, verifier, expectedEpoch, expectedFactDigest, candidate)\n"
            "    return acquireErr\n"
            "  })\n"
            "}\n"
            "func (verifier *darwinProvisionalOwnerVerifier) WithCurrentOwnerLock() {}\n"
        )
        source.write_text(body, encoding="utf-8")

    def test_owner_provisional_ast_accepts_exact_one_shot_shape(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.owner_provisional_fixture(root)
            self.assertEqual(provisional_owner_ast_inversions(root), [])

    def test_owner_provisional_ast_rejects_escape_and_reuse_fixtures(self) -> None:
        mutations = {
            "interface-assignment": "escaped = verifier",
            "return": "return verifier",
            "setter": "lock.setVerifier(verifier)",
            "with-current-reuse": "verifier.WithCurrentOwnerLock()",
        }
        for name, mutation in mutations.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                self.owner_provisional_fixture(root, mutation)
                self.assertIn(
                    "owner-provisional-ast:verifier-escape",
                    provisional_owner_ast_inversions(root),
                )

    def test_owner_provisional_ast_rejects_constructor_field_and_callsite_fixtures(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.owner_provisional_fixture(root)
            source = root / "internal/productionruntime/owner_lock_darwin.go"
            source.write_text(
                source.read_text(encoding="utf-8")
                + "type holder struct { verifier *darwinProvisionalOwnerVerifier }\n",
                encoding="utf-8",
            )
            self.assertIn(
                "owner-provisional-ast:type-shape",
                provisional_owner_ast_inversions(root),
            )
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.owner_provisional_fixture(root)
            extra = root / "internal/productionruntime/other.go"
            extra.write_text(
                "package productionruntime\nfunc other() { store.AcquireOwner(ctx, verifier, expectedEpoch, expectedFactDigest, candidate) }\n",
                encoding="utf-8",
            )
            self.assertIn(
                "owner-provisional-ast:acquire-call-shape",
                provisional_owner_ast_inversions(root),
            )

    def test_owner_provisional_ast_rejects_wrong_store_or_candidate(self) -> None:
        for old, new in (
            ("store.AcquireOwner", "other.AcquireOwner"),
            ("store.AcquireOwner", "holder.store.AcquireOwner"),
            (", candidate)", ", sibling)"),
        ):
            with self.subTest(replacement=new), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                self.owner_provisional_fixture(root)
                source = root / "internal/productionruntime/owner_lock_darwin.go"
                source.write_text(source.read_text(encoding="utf-8").replace(old, new), encoding="utf-8")
                self.assertIn(
                    "owner-provisional-ast:acquire-call-shape",
                    provisional_owner_ast_inversions(root),
                )
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

    def test_freezes_existing_input_adapter_debt_but_rejects_new_dependency(self) -> None:
        packages = production_graph(
            {f"{MODULE}/internal/cli": [f"{MODULE}/internal/execution"]}
        )
        self.assertEqual(production_dependency_inversions(packages), [])
        wrapper = f"{MODULE}/internal/newwrapper"
        packages = production_graph(
            {
                f"{MODULE}/cmd/marshal": [wrapper],
                wrapper: [f"{MODULE}/internal/resultingress"],
            }
        )
        self.assertEqual(
            production_dependency_inversions(packages),
            [f"{MODULE}/cmd/marshal:{wrapper}->{MODULE}/internal/resultingress"],
        )

    def test_application_and_runtime_bridge_imports_fail_closed(self) -> None:
        app_wrapper = f"{MODULE}/internal/appwrapper"
        runtime_wrapper = f"{MODULE}/internal/runtimewrapper"
        packages = production_graph(
            {
                APPLICATION_PACKAGE: [app_wrapper],
                app_wrapper: [f"{MODULE}/internal/planning"],
                PRODUCTION_RUNTIME_PACKAGE: [
                    f"{MODULE}/internal/resultingress",
                    runtime_wrapper,
                ],
                runtime_wrapper: [f"{MODULE}/internal/processsupervisor"],
            }
        )
        self.assertEqual(
            production_dependency_inversions(packages),
            sorted(
                [
                    f"{APPLICATION_PACKAGE}:{app_wrapper}->{MODULE}/internal/planning",
                    f"{PRODUCTION_RUNTIME_PACKAGE}:{runtime_wrapper}->{MODULE}/internal/processsupervisor",
                ]
            ),
        )

    def test_allows_the_unique_runtime_resultingress_edge_from_an_input_root(self) -> None:
        packages = production_graph(
            {
                f"{MODULE}/cmd/marshal": [PRODUCTION_RUNTIME_PACKAGE],
                PRODUCTION_RUNTIME_PACKAGE: [f"{MODULE}/internal/resultingress"],
            }
        )
        self.assertEqual(production_dependency_inversions(packages), [])

    def test_allows_the_accepted_runtime_runstore_resultingress_edge(self) -> None:
        packages = production_graph(
            {
                PRODUCTION_RUNTIME_PACKAGE: [f"{MODULE}/internal/runstore"],
                f"{MODULE}/internal/runstore": [f"{MODULE}/internal/resultingress"],
            }
        )
        self.assertEqual(production_dependency_inversions(packages), [])

    def test_freezes_the_s1_input_root_runstore_resultingress_edge(self) -> None:
        packages = production_graph(
            {
                f"{MODULE}/cmd/marshal": [f"{MODULE}/internal/runstore"],
                f"{MODULE}/internal/runstore": [f"{MODULE}/internal/resultingress"],
            }
        )
        self.assertEqual(production_dependency_inversions(packages), [])

    def test_rejects_an_unreviewed_command_composition_root(self) -> None:
        extra = f"{MODULE}/cmd/alternate"
        packages = production_graph({extra: []})
        self.assertEqual(
            production_dependency_inversions(packages),
            [f"unexpected-production-root:{extra}"],
        )

    def test_source_gate_rejects_legacy_selectors(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            debt = root / "internal/app/sandbox.go"
            debt.parent.mkdir(parents=True)
            debt.write_text(
                '// MARSHAL_PRODUCTION_GATE is documentation only.\n'
                'const value = "MARSHAL_EMBEDDED_SANDBOX"\n',
                encoding="utf-8",
            )
            forbidden = root / "internal/server/new.go"
            forbidden.parent.mkdir(parents=True)
            forbidden.write_text('const value = "MARSHAL_PRODUCTION_GATE"\n', encoding="utf-8")
            self.assertEqual(
                production_source_inversions(root),
                [
                    "legacy-selector:internal/app/sandbox.go:MARSHAL_EMBEDDED_SANDBOX",
                    "legacy-selector:internal/server/new.go:MARSHAL_PRODUCTION_GATE",
                ],
            )

    def test_source_gate_ignores_test_fixtures(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture = root / "internal/server/api_test.go"
            fixture.parent.mkdir(parents=True)
            fixture.write_text('const value = "MARSHAL_PRODUCTION_GATE"\n', encoding="utf-8")
            self.assertEqual(production_source_inversions(root), [])

    def test_source_gate_rejects_new_server_child_task_run(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "internal/server/child.go"
            source.parent.mkdir(parents=True)
            source.write_text(
                'var args = []string{"task", "run"}\n'
                'func run() { exec.CommandContext(ctx, executable, args...) }\n',
                encoding="utf-8",
            )
            violations = production_source_inversions(root)
            self.assertEqual(len(violations), 1)
            self.assertTrue(
                violations[0].startswith(
                    "server-process-spawn:internal/server/child.go:i:exec|p:.|i:CommandContext"
                )
            )

    def test_source_gate_rejects_cross_file_arguments_at_a_frozen_spawn_site(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            constants = root / "cmd/marshal-server/arguments.go"
            constants.parent.mkdir(parents=True)
            constants.write_text(
                'var childArgs = []string{"task", "run"}\n',
                encoding="utf-8",
            )
            source = root / "cmd/marshal-server/main.go"
            source.write_text(
                'func executeRunThroughFixedCLI() { exec.CommandContext(ctx, executable.Path, childArgs...) }\n',
                encoding="utf-8",
            )
            violations = production_source_inversions(root)
            self.assertEqual(len(violations), 1)
            self.assertTrue(
                violations[0].startswith(
                    "server-process-spawn:cmd/marshal-server/main.go:i:exec|p:.|i:CommandContext"
                )
            )

    def test_source_gate_resolves_spawn_import_aliases(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "internal/server/alias.go"
            source.parent.mkdir(parents=True)
            source.write_text(
                'package server\nimport ex "os/exec"\n'
                'func run() { ex.Command(executable, arguments...) }\n',
                encoding="utf-8",
            )
            violations = production_source_inversions(root)
            self.assertEqual(len(violations), 1)
            self.assertTrue(
                violations[0].startswith(
                    "server-process-spawn:internal/server/alias.go:i:ex|p:.|i:Command"
                )
            )

    def test_source_gate_rejects_exported_and_private_controller_bypasses(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runtime_source = root / "internal/productionruntime/bypass.go"
            runtime_source.parent.mkdir(parents=True)
            runtime_source.write_text(
                "type Controller struct{}\n"
                "func bypass(c *controller) { c.startPreparedRun(ctx, prepared) }\n"
                "func (c *controller) StartPreparedRun() {}\n"
                "type darwinRepositoryOwnerLock struct{}\n"
                "func (l *darwinRepositoryOwnerLock) ClaimRuntime() {}\n",
                encoding="utf-8",
            )
            external = root / "internal/cli/bypass.go"
            external.parent.mkdir(parents=True)
            external.write_text(
                "var value pr.NewController\n",
                encoding="utf-8",
            )
            self.assertEqual(
                production_source_inversions(root),
                sorted(
                    [
                        "production-runtime-bypass:internal/cli/bypass.go:external-bypass:NewController",
                        "production-runtime-bypass:internal/productionruntime/bypass.go:exported-bypass:Controller",
                        "production-runtime-bypass:internal/productionruntime/bypass.go:exported-receiver:controller.StartPreparedRun",
                        "production-runtime-bypass:internal/productionruntime/bypass.go:exported-receiver:darwinRepositoryOwnerLock.ClaimRuntime",
                        "production-runtime-bypass:internal/productionruntime/bypass.go:private-mutation:startPreparedRun",
                    ]
                ),
            )


if __name__ == "__main__":
    unittest.main()
