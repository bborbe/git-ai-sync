"""Test that production code does not use print() for logging."""

import ast
from pathlib import Path

SRC_DIR = Path(__file__).parent.parent / "src"

# Functions allowed to use print()
ALLOWLIST = {
    "cmd_version",  # CLI version output goes to stdout
}


def _find_enclosing_function(tree: ast.Module, target: ast.AST) -> str | None:
    """Find the name of the function containing the target node."""
    for node in ast.walk(tree):
        if isinstance(node, ast.FunctionDef | ast.AsyncFunctionDef):
            for child in ast.walk(node):
                if child is target:
                    return node.name
    return None


def test_no_print_in_source() -> None:
    """Verify no print() calls in production code except allowlisted functions."""
    violations: list[str] = []

    for py_file in sorted(SRC_DIR.rglob("*.py")):
        source = py_file.read_text()
        tree = ast.parse(source, filename=str(py_file))

        for node in ast.walk(tree):
            if not isinstance(node, ast.Call):
                continue
            func = node.func
            if not (isinstance(func, ast.Name) and func.id == "print"):
                continue

            # Check if inside allowlisted function
            parent_func = _find_enclosing_function(tree, node)
            if parent_func and parent_func in ALLOWLIST:
                continue

            rel_path = py_file.relative_to(SRC_DIR)
            violations.append(f"{rel_path}:{node.lineno}")

    assert not violations, "Found print() calls in production code:\n" + "\n".join(
        f"  {v}" for v in violations
    )
