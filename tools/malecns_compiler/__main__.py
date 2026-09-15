"""Command-line entry point for ``python -m tools.malecns_compiler``."""

from .compiler import main


if __name__ == "__main__":
    raise SystemExit(main())
