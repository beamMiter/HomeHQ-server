"""The decision behind the billing kill switch, kept free of Google libraries
so it can be tested without them."""

import base64
import json
import math
from dataclasses import dataclass


@dataclass(frozen=True)
class Decision:
    disable: bool
    reason: str


def decide(event_data: dict) -> Decision:
    """Reads a budget notification (a Pub/Sub CloudEvent's data) and says
    whether billing should be switched off.

    Billing goes off only when the notification is readable and the cost is
    plainly above the budget. Anything unclear (missing fields, text that is not
    JSON, NaN, a budget that is not positive) does nothing: a kill switch that
    fires on garbage would take the server down for no reason.
    """
    try:
        raw = base64.b64decode(event_data["message"]["data"]).decode("utf-8")
        msg = json.loads(raw)
        cost = float(msg["costAmount"])
        budget = float(msg["budgetAmount"])
    except (KeyError, TypeError, ValueError) as err:
        return Decision(False, f"unreadable notification: {err!r}")

    if not (math.isfinite(cost) and math.isfinite(budget)):
        return Decision(False, f"cost {cost} or budget {budget} is not a finite number")
    if budget <= 0:
        return Decision(False, f"budget {budget} is not positive")
    if cost <= budget:
        return Decision(False, f"cost {cost} is within the budget {budget}")
    return Decision(True, f"cost {cost} exceeds the budget {budget}")
