"""Deterministic offline compiler for the official MaleCNS v1.0 release."""

from .compiler import CompilationError, CompilerConfig, compile_corridor

__all__ = ["CompilationError", "CompilerConfig", "compile_corridor"]
