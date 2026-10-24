import base64
import json
import unittest

from decision import decide


def event(payload):
    """A Pub/Sub CloudEvent's data, as the function receives it."""
    raw = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
    return {"message": {"data": base64.b64encode(raw).decode()}}


class DecideTest(unittest.TestCase):
    def test_over_budget_switches_off(self):
        d = decide(event({"costAmount": 1.01, "budgetAmount": 1}))
        self.assertTrue(d.disable, d.reason)

    def test_at_or_under_budget_does_nothing(self):
        for cost in (0, 0.5, 1, 1.0):
            d = decide(event({"costAmount": cost, "budgetAmount": 1}))
            self.assertFalse(d.disable, f"cost {cost}: {d.reason}")

    def test_numbers_as_strings_still_work(self):
        self.assertTrue(decide(event({"costAmount": "2.5", "budgetAmount": "1"})).disable)

    def test_the_other_fields_of_a_real_notification_are_ignored(self):
        msg = {
            "budgetDisplayName": "homehq cap",
            "alertThresholdExceeded": 0.5,
            "costAmount": 0.2,
            "costIntervalStart": "2026-10-01T07:00:00Z",
            "budgetAmount": 1,
            "budgetAmountType": "SPECIFIED_AMOUNT",
            "currencyCode": "USD",
        }
        self.assertFalse(decide(event(msg)).disable)

    def test_garbage_never_switches_billing_off(self):
        cases = {
            "missing cost": {"budgetAmount": 1},
            "missing budget": {"costAmount": 5},
            "not json": b"not json at all",
            "json but not an object": b"[1, 2]",
            "null cost": {"costAmount": None, "budgetAmount": 1},
            "text cost": {"costAmount": "lots", "budgetAmount": 1},
            "NaN cost": b'{"costAmount": NaN, "budgetAmount": 1}',
            "infinite cost": b'{"costAmount": Infinity, "budgetAmount": 1}',
            "zero budget": {"costAmount": 5, "budgetAmount": 0},
            "negative budget": {"costAmount": 5, "budgetAmount": -1},
        }
        for name, payload in cases.items():
            with self.subTest(name):
                self.assertFalse(decide(event(payload)).disable)

    def test_malformed_events_never_switch_billing_off(self):
        for name, data in {
            "empty": {},
            "no message": {"other": 1},
            "no data": {"message": {}},
            "data not base64": {"message": {"data": "%%%not-base64%%%"}},
            "data not utf-8": {"message": {"data": base64.b64encode(b"\xff\xfe").decode()}},
            "none": None,
        }.items():
            with self.subTest(name):
                self.assertFalse(decide(data).disable)


if __name__ == "__main__":
    unittest.main()
