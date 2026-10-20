"""ADR-008 D5 cleanup fixtures, and the data files the harness seeds from.

A cleanup module here defines a function that runs in the
``shared-clean-fixtures`` cleanup chain — between tests declaring the
``stack_isolation("shared-clean-fixtures")`` marker. Tests do NOT
import these fixtures directly; the framework's per-test hook reads
the declared mode and dispatches the appropriate cleanups (D5).

The chain is currently empty; ``conftest.py`` says why above
``SHARED_CLEAN_FIXTURES``. Adding a cleanup: write a zero-argument
function in a module here and append it to ``SHARED_CLEAN_FIXTURES`` in
``conftest.py`` so the per-test hook runs it.
"""
