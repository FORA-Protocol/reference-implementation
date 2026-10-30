"""Changing a generated model's members outside an SDK verb, without losing one to a typo.

pydantic builds a model without validating it in two places the harness uses:
``model_construct``, for a query the SDK stamps the requester into
(:func:`harness.broker_purchase.unstamped`), and ``model_copy(update=...)``, for a
message or document that differs from another in a member or two. Both keep a
member name the model does not declare off the wire without a word, and the type
checker reports neither, so a misspelled member would turn a test into one about
some other message. :func:`require_declared` refuses such a name, and every copy
with members replaced goes through :func:`with_members`.
"""

from __future__ import annotations

from collections.abc import Iterable
from typing import Any, TypeVar

from pydantic import BaseModel

M = TypeVar("M", bound=BaseModel)


def require_declared(model: type[BaseModel], names: Iterable[str]) -> None:
    """Refuse any of ``names`` that ``model`` does not declare as a field."""
    unknown = sorted(set(names) - set(model.model_fields))
    assert not unknown, f"{model.__name__} declares no field {unknown}"


def with_members(message: M, **members: Any) -> M:
    """A copy of ``message`` with ``members`` replaced, refused for a member it does not declare.

    It sends nothing and checks no schema, and the new values are not validated: an
    SDK verb validates and strict-checks the message it sends before it signs it,
    and a document a test publishes may carry a value the protocol refuses on
    purpose. A negative whose request the strict schema must refuse is still
    declared with ``harness.malformed.malformed``, which sends its control first.
    """
    require_declared(type(message), members)
    return message.model_copy(update=members)
