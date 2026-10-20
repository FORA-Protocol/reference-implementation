"""Obligation-driven E2E tests.

Each test here proves one or more entries of the obligation registry under
``tests/e2e/obligations/`` and says which with ``@obligation(Obligation.<ID>)``.
That marker is the only record of the link; the docstring names the same IDs for
a reader. When production does not yet exhibit an obligation, the test is a
strict xfail whose reason names the defect.
"""
